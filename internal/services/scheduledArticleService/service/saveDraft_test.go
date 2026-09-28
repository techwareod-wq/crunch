package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/scheduledArticleService"
	"github.com/atharva-ng/crunch/internal/services/scheduledArticleService/store"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// --- fakes ---

// fakeStore embeds store.Store so only the methods SaveDraft touches need
// implementing; any unimplemented call would nil-panic, which is the signal we
// exercised an unexpected path.
type fakeStore struct {
	store.Store

	sa      *models.ScheduledArticle
	saFound bool

	mc      *models.WebEntityMasterContext
	mcFound bool

	gotWEMCUpdate models.WEMCUpdateReq
	gotSAUpdate   models.ScheduledArticleUpdateReq

	// publishAsLiveCalls records each SetScheduledArticlePublishAsLive value the
	// service wrote (nil until the first call), so SetPublishState tests can
	// assert whether the store was hit and with what.
	publishAsLiveCalls []bool

	// List-path fixtures (GetScheduledArticles / GetScheduledArticleStatuses).
	we              *models.WebEntity
	weFound         bool
	articlesInRange []*models.ScheduledArticle
	mcsByID         []models.WebEntityMasterContext
	keywordsByIDs   []models.Keyword
	// keywordsByIDsCalls counts GetKeywordsByIDs invocations so the compact
	// view=status path can be asserted to skip the keyword hydration.
	keywordsByIDsCalls int

	// clearedPendingRegen records SaveDraft settling the pending-regen set
	// (method defined in saveDraft_regen_test.go).
	clearedPendingRegen bool
}

func (f *fakeStore) GetScheduledArticle(context.Context, string) (bool, *models.ScheduledArticle, error) {
	return f.saFound, f.sa, nil
}
func (f *fakeStore) GetWebEntityMasterContextByScheduledArticleID(context.Context, string) (bool, *models.WebEntityMasterContext, error) {
	return f.mcFound, f.mc, nil
}
func (f *fakeStore) UpdateWebEntityMasterContext(_ context.Context, _ string, req models.WEMCUpdateReq) error {
	f.gotWEMCUpdate = req
	return nil
}
func (f *fakeStore) UpdateScheduledArticle(_ context.Context, _ string, req models.ScheduledArticleUpdateReq) error {
	f.gotSAUpdate = req
	return nil
}
func (f *fakeStore) SetScheduledArticlePublishAsLive(_ context.Context, _ string, live bool) error {
	f.publishAsLiveCalls = append(f.publishAsLiveCalls, live)
	return nil
}

// fakeS3 embeds interfaces.S3 and overrides only the methods SaveDraft uses.
type fakeS3 struct {
	interfaces.S3
	deleted []string
}

func (s *fakeS3) GetS3UrlFromBucketAndFileName(bucket, key string) string {
	return "https://" + bucket + ".s3.amazonaws.com/" + key
}
func (s *fakeS3) DeleteFile(_ context.Context, _ string, key string) error {
	s.deleted = append(s.deleted, key)
	return nil
}

const testBucket = "test-bucket"

func newFixture(images []models.CGEImage, content string) (*svc, *fakeStore, *fakeS3) {
	userID := primitive.NewObjectID()
	mcID := primitive.NewObjectID()
	st := &fakeStore{
		saFound: true,
		sa:      &models.ScheduledArticle{UserID: userID},
		mcFound: true,
		mc: &models.WebEntityMasterContext{
			ID:             mcID,
			Images:         images,
			ArticleContent: content,
		},
	}
	s3 := &fakeS3{}
	s := &svc{store: st, s3: s3, s3Bucket: testBucket}
	return s, st, s3
}

func userID(st *fakeStore) string { return st.sa.UserID.Hex() }
func mcID(st *fakeStore) string   { return st.mc.ID.Hex() }

// --- tests ---

func TestSaveDraft_AltOnlyEdit(t *testing.T) {
	s, st, s3 := newFixture(
		[]models.CGEImage{{Position: "thumbnail", S3Key: "generated_images/old.png", Alt: "old alt"}},
		"body",
	)

	err := s.SaveDraft(context.Background(), userID(st), "sa1", scheduledArticleService.SaveDraftPayload{
		ArticleContent: "body",
		Images:         []scheduledArticleService.SaveDraftImage{{Position: "thumbnail", Alt: "new alt"}},
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	if got := st.gotWEMCUpdate.Images; len(got) != 1 || got[0].Alt != "new alt" {
		t.Fatalf("alt not updated: %+v", got)
	}
	if got := st.gotWEMCUpdate.Images[0].S3Key; got != "generated_images/old.png" {
		t.Fatalf("s3 key should be untouched on alt-only edit, got %q", got)
	}
	if len(s3.deleted) != 0 {
		t.Fatalf("no deletes expected on alt-only edit, got %v", s3.deleted)
	}
}

func TestSaveDraft_ImageReplacement(t *testing.T) {
	oldKey := "generated_images/old.png"
	oldURL := "https://" + testBucket + ".s3.amazonaws.com/" + oldKey
	s, st, s3 := newFixture(
		[]models.CGEImage{{Position: "thumbnail", S3Key: oldKey, Alt: "old alt"}},
		"intro\n<img src=\""+oldURL+"\" alt=\"old alt\" />\noutro",
	)

	newKey := userUploadedImageKeyPrefix(userID(st), mcID(st), "thumbnail") + "-123.png"
	err := s.SaveDraft(context.Background(), userID(st), "sa1", scheduledArticleService.SaveDraftPayload{
		ArticleContent: "intro\n<img src=\"" + oldURL + "\" alt=\"old alt\" />\noutro",
		Images:         []scheduledArticleService.SaveDraftImage{{Position: "thumbnail", Alt: "new alt", NewS3Key: newKey}},
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	// Stored image swapped to the new key + alt.
	img := st.gotWEMCUpdate.Images[0]
	if img.S3Key != newKey || img.Alt != "new alt" {
		t.Fatalf("image not replaced: %+v", img)
	}

	// Body URL rewritten old → new.
	newURL := "https://" + testBucket + ".s3.amazonaws.com/" + newKey
	content := *st.gotWEMCUpdate.Edited
	if got := content.ArticleContent; !strings.Contains(got, newURL) || strings.Contains(got, oldURL) {
		t.Fatalf("body not rewritten to new url: %q", got)
	}

	// Old object deleted after the new pointer was persisted.
	if len(s3.deleted) != 1 || s3.deleted[0] != oldKey {
		t.Fatalf("old key should be deleted, got %v", s3.deleted)
	}
}

func TestSaveDraft_SplitsTitleAndThumbnailFromBody(t *testing.T) {
	s, st, _ := newFixture(
		[]models.CGEImage{{Position: "thumbnail", S3Key: "generated_images/thumb.png", Alt: "loaf"}},
		"", // stored body starts clean; the review UI sends the merged doc back
	)

	// The editor returns the merged document: thumbnail lead image, H1 title,
	// then the body (with the mid-article image left inline).
	merged := "![loaf](https://cdn/thumb.png)\n\n# New Title\n\nBody paragraph.\n\n![mid](https://cdn/mid.png)"
	err := s.SaveDraft(context.Background(), userID(st), "sa1", scheduledArticleService.SaveDraftPayload{
		ArticleContent: merged,
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	// Stored body has the title + thumbnail stripped, mid-article image kept.
	wantBody := "Body paragraph.\n\n![mid](https://cdn/mid.png)"
	if got := st.gotWEMCUpdate.Edited.ArticleContent; got != wantBody {
		t.Fatalf("stored body = %q, want %q", got, wantBody)
	}
	// The H1 is promoted to the scheduled article's title.
	if st.gotSAUpdate.Title == nil || *st.gotSAUpdate.Title != "New Title" {
		t.Fatalf("title not extracted to scheduled article: %+v", st.gotSAUpdate.Title)
	}
}

func TestSaveDraft_PromotesInlineKeyToThumbnail(t *testing.T) {
	oldKey := "generated_images/old-thumb.png"
	s, st, s3 := newFixture(
		[]models.CGEImage{{Position: "thumbnail", S3Key: oldKey, Alt: "old"}},
		"body",
	)

	// An inline-namespace key (no position slot) the user uploaded for this
	// article — the hero image they dropped above the title.
	inlineKey := userUploadedArticlePrefix(userID(st), mcID(st)) + "-inline-123.png"
	err := s.SaveDraft(context.Background(), userID(st), "sa1", scheduledArticleService.SaveDraftPayload{
		ArticleContent: "body",
		Images:         []scheduledArticleService.SaveDraftImage{{Position: "thumbnail", Alt: "hero", NewS3Key: inlineKey}},
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	if got := st.gotWEMCUpdate.Images[0]; got.S3Key != inlineKey || got.Alt != "hero" {
		t.Fatalf("thumbnail not promoted to inline key: %+v", got)
	}
	if len(s3.deleted) != 1 || s3.deleted[0] != oldKey {
		t.Fatalf("old thumbnail object should be deleted, got %v", s3.deleted)
	}
}

func TestSaveDraft_RejectsForeignImageKey(t *testing.T) {
	s, st, _ := newFixture(
		[]models.CGEImage{{Position: "thumbnail", S3Key: "generated_images/old.png", Alt: "old"}},
		"body",
	)

	// A key that doesn't belong to this user's user-uploaded namespace.
	err := s.SaveDraft(context.Background(), userID(st), "sa1", scheduledArticleService.SaveDraftPayload{
		ArticleContent: "body",
		Images:         []scheduledArticleService.SaveDraftImage{{Position: "thumbnail", Alt: "x", NewS3Key: "user-uploaded/someone-else-thumbnail-9.png"}},
	})
	if !errors.Is(err, scheduledArticleService.ErrInvalidImageKey) {
		t.Fatalf("expected ErrInvalidImageKey, got %v", err)
	}
}

func TestSaveDraft_RejectsUnknownPosition(t *testing.T) {
	s, st, _ := newFixture(
		[]models.CGEImage{{Position: "thumbnail", S3Key: "generated_images/old.png", Alt: "old"}},
		"body",
	)

	err := s.SaveDraft(context.Background(), userID(st), "sa1", scheduledArticleService.SaveDraftPayload{
		ArticleContent: "body",
		Images:         []scheduledArticleService.SaveDraftImage{{Position: "sidebar", Alt: "x"}},
	})
	if !errors.Is(err, scheduledArticleService.ErrInvalidImagePosition) {
		t.Fatalf("expected ErrInvalidImagePosition, got %v", err)
	}
}

func TestCreateImageUploadURL_RejectsBadContentType(t *testing.T) {
	s, st, _ := newFixture(nil, "body")

	_, err := s.CreateImageUploadURL(context.Background(), userID(st), "sa1", "thumbnail", "application/pdf")
	if !errors.Is(err, scheduledArticleService.ErrInvalidImageContentType) {
		t.Fatalf("expected ErrInvalidImageContentType, got %v", err)
	}
}

func TestCreateInlineImageUploadURL_RejectsBadContentType(t *testing.T) {
	s, st, _ := newFixture(nil, "body")

	_, err := s.CreateInlineImageUploadURL(context.Background(), userID(st), "sa1", "application/pdf")
	if !errors.Is(err, scheduledArticleService.ErrInvalidImageContentType) {
		t.Fatalf("expected ErrInvalidImageContentType, got %v", err)
	}
}

func TestCreateInlineImageUploadURL_RejectsForeignArticle(t *testing.T) {
	s, st, _ := newFixture(nil, "body")

	_, err := s.CreateInlineImageUploadURL(context.Background(), userID(st)+"different", "sa1", "image/png")
	if !errors.Is(err, scheduledArticleService.ErrScheduledArticleNotFound) {
		t.Fatalf("expected ErrScheduledArticleNotFound, got %v", err)
	}
}

// compile-time guard the fakes satisfy the interfaces they stand in for.
var (
	_ store.Store   = (*fakeStore)(nil)
	_ interfaces.S3 = (*fakeS3)(nil)
)
