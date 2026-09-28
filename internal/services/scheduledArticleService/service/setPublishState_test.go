package service

import (
	"context"
	"errors"
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/scheduledArticleService"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func newPublishStateFixture(status models.ScheduledArticleStatus) (*svc, *fakeStore) {
	st := &fakeStore{
		saFound: true,
		sa: &models.ScheduledArticle{
			UserID: primitive.NewObjectID(),
			Status: status,
		},
	}
	return &svc{store: st}, st
}

func TestSetPublishState_PublishedIsAllowed(t *testing.T) {
	// Q9: the control must remain editable when published (drives Republish).
	s, st := newPublishStateFixture(models.ScheduledArticleStatusPublished)

	if err := s.SetPublishState(context.Background(), userID(st), "sa1", true); err != nil {
		t.Fatalf("SetPublishState on a published article should be allowed, got %v", err)
	}
	if len(st.publishAsLiveCalls) != 1 || st.publishAsLiveCalls[0] != true {
		t.Fatalf("expected one write of true, got %v", st.publishAsLiveCalls)
	}
}

func TestSetPublishState_GeneratingIsRejected(t *testing.T) {
	s, st := newPublishStateFixture(models.ScheduledArticleStatusGenerating)

	err := s.SetPublishState(context.Background(), userID(st), "sa1", false)
	if !errors.Is(err, scheduledArticleService.ErrScheduledArticleNotEditable) {
		t.Fatalf("expected ErrScheduledArticleNotEditable while generating, got %v", err)
	}
	if len(st.publishAsLiveCalls) != 0 {
		t.Fatalf("no write expected when rejected, got %v", st.publishAsLiveCalls)
	}
}

func TestSetPublishState_ForeignArticleIsNotFound(t *testing.T) {
	s, st := newPublishStateFixture(models.ScheduledArticleStatusScheduled)

	err := s.SetPublishState(context.Background(), userID(st)+"different", "sa1", true)
	if !errors.Is(err, scheduledArticleService.ErrScheduledArticleNotFound) {
		t.Fatalf("expected ErrScheduledArticleNotFound for a foreign article, got %v", err)
	}
	if len(st.publishAsLiveCalls) != 0 {
		t.Fatalf("no write expected on ownership failure, got %v", st.publishAsLiveCalls)
	}
}

func TestSetPublishState_MissingArticleIsNotFound(t *testing.T) {
	st := &fakeStore{saFound: false}
	s := &svc{store: st}

	err := s.SetPublishState(context.Background(), "user1", "sa1", true)
	if !errors.Is(err, scheduledArticleService.ErrScheduledArticleNotFound) {
		t.Fatalf("expected ErrScheduledArticleNotFound for a missing article, got %v", err)
	}
}
