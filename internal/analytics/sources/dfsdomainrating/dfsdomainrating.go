// Package dfsdomainrating is the monthly domain-authority analytics source
// (LLD §4.2): one DataForSEO backlinks-summary call per finalised entity per
// month, normalized to a single grain="domain_rating" fact. Connection-less —
// the DataForSEO credentials are ours, so the cron resolver covers all
// finalised entities and no user action is required.
package dfsdomainrating

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/atharva-ng/crunch/internal/analytics"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/utils"
)

// SourceName is the registry name and every fact's source value.
const SourceName = "dfs_domain_rating"

// GrainDomainRating is the single fact grain this source writes.
const GrainDomainRating = "domain_rating"

// MetRating is the 0–100 domain-authority score metric key.
const MetRating = "rating"

// pullSummary names the one monthly pull.
const pullSummary = "summary"

// ratingPayload is the raw-doc payload: the extracted rank, enough to replay
// the one metric this source produces.
type ratingPayload struct {
	Rank int `bson:"rank"`
}

// Source implements analytics.Source for the monthly domain-rating snapshot.
type Source struct {
	provider interfaces.DataForSEO
}

var _ analytics.Source = (*Source)(nil)

func New(provider interfaces.DataForSEO) *Source {
	return &Source{provider: provider}
}

func (s *Source) Name() string             { return SourceName }
func (s *Source) RequiresConnection() bool { return false }

// VerifyConnection trivially succeeds — our credentials, no user action.
func (s *Source) VerifyConnection(ctx context.Context, entity *models.WebEntity) (map[string]string, error) {
	return nil, nil
}

// Schedule: monthly on the 1st; dates are UTC first-of-month (no reporting
// zone — this is our own snapshot instant, not a source-reported day).
func (s *Source) Schedule() analytics.SourceSchedule {
	return analytics.SourceSchedule{
		Cadence:    "monthly",
		DefaultAt:  "07:00",
		DayOfMonth: 1,
	}
}

// MergePolicy: rating is a non-additive gauge — last wins.
func (s *Source) MergePolicy() map[string]analytics.MergeOp {
	return map[string]analytics.MergeOp{MetRating: analytics.MergeLast()}
}

// FetchWindow is one backlinks-summary call through the shared domain-rating
// util (same task shape as onboarding/SIE, so semantics never drift). Errors
// are plain — our credentials can't be "revoked" by the user, so the unit
// fails and SQS retries it.
func (s *Source) FetchWindow(ctx context.Context, entity *models.WebEntity, window analytics.DateWindow) ([]analytics.RawPull, error) {
	rating, err := utils.FetchDomainRating(ctx, s.provider, entity.WebsiteUrl)
	if err != nil {
		return nil, fmt.Errorf("dfs_domain_rating: fetch for entity %s: %w", entity.ID.Hex(), err)
	}
	payload, err := bson.Marshal(ratingPayload{Rank: rating})
	if err != nil {
		return nil, fmt.Errorf("dfs_domain_rating: marshal payload: %w", err)
	}
	return []analytics.RawPull{{
		Date:     window.From, // first-of-month (the monthly IngestWindow is single-date)
		Pull:     pullSummary,
		Payload:  payload,
		RowCount: 1,
	}}, nil
}

// Normalize emits the one monthly fact (pure — replay-safe).
func (s *Source) Normalize(ctx context.Context, raw *models.AnalyticsRaw, nctx analytics.NormalizeContext) ([]models.AnalyticsFact, error) {
	var payload ratingPayload
	if err := bson.Unmarshal(raw.Payload, &payload); err != nil {
		return nil, fmt.Errorf("dfs_domain_rating: unmarshal raw payload %s: %w", raw.Date, err)
	}
	return []models.AnalyticsFact{{
		Grain:   GrainDomainRating,
		Date:    raw.Date,
		Metrics: map[string]float64{MetRating: float64(payload.Rank)},
	}}, nil
}
