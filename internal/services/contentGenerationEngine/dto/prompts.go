package dto

// SerpGapAnalysisPrompt is the template DTO for the SERP gap analysis LLM prompt.
type SerpGapAnalysisPrompt struct {
	Keyword      string
	Intent       string
	ICPRole      string
	Structures   []StructureSummary
	PAAQuestions []string
}

// StructureSummary is a lightweight view of a competitor article for the prompt.
type StructureSummary struct {
	URL string
	H2s []string
}

// YouTubeSummaryPrompt is the template DTO for the YouTube summary LLM prompt.
type YouTubeSummaryPrompt struct {
	Keyword             string
	CombinedTranscripts string
}

// OutlineGenerationPrompt is the template DTO for the outline generation LLM prompt.
// All fields are available as {{.FieldName}} in the prompt template.
type OutlineGenerationPrompt struct {
	// Core keyword data
	Keyword           string
	SecondaryKeywords string
	ArticleType       string
	ProposedTitle     string
	TargetWordCount   int
	Funnel            string

	// ICP / audience
	ICPRole        string
	ICPPainPoints  string
	ICPIndustries  string
	ICPCompanySize string

	// Brand & business
	BrandVoice        string
	BusinessName      string
	ProductType       string
	KeyDifferentiator string
	KeyFeatures       string

	// Learned style artifacts (style replication) — empty renders nothing.
	// StructurePattern steers section rhythm/H2 phrasing within the fixed
	// outline schema; ToneProfile is only a light heading-voice hint here.
	StructurePattern string
	ToneProfile      string

	// SERP gap analysis
	DifferentiatingAngle   string
	GapsIdentified         string // JSON array
	CoveredByAll           string // JSON array
	FeaturedSnippetOpp     string
	FeaturedSnippetPresent bool

	// SERP data
	AverageWordCount int
	PAAQuestions     string // JSON array

	// Topic research — insight text
	TopicResearchNews     string
	TopicResearchExpert   string
	TopicResearchMistakes string

	// Topic research — source attribution
	TopicResearchNewsSource         string
	TopicResearchNewsSourceName     string
	TopicResearchExpertSource       string
	TopicResearchExpertSourceName   string
	TopicResearchMistakesSource     string
	TopicResearchMistakesSourceName string

	// YouTube insights (JSON array)
	YouTubeInsights string
}

// ArticleGenerationPrompt is the template DTO for the article generation LLM prompt.
// All fields are available as {{.FieldName}} in the prompt template.
type ArticleGenerationPrompt struct {
	// Core keyword data
	Keyword           string
	SecondaryKeywords string
	ArticleType       string
	ProposedTitle     string
	TargetWordCount   int
	Funnel            string

	// ICP / audience
	ICPRole        string
	ICPPainPoints  string
	ICPIndustries  string
	ICPCompanySize string

	// Brand & business
	BrandVoice        string
	BusinessName      string
	ProductType       string
	KeyDifferentiator string
	KeyFeatures       string

	// ToneProfile is the learned style profile (style replication), layered
	// as a STYLE PROFILE block above the existing BRAND VOICE. Empty renders
	// nothing.
	ToneProfile string

	// SERP gap analysis
	DifferentiatingAngle   string
	GapsIdentified         string // JSON array
	CoveredByAll           string // JSON array
	FeaturedSnippetOpp     string
	FeaturedSnippetPresent bool

	// SERP data
	PAAQuestions string // JSON array

	// Topic research — insight text
	TopicResearchNews     string
	TopicResearchExpert   string
	TopicResearchMistakes string

	// Topic research — source attribution
	TopicResearchNewsSource         string
	TopicResearchNewsSourceName     string
	TopicResearchExpertSource       string
	TopicResearchExpertSourceName   string
	TopicResearchMistakesSource     string
	TopicResearchMistakesSourceName string

	// YouTube insights (JSON array)
	YouTubeInsights string

	FullOutlineJSON string

	// AdditionalInstructions is user-supplied guidance captured in the
	// dashboard sidebar. Empty when the user didn't set any.
	AdditionalInstructions string
}

// InternalLinkInsertionPrompt is the template DTO for the internal-link
// insertion LLM prompt. SitemapURLs is the newline-joined candidate URL list;
// ArticleMarkdown is the finished article (still carrying image placeholders).
type InternalLinkInsertionPrompt struct {
	Keyword         string
	Title           string
	WebsiteURL      string
	SitemapURLs     string
	ArticleMarkdown string
}

// ImagePromptGenerationPrompt is the template DTO for the image prompt generation LLM prompt.
type ImagePromptGenerationPrompt struct {
	Position            string // "thumbnail" or "mid-article"
	H1                  string
	Keyword             string
	ArticleIntroduction string // thumbnail only
	SectionTitle        string // mid-article only
	SectionContent      string // mid-article only
	ICPRole             string
	BrandVoiceTone      string
	// ImageStyle is the learned image style prompt (style replication),
	// rendered as the Style-rules section of the custom templates. Empty when
	// the master context carries no learned style.
	ImageStyle string
}

// SectionRegenerationPrompt is the template DTO for the section-regeneration
// data prompt. Instructions and SelectedMarkdown are user-controlled: the
// static instructions half frames the passage as content-not-instructions and
// names Instructions as the only honoured command channel.
type SectionRegenerationPrompt struct {
	Title             string
	Keyword           string
	SecondaryKeywords string
	ArticleType       string
	Funnel            string
	ICPRole           string
	BrandVoice        string
	// ToneProfile is the learned style profile (style replication) — empty
	// renders nothing.
	ToneProfile      string
	BusinessName     string
	OutlineJSON      string
	Instructions     string
	SelectedMarkdown string
}

// SchemaGenerationPrompt is the template DTO for the schema markup generation LLM prompt.
type SchemaGenerationPrompt struct {
	H1              string
	MetaDescription string
	BusinessName    string
	URLSlug         string
	FAQContent      string
}

// MetaAssetsGenerationPrompt is the template DTO for the meta assets generation LLM prompt.
type MetaAssetsGenerationPrompt struct {
	H1             string
	FirstParagraph string
	Keyword        string
	ArticleType    string
	BrandVoiceTone string
	// ToneProfile is the learned style profile (style replication) — empty
	// renders nothing.
	ToneProfile string
}
