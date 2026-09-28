// Package entitlements owns feature-based authorization: the typed feature
// keys, the plans cache (price_id → plan/app resolution), and the request-time
// resolver. It sits between models (storage) and middleware (enforcement).
package entitlements

// Feature is a feature key. Typed constants are the only way code references
// features; plan docs and overrides store these strings. Unknown strings in
// the DB are tolerated (forward compat) — but admin override writes validate
// against the known set so a typo'd revoke can't silently no-op.
type Feature string

const (
	// indexly (SEO blog)
	FeatureArticleGenerate Feature = "article.generate"  // seo-blog orchestrate/retry
	FeatureArticleSchedule Feature = "article.schedule"  // scheduled-articles scheduling
	FeatureArticleEdit     Feature = "article.edit"      // draft/edit/delete/image-upload/publish-state
	FeatureArticleAIAssist Feature = "article.ai_assist" // AI title help: suggest-titles/retitle-preview
	FeatureCMSPublish      Feature = "cms.publish"       // content-bridge publish
	FeatureKeywordManual   Feature = "keyword.manual"    // site-intelligence manual keyword
	FeatureKeywordResearch Feature = "keyword.research"  // site-intelligence dispatch-* pipelines
	FeatureSiteProcess     Feature = "site.process"      // site-intelligence process
	// View features gate the read-only GET surfaces (keyword data, generated
	// articles, calendar). Every real plan includes them; the trial_expired
	// virtual plan includes ONLY them, which is what makes any expired
	// entitlement (timed-out trial or lapsed paid sub) browse-but-not-act.
	FeatureKeywordView  Feature = "keyword.view"  // site-intelligence keyword-data/cluster-supporting/keyword-search
	FeatureArticleView  Feature = "article.view"  // seo-blog read surfaces (thumbnail-styles catalog)
	FeatureScheduleView Feature = "schedule.view" // scheduled-articles list + article reads
	// FeatureAnalyticsView gates the /v1/analytics surface. Declared-but-
	// universal: granted in EVERY plan (including free) in plans.json, so
	// gating later is a config change, not a code change.
	FeatureAnalyticsView Feature = "analytics.view"
	// FeatureStyleReplication gates the /v1/style-replication surface (learn
	// tone/structure/image style from existing articles). Declared-but-
	// universal like analytics.view: granted in every plan incl. the free
	// trial list, registered so future gating is a config change. NOT in
	// trial_expired — it's a write surface, and lapsed users are view-only.
	FeatureStyleReplication Feature = "style.replication"
	// FeatureAudit gates the tenant /v1/audit surface (SEO/AEO audit engine).
	// Declared-but-universal: granted in every plan in plans.json so future
	// per-plan gating is a config change (LLD §11); usage limits (one free
	// audit per website, weekly paid cadence) are enforced in the service
	// guards, not here. The public lead-magnet routes bypass entitlements
	// entirely.
	FeatureAudit Feature = "audit.run"
	// extend per app
)

// TierTrialExpired is the virtual (active:false, priceless) plan tier the
// resolver falls back to for ANY expired entitlement — a timed-out local trial
// OR a lapsed paid sub past its paid-through date: view features only, so the
// user can browse what they produced but every write 402s. (The tier is named
// for its first use; it now serves lapsed paid subs too.)
const TierTrialExpired = "trial_expired"

// TierTeam is the per-seat company plan: one Paddle subscription on the
// company whose quantity buys seats, projected as a full entitlement onto
// every claimed member. Same feature list as Pro, no trial. Ships inactive
// with no price until the Paddle team product exists (post-merge ops flip).
const TierTeam = "team"

// knownFeatures is the validation set for admin override writes.
var knownFeatures = map[Feature]bool{
	FeatureArticleGenerate:  true,
	FeatureArticleSchedule:  true,
	FeatureArticleEdit:      true,
	FeatureArticleAIAssist:  true,
	FeatureCMSPublish:       true,
	FeatureKeywordManual:    true,
	FeatureKeywordResearch:  true,
	FeatureSiteProcess:      true,
	FeatureKeywordView:      true,
	FeatureArticleView:      true,
	FeatureScheduleView:     true,
	FeatureAnalyticsView:    true,
	FeatureStyleReplication: true,
	FeatureAudit:            true,
}

// IsKnownFeature reports whether key is a defined feature constant.
func IsKnownFeature(key string) bool {
	return knownFeatures[Feature(key)]
}
