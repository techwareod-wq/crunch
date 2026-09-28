package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/services/contentBridge"
	"github.com/atharva-ng/crunch/internal/services/contentBridge/platforms/sidecar"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// HandlePublish is the async worker entry point. It rebuilds the BlogPost from
// the scheduled article's master context, calls the pure PostBlogStructure, and
// persists the outcome (success or failure). Transient failures bubble up so the
// async handler retries / DLQs; persistence failures are logged best-effort
// rather than masking the publish result.
func (s *contentBridgeService) HandlePublish(ctx context.Context, userId string, payload contentBridge.PublishPayload) error {
	found, mc, err := s.store.GetMasterContextByScheduledArticleID(ctx, payload.ScheduledArticleID)
	if err != nil {
		return fmt.Errorf("content bridge: load master context: %w", err)
	}
	if !found || mc == nil {
		return fmt.Errorf("content bridge: no master context for scheduled article %s", payload.ScheduledArticleID)
	}

	// The canonical (user-editable) title lives on the ScheduledArticle doc, not
	// the master context — load it so it maps onto the platform's title field,
	// and reuse the same doc to resolve the per-article live/draft override.
	// Best-effort: a missing slot falls back to the pipeline's proposed title.
	var title string
	var sa *models.ScheduledArticle
	if saFound, loaded, saErr := s.store.GetScheduledArticle(ctx, payload.ScheduledArticleID); saErr != nil {
		log.Error("content_bridge.publish.load_scheduled_article_failed",
			"scheduledArticleId", payload.ScheduledArticleID, "err", saErr)
	} else if saFound && loaded != nil {
		sa = loaded
		title = sa.Title
	}

	// A single WebEntity load serves both the platform label (for the publish
	// state record) and the generic live/draft default. Best-effort: an empty
	// platform is acceptable — PostBlogStructure surfaces the real config error.
	var we *models.WebEntity
	if found, loaded, weErr := s.store.FindWebEntityByUserID(ctx, userId); weErr != nil {
		log.Error("content_bridge.publish.load_web_entity_failed", "userId", userId, "err", weErr)
	} else if found {
		we = loaded
	}
	platform := ""
	if we != nil && we.Publishing != nil {
		platform = we.Publishing.Platform
	}

	live := resolveLive(sa, we)
	// The scheduled calendar date rides onto the platform's date field so the CMS
	// records the day the article was planned for, not the day the publish runs. A
	// missing slot leaves it zero, and buildBlogPost/the sidecar fall back to now.
	var scheduleDate time.Time
	if sa != nil {
		scheduleDate = sa.ScheduleDate
	}
	// The triggering keyword's text maps onto an SEO focus-keyword field when
	// the platform has one. Best-effort: a lookup failure publishes without it.
	focusKeyword := ""
	if !mc.KeywordID.IsZero() {
		if kwFound, kw, kwErr := s.store.GetKeyword(ctx, mc.KeywordID.Hex()); kwErr != nil {
			log.Error("content_bridge.publish.load_keyword_failed",
				"keywordId", mc.KeywordID.Hex(), "err", kwErr)
		} else if kwFound && kw != nil {
			focusKeyword = kw.Keyword
		}
	}
	post := buildBlogPost(mc, title, scheduleDate, focusKeyword, s.resolveImageURL)
	post.Draft = !live
	attempts := publishAttempts(mc) + 1

	log.Info("content_bridge.publish.start",
		"userId", userId,
		"scheduledArticleId", payload.ScheduledArticleID,
		"masterContextId", mc.ID.Hex())

	start := time.Now()
	res, pubErr := s.PostBlogStructure(ctx, userId, post)
	if pubErr != nil {
		failed := models.CGEPublishState{
			Status:    models.CGEPublishStatusFailed,
			Platform:  platform,
			LastError: pubErr.Error(), // PublishError includes its code/category
			Attempts:  attempts,
			// Keep the idempotency key across failed attempts: without it a
			// retry would re-create the CMS item instead of updating it.
			RemoteItemID: post.RemoteItemID,
			Live:         live,
		}
		// Partial failure (item upserted, site publish failed): the sidecar
		// reports the new item id in the error details — persist it BEFORE
		// surfacing the error so the retry updates rather than duplicates.
		var pe *sidecar.PublishError
		if errors.As(pubErr, &pe) && pe.RemoteItemID != "" {
			failed.RemoteItemID = pe.RemoteItemID
		}
		s.persistState(ctx, mc.ID.Hex(), failed)
		// A failed publish must not leave the article badged "published" from an
		// earlier success: the remote item may be gone (deleted in the CMS), which
		// is one of the ways we get here. Only Published is reverted — a Draft
		// article keeps its own state — and the badge returns on the next success.
		if sa != nil && sa.Status == models.ScheduledArticleStatusPublished {
			if statusErr := s.store.SetScheduledArticleStatus(ctx, payload.ScheduledArticleID, models.ScheduledArticleStatusReadyForReview); statusErr != nil {
				log.Error("content_bridge.publish.status_revert_failed",
					"userId", userId,
					"scheduledArticleId", payload.ScheduledArticleID,
					"err", statusErr)
			}
		}
		log.Error("content_bridge.publish.fail",
			"userId", userId,
			"masterContextId", mc.ID.Hex(),
			"attempt", attempts,
			"latencyMs", time.Since(start).Milliseconds(),
			"err", pubErr)
		// Permanent sidecar failures (revoked key, unmappable required field)
		// short-circuit the async retry loop straight to the DLQ.
		if sidecar.IsPermanent(pubErr) {
			return fmt.Errorf("%w: %w", pipeline.ErrPermanent, pubErr)
		}
		return pubErr // async handler retries / DLQs
	}

	s.persistState(ctx, mc.ID.Hex(), models.CGEPublishState{
		Status:        models.CGEPublishStatusPublished,
		Platform:      platform,
		RemoteItemID:  res.RemoteItemID,
		RemoteURL:     res.RemoteURL,
		PublishedAt:   time.Now(),
		Attempts:      attempts,
		SitePublished: res.SitePublished, // false for drafts (publishSite=false)
		Live:          live,
	})
	if statusErr := s.store.SetScheduledArticleStatus(ctx, payload.ScheduledArticleID, models.ScheduledArticleStatusPublished); statusErr != nil {
		log.Error("content_bridge.publish.status_mirror_failed",
			"userId", userId,
			"scheduledArticleId", payload.ScheduledArticleID,
			"err", statusErr)
	}

	log.Info("content_bridge.publish.ok",
		"userId", userId,
		"masterContextId", mc.ID.Hex(),
		"remoteItemId", res.RemoteItemID,
		"remoteUrl", res.RemoteURL,
		"updated", res.Updated,
		"latencyMs", time.Since(start).Milliseconds())
	return nil
}

// persistState writes the publish block, logging (but not returning) a write
// failure — the publish outcome itself has already been decided by the caller.
func (s *contentBridgeService) persistState(ctx context.Context, masterContextID string, state models.CGEPublishState) {
	if err := s.store.SetPublishState(ctx, masterContextID, state); err != nil {
		log.Error("content_bridge.publish.state_write_failed",
			"masterContextId", masterContextID,
			"status", state.Status,
			"err", err)
	}
}

// resolveLive applies the precedence: per-article override, then the WebEntity
// generic default, then the product default (draft = false). The single source
// of truth for whether a publish goes live. See [[Publish Draft State]].
func resolveLive(sa *models.ScheduledArticle, we *models.WebEntity) bool {
	if sa != nil && sa.PublishAsLive != nil {
		return *sa.PublishAsLive
	}
	if we != nil && we.PublishAsLive != nil {
		return *we.PublishAsLive
	}
	return false // product default: push as draft
}

func publishAttempts(mc *models.WebEntityMasterContext) int {
	if mc.Publish == nil {
		return 0
	}
	return mc.Publish.Attempts
}
