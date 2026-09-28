package models

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// FreePlanBackfillReport summarises one BackfillFreePlan run.
type FreePlanBackfillReport struct {
	EntitiesScanned int      // distinct web entities considered for counter init
	CountersInit    int      // entities whose lifetime counter was raised
	ArticlesFlagged int      // scheduled articles marked generation_counted
	TrialsExtended  int      // live local trials pushed to the far-future valid_till
	Recomputed      int      // entitlement projections recomputed
	Errors          []string // per-item failures (the run continues)
}

// BackfillFreePlan migrates existing data onto the free-plan model (trial
// never lapses by date; a monotonic lifetime article counter backs the cap):
//
//  1. For every web entity owning a generation-started scheduled article,
//     raise its lifetime_articles_generated to at least the live-doc count
//     ($max — never lowers, so the run is re-runnable) and mark those articles
//     generation_counted so a later retry can't double-charge them.
//  2. Push every source:"trial" status:"trialing" subscription's valid_till to
//     LocalTrialValidTill(now). This includes trials already lapsed by date —
//     card-less trials keep status "trialing" forever (expiry was purely the
//     resolver's time check), so previously-expired trial users regain access,
//     capped by the article counter like everyone else.
//  3. Recompute the entitlement projection for each extended trial's user.
//
// Counters are initialised BEFORE trials are extended so a lapsed-trial user
// can't sneak an uncounted generation in between the two passes. Articles
// deleted before this run are gone and cannot be counted — that slack favors
// the user once, then the monotonic counter takes over.
func BackfillFreePlan(ctx context.Context, dryRun bool) (*FreePlanBackfillReport, error) {
	report := &FreePlanBackfillReport{}

	startedStatuses := bson.A{
		ScheduledArticleStatusGenerating,
		ScheduledArticleStatusReadyForReview,
		ScheduledArticleStatusDraft,
		ScheduledArticleStatusPublished,
	}

	// Pass 1: counter init + per-article claim flags.
	entityIDs, err := Collection(scheduledArticleCollection).Distinct(ctx, "web_entity_id",
		bson.M{"status": bson.M{"$in": startedStatuses}})
	if err != nil {
		return nil, fmt.Errorf("distinct entities with started articles: %w", err)
	}
	for _, raw := range entityIDs {
		entityID, ok := raw.(primitive.ObjectID)
		if !ok {
			continue
		}
		report.EntitiesScanned++

		used, err := CountGenerationStartedArticlesForEntity(ctx, entityID)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("count articles for %s: %v", entityID.Hex(), err))
			continue
		}
		current, err := GetLifetimeArticlesGeneratedForEntity(ctx, entityID)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("read counter for %s: %v", entityID.Hex(), err))
			continue
		}
		if current < used {
			report.CountersInit++
		}
		unflagged, err := Collection(scheduledArticleCollection).CountDocuments(ctx, bson.M{
			"web_entity_id":      entityID,
			"status":             bson.M{"$in": startedStatuses},
			"generation_counted": bson.M{"$ne": true},
		})
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("count unflagged articles for %s: %v", entityID.Hex(), err))
			continue
		}
		report.ArticlesFlagged += int(unflagged)
		if dryRun {
			continue
		}

		if _, err := Collection(webEntityCollection).UpdateOne(ctx,
			bson.M{"_id": entityID},
			bson.M{"$max": bson.M{"lifetime_articles_generated": used}},
		); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("init counter for %s: %v", entityID.Hex(), err))
			continue
		}
		if _, err := Collection(scheduledArticleCollection).UpdateMany(ctx,
			bson.M{
				"web_entity_id":      entityID,
				"status":             bson.M{"$in": startedStatuses},
				"generation_counted": bson.M{"$ne": true},
			},
			bson.M{"$set": bson.M{"generation_counted": true}},
		); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("flag articles for %s: %v", entityID.Hex(), err))
		}
	}

	// Pass 2 + 3: extend live local trials and recompute their projections.
	farFuture := LocalTrialValidTill(time.Now().UTC())
	cursor, err := Collection(subscriptionsCollection).Find(ctx, bson.M{
		"source":     SubSourceTrial,
		"status":     SubStatusTrialing,
		"valid_till": bson.M{"$lt": farFuture},
	})
	if err != nil {
		return nil, fmt.Errorf("find local trials: %w", err)
	}
	var trials []Subscription
	if err := cursor.All(ctx, &trials); err != nil {
		return nil, fmt.Errorf("decode local trials: %w", err)
	}
	for _, sub := range trials {
		report.TrialsExtended++
		if dryRun {
			continue
		}
		if err := SetTrialValidTill(ctx, sub.PaddleSubscriptionID, farFuture); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("extend trial %s: %v", sub.PaddleSubscriptionID, err))
			continue
		}
		appID := sub.AppID
		if appID == "" {
			appID = AppIDIndexly
		}
		if err := RecomputeUserEntitlement(ctx, sub.UserID, appID); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("recompute %s/%s: %v", sub.UserID.Hex(), appID, err))
			continue
		}
		report.Recomputed++
	}

	return report, nil
}
