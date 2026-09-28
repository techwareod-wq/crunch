package models

import (
	"context"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/atharva-ng/crunch/internal/utils"
)

// NormalizedURLBackfillReport summarises one BackfillPublishNormalizedURL run.
type NormalizedURLBackfillReport struct {
	Scanned int      // master contexts with a publish block and no normalized_url
	Stamped int      // docs whose normalized_url was (or would be) written
	Skipped int      // docs where neither RemoteURL nor WebsiteURL+slug resolved
	Errors  []string // per-item failures (the run continues)
}

// BackfillPublishNormalizedURL stamps publish.normalized_url on master
// contexts published before the stamp existed (GSC plan Step 5), and
// RE-stamps docs whose stored key predates a derivation fix: CMS-admin
// RemoteURLs (Payload's /admin/… management link, never a public page) and
// www-carrying keys (the normalizer now collapses www). Same precedence as
// the live write path (SetCGEPublishState): public RemoteURL when present,
// else WebsiteURL + resolved slug, both through utils.NormalizeGSCPageURL.
// Uses a dotted-path $set, so nothing else in the publish block is touched.
// Re-runnable: correctly-stamped docs derive to their stored value and skip.
func BackfillPublishNormalizedURL(ctx context.Context, dryRun bool) (*NormalizedURLBackfillReport, error) {
	report := &NormalizedURLBackfillReport{}

	cur, err := Collection(webEntityMasterContextCollection).Find(ctx, bson.M{
		"publish": bson.M{"$exists": true},
		"$or": []bson.M{
			{"publish.normalized_url": bson.M{"$exists": false}},
			{"publish.normalized_url": bson.M{"$regex": `^https://[^/]*/(admin|wp-admin)(/|$)`}},
			{"publish.normalized_url": bson.M{"$regex": `^https://www\.`}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("find unstamped master contexts: %w", err)
	}
	defer cur.Close(ctx)

	var ops []mongo.WriteModel
	for cur.Next(ctx) {
		var mc WebEntityMasterContext
		if err := cur.Decode(&mc); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("decode master context: %v", err))
			continue
		}
		report.Scanned++

		normalized := ""
		if mc.Publish != nil {
			if remote := PublicRemoteURL(mc.Publish.RemoteURL); remote != "" {
				normalized = utils.NormalizeGSCPageURL(remote)
			}
		}
		if normalized == "" {
			if slug := mc.EffectiveSlug(); mc.WebsiteURL != "" && slug != "" {
				normalized = utils.NormalizeGSCPageURL(strings.TrimRight(mc.WebsiteURL, "/") + "/" + slug)
			}
		}
		if normalized == "" || (mc.Publish != nil && mc.Publish.NormalizedURL == normalized) {
			report.Skipped++
			continue
		}

		report.Stamped++
		if dryRun {
			continue
		}
		ops = append(ops, mongo.NewUpdateOneModel().
			SetFilter(bson.M{"_id": mc.ID}).
			SetUpdate(bson.M{"$set": bson.M{"publish.normalized_url": normalized}}))
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("iterate unstamped master contexts: %w", err)
	}

	if !dryRun && len(ops) > 0 {
		if _, err := Collection(webEntityMasterContextCollection).BulkWrite(ctx, ops,
			options.BulkWrite().SetOrdered(false)); err != nil {
			return nil, fmt.Errorf("bulk stamp normalized urls: %w", err)
		}
	}
	return report, nil
}
