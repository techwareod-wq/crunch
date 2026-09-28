package service

import (
	"context"
	"fmt"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	se "github.com/atharva-ng/crunch/internal/services/schedulingEngine"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// ScheduleArticle persists a single user-initiated calendar slot and kicks off
// title generation for it. Unlike the bulk Orchestrate flow, the article type
// is supplied by the user, so only the title step runs. The slot is created in
// the transient ScheduledArticleStatusScheduling state and flips to
// ScheduledArticleStatusScheduled once GenerateTitle persists the title.
//
// Ownership and identity are resolved server-side: the keyword text is matched
// against the user's web entity context so callers can't schedule against
// keywords they don't own.
//
// A keyword may be scheduled any number of times — manual reuse of an
// already-used keyword is allowed by design. Only the bulk auto-scheduling
// flows skip keywords that already carry an article.
func (s *schedulingEngineService) ScheduleArticle(ctx context.Context, userId string, params se.ScheduleArticleParams) (*models.ScheduledArticle, *models.Keyword, error) {
	if _, valid := models.ParseArticleType(string(params.ArticleType)); !valid {
		return nil, nil, se.ErrInvalidArticleType
	}

	// Normalize to UTC midnight for storage. Past, today, and future are all
	// valid — same contract as dashboard edit/reschedule. Same-day slots still
	// get an auto-publish target an hour out (set below).
	scheduleDate := params.ScheduleDate.UTC()
	now := time.Now().UTC()
	todayUTC := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	ok, wec, err := s.store.GetWebEntityContextForUser(ctx, params.WebEntityID, userId)
	if err != nil {
		return nil, nil, fmt.Errorf("get web entity context: %w", err)
	}
	if !ok {
		return nil, nil, se.ErrWebEntityContextNotFound
	}

	ok, kw, err := s.store.GetKeyword(ctx, params.KeywordID)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve keyword: %w", err)
	}
	// The keyword must belong to the user's resolved context — guards against
	// scheduling against a keyword id from another user's WEC.
	if !ok || kw.WebEntityContextID != wec.ID {
		return nil, nil, se.ErrKeywordNotFound
	}

	userOID, err := primitive.ObjectIDFromHex(userId)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid user ID: %w", err)
	}
	webEntityOID, err := primitive.ObjectIDFromHex(params.WebEntityID)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid web entity ID: %w", err)
	}

	// Snapshot the web entity's internal-linking default onto the slot so the
	// per-article toggle can be edited independently later.
	_, we, err := s.store.GetWebEntityByID(ctx, params.WebEntityID)
	if err != nil {
		return nil, nil, fmt.Errorf("get web entity: %w", err)
	}
	internalLinking := we.InternalLinkingEnabledOrDefault()

	sa := &models.ScheduledArticle{
		KeywordID:              kw.ID,
		UserID:                 userOID,
		WebEntityID:            webEntityOID,
		WebEntityContextID:     wec.ID,
		ArticleType:            params.ArticleType,
		ScheduleDate:           scheduleDate,
		InternalLinkingEnabled: &internalLinking,
		Status:                 models.ScheduledArticleStatusScheduling,
	}
	// Same-day slot: target auto-publish an hour from now so the deferred
	// publish runner has a concrete instant. Past/future-dated slots leave
	// PublishAt nil (the runner picks the time for future; past needs no stamp).
	if scheduleDate.Equal(todayUTC) {
		publishAt := now.Add(time.Hour)
		sa.PublishAt = &publishAt
	}
	if err := s.store.CreateScheduledArticle(ctx, sa); err != nil {
		return nil, nil, fmt.Errorf("persist scheduled article: %w", err)
	}

	msg := se.SEArticleStepPayload{ScheduledArticleID: sa.ID.Hex()}
	if err := s.dispatcher.Dispatch(ctx, string(se.ProcessSchedulingGenerateTitle), userId, msg); err != nil {
		return nil, nil, fmt.Errorf("dispatch title generation: %w", err)
	}

	return sa, kw, nil
}
