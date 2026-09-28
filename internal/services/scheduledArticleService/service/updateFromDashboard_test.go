package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/scheduledArticleService"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// newDashboardFixture builds an svc + fakeStore around a single scheduled
// article. UpdateFromDashboard only touches GetScheduledArticle /
// UpdateScheduledArticle on the store, both already faked in saveDraft_test.go.
func newDashboardFixture(status models.ScheduledArticleStatus, currentDate time.Time) (*svc, *fakeStore) {
	st := &fakeStore{
		saFound: true,
		sa: &models.ScheduledArticle{
			UserID:       primitive.NewObjectID(),
			Status:       status,
			ScheduleDate: currentDate,
		},
	}
	return &svc{store: st}, st
}

func ptrTime(t time.Time) *time.Time { return &t }

func strPtr(s string) *string { return &s }

func TestUpdateFromDashboard_RescheduleToToday_StampsPublishAt(t *testing.T) {
	today := startOfDayUTC(time.Now())
	// Current date is in the future so the scheduled row is editable.
	s, st := newDashboardFixture(models.ScheduledArticleStatusScheduled, today.AddDate(0, 0, 5))

	before := time.Now()
	err := s.UpdateFromDashboard(context.Background(), userID(st), "sa1", scheduledArticleService.DashboardEditPayload{
		ScheduleDate: ptrTime(today),
	})
	if err != nil {
		t.Fatalf("UpdateFromDashboard: %v", err)
	}

	if st.gotSAUpdate.ScheduleDate == nil || !st.gotSAUpdate.ScheduleDate.Equal(today) {
		t.Fatalf("schedule date not set to today midnight UTC: %+v", st.gotSAUpdate.ScheduleDate)
	}
	if st.gotSAUpdate.PublishAt == nil {
		t.Fatalf("expected PublishAt to be stamped for a same-day reschedule")
	}
	// PublishAt is now + 1h — assert it lands roughly an hour out.
	gotPublish := *st.gotSAUpdate.PublishAt
	if gotPublish.Before(before.Add(time.Hour-time.Minute)) || gotPublish.After(time.Now().Add(time.Hour+time.Minute)) {
		t.Fatalf("PublishAt should be ~now+1h, got %v", gotPublish)
	}
}

func TestUpdateFromDashboard_RescheduleToFuture_NoPublishAt(t *testing.T) {
	today := startOfDayUTC(time.Now())
	s, st := newDashboardFixture(models.ScheduledArticleStatusScheduled, today.AddDate(0, 0, 5))

	err := s.UpdateFromDashboard(context.Background(), userID(st), "sa1", scheduledArticleService.DashboardEditPayload{
		ScheduleDate: ptrTime(today.AddDate(0, 0, 3)),
	})
	if err != nil {
		t.Fatalf("UpdateFromDashboard: %v", err)
	}
	if st.gotSAUpdate.PublishAt != nil {
		t.Fatalf("future-dated reschedule must not stamp PublishAt, got %v", *st.gotSAUpdate.PublishAt)
	}
}

func TestUpdateFromDashboard_RescheduleToPast_Allowed(t *testing.T) {
	today := startOfDayUTC(time.Now())
	past := today.AddDate(0, 0, -1)
	s, st := newDashboardFixture(models.ScheduledArticleStatusScheduled, today.AddDate(0, 0, 5))

	err := s.UpdateFromDashboard(context.Background(), userID(st), "sa1", scheduledArticleService.DashboardEditPayload{
		ScheduleDate: ptrTime(past),
	})
	if err != nil {
		t.Fatalf("UpdateFromDashboard past date: %v", err)
	}
	if st.gotSAUpdate.ScheduleDate == nil || !st.gotSAUpdate.ScheduleDate.Equal(past) {
		t.Fatalf("expected schedule date %v, got %+v", past, st.gotSAUpdate.ScheduleDate)
	}
	if st.gotSAUpdate.PublishAt != nil {
		t.Fatalf("past-dated reschedule must not stamp PublishAt, got %v", *st.gotSAUpdate.PublishAt)
	}
}

func TestUpdateFromDashboard_PastScheduledRow_StillEditable(t *testing.T) {
	today := startOfDayUTC(time.Now())
	s, st := newDashboardFixture(models.ScheduledArticleStatusScheduled, today.AddDate(0, 0, -3))

	err := s.UpdateFromDashboard(context.Background(), userID(st), "sa1", scheduledArticleService.DashboardEditPayload{
		Title: strPtr("Retitled past slot"),
	})
	if err != nil {
		t.Fatalf("past scheduled row should stay editable, got %v", err)
	}
	if st.gotSAUpdate.Title == nil || *st.gotSAUpdate.Title != "Retitled past slot" {
		t.Fatalf("expected title update, got %+v", st.gotSAUpdate.Title)
	}
}

func TestUpdateFromDashboard_ThumbnailStyle_SetsOverride(t *testing.T) {
	today := startOfDayUTC(time.Now())
	s, st := newDashboardFixture(models.ScheduledArticleStatusScheduled, today.AddDate(0, 0, 5))

	err := s.UpdateFromDashboard(context.Background(), userID(st), "sa1", scheduledArticleService.DashboardEditPayload{
		ThumbnailStyle: strPtr("blueprint"),
	})
	if err != nil {
		t.Fatalf("UpdateFromDashboard: %v", err)
	}
	if st.gotSAUpdate.ThumbnailStyle == nil || *st.gotSAUpdate.ThumbnailStyle != "blueprint" {
		t.Fatalf("expected thumbnail style override 'blueprint', got %+v", st.gotSAUpdate.ThumbnailStyle)
	}
}

func TestUpdateFromDashboard_ThumbnailStyle_ResetToInherit(t *testing.T) {
	today := startOfDayUTC(time.Now())
	s, st := newDashboardFixture(models.ScheduledArticleStatusScheduled, today.AddDate(0, 0, 5))

	err := s.UpdateFromDashboard(context.Background(), userID(st), "sa1", scheduledArticleService.DashboardEditPayload{
		ThumbnailStyle: strPtr(""),
	})
	if err != nil {
		t.Fatalf("UpdateFromDashboard: %v", err)
	}
	// Empty string flows through as a non-nil empty pointer — the store maps it
	// to $unset (reset to inherit).
	if st.gotSAUpdate.ThumbnailStyle == nil || *st.gotSAUpdate.ThumbnailStyle != "" {
		t.Fatalf("expected reset-to-inherit (non-nil empty), got %+v", st.gotSAUpdate.ThumbnailStyle)
	}
}

func TestUpdateFromDashboard_ThumbnailStyle_InvalidRejected(t *testing.T) {
	today := startOfDayUTC(time.Now())
	s, st := newDashboardFixture(models.ScheduledArticleStatusScheduled, today.AddDate(0, 0, 5))

	err := s.UpdateFromDashboard(context.Background(), userID(st), "sa1", scheduledArticleService.DashboardEditPayload{
		ThumbnailStyle: strPtr("not-a-real-style"),
	})
	if !errors.Is(err, scheduledArticleService.ErrInvalidThumbnailStyle) {
		t.Fatalf("expected ErrInvalidThumbnailStyle, got %v", err)
	}
}

func TestUpdateFromDashboard_ThumbnailStyle_GatedAfterGeneration(t *testing.T) {
	today := startOfDayUTC(time.Now())
	// A draft (post-generation) row: the brief fields (incl. thumbnail style) are
	// locked, so an attempt to change the style is rejected.
	s, st := newDashboardFixture(models.ScheduledArticleStatusDraft, today.AddDate(0, 0, 5))

	err := s.UpdateFromDashboard(context.Background(), userID(st), "sa1", scheduledArticleService.DashboardEditPayload{
		ThumbnailStyle: strPtr("blueprint"),
	})
	if !errors.Is(err, scheduledArticleService.ErrFieldNotEditableForStatus) {
		t.Fatalf("expected ErrFieldNotEditableForStatus for a draft, got %v", err)
	}
}

func TestResolveOrchestrateContext_PassesThumbnailStyleThrough(t *testing.T) {
	style := "blueprint"
	st := &fakeStore{
		saFound: true,
		sa: &models.ScheduledArticle{
			UserID:         primitive.NewObjectID(),
			ThumbnailStyle: &style,
		},
	}
	s := &svc{store: st}

	resolved, err := s.ResolveOrchestrateContext(context.Background(), userID(st), "sa1")
	if err != nil {
		t.Fatalf("ResolveOrchestrateContext: %v", err)
	}
	if resolved.ThumbnailStyle == nil || *resolved.ThumbnailStyle != "blueprint" {
		t.Fatalf("expected thumbnail style 'blueprint' passed through, got %+v", resolved.ThumbnailStyle)
	}
}
