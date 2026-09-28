package models

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const styleReplicationRunCollection = "styleReplicationRun"

// Style replication run status ladder. A run is "active" (blocks concurrent
// starts) in any state below Done; Error is terminal like Done but does not
// count toward the daily rate limit.
const (
	StyleRunStatusCreated        = 0
	StyleRunStatusDiscovering    = 1
	StyleRunStatusAwaitingURLs   = 2
	StyleRunStatusScraping       = 3
	StyleRunStatusAwaitingReview = 4
	StyleRunStatusSynthesizing   = 5
	StyleRunStatusDone           = 6
	StyleRunStatusError          = 7
)

// Candidate URL sources.
const (
	StyleCandidateSourceGSC     = "gsc"
	StyleCandidateSourceSitemap = "sitemap"
	StyleCandidateSourceManual  = "manual"
)

// EnsureStyleReplicationRunIndexes creates the web_entity_id index — every
// non-_id read (active-run lookup, daily rate-limit count) keys off it.
// Not unique: an entity accumulates finished runs; concurrency is enforced by
// the active-status guarded insert in the controller, not the index. Idempotent.
func EnsureStyleReplicationRunIndexes(ctx context.Context) error {
	idx := mongo.IndexModel{Keys: bson.D{{Key: "web_entity_id", Value: 1}}}
	if _, err := Collection(styleReplicationRunCollection).Indexes().CreateOne(ctx, idx); err != nil {
		return fmt.Errorf("ensure style replication run indexes: %w", err)
	}
	return nil
}

// StyleCandidateURL is one discovered (or manually added) source article.
type StyleCandidateURL struct {
	URL    string `bson:"url" json:"url"`
	Title  string `bson:"title,omitempty" json:"title,omitempty"`
	Source string `bson:"source" json:"source"` // gsc | sitemap | manual
}

// StyleLearnSelection is the user's step-2 choice of WHICH artifacts this run
// learns — five-granular so a run can re-learn one segment (e.g. just the
// mid-article style) without touching the rest. Synthesis writes only the
// selected artifacts; the merge-on-write keeps everything else.
type StyleLearnSelection struct {
	ToneProfile      bool `bson:"tone_profile" json:"toneProfile"`
	StructurePattern bool `bson:"structure_pattern" json:"structurePattern"`
	TitlePattern     bool `bson:"title_pattern" json:"titlePattern"`
	ThumbnailStyle   bool `bson:"thumbnail_style" json:"thumbnailStyle"`
	MidArticleStyle  bool `bson:"mid_article_style" json:"midArticleStyle"`
}

// Articles reports whether the run needs scraped article TEXT (any of the
// three text artifacts).
func (l StyleLearnSelection) Articles() bool {
	return l.ToneProfile || l.StructurePattern || l.TitlePattern
}

// Images reports whether the run needs scraped images (either bucket).
func (l StyleLearnSelection) Images() bool {
	return l.ThumbnailStyle || l.MidArticleStyle
}

// WantsPosition reports whether the given image bucket is being learned.
func (l StyleLearnSelection) WantsPosition(position string) bool {
	switch position {
	case ImagePositionThumbnail:
		return l.ThumbnailStyle
	case ImagePositionMidArticle:
		return l.MidArticleStyle
	default:
		return false
	}
}

// Any reports whether the selection learns anything at all.
func (l StyleLearnSelection) Any() bool {
	return l.Articles() || l.Images()
}

// StyleScrapedContent is one URL's extracted body text (capped by
// values.styleReplication.maxContentCharsPerURL at write time).
type StyleScrapedContent struct {
	URL     string `bson:"url"`
	Title   string `bson:"title,omitempty"`
	Content string `bson:"content"`
}

// StyleScrapedImage is one candidate image surfaced to the review grid.
// Selected defaults to true; the approve call flips deselected entries off.
// Position buckets the image for its own synthesis call: og:image references
// land as "thumbnail", in-article imagery as "mid-article" (uploads carry the
// user's explicit choice). User-uploaded reference images (registered at
// approve time) carry an S3Key and no SourceArticle; their URL is the asset
// bucket's public URL, so the synthesis fetch path treats them exactly like
// scraped images. The keys are deleted best-effort after synthesis completes.
type StyleScrapedImage struct {
	URL           string `bson:"url" json:"url"`
	Position      string `bson:"position,omitempty" json:"position,omitempty"` // ImagePositionThumbnail | ImagePositionMidArticle
	SourceArticle string `bson:"source_article,omitempty" json:"sourceArticle,omitempty"`
	S3Key         string `bson:"s3_key,omitempty" json:"s3Key,omitempty"`
	Selected      bool   `bson:"selected" json:"selected"`
}

// StyleReplicationRun is the per-run state document for the style replication
// wizard. One active run per web entity (guarded at start). Bulky scraped
// payloads (contents, images) are pruned when the run completes.
type StyleReplicationRun struct {
	ID          primitive.ObjectID `bson:"_id,omitempty"`
	WebEntityID primitive.ObjectID `bson:"web_entity_id"`
	CompanyID   primitive.ObjectID `bson:"company_id,omitempty"`
	UserID      primitive.ObjectID `bson:"user_id"`

	Status int `bson:"status"`

	// Candidates is the discovery output (GSC top performers or sitemap
	// latest); the finalized user list lands in ScrapeStatuses order.
	Candidates []StyleCandidateURL `bson:"candidates,omitempty"`
	Learn      StyleLearnSelection `bson:"learn"`

	// ScrapeStatuses tracks per-URL scrape progress (reuses the CGE shape).
	ScrapeStatuses []UrlScrapeStatus `bson:"scrape_statuses,omitempty"`
	// ScrapesRemaining is the atomic fan-in counter — the worker that
	// decrements it to 0 flips the run to awaiting_review.
	ScrapesRemaining int `bson:"scrapes_remaining"`

	ScrapedContents []StyleScrapedContent `bson:"scraped_contents,omitempty"`
	ScrapedImages   []StyleScrapedImage   `bson:"scraped_images,omitempty"`

	// Artifacts produced by synthesis, copied onto the WebEntity on success
	// and kept here so the done-poll can render them without a second read.
	ToneProfile           string `bson:"tone_profile,omitempty"`
	StructurePattern      string `bson:"structure_pattern,omitempty"`
	TitlePattern          string `bson:"title_pattern,omitempty"`
	ThumbnailStylePrompt  string `bson:"thumbnail_style_prompt,omitempty"`
	MidArticleStylePrompt string `bson:"mid_article_style_prompt,omitempty"`

	Error string `bson:"error,omitempty"`

	CreatedAt time.Time `bson:"created_at,omitempty"`
	UpdatedAt time.Time `bson:"updated_at,omitempty"`
}

// ActiveStyleRunStatuses are the states that block a concurrent start.
var ActiveStyleRunStatuses = []int{
	StyleRunStatusCreated,
	StyleRunStatusDiscovering,
	StyleRunStatusAwaitingURLs,
	StyleRunStatusScraping,
	StyleRunStatusAwaitingReview,
	StyleRunStatusSynthesizing,
}

func CreateStyleReplicationRun(ctx context.Context, run *StyleReplicationRun) error {
	now := time.Now()
	run.CreatedAt = now
	run.UpdatedAt = now
	id, err := InsertOne(ctx, styleReplicationRunCollection, run)
	if err != nil {
		return err
	}
	run.ID = id
	return nil
}

func GetStyleReplicationRun(ctx context.Context, id string) (bool, *StyleReplicationRun, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return false, nil, fmt.Errorf("invalid style replication run ID: %w", err)
	}
	var run StyleReplicationRun
	found, err := FindOne(ctx, styleReplicationRunCollection, bson.M{"_id": oid}, &run)
	if err != nil {
		return false, nil, fmt.Errorf("find style replication run: %w", err)
	}
	return found, &run, nil
}

// FindActiveStyleReplicationRunForEntity returns the entity's in-flight run,
// if any (one active run per entity — the start guard enforces it).
func FindActiveStyleReplicationRunForEntity(ctx context.Context, entityID primitive.ObjectID) (bool, *StyleReplicationRun, error) {
	var run StyleReplicationRun
	found, err := FindOne(ctx, styleReplicationRunCollection, bson.M{
		"web_entity_id": entityID,
		"status":        bson.M{"$in": ActiveStyleRunStatuses},
	}, &run)
	if err != nil {
		return false, nil, fmt.Errorf("find active style replication run: %w", err)
	}
	return found, &run, nil
}

// FindLatestStyleReplicationRunForEntity returns the entity's newest run in
// ANY state — the poll endpoint's read when no run id is supplied, so the
// wizard can resume an active run or show the last outcome.
func FindLatestStyleReplicationRunForEntity(ctx context.Context, entityID primitive.ObjectID) (bool, *StyleReplicationRun, error) {
	var run StyleReplicationRun
	err := Collection(styleReplicationRunCollection).FindOne(ctx,
		bson.M{"web_entity_id": entityID},
		options.FindOne().SetSort(bson.M{"created_at": -1}),
	).Decode(&run)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("find latest style replication run: %w", err)
	}
	return true, &run, nil
}

// CountStyleReplicationRunsSince counts the entity's runs created at/after
// `since`, EXCLUDING errored runs — a bot-walled first attempt doesn't burn a
// daily rate-limit slot.
func CountStyleReplicationRunsSince(ctx context.Context, entityID primitive.ObjectID, since time.Time) (int64, error) {
	n, err := Collection(styleReplicationRunCollection).CountDocuments(ctx, bson.M{
		"web_entity_id": entityID,
		"created_at":    bson.M{"$gte": since},
		"status":        bson.M{"$ne": StyleRunStatusError},
	})
	if err != nil {
		return 0, fmt.Errorf("count style replication runs: %w", err)
	}
	return n, nil
}

// UpdateStyleReplicationRun applies a $set document (plus updated_at).
func UpdateStyleReplicationRun(ctx context.Context, id string, set bson.M) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid style replication run ID: %w", err)
	}
	set["updated_at"] = time.Now()
	return UpdateOne(ctx, styleReplicationRunCollection, bson.M{"_id": oid}, bson.M{"$set": set})
}

// SetStyleRunStatus writes a bare status transition.
func SetStyleRunStatus(ctx context.Context, id string, status int) error {
	return UpdateStyleReplicationRun(ctx, id, bson.M{"status": status})
}

// SetStyleRunError flips the run to the terminal error state.
func SetStyleRunError(ctx context.Context, id, message string) error {
	return UpdateStyleReplicationRun(ctx, id, bson.M{
		"status": StyleRunStatusError,
		"error":  message,
	})
}

// TryAdvanceStyleRunStatus CASes status from one of `from` to `to`. Returns
// whether this caller won the transition — the guard that keeps double-posted
// url/approve submissions from double-dispatching fan-outs.
func TryAdvanceStyleRunStatus(ctx context.Context, id string, from []int, to int, extraSet bson.M) (bool, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return false, fmt.Errorf("invalid style replication run ID: %w", err)
	}
	set := bson.M{"status": to, "updated_at": time.Now()}
	for k, v := range extraSet {
		set[k] = v
	}
	res, err := Collection(styleReplicationRunCollection).UpdateOne(ctx,
		bson.M{"_id": oid, "status": bson.M{"$in": from}},
		bson.M{"$set": set},
	)
	if err != nil {
		return false, fmt.Errorf("advance style replication run status: %w", err)
	}
	return res.ModifiedCount > 0, nil
}

// MarkStyleRunURLScraped records one URL's scrape outcome: the positional
// status flip plus (on success) the appended content/images. Push and
// positional set ride one update so a crash can't leave content without its
// done flag.
func MarkStyleRunURLScraped(ctx context.Context, id, url string, failed bool, content *StyleScrapedContent, images []StyleScrapedImage) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid style replication run ID: %w", err)
	}
	set := bson.M{
		"scrape_statuses.$.done":   !failed,
		"scrape_statuses.$.failed": failed,
		"updated_at":               time.Now(),
	}
	update := bson.M{"$set": set}
	push := bson.M{}
	if content != nil {
		push["scraped_contents"] = *content
	}
	if len(images) > 0 {
		push["scraped_images"] = bson.M{"$each": images}
	}
	if len(push) > 0 {
		update["$push"] = push
	}
	return UpdateOne(ctx, styleReplicationRunCollection,
		bson.M{"_id": oid, "scrape_statuses.url": url}, update)
}

// DecrementStyleRunScrapesRemaining atomically decrements scrapes_remaining.
// Returns the remaining count after decrement — only the caller that receives
// 0 proceeds with the awaiting_review flip (fan-in gate; the
// DecrementUrlScrapesRemaining pattern).
func DecrementStyleRunScrapesRemaining(ctx context.Context, id string) (int, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return -1, fmt.Errorf("invalid style replication run ID: %w", err)
	}
	filter := bson.M{"_id": oid, "scrapes_remaining": bson.M{"$gt": 0}}
	update := bson.M{
		"$inc": bson.M{"scrapes_remaining": -1},
		"$set": bson.M{"updated_at": time.Now()},
	}
	var result StyleReplicationRun
	err = Collection(styleReplicationRunCollection).FindOneAndUpdate(
		ctx, filter, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&result)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return -1, fmt.Errorf("style run scrapes remaining already 0 or run not found")
		}
		return -1, fmt.Errorf("decrement style run scrapes remaining: %w", err)
	}
	return result.ScrapesRemaining, nil
}

// AppendStyleRunUploadedImages appends user-uploaded reference images to the
// review pool (the approve call's upload list — registered after the CAS into
// synthesizing, so exactly one writer appends).
func AppendStyleRunUploadedImages(ctx context.Context, id string, images []StyleScrapedImage) error {
	if len(images) == 0 {
		return nil
	}
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid style replication run ID: %w", err)
	}
	return UpdateOne(ctx, styleReplicationRunCollection, bson.M{"_id": oid}, bson.M{
		"$push": bson.M{"scraped_images": bson.M{"$each": images}},
		"$set":  bson.M{"updated_at": time.Now()},
	})
}

// DeselectStyleRunImages flips Selected=false on the given image URLs (the
// approve call's deselection list).
func DeselectStyleRunImages(ctx context.Context, id string, urls []string) error {
	if len(urls) == 0 {
		return nil
	}
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid style replication run ID: %w", err)
	}
	_, err = Collection(styleReplicationRunCollection).UpdateOne(ctx,
		bson.M{"_id": oid},
		bson.M{
			"$set": bson.M{
				"scraped_images.$[img].selected": false,
				"updated_at":                     time.Now(),
			},
		},
		options.Update().SetArrayFilters(options.ArrayFilters{
			Filters: []interface{}{bson.M{"img.url": bson.M{"$in": urls}}},
		}),
	)
	if err != nil {
		return fmt.Errorf("deselect style run images: %w", err)
	}
	return nil
}

// StyleRunArtifacts is the synthesis output copied onto the run's done state
// (the WebEntity write shapes them into a StyleReplication separately).
type StyleRunArtifacts struct {
	ToneProfile           string
	StructurePattern      string
	TitlePattern          string
	ThumbnailStylePrompt  string
	MidArticleStylePrompt string
}

// Empty reports whether synthesis produced nothing at all.
func (a StyleRunArtifacts) Empty() bool {
	return a.ToneProfile == "" && a.StructurePattern == "" && a.TitlePattern == "" &&
		a.ThumbnailStylePrompt == "" && a.MidArticleStylePrompt == ""
}

// CompleteStyleReplicationRun flips the run to done with its artifacts and
// prunes the bulky scraped payloads.
func CompleteStyleReplicationRun(ctx context.Context, id string, artifacts StyleRunArtifacts) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("invalid style replication run ID: %w", err)
	}
	return UpdateOne(ctx, styleReplicationRunCollection, bson.M{"_id": oid}, bson.M{
		"$set": bson.M{
			"status":                   StyleRunStatusDone,
			"tone_profile":             artifacts.ToneProfile,
			"structure_pattern":        artifacts.StructurePattern,
			"title_pattern":            artifacts.TitlePattern,
			"thumbnail_style_prompt":   artifacts.ThumbnailStylePrompt,
			"mid_article_style_prompt": artifacts.MidArticleStylePrompt,
			"updated_at":               time.Now(),
		},
		"$unset": bson.M{
			"scraped_contents": "",
			"scraped_images":   "",
		},
	})
}
