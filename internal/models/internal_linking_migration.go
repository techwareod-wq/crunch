package models

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
)

// BackfillInternalLinkingDefaults sets internal_linking_enabled = true on every
// document that predates the field, so existing data behaves consistently with
// the on-by-default rule and the settings/sidebar toggles render the right
// state. Two collections are touched:
//
//   - webEntity: every doc missing the field (the web-entity-wide default).
//   - scheduledArticle: docs missing the field that are still pre-generation
//     (scheduled / scheduling) — the only slots whose toggle still affects a
//     future generation run. Already-generated slots are left untouched.
//
// On a dry run it returns the would-change counts without writing; on apply it
// returns the modified counts.
func BackfillInternalLinkingDefaults(ctx context.Context, dryRun bool) (webEntities int64, scheduledArticles int64, err error) {
	weFilter := bson.M{"internal_linking_enabled": bson.M{"$exists": false}}
	saFilter := bson.M{
		"internal_linking_enabled": bson.M{"$exists": false},
		"status": bson.M{"$in": bson.A{
			int(ScheduledArticleStatusScheduled),
			int(ScheduledArticleStatusScheduling),
		}},
	}

	if dryRun {
		webEntities, err = Collection(webEntityCollection).CountDocuments(ctx, weFilter)
		if err != nil {
			return 0, 0, fmt.Errorf("count web entities: %w", err)
		}
		scheduledArticles, err = Collection(scheduledArticleCollection).CountDocuments(ctx, saFilter)
		if err != nil {
			return webEntities, 0, fmt.Errorf("count scheduled articles: %w", err)
		}
		return webEntities, scheduledArticles, nil
	}

	set := bson.M{"$set": bson.M{"internal_linking_enabled": true}}

	weRes, err := Collection(webEntityCollection).UpdateMany(ctx, weFilter, set)
	if err != nil {
		return 0, 0, fmt.Errorf("update web entities: %w", err)
	}
	saRes, err := Collection(scheduledArticleCollection).UpdateMany(ctx, saFilter, set)
	if err != nil {
		return weRes.ModifiedCount, 0, fmt.Errorf("update scheduled articles: %w", err)
	}
	return weRes.ModifiedCount, saRes.ModifiedCount, nil
}
