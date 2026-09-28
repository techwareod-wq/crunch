package service

import (
	"context"
	"errors"
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/services/contentBridge"
	"github.com/atharva-ng/crunch/internal/services/contentBridge/dto"
	"github.com/atharva-ng/crunch/internal/services/contentBridge/platforms"
	"github.com/atharva-ng/crunch/internal/services/contentBridge/platforms/sidecar"
	"github.com/atharva-ng/crunch/internal/services/onboardingService/constants"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// --- fakes ---

type fakeStore struct {
	we      *models.WebEntity
	weFound bool
	weErr   error

	mc      *models.WebEntityMasterContext
	mcFound bool
	mcErr   error

	sa      *models.ScheduledArticle
	saFound bool
	saErr   error

	kw      *models.Keyword
	kwFound bool
	kwErr   error

	states      []models.CGEPublishState
	setStateErr error

	statuses     []models.ScheduledArticleStatus
	setStatusErr error
}

func (f *fakeStore) FindWebEntityByUserID(context.Context, string) (bool, *models.WebEntity, error) {
	return f.weFound, f.we, f.weErr
}
func (f *fakeStore) GetMasterContextByScheduledArticleID(context.Context, string) (bool, *models.WebEntityMasterContext, error) {
	return f.mcFound, f.mc, f.mcErr
}
func (f *fakeStore) GetScheduledArticle(context.Context, string) (bool, *models.ScheduledArticle, error) {
	return f.saFound, f.sa, f.saErr
}
func (f *fakeStore) GetKeyword(context.Context, string) (bool, *models.Keyword, error) {
	return f.kwFound, f.kw, f.kwErr
}
func (f *fakeStore) SetPublishState(_ context.Context, _ string, state models.CGEPublishState) error {
	f.states = append(f.states, state)
	return f.setStateErr
}
func (f *fakeStore) SetScheduledArticleStatus(_ context.Context, _ string, status models.ScheduledArticleStatus) error {
	f.statuses = append(f.statuses, status)
	return f.setStatusErr
}

type fakePublisher struct {
	platform string

	schema         dto.BlogSchema
	schemaErr      error
	result         dto.PublishResult
	publishErr     error
	collections    []dto.CollectionSummary
	collectionsErr error

	gotPost    dto.BlogPost
	gotColl    string
	gotCreds   platforms.PlatformCredentials
	fetchCalls int
}

func (p *fakePublisher) Platform() string { return p.platform }
func (p *fakePublisher) ListCollections(_ context.Context, creds platforms.PlatformCredentials) ([]dto.CollectionSummary, error) {
	p.gotCreds = creds
	return p.collections, p.collectionsErr
}
func (p *fakePublisher) FetchSchema(_ context.Context, _ platforms.PlatformCredentials, _ string) (dto.BlogSchema, error) {
	p.fetchCalls++
	return p.schema, p.schemaErr
}
func (p *fakePublisher) PublishItem(_ context.Context, creds platforms.PlatformCredentials, coll string, post dto.BlogPost, _ dto.BlogSchema) (dto.PublishResult, error) {
	p.gotPost = post
	p.gotColl = coll
	p.gotCreds = creds
	return p.result, p.publishErr
}

func framerWebEntity() *models.WebEntity {
	return &models.WebEntity{
		Publishing: &models.PublishingConfig{
			Platform:     string(constants.PlatformFramer),
			ApiKey:       "secret-key",
			CollectionID: "coll-123",
		},
	}
}

// --- PostBlogStructure ---

func TestPostBlogStructure(t *testing.T) {
	tests := []struct {
		name         string
		remoteItemID string
	}{
		{name: "create forwards post without remote id", remoteItemID: ""},
		{name: "update forwards post with remote id", remoteItemID: "item-9"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pub := &fakePublisher{
				platform: string(constants.PlatformFramer),
				result:   dto.PublishResult{RemoteItemID: "item-9", RemoteURL: "https://x/y"},
			}
			s := &contentBridgeService{
				store:    &fakeStore{we: framerWebEntity(), weFound: true},
				registry: platforms.NewRegistry(pub),
			}

			post := dto.BlogPost{Title: "T", RemoteItemID: tt.remoteItemID}
			_, err := s.PostBlogStructure(context.Background(), "user1", post)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pub.fetchCalls != 1 {
				t.Fatalf("expected schema fetched once, got %d", pub.fetchCalls)
			}
			if pub.gotPost.RemoteItemID != tt.remoteItemID {
				t.Fatalf("publisher got remoteItemID %q, want %q", pub.gotPost.RemoteItemID, tt.remoteItemID)
			}
			if pub.gotColl != "coll-123" {
				t.Fatalf("publisher got collection %q, want coll-123", pub.gotColl)
			}
			if pub.gotCreds.APIKey != "secret-key" {
				t.Fatalf("publisher got api key %q, want secret-key", pub.gotCreds.APIKey)
			}
		})
	}
}

func TestPostBlogStructureErrors(t *testing.T) {
	t.Run("no web entity", func(t *testing.T) {
		s := &contentBridgeService{store: &fakeStore{weFound: false}, registry: platforms.NewRegistry()}
		if _, err := s.PostBlogStructure(context.Background(), "user1", dto.BlogPost{}); !errors.Is(err, ErrWebEntityNotFound) {
			t.Fatalf("got %v, want ErrWebEntityNotFound", err)
		}
	})

	t.Run("publishing not configured", func(t *testing.T) {
		s := &contentBridgeService{
			store:    &fakeStore{we: &models.WebEntity{}, weFound: true},
			registry: platforms.NewRegistry(),
		}
		if _, err := s.PostBlogStructure(context.Background(), "user1", dto.BlogPost{}); !errors.Is(err, ErrPublishingNotConfigured) {
			t.Fatalf("got %v, want ErrPublishingNotConfigured", err)
		}
	})

	t.Run("unregistered platform", func(t *testing.T) {
		s := &contentBridgeService{
			store:    &fakeStore{we: framerWebEntity(), weFound: true},
			registry: platforms.NewRegistry(), // empty
		}
		if _, err := s.PostBlogStructure(context.Background(), "user1", dto.BlogPost{}); err == nil {
			t.Fatal("expected error for unregistered platform, got nil")
		}
	})
}

// --- ListCollections ---

func TestListCollections(t *testing.T) {
	t.Run("strips query and fragment from project url", func(t *testing.T) {
		pub := &fakePublisher{
			platform:    string(constants.PlatformFramer),
			collections: []dto.CollectionSummary{{ID: "c1", Name: "Blog"}},
		}
		s := &contentBridgeService{store: &fakeStore{}, registry: platforms.NewRegistry(pub)}

		got, err := s.ListCollections(context.Background(),
			string(constants.PlatformFramer), " key-1 ", "https://framer.com/projects/x?node=abc#frag", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pub.gotCreds.ProjectURL != "https://framer.com/projects/x" {
			t.Fatalf("project url on wire = %q, want query+fragment stripped", pub.gotCreds.ProjectURL)
		}
		if pub.gotCreds.APIKey != "key-1" {
			t.Fatalf("api key on wire = %q, want trimmed key-1", pub.gotCreds.APIKey)
		}
		if len(got) != 1 || got[0].ID != "c1" {
			t.Fatalf("collections = %+v", got)
		}
	})

	t.Run("invalid inputs are rejected before any platform call", func(t *testing.T) {
		pub := &fakePublisher{platform: string(constants.PlatformFramer)}
		s := &contentBridgeService{store: &fakeStore{}, registry: platforms.NewRegistry(pub)}

		cases := []struct {
			name                         string
			platform, apiKey, projectURL string
		}{
			{"unknown platform", "wordpress", "key", "https://x.com"},
			{"empty api key", string(constants.PlatformFramer), "  ", "https://x.com"},
			{"non-http url", string(constants.PlatformFramer), "key", "ftp://x.com"},
			{"unparseable url", string(constants.PlatformFramer), "key", "://nope"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := s.ListCollections(context.Background(), tc.platform, tc.apiKey, tc.projectURL, ""); !errors.Is(err, ErrInvalidCredentials) {
					t.Fatalf("got %v, want ErrInvalidCredentials", err)
				}
			})
		}
	})

	t.Run("publisher error propagates", func(t *testing.T) {
		pub := &fakePublisher{
			platform:       string(constants.PlatformFramer),
			collectionsErr: errors.New("boom"),
		}
		s := &contentBridgeService{store: &fakeStore{}, registry: platforms.NewRegistry(pub)}

		if _, err := s.ListCollections(context.Background(),
			string(constants.PlatformFramer), "key", "https://x.com", ""); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

// --- HandlePublish ---

func TestHandlePublishSuccess(t *testing.T) {
	pub := &fakePublisher{
		platform: string(constants.PlatformFramer),
		result:   dto.PublishResult{RemoteItemID: "remote-1", RemoteURL: "https://site/post", Updated: false},
	}
	st := &fakeStore{
		we:      framerWebEntity(),
		weFound: true,
		mc:      &models.WebEntityMasterContext{ID: primitive.NewObjectID(), ProposedTitle: "Hello"},
		mcFound: true,
		sa:      &models.ScheduledArticle{Title: "Edited Title"},
		saFound: true,
	}
	s := &contentBridgeService{store: st, registry: platforms.NewRegistry(pub)}

	err := s.HandlePublish(context.Background(), "user1", contentBridge.PublishPayload{ScheduledArticleID: "sa-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The published post's title comes from the ScheduledArticle doc, not the
	// master context's ProposedTitle.
	if pub.gotPost.Title != "Edited Title" {
		t.Fatalf("published title = %q, want the ScheduledArticle title", pub.gotPost.Title)
	}
	if len(st.states) != 1 {
		t.Fatalf("expected 1 publish state write, got %d", len(st.states))
	}
	got := st.states[0]
	if got.Status != models.CGEPublishStatusPublished {
		t.Fatalf("status = %q, want published", got.Status)
	}
	if got.RemoteItemID != "remote-1" || got.RemoteURL != "https://site/post" {
		t.Fatalf("remote fields not persisted: %+v", got)
	}
	if got.Platform != string(constants.PlatformFramer) {
		t.Fatalf("platform = %q, want framer", got.Platform)
	}
	if got.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", got.Attempts)
	}
	if len(st.statuses) != 1 || st.statuses[0] != models.ScheduledArticleStatusPublished {
		t.Fatalf("scheduled article status mirror not applied: %+v", st.statuses)
	}
}

func TestHandlePublishFailurePersistsFailedState(t *testing.T) {
	pub := &fakePublisher{
		platform:   string(constants.PlatformFramer),
		publishErr: errors.New("framer down"),
	}
	st := &fakeStore{
		we:      framerWebEntity(),
		weFound: true,
		mc: &models.WebEntityMasterContext{
			ID:      primitive.NewObjectID(),
			Publish: &models.CGEPublishState{Attempts: 2}, // prior attempts
		},
		mcFound: true,
	}
	s := &contentBridgeService{store: st, registry: platforms.NewRegistry(pub)}

	err := s.HandlePublish(context.Background(), "user1", contentBridge.PublishPayload{ScheduledArticleID: "sa-1"})
	if err == nil {
		t.Fatal("expected error to bubble up for async retry, got nil")
	}
	if len(st.states) != 1 {
		t.Fatalf("expected 1 publish state write, got %d", len(st.states))
	}
	got := st.states[0]
	if got.Status != models.CGEPublishStatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.LastError == "" {
		t.Fatal("expected LastError to be recorded")
	}
	if got.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (prior 2 + 1)", got.Attempts)
	}
	if len(st.statuses) != 0 {
		t.Fatalf("scheduled article status must not flip on failure, got %+v", st.statuses)
	}
}

// A publish that fails after an earlier success must retract the "published"
// badge: the CMS item may have been deleted out of band (exactly how a stale
// RemoteItemID starts 404ing), and claiming an article is live when nothing is
// live also hides the retry affordance.
func TestHandlePublishFailureRevertsPublishedStatus(t *testing.T) {
	tests := []struct {
		name    string
		status  models.ScheduledArticleStatus
		reverts bool
	}{
		{name: "published reverts to ready for review", status: models.ScheduledArticleStatusPublished, reverts: true},
		{name: "ready for review is left alone", status: models.ScheduledArticleStatusReadyForReview},
		{name: "user draft keeps its own state", status: models.ScheduledArticleStatusDraft},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pub := &fakePublisher{
				platform:   string(constants.PlatformFramer),
				publishErr: errors.New("cms down"),
			}
			st := &fakeStore{
				we:      framerWebEntity(),
				weFound: true,
				mc:      &models.WebEntityMasterContext{ID: primitive.NewObjectID()},
				mcFound: true,
				sa:      &models.ScheduledArticle{Title: "T", Status: tt.status},
				saFound: true,
			}
			s := &contentBridgeService{store: st, registry: platforms.NewRegistry(pub)}

			if err := s.HandlePublish(context.Background(), "user1", contentBridge.PublishPayload{ScheduledArticleID: "sa-1"}); err == nil {
				t.Fatal("expected the publish error to bubble up")
			}

			if !tt.reverts {
				if len(st.statuses) != 0 {
					t.Fatalf("status must be untouched, got %+v", st.statuses)
				}
				return
			}
			if len(st.statuses) != 1 || st.statuses[0] != models.ScheduledArticleStatusReadyForReview {
				t.Fatalf("status writes = %+v, want one readyForReview", st.statuses)
			}
		})
	}
}

func TestHandlePublishPermanentErrorShortCircuits(t *testing.T) {
	pub := &fakePublisher{
		platform: string(constants.PlatformFramer),
		publishErr: &sidecar.PublishError{
			Code:     "UNAUTHORIZED",
			Category: sidecar.CategoryPermanent,
			Message:  "Framer rejected the API key",
		},
	}
	st := &fakeStore{
		we:      framerWebEntity(),
		weFound: true,
		mc:      &models.WebEntityMasterContext{ID: primitive.NewObjectID()},
		mcFound: true,
	}
	s := &contentBridgeService{store: st, registry: platforms.NewRegistry(pub)}

	err := s.HandlePublish(context.Background(), "user1", contentBridge.PublishPayload{ScheduledArticleID: "sa-1"})
	if !errors.Is(err, pipeline.ErrPermanent) {
		t.Fatalf("permanent sidecar error must wrap pipeline.ErrPermanent, got %v", err)
	}
	var pe *sidecar.PublishError
	if !errors.As(err, &pe) {
		t.Fatalf("original PublishError must stay in the chain, got %v", err)
	}
	if len(st.states) != 1 || st.states[0].Status != models.CGEPublishStatusFailed {
		t.Fatalf("failed state not persisted: %+v", st.states)
	}
	if st.states[0].LastError == "" {
		t.Fatal("LastError must record the coded error")
	}
}

// Partial failure: the sidecar upserted the CMS item but the site publish
// failed. The item id from error.details must be persisted BEFORE the error
// surfaces, so the retry updates the item instead of creating a duplicate.
func TestHandlePublishPartialFailurePersistsRemoteItemID(t *testing.T) {
	pub := &fakePublisher{
		platform: string(constants.PlatformFramer),
		publishErr: &sidecar.PublishError{
			Code:         "PROVIDER_UNAVAILABLE",
			Category:     sidecar.CategoryTransient,
			Message:      "item upserted but site publish failed",
			RemoteItemID: "item-fresh",
		},
	}
	st := &fakeStore{
		we:      framerWebEntity(),
		weFound: true,
		mc:      &models.WebEntityMasterContext{ID: primitive.NewObjectID()},
		mcFound: true,
	}
	s := &contentBridgeService{store: st, registry: platforms.NewRegistry(pub)}

	err := s.HandlePublish(context.Background(), "user1", contentBridge.PublishPayload{ScheduledArticleID: "sa-1"})
	if err == nil {
		t.Fatal("expected transient error to bubble up for retry")
	}
	if errors.Is(err, pipeline.ErrPermanent) {
		t.Fatal("transient error must not carry the permanent sentinel")
	}
	if len(st.states) != 1 {
		t.Fatalf("expected 1 state write, got %d", len(st.states))
	}
	if st.states[0].RemoteItemID != "item-fresh" {
		t.Fatalf("RemoteItemID = %q, want item-fresh (persisted from error details)", st.states[0].RemoteItemID)
	}
}

// A failed retry must not erase the idempotency key from an earlier attempt.
func TestHandlePublishFailureKeepsPriorRemoteItemID(t *testing.T) {
	pub := &fakePublisher{
		platform:   string(constants.PlatformFramer),
		publishErr: errors.New("framer down"),
	}
	st := &fakeStore{
		we:      framerWebEntity(),
		weFound: true,
		mc: &models.WebEntityMasterContext{
			ID:      primitive.NewObjectID(),
			Publish: &models.CGEPublishState{RemoteItemID: "item-old", Attempts: 1},
		},
		mcFound: true,
	}
	s := &contentBridgeService{store: st, registry: platforms.NewRegistry(pub)}

	if err := s.HandlePublish(context.Background(), "user1", contentBridge.PublishPayload{ScheduledArticleID: "sa-1"}); err == nil {
		t.Fatal("expected error")
	}
	if len(st.states) != 1 || st.states[0].RemoteItemID != "item-old" {
		t.Fatalf("prior RemoteItemID must survive a failed attempt, got %+v", st.states)
	}
}

func TestHandlePublishPersistsSitePublished(t *testing.T) {
	pub := &fakePublisher{
		platform: string(constants.PlatformFramer),
		result:   dto.PublishResult{RemoteItemID: "r1", SitePublished: true},
	}
	st := &fakeStore{
		we:      framerWebEntity(),
		weFound: true,
		mc:      &models.WebEntityMasterContext{ID: primitive.NewObjectID()},
		mcFound: true,
	}
	s := &contentBridgeService{store: st, registry: platforms.NewRegistry(pub)}

	if err := s.HandlePublish(context.Background(), "user1", contentBridge.PublishPayload{ScheduledArticleID: "sa-1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(st.states) != 1 || !st.states[0].SitePublished {
		t.Fatalf("SitePublished must be persisted, got %+v", st.states)
	}
}

func TestHandlePublishMissingMasterContext(t *testing.T) {
	s := &contentBridgeService{
		store:    &fakeStore{mcFound: false},
		registry: platforms.NewRegistry(),
	}
	if err := s.HandlePublish(context.Background(), "user1", contentBridge.PublishPayload{ScheduledArticleID: "sa-1"}); err == nil {
		t.Fatal("expected error for missing master context, got nil")
	}
}

// --- resolveLive precedence ---

func TestResolveLive(t *testing.T) {
	p := func(b bool) *bool { return &b }
	cases := []struct {
		name   string
		saLive *bool
		weLive *bool
		want   bool
	}{
		{"both unset -> draft", nil, nil, false},
		{"generic live, no override -> live", nil, p(true), true},
		{"generic draft, no override -> draft", nil, p(false), false},
		{"override live wins over unset generic", p(true), nil, true},
		{"override draft wins over generic live", p(false), p(true), false},
		{"override live wins over generic draft", p(true), p(false), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sa := &models.ScheduledArticle{PublishAsLive: c.saLive}
			we := &models.WebEntity{PublishAsLive: c.weLive}
			if got := resolveLive(sa, we); got != c.want {
				t.Fatalf("resolveLive = %v, want %v", got, c.want)
			}
		})
	}
	if resolveLive(nil, nil) {
		t.Fatal("resolveLive(nil, nil) must be draft (false)")
	}
}

// A live-resolving publish sends draft=false and persists Live=true; the
// product default (no override, no generic) sends draft=true, Live=false.
func TestHandlePublishResolvesDraftAndLive(t *testing.T) {
	p := func(b bool) *bool { return &b }
	cases := []struct {
		name      string
		weLive    *bool
		wantDraft bool
		wantLive  bool
	}{
		{"default is draft", nil, true, false},
		{"generic live default", p(true), false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			we := framerWebEntity()
			we.PublishAsLive = c.weLive
			pub := &fakePublisher{
				platform: string(constants.PlatformFramer),
				result:   dto.PublishResult{RemoteItemID: "r1"},
			}
			st := &fakeStore{
				we:      we,
				weFound: true,
				mc:      &models.WebEntityMasterContext{ID: primitive.NewObjectID(), ProposedTitle: "Hello"},
				mcFound: true,
			}
			s := &contentBridgeService{store: st, registry: platforms.NewRegistry(pub)}

			if err := s.HandlePublish(context.Background(), "user1", contentBridge.PublishPayload{ScheduledArticleID: "sa-1"}); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if pub.gotPost.Draft != c.wantDraft {
				t.Fatalf("post.Draft = %v, want %v", pub.gotPost.Draft, c.wantDraft)
			}
			if len(st.states) != 1 || st.states[0].Live != c.wantLive {
				t.Fatalf("persisted Live = %+v, want %v", st.states, c.wantLive)
			}
		})
	}
}

// The per-article override wins over the generic default at publish time.
func TestHandlePublishArticleOverrideWins(t *testing.T) {
	live := false
	we := framerWebEntity()
	we.PublishAsLive = boolPtr(true) // generic says live...
	pub := &fakePublisher{
		platform: string(constants.PlatformFramer),
		result:   dto.PublishResult{RemoteItemID: "r1"},
	}
	st := &fakeStore{
		we:      we,
		weFound: true,
		mc:      &models.WebEntityMasterContext{ID: primitive.NewObjectID()},
		mcFound: true,
		sa:      &models.ScheduledArticle{PublishAsLive: &live}, // ...but the article overrides to draft
		saFound: true,
	}
	s := &contentBridgeService{store: st, registry: platforms.NewRegistry(pub)}

	if err := s.HandlePublish(context.Background(), "user1", contentBridge.PublishPayload{ScheduledArticleID: "sa-1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pub.gotPost.Draft {
		t.Fatal("article override to draft must win over generic live default")
	}
	if len(st.states) != 1 || st.states[0].Live {
		t.Fatalf("persisted Live must be false (draft override), got %+v", st.states)
	}
}

// Live is recorded even when the publish fails, so the UI keeps a truthful
// live/draft indicator across a failed attempt.
func TestHandlePublishFailurePersistsLive(t *testing.T) {
	we := framerWebEntity()
	we.PublishAsLive = boolPtr(true)
	pub := &fakePublisher{
		platform:   string(constants.PlatformFramer),
		publishErr: errors.New("framer down"),
	}
	st := &fakeStore{
		we:      we,
		weFound: true,
		mc:      &models.WebEntityMasterContext{ID: primitive.NewObjectID()},
		mcFound: true,
	}
	s := &contentBridgeService{store: st, registry: platforms.NewRegistry(pub)}

	if err := s.HandlePublish(context.Background(), "user1", contentBridge.PublishPayload{ScheduledArticleID: "sa-1"}); err == nil {
		t.Fatal("expected error")
	}
	if len(st.states) != 1 || !st.states[0].Live {
		t.Fatalf("failed state must record Live=true, got %+v", st.states)
	}
}

func boolPtr(b bool) *bool { return &b }
