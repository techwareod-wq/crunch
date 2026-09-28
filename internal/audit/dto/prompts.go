// Package dto holds the audit engine's prompt input/output structs — the
// ConstructPrompt targets for the two LLM calls (content judgment +
// narrative) and their parsed response shapes.
package dto

// ContentJudgmentPage is one sampled page's material for the E-E-A-T rubric.
type ContentJudgmentPage struct {
	URL     string
	Title   string
	Excerpt string
}

// ContentJudgmentPrompt renders prompts/content_judgment.md.
type ContentJudgmentPrompt struct {
	Domain    string
	PageCount int
	Pages     []ContentJudgmentPage
	// EvidenceJSON is the deterministic sibling-check evidence (content
	// scorers, trust signals, parasite markers) serialized for the prompt
	// (decision 12: hard evidence, not vibes).
	EvidenceJSON string
	// SiteContext feeds the strategy section (quality uplift 4.2): detected
	// stack, site age, authority numbers, brand footprint. Empty fields are
	// simply omitted from the prompt.
	SiteContext ContentJudgmentSiteContext
}

// ContentJudgmentSiteContext is the strategy section's raw material.
type ContentJudgmentSiteContext struct {
	DetectedTech        []string
	EarliestContentDate string
	// Domain overview numbers (zero = unknown, omitted in-template).
	DomainRating     int
	ReferringDomains int64
	KeywordsCount    int64
	// Brand footprint (present only when the mentions collector ran).
	HasMentionsData   bool
	ThirdPartyCount   int
	ThirdPartyDomains []string
	// Category occupancy (present only when the mentions collector derived
	// and probed a category phrase): who owns the query the site exists to
	// answer. TargetPosition 0 = absent from the probed results.
	CategoryQuery          string
	CategoryTopDomains     []string
	CategoryTargetPosition int
	// ContentPageTitles are "title — path" lines for the site's content pages
	// (capped) — the raw material for concrete topic-cluster/hub proposals.
	ContentPageTitles []string
	// PAAQuestions are People-Also-Ask questions from the SERPs of ranking
	// keywords — the questions searchers actually ask in this space.
	PAAQuestions []string
	// Structured-data + llms.txt state, measured by the deterministic layer.
	// Without these the strategy model guessed ("no schema markup evidence
	// surfaces") and contradicted the scored schema category in the same
	// report.
	SchemaTypesPresent      []string
	OrganizationSameAsCount int
	HasLlmsTxt              bool
	LlmsTxtHasKeyFacts      bool
	// InvalidSchemaBlockCount counts sampled JSON-LD blocks that failed to
	// parse — their declared types are UNKNOWN, so the model must not claim
	// a specific type is absent (a broken block may declare it). Without this
	// the judgment reported "SoftwareApplication schema missing" on a page
	// whose SoftwareApplication block was merely invalid, contradicting the
	// schema-validity finding in the same report.
	InvalidSchemaBlockCount int
	InvalidSchemaPages      []string
}

// ContentJudgmentResponse is the judgment call's parsed JSON.
type ContentJudgmentResponse struct {
	Score     int                       `json:"score"`
	SubScores ContentJudgmentSubScores  `json:"subScores"`
	Findings  []ContentJudgmentFinding  `json:"findings"`
	Strategy  []ContentJudgmentStrategy `json:"strategy"`
}

// ContentJudgmentStrategy is one strategic opportunity the model surfaces —
// narrative output with NO falsifiability contract and NO score impact
// (quality uplift 4.2).
type ContentJudgmentStrategy struct {
	Title     string   `json:"title"`
	Detail    string   `json:"detail"`
	Rationale string   `json:"rationale"`
	Plays     []string `json:"plays"`
}

// ContentJudgmentSubScores mirrors the locked E-E-A-T sub-weights
// (Trust 30 / Expertise 25 / Authority 25 / Experience 20).
type ContentJudgmentSubScores struct {
	Trust      int `json:"trust"`
	Expertise  int `json:"expertise"`
	Authority  int `json:"authority"`
	Experience int `json:"experience"`
}

// ContentJudgmentFinding is one LLM-authored finding; the check maps it onto
// core.Finding (validating the severity enum).
type ContentJudgmentFinding struct {
	Severity       string   `json:"severity"`
	Title          string   `json:"title"`
	Detail         string   `json:"detail"`
	Pages          []string `json:"pages"`
	Recommendation string   `json:"recommendation"`
	Falsifiability string   `json:"falsifiability"`
	// Snippet is the optional exact artifact (replacement copy / markup)
	// implementing the recommendation — mapped onto core.Finding.Snippet.
	Snippet string `json:"snippet"`
}

// NarrativeFinding is one top finding fed to the narrative call.
type NarrativeFinding struct {
	Severity string
	Category string
	Title    string
	Detail   string
}

// NarrativeCategoryScore is one category row for the narrative's context
// table (quality uplift 4.3).
type NarrativeCategoryScore struct {
	Label  string
	Score  int
	Scored bool
}

// NarrativePrompt renders prompts/narrative.md.
type NarrativePrompt struct {
	Domain       string
	OverallScore int
	Findings     []NarrativeFinding
	// Context (quality uplift 4.3): category scores, stack, site age, and the
	// strengths list — so the narrative can SEQUENCE work instead of listing
	// it.
	CategoryScores      []NarrativeCategoryScore
	DetectedTech        []string
	EarliestContentDate string
	Strengths           []string
}

// NarrativeResponse is the narrative call's parsed JSON.
type NarrativeResponse struct {
	Narrative string `json:"narrative"`
}
