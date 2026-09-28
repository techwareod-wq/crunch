package models

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const scheduledArticleCollection = "scheduledArticle"

// EnsureScheduledArticleIndexes creates:
//
//   - keyword_id — backs the per-keyword article lookups behind keyword
//     status/usage hydration. Deliberately NOT unique: manual scheduling may
//     reuse a keyword, so several articles can share a keyword_id. (The
//     auto-scheduler still never reuses — its candidate filter skips keywords
//     that already have an article.)
//   - web_entity_context_id — backs GetScheduledArticlesByWebEntityContext, the
//     per-WEC fetch every keyword read path runs to hydrate keyword status. That
//     query used to collection-scan; the keyword-search endpoint runs it per
//     keystroke, so the scan had to go.
//   - (status, schedule_date) — backs the cron layer's daily due-sweep
//     (ListDueScheduledArticles): status == scheduled AND schedule_date <=
//     today, one indexed query per occurrence.
//
// Idempotent — re-running is a no-op once the indexes exist.
//
// DEPLOY NOTE: databases that predate keyword reuse carry a UNIQUE keyword_id
// index under the same default name ("keyword_id_1"). CreateMany conflicts on
// it, so that index must be dropped manually (db.scheduledArticle.dropIndex(
// "keyword_id_1")) before this build boots against such a database.
func EnsureScheduledArticleIndexes(ctx context.Context) error {
	_, err := Collection(scheduledArticleCollection).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{{Key: "keyword_id", Value: 1}},
		},
		{
			Keys: bson.D{{Key: "web_entity_context_id", Value: 1}},
		},
		{
			// Backs the cron layer's daily due-sweep: status == scheduled AND
			// schedule_date <= today, one indexed query per occurrence.
			Keys: bson.D{
				{Key: "status", Value: 1},
				{Key: "schedule_date", Value: 1},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("ensure scheduled article indexes: %w", err)
	}
	return nil
}

// ScheduledArticleStatus tracks where a planned article is in its lifecycle.
// Stored as an int in Mongo for cheap range queries, serialised as a string
// label over the wire so the frontend doesn't carry a parallel mapping.
type ScheduledArticleStatus int

const (
	ScheduledArticleStatusScheduled      ScheduledArticleStatus = 0
	ScheduledArticleStatusGenerating     ScheduledArticleStatus = 1
	ScheduledArticleStatusReadyForReview ScheduledArticleStatus = 2
	ScheduledArticleStatusDraft          ScheduledArticleStatus = 3
	ScheduledArticleStatusPublished      ScheduledArticleStatus = 4
	// ScheduledArticleStatusScheduling is the transient state of a single
	// user-initiated calendar slot whose metadata (title) is still being
	// generated asynchronously. The row appears on the dashboard immediately
	// but its details are populated once the title step completes, at which
	// point it flips to ScheduledArticleStatusScheduled. Bulk-scheduled slots
	// never enter this state — they default to Scheduled (0).
	ScheduledArticleStatusScheduling ScheduledArticleStatus = 5
)

// String returns the JSON-wire label for the status.
func (s ScheduledArticleStatus) String() string {
	switch s {
	case ScheduledArticleStatusGenerating:
		return "generating"
	case ScheduledArticleStatusReadyForReview:
		return "readyForReview"
	case ScheduledArticleStatusDraft:
		return "draft"
	case ScheduledArticleStatusPublished:
		return "published"
	case ScheduledArticleStatusScheduling:
		return "scheduling"
	default:
		return "scheduled"
	}
}

// ArticleType is the editorial format chosen for a scheduled article.
// Values are written as the canonical labels supplied by the article-type
// LLM step, so they survive a round-trip through Mongo without translation.
type ArticleType string

const (
	ArticleTypeHowToComparison  ArticleType = "How-To + Comparison"
	ArticleTypeAlternativesList ArticleType = "Alternatives List"
	ArticleTypeListicleRoundup  ArticleType = "Listicle / Roundup"
	ArticleTypeHowToGuide       ArticleType = "How-To Guide"
	ArticleTypeWhatIsDefinition ArticleType = "What-Is / Definition"
)

// AllArticleTypes is the canonical, ordered list of valid article types.
var AllArticleTypes = []ArticleType{
	ArticleTypeHowToComparison,
	ArticleTypeAlternativesList,
	ArticleTypeListicleRoundup,
	ArticleTypeHowToGuide,
	ArticleTypeWhatIsDefinition,
}

// ParseArticleType normalises a free-form string from an LLM into a typed
// ArticleType. Comparison is whitespace-trimmed and case-insensitive.
func ParseArticleType(s string) (ArticleType, bool) {
	t := strings.TrimSpace(s)
	for _, at := range AllArticleTypes {
		if strings.EqualFold(t, string(at)) {
			return at, true
		}
	}
	return "", false
}

// ScheduledArticle is one calendar slot for a future article. Created by the
// Scheduling Engine orchestrate step, then enriched by the article-type and
// title generation steps.
type ScheduledArticle struct {
	ID                 primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	KeywordID          primitive.ObjectID `bson:"keyword_id" json:"keywordId"`
	UserID             primitive.ObjectID `bson:"user_id" json:"userId"`
	WebEntityID        primitive.ObjectID `bson:"web_entity_id" json:"webEntityId"`
	WebEntityContextID primitive.ObjectID `bson:"web_entity_context_id" json:"webEntityContextId"`
	Title              string             `bson:"title,omitempty" json:"title,omitempty"`
	ArticleType        ArticleType        `bson:"article_type,omitempty" json:"articleType,omitempty"`
	Reasoning          string             `bson:"reasoning,omitempty" json:"reasoning,omitempty"`
	// TitleSuggestions is the cached set of LLM-generated alternative titles
	// shown in the dashboard "Regenerate suggestions" control. Generated once
	// per article (on first request) and reused on subsequent requests so the
	// user sees a stable set without re-spending tokens. Cleared whenever the
	// article type changes, since suggestions are format-specific.
	TitleSuggestions []string `bson:"title_suggestions,omitempty" json:"titleSuggestions,omitempty"`
	// AdditionalInstructions is user-supplied free-form guidance shown in the
	// dashboard sidebar. Captured before generation runs and copied onto the
	// WebEntityMasterContext at orchestrate time so the article-generation
	// prompt can honour it. Empty when the user hasn't set anything.
	AdditionalInstructions string    `bson:"additional_instructions,omitempty" json:"additionalInstructions,omitempty"`
	ScheduleDate           time.Time `bson:"schedule_date" json:"scheduleDate"`
	// PublishAt is the target auto-publish instant for a same-day slot, set to
	// edit-time + 1h when an article is scheduled / rescheduled to today. nil for
	// future-dated slots (the deferred auto-publish runner picks the time for
	// those). Distinct from ScheduleDate, which is always 00:00 UTC of the
	// calendar day. See [[ScheduledArticle Model]] § Schema.
	PublishAt *time.Time `bson:"publish_at,omitempty" json:"publishAt,omitempty"`
	// InternalLinkingEnabled is the per-article override for whether the
	// internal-link-insertion step runs during generation. Snapshotted from the
	// WebEntity default at creation; a nil value (legacy slots, or any not yet
	// backfilled) inherits the WebEntity default at orchestrate time. Editable
	// from the dashboard edit sidebar while the slot is still scheduled.
	InternalLinkingEnabled *bool `bson:"internal_linking_enabled,omitempty" json:"-"`
	// PublishAsLive is the per-article override for live vs draft publishing.
	// nil = inherit the WebEntity default (which itself defaults to draft).
	// Resolved at publish time only; editable in every state except
	// mid-generation. See [[Publish Draft State]].
	PublishAsLive *bool `bson:"publish_as_live,omitempty" json:"-"`
	// ThumbnailStyle is the per-article thumbnail-style override. nil = inherit
	// the WebEntity default (which itself falls back to the system default).
	// Deliberately NOT snapshotted from the WebEntity at scheduling time — a nil
	// value keeps a not-yet-generated article tracking later Settings changes.
	// Resolved once at master-context creation; editable from the dashboard edit
	// sidebar while the slot is still pre-generation. See [[Thumbnail Style Selection]].
	ThumbnailStyle *string                `bson:"thumbnail_style,omitempty" json:"-"`
	Status         ScheduledArticleStatus `bson:"status" json:"-"`
	// GenerationCounted marks that this slot has been charged against the
	// owning entity's lifetime generation counter
	// (WebEntity.LifetimeArticlesGenerated). Claimed atomically the first time
	// CGE Orchestrate flips the slot to Generating; retries and re-triggers of
	// the same slot never count twice.
	GenerationCounted bool      `bson:"generation_counted,omitempty" json:"-"`
	CreatedAt         time.Time `bson:"created_at,omitempty" json:"createdAt,omitempty"`
	UpdatedAt         time.Time `bson:"updated_at,omitempty" json:"updatedAt,omitempty"`
}

func CreateScheduledArticle(ctx context.Context, sa *ScheduledArticle) error {
	now := time.Now()
	sa.CreatedAt = now
	sa.UpdatedAt = now

	id, err := InsertOne(ctx, scheduledArticleCollection, sa)
	if err != nil {
		return err
	}
	sa.ID = id
	return nil
}

// CreateScheduledArticles inserts a batch of scheduled articles in one round-trip.
// Sets created_at / updated_at on each input. The returned IDs follow input order.
func CreateScheduledArticles(ctx context.Context, articles []*ScheduledArticle) error {
	if len(articles) == 0 {
		return nil
	}
	now := time.Now()
	docs := make([]interface{}, len(articles))
	for i, a := range articles {
		a.CreatedAt = now
		a.UpdatedAt = now
		docs[i] = a
	}

	res, err := Collection(scheduledArticleCollection).InsertMany(ctx, docs)
	if err != nil {
		return err
	}
	for i, raw := range res.InsertedIDs {
		if oid, ok := raw.(primitive.ObjectID); ok {
			articles[i].ID = oid
		}
	}
	return nil
}

func GetScheduledArticle(ctx context.Context, id string) (bool, *ScheduledArticle, error) {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return false, nil, fmt.Errorf("invalid scheduled article ID: %w", err)
	}
	var sa ScheduledArticle
	found, err := FindOne(ctx, scheduledArticleCollection, bson.M{"_id": objID}, &sa)
	if err != nil {
		return false, nil, err
	}
	return found, &sa, nil
}

// DeleteScheduledArticle removes a single scheduled article by hex ID. Returns
// an error when the ID is malformed or no document matched the filter.
func DeleteScheduledArticle(ctx context.Context, id string) error {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid scheduled article ID: %w", err)
	}
	return DeleteOne(ctx, scheduledArticleCollection, bson.M{"_id": objID})
}

// DeleteScheduledArticlesByUserID removes every scheduled article the user
// owns. Admin deletion cascade only — run after master contexts are deleted
// (children first). Zero matches is a no-op so re-runs converge.
func DeleteScheduledArticlesByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	res, err := Collection(scheduledArticleCollection).DeleteMany(ctx, bson.M{"user_id": userID})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// GetScheduledArticlesByWebEntityContext returns every scheduled article tied
// to a given WebEntityContext, ordered by schedule_date ascending.
func GetScheduledArticlesByWebEntityContext(ctx context.Context, webEntityContextID string) ([]*ScheduledArticle, error) {
	wecOID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return nil, fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	cursor, err := Collection(scheduledArticleCollection).Find(ctx, bson.M{"web_entity_context_id": wecOID})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var out []*ScheduledArticle
	if err := cursor.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetFirstScheduledArticleForContext returns the earliest-scheduled article for
// a WebEntityContext — the head of the calendar the dashboard renders. Ordered
// by schedule_date ascending, then _id ascending so a same-day tie resolves to
// the row the scheduler inserted first (the highest-opportunity keyword lands on
// the earliest slot). Returns found=false when the context has no scheduled
// articles.
func GetFirstScheduledArticleForContext(ctx context.Context, webEntityContextID string) (bool, *ScheduledArticle, error) {
	wecOID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return false, nil, fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	opts := options.FindOne().SetSort(bson.D{{Key: "schedule_date", Value: 1}, {Key: "_id", Value: 1}})
	var sa ScheduledArticle
	err = Collection(scheduledArticleCollection).FindOne(ctx, bson.M{"web_entity_context_id": wecOID}, opts).Decode(&sa)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("get first scheduled article for WEC %s: %w", webEntityContextID, err)
	}
	return true, &sa, nil
}

// GetScheduledArticlesByWebEntityInRange returns the scheduled articles for a
// user's web entity whose schedule_date falls in the half-open window
// [from, to), ordered by schedule_date ascending. Scoping by user_id as well
// as web_entity_id keeps the query both authorisation-safe and indexed.
func GetScheduledArticlesByWebEntityInRange(ctx context.Context, userID, webEntityID string, from, to time.Time) ([]*ScheduledArticle, error) {
	userOID, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		return nil, fmt.Errorf("invalid user ID: %w", err)
	}
	webEntityOID, err := primitive.ObjectIDFromHex(webEntityID)
	if err != nil {
		return nil, fmt.Errorf("invalid web entity ID: %w", err)
	}
	filter := bson.M{
		"user_id":       userOID,
		"web_entity_id": webEntityOID,
		"schedule_date": bson.M{"$gte": from, "$lt": to},
	}
	opts := options.Find().SetSort(bson.M{"schedule_date": 1})

	cursor, err := Collection(scheduledArticleCollection).Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var out []*ScheduledArticle
	if err := cursor.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListDueScheduledArticles returns every article still in the pre-generation
// "scheduled" state whose schedule_date has arrived (≤ until), ordered by
// (user_id, schedule_date) so a per-user overdue cap can be applied in one
// pass. This is the cron daily sweep's single indexed query. The resolver does
// NOT flip status — CGE Orchestrate marks the slot generating itself as its
// first act, so an undispatched article stays visible to the next sweep.
func ListDueScheduledArticles(ctx context.Context, until time.Time) ([]*ScheduledArticle, error) {
	filter := bson.M{
		"status":        ScheduledArticleStatusScheduled,
		"schedule_date": bson.M{"$lte": until},
	}
	opts := options.Find().SetSort(bson.D{{Key: "user_id", Value: 1}, {Key: "schedule_date", Value: 1}})
	cursor, err := Collection(scheduledArticleCollection).Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list due scheduled articles: %w", err)
	}
	defer cursor.Close(ctx)

	var out []*ScheduledArticle
	if err := cursor.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CountGenerationStartedArticlesForEntity counts the entity's LIVE scheduled
// articles whose content generation has been initiated at least once — status
// Generating (stamped by CGE Orchestrate before any work runs) or any later
// lifecycle state. Deleted articles are invisible to it, which is why the
// free-plan cap reads WebEntity.LifetimeArticlesGenerated instead; this
// derived count seeds that counter in the freeplanmigrate backfill. The
// pre-generation states (Scheduled, Scheduling) don't count.
func CountGenerationStartedArticlesForEntity(ctx context.Context, webEntityID primitive.ObjectID) (int, error) {
	n, err := Collection(scheduledArticleCollection).CountDocuments(ctx, bson.M{
		"web_entity_id": webEntityID,
		"status": bson.M{"$in": bson.A{
			ScheduledArticleStatusGenerating,
			ScheduledArticleStatusReadyForReview,
			ScheduledArticleStatusDraft,
			ScheduledArticleStatusPublished,
		}},
	})
	if err != nil {
		return 0, fmt.Errorf("count generation-started articles for entity %s: %w", webEntityID.Hex(), err)
	}
	return int(n), nil
}

// CountScheduledArticlesForContext returns the number of scheduled articles
// already persisted for a given WebEntityContext. Used to make orchestrate
// idempotent (skip if already scheduled).
func CountScheduledArticlesForContext(ctx context.Context, webEntityContextID string) (int64, error) {
	wecOID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return 0, fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	return Collection(scheduledArticleCollection).CountDocuments(ctx, bson.M{"web_entity_context_id": wecOID})
}

func CountScheduledArticlesWithTitleForContext(ctx context.Context, webEntityContextID string) (int64, error) {
	wecOID, err := primitive.ObjectIDFromHex(webEntityContextID)
	if err != nil {
		return 0, fmt.Errorf("invalid webEntityContext ID: %w", err)
	}
	return Collection(scheduledArticleCollection).CountDocuments(ctx, bson.M{
		"web_entity_context_id": wecOID,
		"title":                 bson.M{"$nin": bson.A{"", nil}},
	})
}

// ScheduledArticleUpdateReq is the partial-update payload. Pointer/empty
// semantics match the WECUpdateReq pattern elsewhere in the codebase.
type ScheduledArticleUpdateReq struct {
	Title                  *string
	ArticleType            *ArticleType
	Reasoning              *string
	AdditionalInstructions *string
	ScheduleDate           *time.Time
	// PublishAt sets the same-day auto-publish target (edit-time + 1h). Set only
	// when the article is scheduled / rescheduled to today; nil leaves it
	// untouched.
	PublishAt *time.Time
	Status    *ScheduledArticleStatus
	// TitleSuggestions overwrites the cached suggestion set when non-nil. Pass a
	// pointer to an empty slice to clear it (e.g. on article-type change); nil
	// leaves the persisted value untouched.
	TitleSuggestions *[]string
	// InternalLinkingEnabled overwrites the per-article internal-linking toggle
	// when non-nil; nil leaves the persisted value untouched.
	InternalLinkingEnabled *bool
	// PublishAsLive overwrites the per-article live/draft override when non-nil;
	// nil leaves the persisted value untouched.
	PublishAsLive *bool
	// ThumbnailStyle overwrites the per-article thumbnail-style override:
	//   nil                 → leave the persisted value untouched
	//   non-nil empty string → $unset thumbnail_style (reset to inherit)
	//   non-nil non-empty   → $set the override
	ThumbnailStyle *string
}

func UpdateScheduledArticle(ctx context.Context, id string, req ScheduledArticleUpdateReq) error {
	objID, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid scheduled article ID: %w", err)
	}

	set := bson.M{}
	if req.Title != nil {
		set["title"] = *req.Title
	}
	if req.ArticleType != nil {
		set["article_type"] = *req.ArticleType
	}
	if req.Reasoning != nil {
		set["reasoning"] = *req.Reasoning
	}
	if req.AdditionalInstructions != nil {
		set["additional_instructions"] = *req.AdditionalInstructions
	}
	if req.ScheduleDate != nil {
		set["schedule_date"] = *req.ScheduleDate
	}
	if req.PublishAt != nil {
		set["publish_at"] = *req.PublishAt
	}
	if req.Status != nil {
		set["status"] = *req.Status
	}
	if req.TitleSuggestions != nil {
		set["title_suggestions"] = *req.TitleSuggestions
	}
	if req.InternalLinkingEnabled != nil {
		set["internal_linking_enabled"] = *req.InternalLinkingEnabled
	}
	if req.PublishAsLive != nil {
		set["publish_as_live"] = *req.PublishAsLive
	}
	unset := bson.M{}
	if req.ThumbnailStyle != nil {
		if *req.ThumbnailStyle == "" {
			// Empty string = reset to inherit — drop the stored override so the
			// article resumes tracking the WebEntity default.
			unset["thumbnail_style"] = ""
		} else {
			set["thumbnail_style"] = *req.ThumbnailStyle
		}
	}
	if len(set) == 0 && len(unset) == 0 {
		return nil
	}
	set["updated_at"] = time.Now()

	update := bson.M{"$set": set}
	if len(unset) > 0 {
		update["$unset"] = unset
	}
	return UpdateOne(ctx, scheduledArticleCollection, bson.M{"_id": objID}, update)
}

// SetScheduledArticleStatus is a focused helper used by the content generation
// pipeline to flip a single article's lifecycle status without composing a
// full ScheduledArticleUpdateReq at every call site.
func SetScheduledArticleStatus(ctx context.Context, id string, status ScheduledArticleStatus) error {
	return UpdateScheduledArticle(ctx, id, ScheduledArticleUpdateReq{Status: &status})
}

// SetScheduledArticlePublishAsLive writes the per-article live/draft override.
// Editable in every state except mid-generation (enforced by the caller).
func SetScheduledArticlePublishAsLive(ctx context.Context, id string, live bool) error {
	return UpdateScheduledArticle(ctx, id, ScheduledArticleUpdateReq{PublishAsLive: &live})
}

// RecordArticleGenerationStart charges one generation against the owning web
// entity's lifetime counter, at most once per scheduled-article slot: an
// atomic claim on generation_counted decides whether this call is the first
// start for the slot, and only the winner increments
// WebEntity.LifetimeArticlesGenerated (resolved from the slot's own
// web_entity_id). Retries and re-triggers lose the claim and are free. The
// two writes are not transactional — a crash between them undercounts by one,
// which favors the user; the counter is never incremented without a
// successful claim.
func RecordArticleGenerationStart(ctx context.Context, scheduledArticleID string) error {
	objID, err := primitive.ObjectIDFromHex(scheduledArticleID)
	if err != nil {
		return fmt.Errorf("invalid scheduled article ID: %w", err)
	}
	var claimed ScheduledArticle
	err = Collection(scheduledArticleCollection).FindOneAndUpdate(ctx,
		bson.M{"_id": objID, "generation_counted": bson.M{"$ne": true}},
		bson.M{"$set": bson.M{"generation_counted": true, "updated_at": time.Now()}},
	).Decode(&claimed)
	if err == mongo.ErrNoDocuments {
		// Already counted (or the slot is gone) — nothing to charge.
		return nil
	}
	if err != nil {
		return fmt.Errorf("claim generation slot %s: %w", scheduledArticleID, err)
	}
	return IncrementLifetimeArticlesGeneratedForEntity(ctx, claimed.WebEntityID)
}

// FindScheduledArticlesByIDs loads the given articles keyed by _id — the
// analytics article-rollup join (fact rows carry the scheduledArticle id in
// dims.article_id).
func FindScheduledArticlesByIDs(ctx context.Context, ids []primitive.ObjectID) (map[primitive.ObjectID]*ScheduledArticle, error) {
	out := map[primitive.ObjectID]*ScheduledArticle{}
	if len(ids) == 0 {
		return out, nil
	}
	cursor, err := Collection(scheduledArticleCollection).Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, fmt.Errorf("find scheduled articles by ids: %w", err)
	}
	defer cursor.Close(ctx)
	var rows []ScheduledArticle
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("decode scheduled articles by ids: %w", err)
	}
	for i := range rows {
		out[rows[i].ID] = &rows[i]
	}
	return out, nil
}
