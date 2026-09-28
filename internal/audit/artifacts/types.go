package artifacts

// The normalized artifact shapes. Additive evolution only: a new field on an
// existing struct is a Tier C step-4 extension — old artifacts simply lack it,
// and checks must treat a missing/zero field as "not assessed", never as a
// failure.

// --- KindCrawl ---

// CrawlArtifact is the normalized OnPage crawl output.
type CrawlArtifact struct {
	// StartURL is the crawl entry point (the audited target URL).
	StartURL string `bson:"start_url" json:"startUrl"`
	// PagesCrawled is how many pages the crawl actually fetched.
	PagesCrawled int `bson:"pages_crawled" json:"pagesCrawled"`
	// PageCap is the max_crawl_pages the task ran with.
	PageCap int `bson:"page_cap" json:"pageCap"`
	// OnPageScore is DataForSEO's own 0-100 crawl score (Domain Overview
	// ride-along, decision 8 — never a scoring input).
	OnPageScore float64 `bson:"onpage_score" json:"onpageScore"`
	// SitemapInRobots reports whether the crawl found a sitemap declared for
	// the domain (domain_info.checks.sitemap).
	SitemapInRobots bool `bson:"sitemap_in_robots" json:"sitemapInRobots"`
	// RobotsTxtFound reports domain_info.checks.robots_txt.
	RobotsTxtFound bool `bson:"robots_txt_found" json:"robotsTxtFound"`
	// ValidCertificate reports the SSL check from domain_info.ssl_info.
	// CertificateAssessed distinguishes "provider assessed SSL and it failed"
	// from "ssl_info was absent" — a zero-value false must never fire the
	// invalid-certificate finding (house rule: missing ≠ failing).
	ValidCertificate    bool `bson:"valid_certificate" json:"validCertificate"`
	CertificateAssessed bool `bson:"certificate_assessed,omitempty" json:"certificateAssessed,omitempty"`
	// LinksAssessed distinguishes "links endpoint returned data (possibly
	// zero broken)" from "the links fetch failed and degraded" — the latter
	// must not score as zero broken links.
	LinksAssessed bool `bson:"links_assessed,omitempty" json:"linksAssessed,omitempty"`

	Pages []CrawlPage `bson:"pages" json:"pages"`
	// BrokenLinks are internal links whose target failed (from on_page/links
	// is_broken + target status codes).
	BrokenLinks []BrokenLink `bson:"broken_links,omitempty" json:"brokenLinks,omitempty"`
	// InternalLinksTotal/ExternalLinksTotal come from the crawl summary.
	InternalLinksTotal int `bson:"internal_links_total" json:"internalLinksTotal"`
	ExternalLinksTotal int `bson:"external_links_total" json:"externalLinksTotal"`
	// DuplicatePages are near-duplicate content groups (page duplicate_content
	// flags enriched by on_page/duplicate_content for a bounded sample).
	DuplicatePages []DuplicateContentGroup `bson:"duplicate_pages,omitempty" json:"duplicatePages,omitempty"`

	// DeepPassSample is the deep-pass page selection made at crawl time
	// (homepage + most-linked/shallowest, decision 15) — the htmldeep and serp
	// collectors read it so sampling lives in exactly one place.
	DeepPassSample []string `bson:"deep_pass_sample" json:"deepPassSample"`
}

// CrawlPage is one crawled HTML page's normalized fields.
type CrawlPage struct {
	URL        string `bson:"url" json:"url"`
	StatusCode int    `bson:"status_code" json:"statusCode"`
	ClickDepth int    `bson:"click_depth" json:"clickDepth"`

	Title           string `bson:"title,omitempty" json:"title,omitempty"`
	MetaDescription string `bson:"meta_description,omitempty" json:"metaDescription,omitempty"`
	Canonical       string `bson:"canonical,omitempty" json:"canonical,omitempty"`
	H1Count         int    `bson:"h1_count" json:"h1Count"`
	// H1Texts carries the page's H1 strings (capped) — cross-page duplicate
	// H1 detection needs the text, not just the count. Empty on legacy
	// artifacts (not assessed).
	H1Texts []string `bson:"h1_texts,omitempty" json:"h1Texts,omitempty"`

	WordCount          int     `bson:"word_count" json:"wordCount"`
	InternalLinksCount int     `bson:"internal_links_count" json:"internalLinksCount"`
	ExternalLinksCount int     `bson:"external_links_count" json:"externalLinksCount"`
	InboundLinksCount  int     `bson:"inbound_links_count" json:"inboundLinksCount"`
	ImagesCount        int     `bson:"images_count" json:"imagesCount"`
	OnPageScore        float64 `bson:"onpage_score" json:"onpageScore"`

	IsHTTPS    bool `bson:"is_https" json:"isHttps"`
	IsRedirect bool `bson:"is_redirect" json:"isRedirect"`
	IsBroken   bool `bson:"is_broken" json:"isBroken"`
	// NoIndex reflects a meta-robots/x-robots noindex on the page.
	NoIndex bool `bson:"no_index" json:"noIndex"`
	// CanonicalChain / CanonicalToRedirect / CanonicalToBroken /
	// RecursiveCanonical mirror the OnPage canonical checks.
	CanonicalChain      bool `bson:"canonical_chain,omitempty" json:"canonicalChain,omitempty"`
	CanonicalToRedirect bool `bson:"canonical_to_redirect,omitempty" json:"canonicalToRedirect,omitempty"`
	CanonicalToBroken   bool `bson:"canonical_to_broken,omitempty" json:"canonicalToBroken,omitempty"`
	RecursiveCanonical  bool `bson:"recursive_canonical,omitempty" json:"recursiveCanonical,omitempty"`
	// HTTPSToHTTPLinks flags https pages linking to http resources.
	HTTPSToHTTPLinks bool `bson:"https_to_http_links,omitempty" json:"httpsToHttpLinks,omitempty"`
	// DuplicateContent is OnPage's near-duplicate flag for the page.
	DuplicateContent bool `bson:"duplicate_content,omitempty" json:"duplicateContent,omitempty"`
	// LCPMs is the OnPage lab largest_contentful_paint (ms) — finding detail
	// only, never a Performance scoring input (decision 6).
	LCPMs float64 `bson:"lcp_ms,omitempty" json:"lcpMs,omitempty"`
}

// BrokenLink is one failed internal link.
type BrokenLink struct {
	FromURL string `bson:"from_url" json:"fromUrl"`
	ToURL   string `bson:"to_url" json:"toUrl"`
}

// DuplicateContentGroup records one near-duplicate relationship surfaced by
// the crawl (similarity on DataForSEO's 0-10 scale when known, 0 = unknown).
type DuplicateContentGroup struct {
	URL        string   `bson:"url" json:"url"`
	Similar    []string `bson:"similar" json:"similar"`
	Similarity int      `bson:"similarity,omitempty" json:"similarity,omitempty"`
}

// --- KindHTMLDeep ---

// HTMLDeepArtifact is the deep-pass output: per-sampled-page extracted
// features plus site-level probes. NEVER raw HTML.
type HTMLDeepArtifact struct {
	Pages []DeepPage `bson:"pages" json:"pages"`

	// Site-level probes (fetched once, against the validated target host).
	Sitemap  SitemapProbe  `bson:"sitemap" json:"sitemap"`
	Robots   RobotsProbe   `bson:"robots" json:"robots"`
	IndexNow IndexNowProbe `bson:"indexnow" json:"indexnow"`
	// LlmsTxtFound reports /llms.txt presence — reported, zero ranking weight
	// (fidelity must, scope §5). LlmsTxt carries the parsed depth signals
	// (still zero weight — richer findings only). LlmsFullTxtFound reports the
	// companion /llms-full.txt (the full-content variant coding agents read).
	LlmsTxtFound     bool         `bson:"llms_txt_found" json:"llmsTxtFound"`
	LlmsTxt          LlmsTxtProbe `bson:"llms_txt,omitempty" json:"llmsTxt,omitempty"`
	LlmsFullTxtFound bool         `bson:"llms_full_txt_found,omitempty" json:"llmsFullTxtFound,omitempty"`
	// Headers is the homepage response-header capture (security headers).
	Headers HeadersProbe `bson:"headers,omitempty" json:"headers,omitempty"`
	// Soft404 records the deterministic nonexistent-URL probe.
	Soft404 Soft404Probe `bson:"soft404,omitempty" json:"soft404,omitempty"`
	// SiteFiles are the small findings-only presence probes.
	SiteFiles SiteFilesProbe `bson:"site_files,omitempty" json:"siteFiles,omitempty"`

	// DetectedTech unions per-page stack markers (Next.js, WordPress, ...) —
	// LLM-layer context only, never a scoring input.
	DetectedTech []string `bson:"detected_tech,omitempty" json:"detectedTech,omitempty"`
	// EarliestContentDate is the min of sampled published dates + sitemap
	// lastmods (YYYY-MM-DD) — a site-age proxy for the LLM layers.
	EarliestContentDate string `bson:"earliest_content_date,omitempty" json:"earliestContentDate,omitempty"`
}

// LlmsTxtProbe is the parsed /llms.txt body (it's markdown — cheap line
// parsing). Zero score contribution ever — the locked fidelity-must stands.
type LlmsTxtProbe struct {
	Found bool `bson:"found" json:"found"`
	Bytes int  `bson:"bytes,omitempty" json:"bytes,omitempty"`
	// LinkCount counts markdown links; HasHomepageLink / LinksAboutOrProduct
	// report whether the file covers the homepage/about/product layer or only
	// lists content.
	LinkCount           int      `bson:"link_count,omitempty" json:"linkCount,omitempty"`
	HasHomepageLink     bool     `bson:"has_homepage_link,omitempty" json:"hasHomepageLink,omitempty"`
	LinksAboutOrProduct bool     `bson:"links_about_or_product,omitempty" json:"linksAboutOrProduct,omitempty"`
	SectionHeadings     []string `bson:"section_headings,omitempty" json:"sectionHeadings,omitempty"`
	// HasSummary reports a "> ..." blockquote summary line (the llms.txt
	// convention for the one-line identity statement). FactLineCount counts
	// bullet lines stating plain facts (no URL) — the "Key facts" depth an
	// agent can quote without fetching anything; HasKeyFacts is the ≥3
	// threshold on it.
	HasSummary    bool `bson:"has_summary,omitempty" json:"hasSummary,omitempty"`
	FactLineCount int  `bson:"fact_line_count,omitempty" json:"factLineCount,omitempty"`
	HasKeyFacts   bool `bson:"has_key_facts,omitempty" json:"hasKeyFacts,omitempty"`
}

// HeadersProbe captures the homepage response's security headers. Fetched
// false ⇒ the probe never landed and the check skips (never guess).
type HeadersProbe struct {
	Fetched             bool `bson:"fetched" json:"fetched"`
	HSTS                bool `bson:"hsts" json:"hsts"`
	CSP                 bool `bson:"csp" json:"csp"`
	XContentTypeOptions bool `bson:"x_content_type_options" json:"xContentTypeOptions"`
	// FrameProtection is X-Frame-Options OR a CSP frame-ancestors directive.
	FrameProtection   bool `bson:"frame_protection" json:"frameProtection"`
	ReferrerPolicy    bool `bson:"referrer_policy" json:"referrerPolicy"`
	PermissionsPolicy bool `bson:"permissions_policy" json:"permissionsPolicy"`
	// CacheControlNoStore flags Cache-Control: no-store on the homepage
	// document — it disqualifies the page from the back/forward cache.
	CacheControlNoStore bool `bson:"cache_control_no_store,omitempty" json:"cacheControlNoStore,omitempty"`
}

// Soft404Probe is the GET of a deterministic per-run nonexistent path.
// Status is the initial response; FinalStatus is after following redirects
// (equal to Status when no redirect happened).
type Soft404Probe struct {
	Fetched     bool `bson:"fetched" json:"fetched"`
	Status      int  `bson:"status,omitempty" json:"status,omitempty"`
	FinalStatus int  `bson:"final_status,omitempty" json:"finalStatus,omitempty"`
}

// SiteFilesProbe reports the small conventional files (findings-only).
type SiteFilesProbe struct {
	Probed           bool `bson:"probed" json:"probed"`
	ManifestFound    bool `bson:"manifest_found" json:"manifestFound"`
	SecurityTxtFound bool `bson:"security_txt_found" json:"securityTxtFound"`
}

// SitemapProbe is the sitemap discovery + quality snapshot. The per-entry
// detail (Entries + the coverage counts) is a quality-uplift extension —
// artifacts predating it carry EntryCount > 0 with empty Entries, and the
// sitemap check falls back to the legacy HasLastmod scoring for them.
type SitemapProbe struct {
	Found      bool   `bson:"found" json:"found"`
	URL        string `bson:"url,omitempty" json:"url,omitempty"`
	EntryCount int    `bson:"entry_count" json:"entryCount"`
	HasLastmod bool   `bson:"has_lastmod" json:"hasLastmod"`
	// LastmodCount / PriorityCount / ChangefreqCount count entries carrying
	// each field — lastmod coverage is prorated, priority/changefreq is noise
	// Google ignores.
	LastmodCount    int `bson:"lastmod_count,omitempty" json:"lastmodCount,omitempty"`
	PriorityCount   int `bson:"priority_count,omitempty" json:"priorityCount,omitempty"`
	ChangefreqCount int `bson:"changefreq_count,omitempty" json:"changefreqCount,omitempty"`
	// Entries lists the sitemap's page entries (capped at the fetch's MaxURLs,
	// 500) — the coverage-delta and burst signals read them.
	Entries []SitemapProbeEntry `bson:"entries,omitempty" json:"entries,omitempty"`
}

// SitemapProbeEntry is one sitemap <url> row (LastMod as a YYYY-MM-DD prefix,
// empty when the entry carries none).
type SitemapProbeEntry struct {
	URL     string `bson:"url" json:"url"`
	LastMod string `bson:"lastmod,omitempty" json:"lastmod,omitempty"`
}

// RobotsProbe is the robots.txt AI-crawler posture. Crawlers maps a known AI
// user-agent token to whether it is ALLOWED (true) under the site's rules.
type RobotsProbe struct {
	Found bool `bson:"found" json:"found"`
	// DisallowAll flags a blanket "User-agent: * / Disallow: /".
	DisallowAll bool            `bson:"disallow_all" json:"disallowAll"`
	Crawlers    map[string]bool `bson:"crawlers,omitempty" json:"crawlers,omitempty"`
	// HasExplicitAIRules reports whether any known AI crawler appears as its
	// own User-agent group (vs riding the * default) — explicit rules document
	// intent and survive a future blanket Disallow.
	HasExplicitAIRules bool `bson:"has_explicit_ai_rules,omitempty" json:"hasExplicitAiRules,omitempty"`
	// CommentLineCount counts # comment lines; SuspectComments carries the
	// ones that read as internal engineering notes (source paths, tooling,
	// bug references) — the file is public, and those comments are free
	// reconnaissance.
	CommentLineCount int      `bson:"comment_line_count,omitempty" json:"commentLineCount,omitempty"`
	SuspectComments  []string `bson:"suspect_comments,omitempty" json:"suspectComments,omitempty"`
	// HasHostDirective reports a Host: line — a Yandex-only convention that
	// Google and Bing ignore (dead weight worth a cleanup nudge).
	HasHostDirective bool `bson:"has_host_directive,omitempty" json:"hasHostDirective,omitempty"`
	// SitemapDirectiveCount counts Sitemap: lines — 0 with a live sitemap is
	// a missed hint, ≥2 is duplicate boilerplate worth a cleanup nudge.
	SitemapDirectiveCount int `bson:"sitemap_directive_count,omitempty" json:"sitemapDirectiveCount,omitempty"`
}

// IndexNowProbe reports IndexNow adoption as far as it is detectable from the
// outside (a conventional key file at /indexnow.txt).
type IndexNowProbe struct {
	KeyFileFound bool `bson:"key_file_found" json:"keyFileFound"`
}

// DeepPage is one sampled page's extracted features.
type DeepPage struct {
	URL     string `bson:"url" json:"url"`
	Fetched bool   `bson:"fetched" json:"fetched"`
	// BlockedReason is set when the fetch failed (ErrScrapeBlocked etc.) —
	// the per-page constraint of §8.3.
	BlockedReason string `bson:"blocked_reason,omitempty" json:"blockedReason,omitempty"`
	IsHomepage    bool   `bson:"is_homepage" json:"isHomepage"`

	Title     string `bson:"title,omitempty" json:"title,omitempty"`
	WordCount int    `bson:"word_count" json:"wordCount"`

	// Readability inputs (content.readability).
	SentenceCount        int     `bson:"sentence_count" json:"sentenceCount"`
	AvgSentenceWords     float64 `bson:"avg_sentence_words" json:"avgSentenceWords"`
	ParagraphCount       int     `bson:"paragraph_count" json:"paragraphCount"`
	LongParagraphCount   int     `bson:"long_paragraph_count" json:"longParagraphCount"` // > ~150 words
	FleschReadingEase    float64 `bson:"flesch_reading_ease" json:"fleschReadingEase"`
	UniqueWordRatio      float64 `bson:"unique_word_ratio" json:"uniqueWordRatio"`            // info-density scorer input
	TopKeywordDensityPct float64 `bson:"top_keyword_density_pct" json:"topKeywordDensityPct"` // most frequent non-stopword, % of words

	// Body-scoped links (quality uplift 1.1/1.2): anchors inside the content
	// container only (first <article>, else <main>, else the document minus
	// nav/header/footer subtrees). BodyLinksAssessed distinguishes "extracted
	// and found zero" from an artifact predating these fields.
	BodyLinksAssessed     bool     `bson:"body_links_assessed,omitempty" json:"bodyLinksAssessed,omitempty"`
	BodyInternalLinkCount int      `bson:"body_internal_link_count,omitempty" json:"bodyInternalLinkCount,omitempty"`
	BodyInternalTargets   []string `bson:"body_internal_targets,omitempty" json:"bodyInternalTargets,omitempty"` // resolved absolute URLs, capped
	BodyExternalLinkCount int      `bson:"body_external_link_count,omitempty" json:"bodyExternalLinkCount,omitempty"`
	// BodyCitationCount excludes CTA/social domains (wa.me, socials, own
	// domain) — the "real citations" count trust_signals keys off.
	BodyCitationCount   int      `bson:"body_citation_count,omitempty" json:"bodyCitationCount,omitempty"`
	BodyCitationDomains []string `bson:"body_citation_domains,omitempty" json:"bodyCitationDomains,omitempty"`

	// H1 text-extraction sanity (quality uplift 2.1): the first H1's extracted
	// text plus deterministic corruption signals (animated heroes dumping every
	// rotation state into textContent).
	H1Text          string `bson:"h1_text,omitempty" json:"h1Text,omitempty"`
	H1Suspect       bool   `bson:"h1_suspect,omitempty" json:"h1Suspect,omitempty"`
	H1SuspectReason string `bson:"h1_suspect_reason,omitempty" json:"h1SuspectReason,omitempty"`

	// Rendered-vs-raw parity (quality uplift 2.2): what a no-JS fetch of the
	// same URL yields. RawFetchOK false ⇒ skip the page (never guess).
	RawFetchOK   bool `bson:"raw_fetch_ok,omitempty" json:"rawFetchOk,omitempty"`
	RawWordCount int  `bson:"raw_word_count,omitempty" json:"rawWordCount,omitempty"`
	RawH1Found   bool `bson:"raw_h1_found,omitempty" json:"rawH1Found,omitempty"`
	// RawMetaDescriptionFound reports a non-empty meta description in the
	// no-JS fetch — a rendered-only description is invisible to raw-HTML
	// consumers (and was the path a missing-description page slipped through:
	// the JS-rendered crawl saw a description the raw HTML never shipped).
	RawMetaDescriptionFound bool `bson:"raw_meta_description_found,omitempty" json:"rawMetaDescriptionFound,omitempty"`
	// RawFallback flags that the "rendered" fetch itself fell back to the
	// no-JS direct path (e.g. DataForSEO's host was busy) — this document was
	// never browser-rendered, so rendered-dependent comparisons must treat
	// the page as not assessed. Legacy artifacts (field absent) predate the
	// flag and keep their original rendered assumption.
	RawFallback bool `bson:"raw_fallback,omitempty" json:"rawFallback,omitempty"`

	// Head hygiene (quality uplift 2.4).
	HasOGTitle     bool   `bson:"has_og_title,omitempty" json:"hasOgTitle,omitempty"`
	HasOGImage     bool   `bson:"has_og_image,omitempty" json:"hasOgImage,omitempty"`
	HasTwitterCard bool   `bson:"has_twitter_card,omitempty" json:"hasTwitterCard,omitempty"`
	HasTwitterSite bool   `bson:"has_twitter_site,omitempty" json:"hasTwitterSite,omitempty"`
	PageLang       string `bson:"page_lang,omitempty" json:"pageLang,omitempty"`

	// TechMarkers are per-page stack detections, unioned into the artifact's
	// DetectedTech (LLM context only).
	TechMarkers []string `bson:"tech_markers,omitempty" json:"techMarkers,omitempty"`

	// Structure (AEO).
	Headings          []DeepHeading `bson:"headings,omitempty" json:"headings,omitempty"`
	QuestionHeadings  int           `bson:"question_headings" json:"questionHeadings"`
	AnswerBlockCount  int           `bson:"answer_block_count" json:"answerBlockCount"` // 134-167-word self-contained blocks
	ListCount         int           `bson:"list_count" json:"listCount"`
	TableCount        int           `bson:"table_count" json:"tableCount"`
	VideoEmbedCount   int           `bson:"video_embed_count" json:"videoEmbedCount"`
	InternalLinkCount int           `bson:"internal_link_count" json:"internalLinkCount"`
	ExternalLinkCount int           `bson:"external_link_count" json:"externalLinkCount"`

	// Schema (JSON-LD).
	SchemaBlocks []SchemaBlock `bson:"schema_blocks,omitempty" json:"schemaBlocks,omitempty"`

	// Trust signals.
	HasByline      bool   `bson:"has_byline" json:"hasByline"`
	AuthorName     string `bson:"author_name,omitempty" json:"authorName,omitempty"`
	HasDates       bool   `bson:"has_dates" json:"hasDates"`
	PublishedDate  string `bson:"published_date,omitempty" json:"publishedDate,omitempty"`
	ModifiedDate   string `bson:"modified_date,omitempty" json:"modifiedDate,omitempty"`
	LinksAboutPage bool   `bson:"links_about_page" json:"linksAboutPage"`
	LinksContact   bool   `bson:"links_contact" json:"linksContact"`
	// OrganizationSameAsCount counts sameAs entries on an Organization/Person
	// schema block (aisearch.authority_proxies).
	OrganizationSameAsCount int  `bson:"organization_same_as_count" json:"organizationSameAsCount"`
	HasOrganizationSchema   bool `bson:"has_organization_schema" json:"hasOrganizationSchema"`
	// OrganizationHasLogo/URL report whether the Organization block declares
	// logo/url (schema.coverage's completeness nudge).
	OrganizationHasLogo bool `bson:"organization_has_logo,omitempty" json:"organizationHasLogo,omitempty"`
	OrganizationHasURL  bool `bson:"organization_has_url,omitempty" json:"organizationHasUrl,omitempty"`
	// SchemaInLanguage is the first inLanguage value any schema block carries
	// (head_hygiene's lang-consistency nudge).
	SchemaInLanguage string `bson:"schema_in_language,omitempty" json:"schemaInLanguage,omitempty"`

	// Content scorers (decision 11 — fed to the LLM rubric as evidence).
	FillerPhraseCount   int     `bson:"filler_phrase_count" json:"fillerPhraseCount"`
	AIPatternCount      int     `bson:"ai_pattern_count" json:"aiPatternCount"`
	RepeatedSentencePct float64 `bson:"repeated_sentence_pct" json:"repeatedSentencePct"`

	// Parasite / site-reputation-abuse markers (deterministic path+topic
	// heuristics; findings-only by nature).
	ParasiteMarkers []string `bson:"parasite_markers,omitempty" json:"parasiteMarkers,omitempty"`

	// Agent-UX (semantic-HTML operability).
	HasMain          bool    `bson:"has_main" json:"hasMain"`
	HasArticleTag    bool    `bson:"has_article_tag" json:"hasArticleTag"`
	HasNav           bool    `bson:"has_nav" json:"hasNav"`
	LandmarkCount    int     `bson:"landmark_count" json:"landmarkCount"`
	ButtonsWithText  int     `bson:"buttons_with_text" json:"buttonsWithText"`
	ButtonsTotal     int     `bson:"buttons_total" json:"buttonsTotal"`
	InputsWithLabels int     `bson:"inputs_with_labels" json:"inputsWithLabels"`
	InputsTotal      int     `bson:"inputs_total" json:"inputsTotal"`
	DivRatio         float64 `bson:"div_ratio" json:"divRatio"` // div+span share of element nodes

	// Speculation Rules / bfcache (performance.speculation_bfcache).
	HasSpeculationRules bool `bson:"has_speculation_rules" json:"hasSpeculationRules"`
	HasUnloadHandler    bool `bson:"has_unload_handler" json:"hasUnloadHandler"`

	// Animation-heaviness proxies (findings-only): inline <svg> elements plus
	// animation hints (@keyframes in inline styles, "animate" class tokens).
	// Heavy animation is where INP typically fails on mid-range mobile — a
	// nudge worth surfacing precisely when CrUX has no field data yet.
	InlineSVGCount     int `bson:"inline_svg_count,omitempty" json:"inlineSvgCount,omitempty"`
	AnimationHintCount int `bson:"animation_hint_count,omitempty" json:"animationHintCount,omitempty"`

	// Images.
	Images []DeepImage `bson:"images,omitempty" json:"images,omitempty"`

	// ContentExcerpt is the capped plain-text sample the Content LLM judgment
	// reads (the one place page text survives into an artifact).
	ContentExcerpt string `bson:"content_excerpt,omitempty" json:"contentExcerpt,omitempty"`
}

// DeepHeading is one heading in document order.
type DeepHeading struct {
	Level int    `bson:"level" json:"level"`
	Text  string `bson:"text" json:"text"`
}

// SchemaBlock is one JSON-LD script's validation summary.
type SchemaBlock struct {
	Types      []string `bson:"types,omitempty" json:"types,omitempty"`
	Valid      bool     `bson:"valid" json:"valid"`
	ParseError string   `bson:"parse_error,omitempty" json:"parseError,omitempty"`
	// MissingContext flags a block without @context.
	MissingContext bool `bson:"missing_context,omitempty" json:"missingContext,omitempty"`
	// HasPlaceholder flags template placeholder text left in the block
	// ("[Business Name]", "REPLACE_ME") — markup that was never filled in.
	HasPlaceholder bool `bson:"has_placeholder,omitempty" json:"hasPlaceholder,omitempty"`
}

// DeepImage is one in-content image's audit-relevant attributes.
type DeepImage struct {
	Src    string `bson:"src" json:"src"`
	Alt    string `bson:"alt" json:"alt"`
	HasAlt bool   `bson:"has_alt" json:"hasAlt"`
	// Width/Height are the DECLARED attribute values (0 = undeclared or
	// non-numeric). DimensionsDeclared reports whether width AND height
	// attributes were present at all — width="100%" declares dimensions
	// (no CLS risk) while parsing to 0.
	Width              int  `bson:"width" json:"width"`
	Height             int  `bson:"height" json:"height"`
	DimensionsDeclared bool `bson:"dimensions_declared,omitempty" json:"dimensionsDeclared,omitempty"`
	// Tier buckets the image by declared size against the 3-tier table
	// (thumbnail/content/hero, scope §5): "thumbnail" | "content" | "hero" |
	// "" when size is undeclared.
	Tier string `bson:"tier,omitempty" json:"tier,omitempty"`
	// Oversized flags a declared size far above its tier ceiling.
	Oversized bool `bson:"oversized,omitempty" json:"oversized,omitempty"`
}

// --- KindPSI ---

// PSIArtifact is the PageSpeed Insights / CrUX snapshot for the homepage
// (mobile). Field data scores the Performance category; lab data rides along
// as finding detail only (decision 6).
type PSIArtifact struct {
	// HasFieldData reports whether CrUX had enough traffic for origin metrics
	// — without it performance.cwv skips and the category renormalizes.
	HasFieldData bool `bson:"has_field_data" json:"hasFieldData"`
	// Origin field metrics (75th percentile).
	OriginLCPMs float64 `bson:"origin_lcp_ms" json:"originLcpMs"`
	OriginINPMs float64 `bson:"origin_inp_ms" json:"originInpMs"`
	OriginCLS   float64 `bson:"origin_cls" json:"originCls"`
	// Page-level field metrics when CrUX has them (0 = absent).
	PageLCPMs float64 `bson:"page_lcp_ms,omitempty" json:"pageLcpMs,omitempty"`
	PageINPMs float64 `bson:"page_inp_ms,omitempty" json:"pageInpMs,omitempty"`
	PageCLS   float64 `bson:"page_cls,omitempty" json:"pageCls,omitempty"`
	// LabPerformanceScore is Lighthouse's 0-100 performance score (detail).
	LabPerformanceScore float64 `bson:"lab_performance_score" json:"labPerformanceScore"`
	// Opportunities are the top Lighthouse savings (finding detail + minor
	// score input for performance.psi_opportunities).
	Opportunities []PSIOpportunity `bson:"opportunities,omitempty" json:"opportunities,omitempty"`
}

// PSIOpportunity is one Lighthouse opportunity audit.
type PSIOpportunity struct {
	ID        string  `bson:"id" json:"id"`
	Title     string  `bson:"title" json:"title"`
	SavingsMs float64 `bson:"savings_ms" json:"savingsMs"`
}

// --- KindAuthority ---

// AuthorityArtifact is the backlinks summary + Labs domain rank overview.
type AuthorityArtifact struct {
	// LocationCode is the DataForSEO market the rank overview was queried
	// against (Labs data is per-location; 0 = legacy artifact, queried US).
	LocationCode int `bson:"location_code,omitempty" json:"locationCode,omitempty"`
	// DomainRating is the 0-100 backlink authority score (rank_scale
	// one_hundred — the utils.FetchDomainRating task shape).
	DomainRating int `bson:"domain_rating" json:"domainRating"`
	// HasBacklinkData is false when the API had no authority data at all.
	HasBacklinkData    bool  `bson:"has_backlink_data" json:"hasBacklinkData"`
	Backlinks          int64 `bson:"backlinks" json:"backlinks"`
	ReferringDomains   int64 `bson:"referring_domains" json:"referringDomains"`
	ReferringMainDoms  int64 `bson:"referring_main_domains" json:"referringMainDomains"`
	NofollowRefDomains int64 `bson:"nofollow_ref_domains" json:"nofollowRefDomains"`
	BrokenBacklinks    int64 `bson:"broken_backlinks" json:"brokenBacklinks"`

	// Rank overview (Labs domain_rank_overview — Domain Overview strip data).
	HasRankOverview bool    `bson:"has_rank_overview" json:"hasRankOverview"`
	KeywordsCount   int64   `bson:"keywords_count" json:"keywordsCount"`
	OrganicETV      float64 `bson:"organic_etv" json:"organicEtv"`
	Pos1            int64   `bson:"pos_1" json:"pos1"`
	Pos2_3          int64   `bson:"pos_2_3" json:"pos2_3"`
	Pos4_10         int64   `bson:"pos_4_10" json:"pos4_10"`
	Pos11_20        int64   `bson:"pos_11_20" json:"pos11_20"`
	Pos21_30        int64   `bson:"pos_21_30" json:"pos21_30"`
	Pos31Plus       int64   `bson:"pos_31_plus" json:"pos31Plus"`
}

// --- KindMentions ---

// Category-probe intent values (MentionsArtifact.CategoryQueryIntent).
const (
	// CategoryIntentBuyer marks a de-hyped buyer-intent query ("best ai audit
	// software") — absence from its results is a real distribution gap.
	CategoryIntentBuyer = "buyer"
	// CategoryIntentSelf marks the site's own phrase searched verbatim —
	// presence proves nothing (self-coined terms rank trivially), absence is a
	// mild note at most.
	CategoryIntentSelf = "self"
)

// MentionsArtifact is the brand-mention footprint snapshot: distinct
// third-party root domains mentioning the brand across two SERP queries
// (quoted domain + quoted label excluding the site). Zero third-party
// footprint makes AI citation structurally impossible — the highest-leverage
// external-audit insight.
type MentionsArtifact struct {
	QueriedTerms      []string `bson:"queried_terms" json:"queriedTerms"`
	ThirdPartyDomains []string `bson:"third_party_domains,omitempty" json:"thirdPartyDomains,omitempty"`
	ThirdPartyCount   int      `bson:"third_party_count" json:"thirdPartyCount"`
	// KeySurfaces flags presence on the surfaces AI engines actually cite
	// (reddit, producthunt, g2, wikipedia, github, ...).
	KeySurfaces map[string]bool `bson:"key_surfaces,omitempty" json:"keySurfaces,omitempty"`

	// Category occupancy: one SERP query for the site's own category phrase
	// (derived from the homepage title minus the brand token). Who actually
	// owns the query the site exists to answer — the framing a brand-mention
	// count alone can't provide. CategoryProbed false = no phrase could be
	// derived or the query failed (legacy artifacts too) — never scored,
	// never guessed.
	CategoryProbed bool `bson:"category_probed,omitempty" json:"categoryProbed,omitempty"`
	// CategoryQuery is the derived phrase the probe searched.
	CategoryQuery string `bson:"category_query,omitempty" json:"categoryQuery,omitempty"`
	// CategoryQueryIntent distinguishes a buyer-intent query ("best ai audit
	// software") from the site's own self-described phrase searched verbatim.
	// A self-phrase result is a weak signal: any site trivially ranks for the
	// category name it coined. Empty = legacy artifact (self-phrase probe).
	CategoryQueryIntent string `bson:"category_query_intent,omitempty" json:"categoryQueryIntent,omitempty"`
	// CategorySourcePhrase is the raw homepage-title phrase the query was
	// derived from (transparency for the report/judgment layers).
	CategorySourcePhrase string `bson:"category_source_phrase,omitempty" json:"categorySourcePhrase,omitempty"`
	// CategoryTopDomains are the distinct organic domains in rank order
	// (capped), the target included when it appears.
	CategoryTopDomains []string `bson:"category_top_domains,omitempty" json:"categoryTopDomains,omitempty"`
	// CategoryTargetPosition is the target's 1-based organic position for the
	// query (0 = absent from the probed depth).
	CategoryTargetPosition int `bson:"category_target_position,omitempty" json:"categoryTargetPosition,omitempty"`
}

// --- KindSERP ---

// SERPArtifact is the SXO data: for each sampled page with a known ranking
// keyword, the SERP item types present for that keyword.
type SERPArtifact struct {
	Pages []SXOPage `bson:"pages" json:"pages"`
}

// SXOPage is one page's SERP-experience snapshot.
type SXOPage struct {
	URL          string `bson:"url" json:"url"`
	Keyword      string `bson:"keyword" json:"keyword"`
	SearchVolume int    `bson:"search_volume" json:"searchVolume"`
	Position     int    `bson:"position" json:"position"`
	// ItemTypes are the SERP element types present on that keyword's page 1
	// (organic, featured_snippet, people_also_ask, video, images, ...).
	ItemTypes []string `bson:"item_types" json:"itemTypes"`
	// TopResults are the top organic results in rank order (capped) — the
	// competitive set the page-type consensus is computed from. Empty on
	// legacy artifacts (checks treat that as "not assessed").
	TopResults []SXOResult `bson:"top_results,omitempty" json:"topResults,omitempty"`
	// PAAQuestions are the People-Also-Ask question texts on that SERP —
	// the questions searchers actually ask around this keyword.
	PAAQuestions []string `bson:"paa_questions,omitempty" json:"paaQuestions,omitempty"`
}

// SXOResult is one organic competitor on a keyword's page 1.
type SXOResult struct {
	Rank   int    `bson:"rank" json:"rank"`
	Domain string `bson:"domain" json:"domain"`
	Title  string `bson:"title" json:"title"`
	URL    string `bson:"url" json:"url"`
}
