package models

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/prompts"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// EnsureWebEntityIndexes creates the partial-unique index on {company_id}
// (tenancy plan §3.3 / D5: one WebEntity per company). The find-then-create
// races (double-clicked onboarding, retried POST) fail on the dup-key error so
// the loser re-reads the winner. The LEGACY plain-unique {user_id} index is no
// longer created here — existing deployments keep it (it still enforces
// one-per-user during transition) until cmd/companymigrate's -drop-user-index
// step removes it in the last phase. Idempotent.
func EnsureWebEntityIndexes(ctx context.Context) error {
	idx := mongo.IndexModel{
		Keys: bson.D{{Key: "company_id", Value: 1}},
		Options: options.Index().
			SetName("company_id_unique").
			SetUnique(true).
			SetPartialFilterExpression(bson.M{"company_id": bson.M{"$exists": true}}),
	}
	if _, err := Collection(webEntityCollection).Indexes().CreateOne(ctx, idx); err != nil {
		return fmt.Errorf("ensure web entity indexes: %w", err)
	}
	return nil
}

type WebEntity struct {
	ID primitive.ObjectID `bson:"_id,omitempty"`
	// CompanyID is the tenancy key (D5: one WebEntity per company — enforced
	// by the partial-unique index). Stamped at insert by every creation path;
	// legacy docs get it from cmd/companymigrate.
	CompanyID primitive.ObjectID `bson:"company_id,omitempty"`
	// UserID is the legacy owner key — kept populated during transition as a
	// read fallback (FindWebEntityForCompany), deleted in the last phase.
	UserID          primitive.ObjectID `bson:"user_id,omitempty"`
	WebsiteUrl      string             `bson:"website_url,omitempty"`
	BusinessContext *BusinessContext   `bson:"context,omitempty"`
	Competitors     []Competitor       `bson:"competitors,omitempty"`
	Publishing      *PublishingConfig  `bson:"publishing,omitempty"`
	CountryCode     string             `bson:"country_code,omitempty"`
	LocationCode    int                `bson:"location_code,omitempty"`
	Finalised       bool               `bson:"finalised,omitempty"`
	// InternalLinkingEnabled is the web-entity-wide default for whether the
	// internal-link-insertion step runs during article generation. A nil value
	// (legacy docs created before this field existed, or before the backfill
	// migration ran) is treated as enabled — see InternalLinkingEnabledOrDefault.
	// Editable from the settings/profile tab; each new ScheduledArticle snapshots
	// this default and may override it per article.
	InternalLinkingEnabled *bool `bson:"internal_linking_enabled,omitempty"`
	// PublishAsLive is the web-entity-wide default for whether a published
	// article lands on the platform live (true) or as a draft (nil/false).
	// nil = draft — the product default is "push as draft". Editable from the
	// settings/profile tab; each publish resolves against it (a per-article
	// ScheduledArticle override takes precedence). See [[Publish Draft State]].
	PublishAsLive *bool `bson:"publish_as_live,omitempty"`
	// ThumbnailStyle is the web-entity-wide default thumbnail style ID (see the
	// prompts.ThumbnailStyleCatalog). nil = the system default (editorial).
	// Editable from the settings/profile tab. A per-article ScheduledArticle
	// override takes precedence; unlike PublishAsLive it is NOT snapshotted onto
	// new ScheduledArticles, so not-yet-generated articles keep tracking later
	// changes here. See [[Thumbnail Style Selection]].
	ThumbnailStyle *string `bson:"thumbnail_style,omitempty"`
	// SEOStrategy is the user's chosen SIE strategy id (see the seo_strategy.go
	// catalog). nil = derive the default from the domain rating at read time
	// (SEOStrategyOrDefault), so legacy entities and pre-choice onboarding both
	// resolve without a stored value. Chosen during onboarding (stamped by the
	// finalise patch) and editable from the settings/profile tab. Selects the
	// DataForSEO filter set and opportunity-score knobs for every SIE run.
	SEOStrategy *string `bson:"seo_strategy,omitempty"`
	// LifetimeArticlesGenerated counts every article generation ever started
	// for this web entity. Monotonic ($inc-only): deleting an article does NOT
	// decrement it, so a free user can't reclaim cap slots by deleting.
	// Incremented once per scheduled-article slot via
	// RecordArticleGenerationStart; backs the free-plan lifetime article cap.
	LifetimeArticlesGenerated int `bson:"lifetime_articles_generated,omitempty"`
	// OnboardingAnalysisDone marks the async onboarding analysis chain
	// (business context + competitor discovery) as complete. Discovery may
	// legitimately yield zero competitors (empty SERP for a niche product), so
	// this flag — not a non-empty competitor list — is what advances
	// GetOnboardingSteps to CONTEXT_CREATED; the profile screen then requires
	// the user to add at least one competitor before finalising. Legacy docs
	// (pre-marker) advance on their non-empty competitor list instead.
	OnboardingAnalysisDone bool `bson:"onboarding_analysis_done,omitempty"`
	// OnboardingError records a permanently failed run of the async onboarding
	// analysis chain (business context / competitor discovery). Presence means
	// the chain is dead — no retry is in flight — so the steps endpoint reports
	// ONBOARDING_FAILED. Cleared ($unset) when OnboardUser re-dispatches the
	// chain on the user's retry.
	OnboardingError *OnboardingErrorData `bson:"onboarding_error,omitempty"`
	// Integrations is the analytics-engine connection state, keyed by source
	// name ("gsc", ...). NOT the same thing as BusinessContext.Integrations
	// (the user-listed tool names under context.integrations) — this map is
	// written only by the /v1/analytics/connections endpoints and the ingest
	// orchestrator, via dotted paths ("integrations.gsc.status"), and is
	// deliberately absent from the settings patch whitelist.
	Integrations map[string]IntegrationState `bson:"integrations,omitempty"`
	// StyleReplication holds the learned style artifacts (tone profile,
	// structure pattern, image style prompt) derived from the user's chosen
	// example articles. Pipeline-owned data at the top level (the Integrations
	// precedent, NOT inside BusinessContext): written by the style-replication
	// synthesis step, hand-editable through the patch whitelist's
	// style_replication.* paths, removed wholesale by the profile DELETE
	// endpoint. Nil = never learned. See [[Style Replication]].
	StyleReplication *StyleReplication `bson:"style_replication,omitempty"`
	CreatedAt        time.Time         `bson:"created_at,omitempty"`
	UpdatedAt        time.Time         `bson:"updated_at,omitempty"`
}

// StyleReplication is the learned per-tenant style profile. Every artifact is
// optional — an article-only run learns tone + structure + title pattern, an
// image-only run learns just the image style prompts, and the two image
// positions learn independently (a run with only thumbnail references leaves
// the mid-article slot empty).
type StyleReplication struct {
	ToneProfile      *string `bson:"tone_profile,omitempty"`
	StructurePattern *string `bson:"structure_pattern,omitempty"`
	// TitlePattern is derived from the scraped articles' TITLES specifically
	// (casing, shape, punctuation, number/year habits) — the scheduling
	// engine's title prompts prefer it over the body-level ToneProfile.
	TitlePattern *string `bson:"title_pattern,omitempty"`
	// Per-position image style prompts: thumbnails learn from og:image/hero
	// references, mid-article from in-article imagery.
	ThumbnailStylePrompt  *string `bson:"thumbnail_style_prompt,omitempty"`
	MidArticleStylePrompt *string `bson:"mid_article_style_prompt,omitempty"`
	// Apply is the user's per-artifact opt-in/out: which learned artifacts
	// actually shape generation. nil (struct or field) = applied — learning
	// something means using it until the user says otherwise. A disabled
	// artifact keeps its text (the settings editor still shows and edits it)
	// but the applied getters below return "" for it, which is what every
	// generation consumer reads — so a toggle needs no consumer changes.
	// Synthesis re-runs overwrite artifact text but never touch Apply, so the
	// user's choices survive a re-learn.
	Apply      *StyleApplySelection `bson:"apply,omitempty"`
	SourceURLs []string             `bson:"source_urls,omitempty"`
	LearnedAt  *time.Time           `bson:"learned_at,omitempty"`
}

// StyleApplySelection is the per-artifact toggle set. Pointer bools so absent
// fields (docs written before a toggle existed) default to applied.
type StyleApplySelection struct {
	ToneProfile      *bool `bson:"tone_profile,omitempty"`
	StructurePattern *bool `bson:"structure_pattern,omitempty"`
	TitlePattern     *bool `bson:"title_pattern,omitempty"`
	ThumbnailStyle   *bool `bson:"thumbnail_style,omitempty"`
	MidArticleStyle  *bool `bson:"mid_article_style,omitempty"`
}

func applyFlag(v *bool) bool { return v == nil || *v }

// Per-artifact applied checks — nil-safe on the receiver, default true.

func (s *StyleReplication) ToneApplied() bool {
	return s == nil || s.Apply == nil || applyFlag(s.Apply.ToneProfile)
}

func (s *StyleReplication) StructureApplied() bool {
	return s == nil || s.Apply == nil || applyFlag(s.Apply.StructurePattern)
}

func (s *StyleReplication) TitleApplied() bool {
	return s == nil || s.Apply == nil || applyFlag(s.Apply.TitlePattern)
}

func (s *StyleReplication) ThumbnailStyleApplied() bool {
	return s == nil || s.Apply == nil || applyFlag(s.Apply.ThumbnailStyle)
}

func (s *StyleReplication) MidArticleStyleApplied() bool {
	return s == nil || s.Apply == nil || applyFlag(s.Apply.MidArticleStyle)
}

// Nil-safe APPLIED artifact getters — prompt builders call these off a
// possibly-nil profile, so absent AND user-disabled artifacts render nothing.
// The settings editor reads the raw fields instead (a disabled artifact keeps
// its text on screen).

func (s *StyleReplication) Tone() string {
	if s == nil || s.ToneProfile == nil || !s.ToneApplied() {
		return ""
	}
	return strings.TrimSpace(*s.ToneProfile)
}

func (s *StyleReplication) Structure() string {
	if s == nil || s.StructurePattern == nil || !s.StructureApplied() {
		return ""
	}
	return strings.TrimSpace(*s.StructurePattern)
}

func (s *StyleReplication) TitleStyle() string {
	if s == nil || s.TitlePattern == nil || !s.TitleApplied() {
		return ""
	}
	return strings.TrimSpace(*s.TitlePattern)
}

// TitleToneFallback is the tone profile as a title-call fallback (profiles
// learned before title analysis existed). It requires BOTH toggles: turning
// title styling off must stop styled titles entirely, not fall back to tone.
func (s *StyleReplication) TitleToneFallback() string {
	if !s.TitleApplied() {
		return ""
	}
	return s.Tone()
}

func (s *StyleReplication) ThumbnailImageStyle() string {
	if s == nil || s.ThumbnailStylePrompt == nil || !s.ThumbnailStyleApplied() {
		return ""
	}
	return strings.TrimSpace(*s.ThumbnailStylePrompt)
}

func (s *StyleReplication) MidArticleImageStyle() string {
	if s == nil || s.MidArticleStylePrompt == nil || !s.MidArticleStyleApplied() {
		return ""
	}
	return strings.TrimSpace(*s.MidArticleStylePrompt)
}

// StyleProfile returns the entity's learned style profile (nil-safe on the
// receiver so callers can chain the artifact getters directly).
func (w *WebEntity) StyleProfile() *StyleReplication {
	if w == nil {
		return nil
	}
	return w.StyleReplication
}

// SetWebEntityStyleReplication overwrites the whole learned profile — the
// synthesis completion write. Overwrite-on-success semantics: a re-run only
// lands here after its own synthesis succeeded.
func SetWebEntityStyleReplication(ctx context.Context, entityID primitive.ObjectID, sr StyleReplication) error {
	return UpdateOne(ctx, webEntityCollection, bson.M{"_id": entityID}, bson.M{"$set": bson.M{
		"style_replication": sr,
		"updated_at":        time.Now(),
	}})
}

// UnsetWebEntityStyleReplication removes the learned profile wholesale (the
// settings "Remove" action). Idempotent — unsetting an absent field matches
// zero paths and still succeeds.
func UnsetWebEntityStyleReplication(ctx context.Context, entityID primitive.ObjectID) error {
	return UpdateOne(ctx, webEntityCollection, bson.M{"_id": entityID}, bson.M{
		"$unset": bson.M{"style_replication": ""},
		"$set":   bson.M{"updated_at": time.Now()},
	})
}

// Integration connection statuses stored on IntegrationState.Status.
const (
	IntegrationStatusConnected    = "connected"
	IntegrationStatusError        = "error"
	IntegrationStatusDisconnected = "disconnected"
)

// IntegrationState is one analytics source's connection state on a WebEntity
// (analytics engine LLD §2.3). Config carries source-specific keys — gsc:
// {"property": "sc-domain:foo.com"}.
type IntegrationState struct {
	Status      string            `bson:"status"`
	ConnectedAt time.Time         `bson:"connected_at"`
	LastSyncAt  time.Time         `bson:"last_sync_at,omitempty"`
	LastError   string            `bson:"last_error,omitempty"`
	Config      map[string]string `bson:"config,omitempty"`
}

// Integration returns the connection state for one analytics source, with a
// found flag (nil-safe on the receiver and the map).
func (w *WebEntity) Integration(source string) (IntegrationState, bool) {
	if w == nil || w.Integrations == nil {
		return IntegrationState{}, false
	}
	st, ok := w.Integrations[source]
	return st, ok
}

// SetWebEntityIntegration overwrites one source's whole integration state via
// a dotted path, leaving the other sources' entries untouched.
func SetWebEntityIntegration(ctx context.Context, entityID primitive.ObjectID, source string, state IntegrationState) error {
	return UpdateOne(ctx, webEntityCollection, bson.M{"_id": entityID}, bson.M{"$set": bson.M{
		"integrations." + source: state,
		"updated_at":             time.Now(),
	}})
}

// SetWebEntityIntegrationFields $sets individual dotted fields under one
// source's integration state (e.g. status/last_error on a fetch 403, or
// last_sync_at after an ingest run) without touching its other fields.
func SetWebEntityIntegrationFields(ctx context.Context, entityID primitive.ObjectID, source string, fields bson.M) error {
	set := bson.M{"updated_at": time.Now()}
	for k, v := range fields {
		set["integrations."+source+"."+k] = v
	}
	return UpdateOne(ctx, webEntityCollection, bson.M{"_id": entityID}, bson.M{"$set": set})
}

// RepresentativeUserIDForWebEntity picks the user a per-entity async unit is
// dispatched under (dispatch is user-keyed): the legacy entity owner when
// present, the company's owner otherwise. Used by the analytics cron resolver
// and admin replay fan-out.
func RepresentativeUserIDForWebEntity(ctx context.Context, entity *WebEntity) (string, error) {
	if !entity.UserID.IsZero() {
		return entity.UserID.Hex(), nil
	}
	if entity.CompanyID.IsZero() {
		return "", fmt.Errorf("web entity %s has neither user_id nor company_id", entity.ID.Hex())
	}
	found, company, err := FindCompanyByID(ctx, entity.CompanyID)
	if err != nil {
		return "", fmt.Errorf("load company %s: %w", entity.CompanyID.Hex(), err)
	}
	if !found || company.OwnerUserID.IsZero() {
		return "", fmt.Errorf("company %s missing or ownerless", entity.CompanyID.Hex())
	}
	return company.OwnerUserID.Hex(), nil
}

// FindWebEntitiesWithIntegrationStatus lists entities whose named source is in
// the given status — the cron resolver read for connection-requiring analytics
// sources (one indexed-enough scan; the connected set is small).
func FindWebEntitiesWithIntegrationStatus(ctx context.Context, source, status string) ([]*WebEntity, error) {
	cursor, err := Collection(webEntityCollection).Find(ctx,
		bson.M{"integrations." + source + ".status": status})
	if err != nil {
		return nil, fmt.Errorf("find web entities with %s integration %s: %w", source, status, err)
	}
	defer cursor.Close(ctx)
	var entities []*WebEntity
	if err := cursor.All(ctx, &entities); err != nil {
		return nil, fmt.Errorf("decode web entities with %s integration %s: %w", source, status, err)
	}
	return entities, nil
}

// FindFinalisedWebEntities lists every finalised entity — the cron resolver
// read for connection-less analytics sources (dfs_domain_rating covers every
// live site regardless of GSC state).
func FindFinalisedWebEntities(ctx context.Context) ([]*WebEntity, error) {
	cursor, err := Collection(webEntityCollection).Find(ctx, bson.M{"finalised": true})
	if err != nil {
		return nil, fmt.Errorf("find finalised web entities: %w", err)
	}
	defer cursor.Close(ctx)
	var entities []*WebEntity
	if err := cursor.All(ctx, &entities); err != nil {
		return nil, fmt.Errorf("decode finalised web entities: %w", err)
	}
	return entities, nil
}

type OnboardingErrorData struct {
	Step    string    `bson:"step,omitempty"`
	Message string    `bson:"message,omitempty"`
	At      time.Time `bson:"at,omitempty"`
}

// SetWebEntityOnboardingError stamps the onboarding-failure marker on a web
// entity. Called from the async registry when an onboarding process exhausts
// its retries (or fails permanently), mirroring how SIE failures append to the
// WebEntityContext via AppendErrorData.
func SetWebEntityOnboardingError(ctx context.Context, webEntityId, step, message string) error {
	oid, err := primitive.ObjectIDFromHex(webEntityId)
	if err != nil {
		return fmt.Errorf("invalid web entity ID: %w", err)
	}
	return UpdateOne(ctx, webEntityCollection, bson.M{"_id": oid}, bson.M{"$set": bson.M{
		"onboarding_error": OnboardingErrorData{Step: step, Message: message, At: time.Now()},
		"updated_at":       time.Now(),
	}})
}

// InternalLinkingEnabledOrDefault resolves the web entity's internal-linking
// default, treating an unset (nil) value as enabled. Internal linking is on by
// default; legacy docs and any not yet touched by the backfill migration have
// no stored value and must still behave as enabled.
func (w *WebEntity) InternalLinkingEnabledOrDefault() bool {
	return w == nil || w.InternalLinkingEnabled == nil || *w.InternalLinkingEnabled
}

// PublishAsLiveOrDefault resolves the generic default, treating unset (nil) as
// draft (false) — the product default is "push as draft". Mirrors the polarity
// of the persisted field so the settings toggle can render it directly.
func (w *WebEntity) PublishAsLiveOrDefault() bool {
	return w != nil && w.PublishAsLive != nil && *w.PublishAsLive
}

// ThumbnailStyleOrDefault resolves the web entity's thumbnail-style default,
// treating an unset (nil) or since-removed (invalid) value as the system
// default. The frontend renders the resolved value directly, so a nil default
// correctly highlights the default style's card.
func (w *WebEntity) ThumbnailStyleOrDefault() string {
	if w == nil || w.ThumbnailStyle == nil || !prompts.IsValidThumbnailStyle(*w.ThumbnailStyle) {
		return prompts.DefaultThumbnailStyleID
	}
	return *w.ThumbnailStyle
}

// SEOStrategyOrDefault resolves the entity's SIE strategy: an explicit valid
// choice wins; otherwise the default is derived from the stored domain rating
// (unset rating → 0 → early_footholds, the right call for domains DataForSEO
// has no authority data for). Every SIE consumer resolves through here so an
// entity never runs with an undefined strategy.
func (w *WebEntity) SEOStrategyOrDefault() string {
	if w == nil {
		return DefaultSEOStrategyForDR(0)
	}
	if w.SEOStrategy != nil && IsValidSEOStrategy(*w.SEOStrategy) {
		return *w.SEOStrategy
	}
	dr := 0
	if w.BusinessContext != nil {
		dr = w.BusinessContext.UserDomainRating
	}
	return DefaultSEOStrategyForDR(dr)
}

type PublishingConfig struct {
	Platform string `bson:"platform,omitempty" json:"platform,omitempty"`
	ApiKey   string `bson:"api_key,omitempty" json:"apiKey,omitempty"`
	// ProjectURL is the platform project the credentials belong to (Framer:
	// the project URL passed to the Server API's connect(); Payload: the
	// server base URL). Required when the platform needs one — validated in
	// sanitizePublishing.
	ProjectURL   string `bson:"project_url,omitempty" json:"projectUrl,omitempty"`
	CollectionID string `bson:"collection_id,omitempty" json:"collectionId,omitempty"`
	// AuthCollection is the Payload collection the API key's user lives in.
	// Optional — the adapter defaults to "users" when empty.
	AuthCollection  string `bson:"auth_collection,omitempty" json:"authCollection,omitempty"`
	ArticlesPerWeek int    `bson:"articles_per_week,omitempty" json:"articlesPerWeek,omitempty"`
	PublishMode     string `bson:"publish_mode,omitempty" json:"publishMode,omitempty"`
}

type BusinessContext struct {
	// ExtractionFailed is the extraction LLM's structural failure signal: the
	// scraped content did not describe an identifiable business (error page,
	// parked domain, bot check). bson:"-" on purpose — a failed extraction is
	// never persisted, so the flag has no reason to exist in Mongo.
	ExtractionFailed  bool        `bson:"-" json:"extraction_failed,omitempty"`
	BusinessName      *string     `bson:"business_name,omitempty" json:"business_name,omitempty"`
	Website           *string     `bson:"website,omitempty" json:"website,omitempty"`
	ProductType       *string     `bson:"product_type,omitempty" json:"product_type,omitempty"`
	PrimaryUseCase    *string     `bson:"primary_use_case,omitempty" json:"primary_use_case,omitempty"`
	KeyFeatures       []string    `bson:"key_features,omitempty" json:"key_features,omitempty"`
	Integrations      []string    `bson:"integrations,omitempty" json:"integrations,omitempty"`
	BusinessModel     *string     `bson:"business_model,omitempty" json:"business_model,omitempty"`
	TargetGeography   *string     `bson:"target_geography,omitempty" json:"target_geography,omitempty"`
	PricingModel      *string     `bson:"pricing_model,omitempty" json:"pricing_model,omitempty"`
	KeyDifferentiator *string     `bson:"key_differentiator,omitempty" json:"key_differentiator,omitempty"`
	ICPSignals        *ICPSignals `bson:"icp_signals,omitempty" json:"icp_signals,omitempty"`
	BrandVoiceSignals *string     `bson:"brand_voice_signals,omitempty" json:"brand_voice_signals,omitempty"`
	InferredFields    []string    `bson:"inferred_fields,omitempty" json:"inferred_fields,omitempty"`
	UserDomainRating  int         `bson:"user_domain_rating,omitempty" json:"user_domain_rating,omitempty"`
}

type ICPSignals struct {
	Roles       []string `bson:"roles,omitempty" json:"roles,omitempty"`
	Industries  []string `bson:"industries,omitempty" json:"industries,omitempty"`
	CompanySize *string  `bson:"company_size,omitempty" json:"company_size,omitempty"`
	PainPoints  []string `bson:"pain_points,omitempty" json:"pain_points,omitempty"`
}

// MissingCoreFields reports which of the fields the SIE prompt pipeline
// consumes unconditionally (keyword expansion, clustering, funnel
// classification) are absent or blank. A non-empty result means the context is
// not complete enough to finalise onboarding or run SIE. Nil-safe.
func (bc *BusinessContext) MissingCoreFields() []string {
	if bc == nil {
		return []string{"context"}
	}
	blank := func(p *string) bool { return p == nil || strings.TrimSpace(*p) == "" }
	var missing []string
	if blank(bc.BusinessName) {
		missing = append(missing, "business_name")
	}
	if blank(bc.ProductType) {
		missing = append(missing, "product_type")
	}
	if blank(bc.KeyDifferentiator) {
		missing = append(missing, "key_differentiator")
	}
	if len(bc.KeyFeatures) == 0 {
		missing = append(missing, "key_features")
	}
	if bc.ICPSignals == nil || len(bc.ICPSignals.Roles) == 0 {
		missing = append(missing, "icp_signals.roles")
	}
	if bc.ICPSignals == nil || len(bc.ICPSignals.PainPoints) == 0 {
		missing = append(missing, "icp_signals.pain_points")
	}
	return missing
}

type Competitor struct {
	Domain      string `bson:"url,omitempty" json:"domain,omitempty"`
	CompanyName string `bson:"company_name,omitempty" json:"company_name,omitempty"`
	Reason      string `bson:"reason,omitempty" json:"reason,omitempty"`
}

func FindWebEntitiesByUserID(ctx context.Context, userId string) ([]*WebEntity, error) {
	userOID, err := primitive.ObjectIDFromHex(userId)
	if err != nil {
		return nil, fmt.Errorf("invalid user ID: %w", err)
	}
	cursor, err := Collection(webEntityCollection).Find(ctx, bson.M{"user_id": userOID})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var entities []*WebEntity
	if err := cursor.All(ctx, &entities); err != nil {
		return nil, err
	}
	return entities, nil
}

// GetLifetimeArticlesGeneratedForEntity reads the entity's monotonic
// generation counter. Read fresh from the DB (projected — not off a cached
// entity snapshot) so the free-plan cap check always sees increments from
// concurrent generations.
func GetLifetimeArticlesGeneratedForEntity(ctx context.Context, webEntityID primitive.ObjectID) (int, error) {
	return readLifetimeArticlesGenerated(ctx, bson.M{"_id": webEntityID})
}

// GetLifetimeArticlesGeneratedForUser resolves the counter through the user's
// single web entity (unique index on user_id). 0 when the user has no entity
// yet. Used where only the user is in hand (the payments status meter).
func GetLifetimeArticlesGeneratedForUser(ctx context.Context, userID primitive.ObjectID) (int, error) {
	return readLifetimeArticlesGenerated(ctx, bson.M{"user_id": userID})
}

func readLifetimeArticlesGenerated(ctx context.Context, filter bson.M) (int, error) {
	var doc struct {
		LifetimeArticlesGenerated int `bson:"lifetime_articles_generated"`
	}
	err := Collection(webEntityCollection).FindOne(ctx, filter,
		options.FindOne().SetProjection(bson.M{"lifetime_articles_generated": 1}),
	).Decode(&doc)
	if err == mongo.ErrNoDocuments {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read lifetime articles generated: %w", err)
	}
	return doc.LifetimeArticlesGenerated, nil
}

// IncrementLifetimeArticlesGeneratedForEntity bumps the monotonic generation
// counter. Only RecordArticleGenerationStart (and the backfill migration)
// should call this — the per-slot claim there is what keeps retries from
// double-counting.
func IncrementLifetimeArticlesGeneratedForEntity(ctx context.Context, webEntityID primitive.ObjectID) error {
	_, err := Collection(webEntityCollection).UpdateOne(ctx,
		bson.M{"_id": webEntityID},
		bson.M{"$inc": bson.M{"lifetime_articles_generated": 1}},
	)
	if err != nil {
		return fmt.Errorf("increment lifetime articles generated for entity %s: %w", webEntityID.Hex(), err)
	}
	return nil
}

// FindWebEntityByUserID resolves the single WebEntity for a user. Onboarding is
// one-web-entity-per-user (enforced by the unique index on user_id), so a found
// match is unambiguous. Returns (false, nil, nil) when the user has none yet.
func FindWebEntityByUserID(ctx context.Context, userId string) (bool, *WebEntity, error) {
	userOID, err := primitive.ObjectIDFromHex(userId)
	if err != nil {
		return false, nil, fmt.Errorf("invalid user ID: %w", err)
	}

	var entity WebEntity
	found, err := FindOne(ctx, webEntityCollection, bson.M{"user_id": userOID}, &entity)
	if err != nil {
		return false, nil, err
	}
	if !found {
		return false, nil, nil
	}
	return true, &entity, nil
}

// IsWebEntityFinalised reports whether the user has finished onboarding — a
// finalised web entity exists. Card-less trials gate on this: no trial before
// the site is set up. One indexed count on the unique {user_id} index.
func IsWebEntityFinalised(ctx context.Context, userID primitive.ObjectID) (bool, error) {
	n, err := Collection(webEntityCollection).CountDocuments(ctx,
		bson.M{"user_id": userID, "finalised": true},
		options.Count().SetLimit(1))
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func FindWebEntityByID(ctx context.Context, id string) (bool, *WebEntity, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return false, nil, err
	}

	var entity WebEntity
	found, err := FindOne(ctx, webEntityCollection, bson.M{"_id": oid}, &entity)
	if err != nil {
		return false, nil, err
	}
	if !found {
		return false, nil, nil
	}
	return true, &entity, nil
}

func CreateWebEntity(ctx context.Context, entity *WebEntity) error {
	now := time.Now()
	entity.CreatedAt = now
	entity.UpdatedAt = now

	id, err := InsertOne(ctx, webEntityCollection, entity)
	if err != nil {
		return err
	}
	entity.ID = id
	return nil
}

func UpdateWebEntity(ctx context.Context, id string, req WebEntity) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return err
	}

	update := bson.M{}

	if req.Competitors != nil {
		update["competitors"] = req.Competitors
	}
	if req.BusinessContext != nil {
		bc := req.BusinessContext
		if bc.BusinessName != nil {
			update["context.business_name"] = *bc.BusinessName
		}
		if bc.Website != nil {
			update["context.website"] = *bc.Website
		}
		if bc.ProductType != nil {
			update["context.product_type"] = *bc.ProductType
		}
		if bc.PrimaryUseCase != nil {
			update["context.primary_use_case"] = *bc.PrimaryUseCase
		}
		if bc.KeyFeatures != nil {
			update["context.key_features"] = bc.KeyFeatures
		}
		if bc.Integrations != nil {
			update["context.integrations"] = bc.Integrations
		}
		if bc.BusinessModel != nil {
			update["context.business_model"] = *bc.BusinessModel
		}
		if bc.TargetGeography != nil {
			update["context.target_geography"] = *bc.TargetGeography
		}
		if bc.PricingModel != nil {
			update["context.pricing_model"] = *bc.PricingModel
		}
		if bc.KeyDifferentiator != nil {
			update["context.key_differentiator"] = *bc.KeyDifferentiator
		}
		if bc.ICPSignals != nil {
			update["context.icp_signals"] = bc.ICPSignals
		}
		if bc.BrandVoiceSignals != nil {
			update["context.brand_voice_signals"] = *bc.BrandVoiceSignals
		}
		if bc.InferredFields != nil {
			update["context.inferred_fields"] = bc.InferredFields
		}
		if bc.UserDomainRating != 0 {
			update["context.user_domain_rating"] = bc.UserDomainRating
		}
	}

	if len(update) == 0 {
		return nil
	}

	update["updated_at"] = time.Now()

	return UpdateOne(ctx, webEntityCollection, bson.M{"_id": oid}, bson.M{"$set": update})
}

// PartialUpdateWebEntity applies arbitrary $set and $unset documents to a
// WebEntity. Caller is responsible for ensuring field paths are whitelisted
// and values are sanitized — this is the low-level escape hatch used by the
// patch flow. updated_at is always bumped.
func PartialUpdateWebEntity(ctx context.Context, id string, set bson.M, unset bson.M) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return err
	}

	if len(set) == 0 && len(unset) == 0 {
		return nil
	}

	if set == nil {
		set = bson.M{}
	}
	set["updated_at"] = time.Now()

	update := bson.M{"$set": set}
	if len(unset) > 0 {
		update["$unset"] = unset
	}

	return UpdateOne(ctx, webEntityCollection, bson.M{"_id": oid}, update)
}

// DeleteWebEntityByUserID removes the user's web entity (unique per user).
// Admin deletion cascade only — always the LAST step, after every child
// collection is gone, so a mid-cascade crash leaves the parent as the re-run
// anchor. Zero matches is a no-op so re-runs converge.
// FindWebEntityIDsByUserID lists the _ids of the user's web entities (same
// user_id filter the deletion cascade removes by) — resolved BEFORE
// DeleteWebEntityByUserID so the analytics raw/facts cascade still knows which
// entities to clear.
func FindWebEntityIDsByUserID(ctx context.Context, userID primitive.ObjectID) ([]primitive.ObjectID, error) {
	cursor, err := Collection(webEntityCollection).Find(ctx,
		bson.M{"user_id": userID},
		options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return nil, fmt.Errorf("find web entity ids by user: %w", err)
	}
	defer cursor.Close(ctx)
	var rows []struct {
		ID primitive.ObjectID `bson:"_id"`
	}
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("decode web entity ids by user: %w", err)
	}
	ids := make([]primitive.ObjectID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids, nil
}

func DeleteWebEntityByUserID(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	res, err := Collection(webEntityCollection).DeleteMany(ctx, bson.M{"user_id": userID})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// FindWebEntityByCompanyID resolves the company's single WebEntity (unique by
// the company_id index). Returns (false, nil, nil) when the company has none.
func FindWebEntityByCompanyID(ctx context.Context, companyID primitive.ObjectID) (bool, *WebEntity, error) {
	var entity WebEntity
	found, err := FindOne(ctx, webEntityCollection, bson.M{"company_id": companyID}, &entity)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &entity, nil
}

// FindWebEntityForCompany is the TRANSITION read: by company_id first, then
// falling back to the legacy user_id key for docs companymigrate hasn't
// stamped yet. The fallback goes away with the user_id index in the last
// phase. fallbackUserID may be zero (no fallback).
func FindWebEntityForCompany(ctx context.Context, companyID, fallbackUserID primitive.ObjectID) (bool, *WebEntity, error) {
	found, entity, err := FindWebEntityByCompanyID(ctx, companyID)
	if err != nil || found {
		return found, entity, err
	}
	if fallbackUserID.IsZero() {
		return false, nil, nil
	}
	var byUser WebEntity
	found, err = FindOne(ctx, webEntityCollection, bson.M{fieldUserID: fallbackUserID}, &byUser)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &byUser, nil
}

// SetWebEntityCompanyID stamps company_id on an existing doc (the
// companymigrate backfill write).
func SetWebEntityCompanyID(ctx context.Context, entityID, companyID primitive.ObjectID) error {
	return UpdateOne(ctx, webEntityCollection, bson.M{fieldID: entityID}, bson.M{"$set": bson.M{
		"company_id":   companyID,
		fieldUpdatedAt: time.Now(),
	}})
}
