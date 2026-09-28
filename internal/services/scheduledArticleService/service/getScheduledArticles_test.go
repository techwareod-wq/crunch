package service

import (
	"context"
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// --- list-path fakes (methods; fields live on fakeStore in saveDraft_test.go) ---

func (f *fakeStore) GetWebEntityByID(context.Context, string) (bool, *models.WebEntity, error) {
	return f.weFound, f.we, nil
}
func (f *fakeStore) GetScheduledArticlesByWebEntityInRange(context.Context, string, string, time.Time, time.Time) ([]*models.ScheduledArticle, error) {
	return f.articlesInRange, nil
}
func (f *fakeStore) GetMasterContextsByScheduledArticleIDs(context.Context, []string) ([]models.WebEntityMasterContext, error) {
	return f.mcsByID, nil
}
func (f *fakeStore) GetKeywordsByIDs(context.Context, []primitive.ObjectID) ([]models.Keyword, error) {
	f.keywordsByIDsCalls++
	return f.keywordsByIDs, nil
}

// newListFixture builds an svc + fakeStore around a single scheduled article
// owned by `owner`, optionally paired with a master context carrying a publish
// block. The article references a keyword so the full list path has something
// to hydrate.
func newListFixture(owner primitive.ObjectID, mc *models.WebEntityMasterContext) (*svc, *fakeStore) {
	saID := primitive.NewObjectID()
	keywordID := primitive.NewObjectID()
	weID := primitive.NewObjectID()

	sa := &models.ScheduledArticle{
		ID:           saID,
		KeywordID:    keywordID,
		UserID:       owner,
		WebEntityID:  weID,
		Title:        "Some Title",
		Status:       models.ScheduledArticleStatusReadyForReview,
		ScheduleDate: time.Now().UTC(),
	}

	st := &fakeStore{
		weFound:         true,
		we:              &models.WebEntity{UserID: owner},
		articlesInRange: []*models.ScheduledArticle{sa},
		keywordsByIDs: []models.Keyword{
			{ID: keywordID, Keyword: "some keyword", Volume: 1234},
		},
	}
	if mc != nil {
		mc.ScheduledArticleID = saID
		st.mcsByID = []models.WebEntityMasterContext{*mc}
	}
	return &svc{store: st}, st
}

func TestGetScheduledArticles_IncludesPublishOutcome(t *testing.T) {
	owner := primitive.NewObjectID()
	published := &models.WebEntityMasterContext{
		WordCount: 900,
		Publish: &models.CGEPublishState{
			Status:    models.CGEPublishStatusPublished,
			RemoteURL: "https://example.com/live",
			Attempts:  1,
			Live:      true,
		},
	}
	s, st := newListFixture(owner, published)

	resp, err := s.GetScheduledArticles(context.Background(), owner.Hex(), st.we.UserID.Hex(), nil, nil)
	if err != nil {
		t.Fatalf("GetScheduledArticles: %v", err)
	}
	if len(resp.Articles) != 1 {
		t.Fatalf("expected 1 article, got %d", len(resp.Articles))
	}
	pub := resp.Articles[0].Publish
	if pub == nil {
		t.Fatal("expected a publish block on the list row")
	}
	if pub.Status != models.CGEPublishStatusPublished || pub.RemoteURL != "https://example.com/live" || !pub.Live {
		t.Fatalf("published block not mapped through: %+v", pub)
	}
}

func TestGetScheduledArticles_IncludesFailedPublishOutcome(t *testing.T) {
	owner := primitive.NewObjectID()
	failed := &models.WebEntityMasterContext{
		WordCount: 0,
		Publish: &models.CGEPublishState{
			Status:    models.CGEPublishStatusFailed,
			LastError: "framer rejected the item",
			Attempts:  2,
		},
	}
	s, st := newListFixture(owner, failed)

	resp, err := s.GetScheduledArticles(context.Background(), owner.Hex(), st.we.UserID.Hex(), nil, nil)
	if err != nil {
		t.Fatalf("GetScheduledArticles: %v", err)
	}
	pub := resp.Articles[0].Publish
	if pub == nil || pub.Status != models.CGEPublishStatusFailed {
		t.Fatalf("expected a failed publish block, got %+v", pub)
	}
	if pub.LastError != "framer rejected the item" || pub.Attempts != 2 {
		t.Fatalf("failed publish block not mapped through: %+v", pub)
	}
}

func TestGetScheduledArticles_NoPublishBlockWhenUnpublished(t *testing.T) {
	owner := primitive.NewObjectID()
	// A master context with content but no publish attempt yet.
	s, st := newListFixture(owner, &models.WebEntityMasterContext{WordCount: 500})

	resp, err := s.GetScheduledArticles(context.Background(), owner.Hex(), st.we.UserID.Hex(), nil, nil)
	if err != nil {
		t.Fatalf("GetScheduledArticles: %v", err)
	}
	if resp.Articles[0].Publish != nil {
		t.Fatalf("expected no publish block before first attempt, got %+v", resp.Articles[0].Publish)
	}
}

func TestGetScheduledArticleStatuses_SkipsKeywordHydration(t *testing.T) {
	owner := primitive.NewObjectID()
	s, st := newListFixture(owner, &models.WebEntityMasterContext{
		WordCount: 700,
		Publish: &models.CGEPublishState{
			Status:   models.CGEPublishStatusPublished,
			Attempts: 1,
			Live:     false,
		},
	})

	resp, err := s.GetScheduledArticleStatuses(context.Background(), owner.Hex(), st.we.UserID.Hex(), nil, nil)
	if err != nil {
		t.Fatalf("GetScheduledArticleStatuses: %v", err)
	}
	if st.keywordsByIDsCalls != 0 {
		t.Fatalf("view=status must not hydrate keywords, GetKeywordsByIDs called %d times", st.keywordsByIDsCalls)
	}
	if len(resp.Articles) != 1 {
		t.Fatalf("expected 1 status row, got %d", len(resp.Articles))
	}
	row := resp.Articles[0]
	// Slim view surfaces status, title, word count + publish outcome...
	if row.Title != "Some Title" || row.WordCount != 700 {
		t.Fatalf("status row missing expected fields: %+v", row)
	}
	if row.Publish == nil || row.Publish.Status != models.CGEPublishStatusPublished {
		t.Fatalf("status row missing publish outcome: %+v", row.Publish)
	}
}

func TestGetScheduledArticleStatuses_DerivesErrorStatus(t *testing.T) {
	owner := primitive.NewObjectID()
	s, st := newListFixture(owner, &models.WebEntityMasterContext{Status: models.CGEStatusError})

	resp, err := s.GetScheduledArticleStatuses(context.Background(), owner.Hex(), st.we.UserID.Hex(), nil, nil)
	if err != nil {
		t.Fatalf("GetScheduledArticleStatuses: %v", err)
	}
	if got := resp.Articles[0].Status; got != "error" {
		t.Fatalf("expected wire-only 'error' status, got %q", got)
	}
}
