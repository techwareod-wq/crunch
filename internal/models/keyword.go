package models

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const keywordCollection = "keyword"

// FunnelStage represents a keyword's position in the marketing funnel.
type FunnelStage string

const (
	FunnelTOFU FunnelStage = "TOFU"
	FunnelMOFU FunnelStage = "MOFU"
	FunnelBOFU FunnelStage = "BOFU"
)

func ParseFunnelStage(s string) FunnelStage {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case string(FunnelTOFU):
		return FunnelTOFU
	case string(FunnelMOFU):
		return FunnelMOFU
	case string(FunnelBOFU):
		return FunnelBOFU
	default:
		return FunnelMOFU
	}
}

// Keyword is the persisted unit of keyword data. Processed keywords live in
// the `keyword` collection; raw user / competitor / expanded keywords still
// embed this struct on WebEntityContext via the KeyWords sub-document — the
// FK and timestamp fields are zero-value / omitempty in that embedded form.
type Keyword struct {
	ID                 primitive.ObjectID `bson:"_id,omitempty"`
	WebEntityContextID primitive.ObjectID `bson:"web_entity_context_id,omitempty"`
	SequenceID         int                `bson:"sequence_id,omitempty"`
	Keyword            string             `bson:"keyword"`
	Description        string             `bson:"description,omitempty"`
	Volume             int                `bson:"volume"`
	CPC                float64            `bson:"cpc,omitempty"`
	KeywordDifficulty  int                `bson:"keyword_difficulty"`
	RankingPosition    int                `bson:"ranking_position"`
	RankingUrl         string             `bson:"ranking_url,omitempty"`
	// CompetitorUrl records which competitor domain this keyword was sourced
	// from. Set only for competitor keywords; empty for user/expanded keywords.
	CompetitorUrl string      `bson:"competitor_url,omitempty"`
	Intent        string      `bson:"intent,omitempty"`
	Funnel        FunnelStage `bson:"funnel,omitempty"`
	// FunnelFailed marks a keyword whose funnel classification was permanently
	// abandoned (the LLM dropped or returned an unparseable value on the final
	// retry). It is the idempotent, per-keyword replacement for the WEC-level
	// $inc failure counter: funnel completion is derived from keyword state
	// (CountFunnelSettledKeywords), so a redelivered chunk can't inflate progress.
	// A keyword is either classified (Funnel set) or FunnelFailed, never both —
	// UpdateKeywordFunnels clears this flag on success.
	FunnelFailed     bool       `bson:"funnel_failed,omitempty"`
	Cluster          string     `bson:"cluster,omitempty"`
	OpportunityScore float64    `bson:"opportunity_score,omitempty"`
	Status           string     `bson:"status,omitempty"`
	ScheduledFor     *time.Time `bson:"scheduled_for,omitempty"`
	ManuallyAdded    bool       `bson:"manually_added,omitempty"`
	// Completed marks a keyword as fully enriched and clustered. Bulk pipeline
	// keywords are flagged at the clustering stage; manually-added keywords stay
	// false until their async enrichment (funnel → cluster → score) finishes.
	Completed bool      `bson:"completed,omitempty"`
	CreatedAt time.Time `bson:"created_at,omitempty"`
	UpdatedAt time.Time `bson:"updated_at,omitempty"`
}

// EnsureKeywordIndexes creates the compound unique index that enforces
// `sequence_id` uniqueness within a WebEntityContext. Idempotent.
func EnsureKeywordIndexes(ctx context.Context) error {
	unique := true
	_, err := Collection(keywordCollection).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "web_entity_context_id", Value: 1},
			{Key: "sequence_id", Value: 1},
		},
		Options: &options.IndexOptions{Unique: &unique},
	})
	if err != nil {
		return fmt.Errorf("ensure keyword indexes: %w", err)
	}
	return nil
}

// DeleteKeywordsForWEC removes every keyword doc for the given
// WebEntityContext. Used by PostProcessing to make the bulk insert
// idempotent on retry.
func DeleteKeywordsForWEC(ctx context.Context, webEntityContextID string) error {
	wecOID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	_, err = Collection(keywordCollection).DeleteMany(ctx, bson.M{
		"web_entity_context_id": wecOID,
	})
	if err != nil {
		return fmt.Errorf("delete keywords for WEC %s: %w", webEntityContextID, err)
	}
	return nil
}

// DeleteKeywordsForWECs is the multi-WEC sibling of DeleteKeywordsForWEC, for
// the admin deletion cascade (keyword docs carry no user_id, so the caller
// passes the WEC ids it captured before deleting the contexts). Zero matches
// is a no-op so re-runs converge.
func DeleteKeywordsForWECs(ctx context.Context, wecIDs []primitive.ObjectID) (int64, error) {
	if len(wecIDs) == 0 {
		return 0, nil
	}
	res, err := Collection(keywordCollection).DeleteMany(ctx, bson.M{
		"web_entity_context_id": bson.M{"$in": wecIDs},
	})
	if err != nil {
		return 0, fmt.Errorf("delete keywords for WECs: %w", err)
	}
	return res.DeletedCount, nil
}

// DeleteKeywordsByIDs removes the given keyword docs in one round-trip. Used
// by the post-processing merged dedupe to drop stored keywords that lost their
// variant group; callers are responsible for excluding protected keywords
// (scheduled-article rows, cluster pillars) and for pulling the IDs out of
// cluster supporting lists.
func DeleteKeywordsByIDs(ctx context.Context, ids []primitive.ObjectID) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := Collection(keywordCollection).DeleteMany(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return fmt.Errorf("delete keywords by ids: %w", err)
	}
	return nil
}

// InsertKeywords stamps web_entity_context_id and timestamps onto each
// keyword and inserts them in one round-trip. The slice is mutated in place
// to populate the assigned ObjectIDs so callers can use them immediately.
func InsertKeywords(ctx context.Context, webEntityContextID string, keywords []Keyword) ([]Keyword, error) {
	if len(keywords) == 0 {
		return keywords, nil
	}
	wecOID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return nil, fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	now := time.Now()
	docs := make([]interface{}, len(keywords))
	for i := range keywords {
		keywords[i].WebEntityContextID = wecOID
		keywords[i].CreatedAt = now
		keywords[i].UpdatedAt = now
		docs[i] = keywords[i]
	}

	res, err := Collection(keywordCollection).InsertMany(ctx, docs)
	if err != nil {
		return nil, fmt.Errorf("insert keywords for WEC %s: %w", webEntityContextID, err)
	}
	for i, raw := range res.InsertedIDs {
		if oid, ok := raw.(primitive.ObjectID); ok {
			keywords[i].ID = oid
		}
	}
	return keywords, nil
}

// GetKeywordsForWEC returns every keyword doc for the given WebEntityContext,
// ordered by sequence_id ascending. Single-query hydration for the read path.
func GetKeywordsForWEC(ctx context.Context, webEntityContextID string) ([]Keyword, error) {
	wecOID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return nil, fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	opts := options.Find().SetSort(bson.D{{Key: "sequence_id", Value: 1}})
	cursor, err := Collection(keywordCollection).Find(ctx, bson.M{
		"web_entity_context_id": wecOID,
	}, opts)
	if err != nil {
		return nil, fmt.Errorf("find keywords for WEC %s: %w", webEntityContextID, err)
	}
	defer cursor.Close(ctx)

	var out []Keyword
	if err := cursor.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("decode keywords for WEC %s: %w", webEntityContextID, err)
	}
	return out, nil
}

func GetKeywordsByIDs(ctx context.Context, ids []primitive.ObjectID) ([]Keyword, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	cursor, err := Collection(keywordCollection).Find(ctx, bson.M{
		"_id": bson.M{"$in": ids},
	})
	if err != nil {
		return nil, fmt.Errorf("find keywords by ids: %w", err)
	}
	defer cursor.Close(ctx)

	var out []Keyword
	if err := cursor.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("decode keywords by ids: %w", err)
	}
	return out, nil
}

// GetKeyword fetches a single keyword by hex ID.
func GetKeyword(ctx context.Context, id string) (bool, *Keyword, error) {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return false, nil, fmt.Errorf("invalid keyword ID: %w", err)
	}
	var kw Keyword
	found, err := FindOne(ctx, keywordCollection, bson.M{"_id": objID}, &kw)
	if err != nil {
		return false, nil, fmt.Errorf("find keyword by ID: %w", err)
	}
	return found, &kw, nil
}

// UpdateKeywordFunnels writes funnel stages onto a batch of keywords. One
// UpdateOne per ID — callers chunk via SQS, so per-batch sizes are small.
// Setting funnel also clears funnel_failed: a successful (re)classification is
// authoritative, keeping the classified/failed sets disjoint so the
// state-derived completion gate can't double-count a keyword.
func UpdateKeywordFunnels(ctx context.Context, updates map[primitive.ObjectID]FunnelStage) error {
	if len(updates) == 0 {
		return nil
	}
	now := time.Now()
	for id, funnel := range updates {
		if err := UpdateOne(ctx, keywordCollection,
			bson.M{"_id": id},
			bson.M{"$set": bson.M{
				"funnel":        funnel,
				"funnel_failed": false,
				"updated_at":    now,
			}},
		); err != nil {
			return fmt.Errorf("update funnel for keyword %s: %w", id.Hex(), err)
		}
	}
	return nil
}

// MarkKeywordsFunnelFailed flags keywords whose funnel classification was
// permanently abandoned (final retry exhausted). It is an idempotent $set of
// funnel_failed=true — re-running a chunk re-sets the same flag, so it cannot
// inflate progress the way the old $inc/$push counter did. Callers only pass
// keywords that were NOT classified this round, keeping the classified/failed
// sets disjoint.
func MarkKeywordsFunnelFailed(ctx context.Context, ids []primitive.ObjectID) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	ops := make([]mongo.WriteModel, 0, len(ids))
	for _, id := range ids {
		ops = append(ops, mongo.NewUpdateOneModel().
			SetFilter(bson.M{"_id": id}).
			SetUpdate(bson.M{"$set": bson.M{
				"funnel_failed": true,
				"updated_at":    now,
			}}))
	}
	if _, err := Collection(keywordCollection).BulkWrite(ctx, ops); err != nil {
		return fmt.Errorf("bulk mark keywords funnel-failed: %w", err)
	}
	return nil
}

// ResetKeywordFunnelState clears funnel classification state (funnel value and
// the funnel_failed flag) for every non-manual keyword in a WEC. Used by the
// funnel-classification retrigger so a re-run starts from zero settled keywords
// — without it the state-derived gate would see the previous run's keywords as
// already settled and advance before re-classification completed.
func ResetKeywordFunnelState(ctx context.Context, webEntityContextID string) error {
	wecOID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	_, err = Collection(keywordCollection).UpdateMany(ctx,
		bson.M{"web_entity_context_id": wecOID, "manually_added": bson.M{"$ne": true}},
		bson.M{"$set": bson.M{
			"funnel":        "",
			"funnel_failed": false,
			"updated_at":    time.Now(),
		}},
	)
	if err != nil {
		return fmt.Errorf("reset funnel state for WEC %s: %w", webEntityContextID, err)
	}
	return nil
}

// CountFunnelSettledKeywords returns how many of a WEC's bulk keywords have
// reached a terminal funnel state (classified or permanently failed) and the
// total bulk keyword count. It is the completion source of truth for the funnel
// stage (P5): both inputs are read from the keyword collection — which records
// success via an idempotent $set and failure via an idempotent flag — so a
// redelivered or re-emitted chunk can never push settled past total the way the
// old $inc counter did. Manually-added keywords are excluded; they enrich via a
// separate flow and are not part of the bulk fan-out.
func CountFunnelSettledKeywords(ctx context.Context, webEntityContextID string) (settled, total int, err error) {
	wecOID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	coll := Collection(keywordCollection)
	base := bson.M{"web_entity_context_id": wecOID, "manually_added": bson.M{"$ne": true}}

	totalCount, err := coll.CountDocuments(ctx, base)
	if err != nil {
		return 0, 0, fmt.Errorf("count total keywords for WEC %s: %w", webEntityContextID, err)
	}

	settledFilter := bson.M{
		"web_entity_context_id": wecOID,
		"manually_added":        bson.M{"$ne": true},
		"$or": []bson.M{
			{"funnel": bson.M{"$nin": []interface{}{"", nil}}},
			{"funnel_failed": true},
		},
	}
	settledCount, err := coll.CountDocuments(ctx, settledFilter)
	if err != nil {
		return 0, 0, fmt.Errorf("count settled keywords for WEC %s: %w", webEntityContextID, err)
	}

	return int(settledCount), int(totalCount), nil
}

// CountKeywordsFunnelState returns the classified (funnel set), permanently
// failed (funnel_failed) and total bulk-keyword counts for a WEC. It backs the
// data-repair migration, which recomputes the WEC's display counters from
// authoritative keyword state.
func CountKeywordsFunnelState(ctx context.Context, webEntityContextID string) (classified, failed, total int, err error) {
	wecOID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	coll := Collection(keywordCollection)
	base := bson.M{"web_entity_context_id": wecOID, "manually_added": bson.M{"$ne": true}}

	totalCount, err := coll.CountDocuments(ctx, base)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count total keywords for WEC %s: %w", webEntityContextID, err)
	}

	classifiedFilter := bson.M{"web_entity_context_id": wecOID, "manually_added": bson.M{"$ne": true}, "funnel": bson.M{"$nin": []interface{}{"", nil}}}
	classifiedCount, err := coll.CountDocuments(ctx, classifiedFilter)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count classified keywords for WEC %s: %w", webEntityContextID, err)
	}

	failedFilter := bson.M{"web_entity_context_id": wecOID, "manually_added": bson.M{"$ne": true}, "funnel_failed": true}
	failedCount, err := coll.CountDocuments(ctx, failedFilter)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("count failed keywords for WEC %s: %w", webEntityContextID, err)
	}

	return int(classifiedCount), int(failedCount), int(totalCount), nil
}

// UpdateKeywordClusters writes the cluster_id onto a batch of keywords.
func UpdateKeywordClusters(ctx context.Context, updates map[primitive.ObjectID]string) error {
	if len(updates) == 0 {
		return nil
	}
	now := time.Now()
	for id, cluster := range updates {
		if err := UpdateOne(ctx, keywordCollection,
			bson.M{"_id": id},
			bson.M{"$set": bson.M{
				"cluster":    cluster,
				"updated_at": now,
			}},
		); err != nil {
			return fmt.Errorf("update cluster for keyword %s: %w", id.Hex(), err)
		}
	}
	return nil
}

// UpdateKeywordOpportunityScores writes opportunity scores for every keyword
// in one BulkWrite — opp-score touches the full set in a single synchronous
// step, so the round-trip savings matter.
func UpdateKeywordOpportunityScores(ctx context.Context, updates map[primitive.ObjectID]float64) error {
	if len(updates) == 0 {
		return nil
	}
	now := time.Now()
	ops := make([]mongo.WriteModel, 0, len(updates))
	for id, score := range updates {
		ops = append(ops, mongo.NewUpdateOneModel().
			SetFilter(bson.M{"_id": id}).
			SetUpdate(bson.M{"$set": bson.M{
				"opportunity_score": score,
				"updated_at":        now,
			}}))
	}
	if _, err := Collection(keywordCollection).BulkWrite(ctx, ops); err != nil {
		return fmt.Errorf("bulk update opportunity scores: %w", err)
	}
	return nil
}

// MarkKeywordsCompleted flips the `completed` flag to true for the given
// keyword IDs in one BulkWrite. Idempotent — a re-run simply re-sets true.
func MarkKeywordsCompleted(ctx context.Context, ids []primitive.ObjectID) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	ops := make([]mongo.WriteModel, 0, len(ids))
	for _, id := range ids {
		ops = append(ops, mongo.NewUpdateOneModel().
			SetFilter(bson.M{"_id": id}).
			SetUpdate(bson.M{"$set": bson.M{
				"completed":  true,
				"updated_at": now,
			}}))
	}
	if _, err := Collection(keywordCollection).BulkWrite(ctx, ops); err != nil {
		return fmt.Errorf("bulk mark keywords completed: %w", err)
	}
	return nil
}

// GetMaxSequenceIDForWEC returns the highest sequence_id assigned within a
// WebEntityContext, or 0 if no keywords exist yet. Used to allocate the next
// sequence_id for a manually-added keyword.
func GetMaxSequenceIDForWEC(ctx context.Context, webEntityContextID string) (int, error) {
	wecOID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return 0, fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	opts := options.FindOne().SetSort(bson.D{{Key: "sequence_id", Value: -1}})
	var kw Keyword
	err = Collection(keywordCollection).FindOne(ctx, bson.M{
		"web_entity_context_id": wecOID,
	}, opts).Decode(&kw)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return 0, nil
		}
		return 0, fmt.Errorf("find max sequence_id for WEC %s: %w", webEntityContextID, err)
	}
	return kw.SequenceID, nil
}

// GetKeywordByWECAndText performs a case-insensitive lookup of a keyword by its
// text within a WebEntityContext. Used by the manual-add duplicate guard.
func GetKeywordByWECAndText(ctx context.Context, webEntityContextID, text string) (bool, *Keyword, error) {
	wecOID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return false, nil, fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	pattern := "^" + regexp.QuoteMeta(strings.TrimSpace(text)) + "$"
	var kw Keyword
	found, err := FindOne(ctx, keywordCollection, bson.M{
		"web_entity_context_id": wecOID,
		"keyword":               primitive.Regex{Pattern: pattern, Options: "i"},
	}, &kw)
	if err != nil {
		return false, nil, fmt.Errorf("find keyword by WEC and text: %w", err)
	}
	return found, &kw, nil
}

// KeywordStats is the slice of a keyword doc the analytics read layer joins
// on: CPC backs traffic value ($), Volume backs demand capture (%).
type KeywordStats struct {
	ID      primitive.ObjectID `bson:"_id"`
	Keyword string             `bson:"keyword"`
	Volume  int                `bson:"volume"`
	CPC     float64            `bson:"cpc"`
}

// FindKeywordStatsByWebEntityContexts loads every keyword's stats for the
// given WECs, keyed by lowercase-trimmed keyword text — the read-time join
// between GSC query strings and stored keyword data (plan D13: zero new API
// cost). Duplicate texts across WECs keep the first doc seen.
func FindKeywordStatsByWebEntityContexts(ctx context.Context, wecIDs []primitive.ObjectID) (map[string]KeywordStats, error) {
	out := map[string]KeywordStats{}
	if len(wecIDs) == 0 {
		return out, nil
	}
	cursor, err := Collection(keywordCollection).Find(ctx,
		bson.M{"web_entity_context_id": bson.M{"$in": wecIDs}},
		options.Find().SetProjection(bson.M{"keyword": 1, "volume": 1, "cpc": 1}))
	if err != nil {
		return nil, fmt.Errorf("find keyword stats by WECs: %w", err)
	}
	defer cursor.Close(ctx)
	var rows []KeywordStats
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("decode keyword stats by WECs: %w", err)
	}
	for _, row := range rows {
		key := strings.ToLower(strings.TrimSpace(row.Keyword))
		if key == "" {
			continue
		}
		if _, seen := out[key]; !seen {
			out[key] = row
		}
	}
	return out, nil
}

// FindKeywordsByIDs loads keywords keyed by _id — the analytics article
// rollup's target-keyword join (scheduledArticle.KeywordID → keyword).
func FindKeywordsByIDs(ctx context.Context, ids []primitive.ObjectID) (map[primitive.ObjectID]*Keyword, error) {
	out := map[primitive.ObjectID]*Keyword{}
	if len(ids) == 0 {
		return out, nil
	}
	cursor, err := Collection(keywordCollection).Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, fmt.Errorf("find keywords by ids: %w", err)
	}
	defer cursor.Close(ctx)
	var rows []Keyword
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("decode keywords by ids: %w", err)
	}
	for i := range rows {
		out[rows[i].ID] = &rows[i]
	}
	return out, nil
}
