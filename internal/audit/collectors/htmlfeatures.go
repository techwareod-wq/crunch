package collectors

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/utils"
)

// Pure feature extraction over a parsed page — the deep pass's entire
// analysis surface. Everything here is deterministic and fixture-testable;
// the DOM is discarded after extraction (normalization rule: never persist
// raw HTML).

// deepExcerptChars caps the plain-text sample persisted for the LLM
// judgment.
const deepExcerptChars = 6000

// extractDeepPageFeatures walks one rendered page into its DeepPage feature
// set.
func extractDeepPageFeatures(doc *html.Node, pageURL string, isHomepage bool) artifacts.DeepPage {
	page := artifacts.DeepPage{URL: pageURL, Fetched: true, IsHomepage: isHomepage}

	base, _ := url.Parse(pageURL)

	// Global walk: chrome-agnostic page properties (landmarks, meta, scripts,
	// agent-UX counters). Content STRUCTURE — headings, paragraphs, lists,
	// images, video — is extracted by the body-scoped walk below, so footer
	// column titles and cookie modals can never masquerade as article
	// structure (gap-closure round 4: a /faqs page whose questions were all
	// <div>s passed the heading check on its six footer <h4>s).
	elementCount, divSpanCount := 0, 0
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			// Head-only tags aren't layout elements; SVG internals would let
			// 200 <path> nodes dilute the div-soup ratio.
			switch tag {
			case "meta", "link", "script", "style", "title", "head":
			default:
				elementCount++
			}
			if strings.Contains(strings.ToLower(attrValue(n, "class")), "animate") {
				page.AnimationHintCount++
			}
			switch tag {
			case "div", "span":
				divSpanCount++
			case "svg":
				page.InlineSVGCount++
				return // don't descend: SVG internals are drawing, not layout
			case "style":
				page.AnimationHintCount += strings.Count(nodeText(n), "@keyframes")
			case "title":
				if page.Title == "" {
					page.Title = strings.TrimSpace(nodeText(n))
				}
			case "main":
				page.HasMain = true
			case "article":
				page.HasArticleTag = true
			case "nav":
				page.HasNav = true
			case "header", "footer", "aside", "section":
				page.LandmarkCount++
			case "button":
				page.ButtonsTotal++
				if strings.TrimSpace(nodeText(n)) != "" || attrValue(n, "aria-label") != "" {
					page.ButtonsWithText++
				}
			case "input":
				typ := strings.ToLower(attrValue(n, "type"))
				if typ != "hidden" && typ != "submit" && typ != "button" {
					page.InputsTotal++
					if attrValue(n, "aria-label") != "" || attrValue(n, "id") != "" {
						page.InputsWithLabels++
					}
				}
			case "img":
				addTechMarker(&page, detectTechFromURL(attrValue(n, "src")))
			case "a":
				classifyAnchor(n, base, &page)
			case "script":
				classifyScript(n, &page)
			case "meta":
				classifyMeta(n, &page)
			case "body":
				if attrValue(n, "onunload") != "" || attrValue(n, "onbeforeunload") != "" {
					page.HasUnloadHandler = true
				}
			case "html":
				if page.PageLang == "" {
					page.PageLang = strings.TrimSpace(attrValue(n, "lang"))
				}
			case "link":
				addTechMarker(&page, detectTechFromURL(attrValue(n, "href")))
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	headings, paragraphWordCounts := extractBodyStructure(doc, base, &page)

	page.Headings = headings
	page.ParagraphCount = len(paragraphWordCounts)
	if page.HasMain {
		page.LandmarkCount++
	}
	if page.HasNav {
		page.LandmarkCount++
	}
	if elementCount > 0 {
		page.DivRatio = float64(divSpanCount) / float64(elementCount)
	}

	// Body-scoped link extraction (quality uplift 1.1/1.2): anchors inside
	// the content container only, so nav/footer chrome can't masquerade as
	// internal linking or citations.
	extractBodyLinkFeatures(doc, base, &page)

	// Body text stats + excerpt.
	bodyText := utils.ExtractArticleBodyText(doc, 40000)
	page.ContentExcerpt = utils.Truncate(bodyText, deepExcerptChars)
	applyTextStats(&page, bodyText)

	// Parasite markers over path + headings.
	page.ParasiteMarkers = detectParasiteMarkers(pageURL, headings)

	// Schema last: it can also set byline/date/org fields.
	extractSchemaFeatures(doc, &page)

	return page
}

const (
	answerBlockMin     = 134
	answerBlockMax     = 167
	longParagraphWords = 150
)

// extractBodyStructure collects the content-structure signals — headings,
// paragraphs/answer blocks, lists, tables, video, images, visible dates —
// from the content container only: first <article>, else <main>, else the
// whole document. nav and footer subtrees are always skipped; header is
// skipped only in whole-document fallback mode, because inside <article>/
// <main> a nested <header> legitimately holds the article's own H1.
func extractBodyStructure(doc *html.Node, base *url.URL, page *artifacts.DeepPage) ([]artifacts.DeepHeading, []int) {
	container := findFirstElement(doc, "article")
	if container == nil {
		container = findFirstElement(doc, "main")
	}
	docFallback := false
	if container == nil {
		container = doc
		docFallback = true
	}

	var headings []artifacts.DeepHeading
	var paragraphWordCounts []int
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			tag := strings.ToLower(n.Data)
			switch tag {
			case "nav", "footer", "script", "style", "noscript", "template":
				return // chrome/code, never body content
			case "header":
				if docFallback {
					return
				}
			case "h1", "h2", "h3", "h4", "h5", "h6":
				level := int(tag[1] - '0')
				text := strings.TrimSpace(nodeText(n))
				if text != "" {
					headings = append(headings, artifacts.DeepHeading{Level: level, Text: text})
					if isQuestionHeading(text) {
						page.QuestionHeadings++
					}
					if tag == "h1" && page.H1Text == "" {
						page.H1Text = text
						page.H1Suspect, page.H1SuspectReason = detectH1Corruption(text)
					}
				}
			case "p":
				words := len(strings.Fields(nodeText(n)))
				if words > 0 {
					paragraphWordCounts = append(paragraphWordCounts, words)
					if words >= answerBlockMin && words <= answerBlockMax {
						page.AnswerBlockCount++
					}
					if words > longParagraphWords {
						page.LongParagraphCount++
					}
				}
			case "ul", "ol":
				if countChildren(n, "li") > 1 {
					page.ListCount++
				}
			case "table":
				page.TableCount++
			case "video":
				page.VideoEmbedCount++
			case "iframe":
				src := strings.ToLower(attrValue(n, "src"))
				if strings.Contains(src, "youtube") || strings.Contains(src, "vimeo") ||
					strings.Contains(src, "wistia") || strings.Contains(src, "loom.com") ||
					strings.Contains(src, "dailymotion") {
					page.VideoEmbedCount++
				}
			case "img":
				if img, ok := extractDeepImage(n, base); ok {
					page.Images = append(page.Images, img)
				}
			case "time":
				// The most common visible-date markup — its absence made the
				// freshness check claim "no publish date" about dated pages.
				if dt := datePrefix(attrValue(n, "datetime")); dt != "" {
					page.HasDates = true
					if page.PublishedDate == "" {
						page.PublishedDate = dt
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(container)
	return headings, paragraphWordCounts
}

func attrValue(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(x *html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
			b.WriteString(" ")
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			// Code injected inside content elements (an ad <script> in a <p>)
			// is not text — it inflated paragraph word counts into the
			// answer-block band. The ROOT's own text always counts, so
			// nodeText(styleNode) still reads the CSS for @keyframes.
			if c.Type == html.ElementNode {
				switch strings.ToLower(c.Data) {
				case "script", "style", "noscript", "template":
					continue
				}
			}
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func countChildren(n *html.Node, tag string) int {
	count := 0
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && strings.EqualFold(c.Data, tag) {
			count++
		}
	}
	return count
}

var questionWords = []string{"how", "what", "why", "when", "where", "which", "who", "can", "does", "do", "is", "are", "should"}

func isQuestionHeading(text string) bool {
	if strings.HasSuffix(text, "?") {
		return true
	}
	// A question-word opener alone misfired on slogans ("Do more with X",
	// "How it works") — require a real clause behind it.
	if len(strings.Fields(text)) < 4 {
		return false
	}
	first := strings.ToLower(strings.Trim(strings.SplitN(text, " ", 2)[0], ".,:;!"))
	for _, q := range questionWords {
		if first == q {
			return true
		}
	}
	return false
}

// Image tier ceilings (the 3-tier table: thumbnail/content/hero, scope §5).
const (
	tierThumbnailMaxW = 400
	tierContentMaxW   = 1200
	tierHeroMaxW      = 2000
)

func extractDeepImage(n *html.Node, base *url.URL) (artifacts.DeepImage, bool) {
	// The canonical lazyload pattern is src="data:...placeholder" +
	// data-src="/real.jpg" — dropping it made fully-illustrated lazyloaded
	// pages read as image-less.
	src := attrValue(n, "src")
	if src == "" || strings.HasPrefix(src, "data:") {
		for _, attr := range []string{"data-src", "data-lazy-src"} {
			if v := attrValue(n, attr); v != "" && !strings.HasPrefix(v, "data:") {
				src = v
				break
			}
		}
	}
	if src == "" || strings.HasPrefix(src, "data:") {
		return artifacts.DeepImage{}, false
	}
	if base != nil {
		if ref, err := url.Parse(src); err == nil {
			src = base.ResolveReference(ref).String()
		}
	}
	alt, hasAlt := "", false
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, "alt") {
			alt, hasAlt = a.Val, true
		}
	}
	w, _ := strconv.Atoi(strings.TrimSuffix(attrValue(n, "width"), "px"))
	h, _ := strconv.Atoi(strings.TrimSuffix(attrValue(n, "height"), "px"))
	// Tracking pixels are not content images.
	if w == 1 && h == 1 {
		return artifacts.DeepImage{}, false
	}

	img := artifacts.DeepImage{Src: src, Alt: alt, HasAlt: hasAlt, Width: w, Height: h}
	// width="100%" parses to 0 but IS a declaration — the sizing check must
	// not report "without declared dimensions" for it.
	img.DimensionsDeclared = attrValue(n, "width") != "" && attrValue(n, "height") != ""
	switch {
	case w == 0:
		// undeclared — tier unknown
	case w <= tierThumbnailMaxW:
		img.Tier = "thumbnail"
	case w <= tierContentMaxW:
		img.Tier = "content"
	case w <= tierHeroMaxW:
		img.Tier = "hero"
	default:
		img.Tier = "hero"
		img.Oversized = true
	}
	return img, true
}

func classifyAnchor(n *html.Node, base *url.URL, page *artifacts.DeepPage) {
	href := attrValue(n, "href")
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:") ||
		strings.HasPrefix(href, "mailto:") || strings.HasPrefix(href, "tel:") {
		return
	}
	ref, err := url.Parse(href)
	if err != nil {
		return
	}
	resolved := ref
	if base != nil {
		resolved = base.ResolveReference(ref)
	}
	external := base != nil && resolved.Host != "" && !strings.EqualFold(stripWWW(resolved.Host), stripWWW(base.Host))
	// About/Contact discoverability is a claim about THIS site — an external
	// footer link to linkedin.com/company/x/about must not satisfy it.
	if !external {
		pathLower := strings.ToLower(resolved.Path)
		if strings.Contains(pathLower, "about") {
			page.LinksAboutPage = true
		}
		if strings.Contains(pathLower, "contact") {
			page.LinksContact = true
		}
	}
	if external {
		page.ExternalLinkCount++
	} else {
		page.InternalLinkCount++
	}
	if rel := strings.ToLower(attrValue(n, "rel")); strings.Contains(rel, "author") {
		page.HasByline = true
		if page.AuthorName == "" {
			page.AuthorName = strings.TrimSpace(nodeText(n))
		}
	}
}

func stripWWW(host string) string {
	return strings.TrimPrefix(strings.ToLower(host), "www.")
}

// --- H1 text-extraction sanity (quality uplift 2.1) ---

// h1MaxChars: past this an "H1" reads as a paragraph (or a dump of animation
// states), not a heading.
const h1MaxChars = 120

// detectH1Corruption flags H1 text that extracts as corrupted/concatenated —
// the animated-hero failure mode where every rotation state lands in
// textContent ("...in WhatsAppiMessagesoonWhatsAppTelegramsoonWhatsApp").
func detectH1Corruption(text string) (bool, string) {
	if len([]rune(text)) > h1MaxChars {
		return true, "over_120_chars"
	}
	words := strings.Fields(text)
	for _, w := range words {
		if len([]rune(w)) > 20 && lowerUpperTransitions(w) >= 2 {
			return true, "concatenated_fragments"
		}
	}
	freq := map[string]int{}
	for _, w := range words {
		lw := strings.ToLower(strings.Trim(w, `.,;:!?"'()[]{}`))
		if len(lw) < 3 {
			continue
		}
		freq[lw]++
		if freq[lw] >= 3 {
			return true, "repeated_words"
		}
	}
	return false, ""
}

// lowerUpperTransitions counts lowercase→uppercase boundaries inside one
// token — ≥2 in a long token means concatenated fragments.
func lowerUpperTransitions(token string) int {
	transitions := 0
	prevLower := false
	for _, r := range token {
		isUpper := r >= 'A' && r <= 'Z'
		if prevLower && isUpper {
			transitions++
		}
		prevLower = r >= 'a' && r <= 'z'
	}
	return transitions
}

// --- Body-scoped link extraction (quality uplift 1.1/1.2) ---

// bodyInternalTargetsCap bounds the persisted per-page target list.
const bodyInternalTargetsCap = 50

// citationExcludedDomains are registrable domains whose links are CTAs or
// social chrome, never citations (1.2's exclusion set). AI-assistant and
// community-invite domains are here too: "ask ChatGPT about us" / "join our
// Discord" buttons scored as external citations and inflated citationShare
// on sites that cite nothing (gap-closure round 4).
var citationExcludedDomains = []string{
	"wa.me", "api.whatsapp.com", "t.me", "m.me",
	"facebook.com", "instagram.com", "x.com", "twitter.com",
	"linkedin.com", "youtube.com", "youtu.be", "tiktok.com", "pinterest.com",
	"chatgpt.com", "chat.openai.com", "claude.ai", "perplexity.ai",
	"grok.com", "gemini.google.com", "copilot.microsoft.com",
	"discord.com", "discord.gg", "slack.com",
}

// extractBodyLinkFeatures walks anchors inside the content container only:
// the first <article>, else <main>, else the whole document minus
// nav/header/footer subtrees (those subtrees are skipped in every mode).
func extractBodyLinkFeatures(doc *html.Node, base *url.URL, page *artifacts.DeepPage) {
	container := findFirstElement(doc, "article")
	if container == nil {
		container = findFirstElement(doc, "main")
	}
	if container == nil {
		container = doc
	}

	page.BodyLinksAssessed = true
	citationDomains := map[string]bool{}

	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "nav", "header", "footer":
				return // chrome, never body content
			case "a":
				classifyBodyAnchor(n, base, page, citationDomains)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(container)

	for d := range citationDomains {
		page.BodyCitationDomains = append(page.BodyCitationDomains, d)
	}
	sortStrings(page.BodyCitationDomains)
}

func classifyBodyAnchor(n *html.Node, base *url.URL, page *artifacts.DeepPage, citationDomains map[string]bool) {
	href := attrValue(n, "href")
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:") || strings.HasPrefix(href, "mailto:") || strings.HasPrefix(href, "tel:") {
		return
	}
	ref, err := url.Parse(href)
	if err != nil {
		return
	}
	resolved := ref
	if base != nil {
		resolved = base.ResolveReference(ref)
	}
	external := base != nil && resolved.Host != "" && !strings.EqualFold(stripWWW(resolved.Host), stripWWW(base.Host))
	if !external {
		page.BodyInternalLinkCount++
		if len(page.BodyInternalTargets) < bodyInternalTargetsCap {
			page.BodyInternalTargets = append(page.BodyInternalTargets, resolved.String())
		}
		return
	}
	page.BodyExternalLinkCount++
	host := stripWWW(resolved.Host)
	if isCitationExcludedHost(host, base) {
		return
	}
	page.BodyCitationCount++
	citationDomains[host] = true
}

// isCitationExcludedHost reports whether an external host is a CTA/social
// domain (or the site's own domain reached via an absolute link).
func isCitationExcludedHost(host string, base *url.URL) bool {
	for _, d := range citationExcludedDomains {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	if base != nil {
		own := stripWWW(base.Host)
		// The audited site's registrable domain and its subdomains are
		// self-links, not citations.
		if host == own || strings.HasSuffix(host, "."+own) || strings.HasSuffix(own, "."+host) {
			return true
		}
	}
	return false
}

func findFirstElement(doc *html.Node, tag string) *html.Node {
	var found *html.Node
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && strings.EqualFold(n.Data, tag) {
			found = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return found
}

func classifyScript(n *html.Node, page *artifacts.DeepPage) {
	typ := strings.ToLower(attrValue(n, "type"))
	if typ == "speculationrules" {
		page.HasSpeculationRules = true
		return
	}
	addTechMarker(page, detectTechFromURL(attrValue(n, "src")))
	if id := strings.ToLower(attrValue(n, "id")); id == "__next_data__" {
		addTechMarker(page, "Next.js")
	}
	if typ == "application/ld+json" {
		return // handled by extractSchemaFeatures
	}
	// Inline-script bfcache blockers + stack markers.
	if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
		src := n.FirstChild.Data
		if strings.Contains(src, "onbeforeunload") ||
			strings.Contains(src, `addEventListener("unload"`) ||
			strings.Contains(src, `addEventListener('unload'`) ||
			strings.Contains(src, `addEventListener("beforeunload"`) ||
			strings.Contains(src, `addEventListener('beforeunload'`) {
			page.HasUnloadHandler = true
		}
		if strings.Contains(src, "__NEXT_DATA__") {
			addTechMarker(page, "Next.js")
		}
	}
}

func classifyMeta(n *html.Node, page *artifacts.DeepPage) {
	name := strings.ToLower(attrValue(n, "name"))
	property := strings.ToLower(attrValue(n, "property"))
	content := strings.TrimSpace(attrValue(n, "content"))
	if content == "" {
		return
	}
	switch {
	case name == "author":
		// Sitewide template meta ("Acme Inc", "Team Acme") is not authorship
		// — only a person-shaped value counts as a byline signal.
		if looksLikePersonName(content) {
			page.HasByline = true
			if page.AuthorName == "" {
				page.AuthorName = content
			}
		}
	case property == "article:published_time":
		page.HasDates = true
		if page.PublishedDate == "" {
			page.PublishedDate = datePrefix(content)
		}
	case property == "article:modified_time":
		page.HasDates = true
		if page.ModifiedDate == "" {
			page.ModifiedDate = datePrefix(content)
		}
	case property == "og:title" || name == "og:title":
		// name= is non-spec for OG but honored by major scrapers.
		page.HasOGTitle = true
	case property == "og:image" || name == "og:image":
		page.HasOGImage = true
	case name == "twitter:card" || property == "twitter:card":
		page.HasTwitterCard = true
	case name == "twitter:site" || property == "twitter:site":
		page.HasTwitterSite = true
	case name == "generator":
		addTechMarker(page, techFromGenerator(content))
	}
}

// looksLikePersonName filters org-shaped "author" values: a person name has
// 2–4 words, no corporate/team tokens, and no domain-ish punctuation.
func looksLikePersonName(s string) bool {
	lower := strings.ToLower(s)
	for _, tok := range []string{"team", "inc", "llc", "ltd", "gmbh", "corp", "staff", "editorial", "admin", "marketing", "agency", "studio", ".com", ".io", ".ai", "http"} {
		if strings.Contains(lower, tok) {
			return false
		}
	}
	words := len(strings.Fields(s))
	return words >= 2 && words <= 4
}

// techFromGenerator maps a meta[name=generator] value onto a stack label.
func techFromGenerator(content string) string {
	c := strings.ToLower(content)
	switch {
	case strings.Contains(c, "wordpress"):
		return "WordPress"
	case strings.Contains(c, "next.js"):
		return "Next.js"
	case strings.Contains(c, "webflow"):
		return "Webflow"
	case strings.Contains(c, "wix"):
		return "Wix"
	case strings.Contains(c, "squarespace"):
		return "Squarespace"
	case strings.Contains(c, "framer"):
		return "Framer"
	case strings.Contains(c, "shopify"):
		return "Shopify"
	case strings.Contains(c, "payload"):
		return "Payload"
	case strings.Contains(c, "gatsby"):
		return "Gatsby"
	case strings.Contains(c, "hugo"):
		return "Hugo"
	case strings.Contains(c, "astro"):
		return "Astro"
	case strings.Contains(c, "ghost"):
		return "Ghost"
	}
	if generator := strings.TrimSpace(content); generator != "" {
		// An unmapped generator still names the stack — pass it through.
		return generator
	}
	return ""
}

// detectTechFromURL maps an asset/script URL onto a stack label ("" = none).
func detectTechFromURL(raw string) string {
	if raw == "" {
		return ""
	}
	u := strings.ToLower(raw)
	switch {
	case strings.Contains(u, "/_next/"):
		return "Next.js"
	case strings.Contains(u, "/wp-content/") || strings.Contains(u, "/wp-includes/"):
		return "WordPress"
	case strings.Contains(u, "cdn.shopify.com"):
		return "Shopify"
	case strings.Contains(u, "assets.website-files.com") || strings.Contains(u, "uploads-ssl.webflow.com"):
		return "Webflow"
	case strings.Contains(u, "framerusercontent.com") || strings.Contains(u, "framerstatic.com"):
		return "Framer"
	case strings.Contains(u, "wixstatic.com") || strings.Contains(u, "parastorage.com"):
		return "Wix"
	case strings.Contains(u, "squarespace-cdn.com") || strings.Contains(u, "static1.squarespace.com"):
		return "Squarespace"
	}
	return ""
}

// addTechMarker records a stack marker once per page.
func addTechMarker(page *artifacts.DeepPage, marker string) {
	if marker == "" {
		return
	}
	for _, m := range page.TechMarkers {
		if m == marker {
			return
		}
	}
	page.TechMarkers = append(page.TechMarkers, marker)
}

// datePrefix normalizes a date-ish string to a validated YYYY-MM-DD prefix.
// Non-ISO values ("March 5, 2024") return "" rather than a garbage 10-char
// slice — downstream date logic treats "" as unknown, never as fresh.
func datePrefix(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 10 {
		return ""
	}
	s = s[:10]
	if _, err := time.Parse("2006-01-02", s); err != nil {
		return ""
	}
	return s
}

// --- JSON-LD schema extraction + validation ---

func extractSchemaFeatures(doc *html.Node, page *artifacts.DeepPage) {
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && strings.EqualFold(n.Data, "script") &&
			strings.EqualFold(attrValue(n, "type"), "application/ld+json") {
			raw := ""
			if n.FirstChild != nil {
				raw = n.FirstChild.Data
			}
			page.SchemaBlocks = append(page.SchemaBlocks, parseSchemaBlock(raw, page))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
}

// schemaPlaceholderRe matches template placeholder text left in JSON-LD —
// bracketed slots ("[Business Name]", "[Your City]") and REPLACE_ME-style
// tokens. Markup shipped with these was never filled in.
// The bracket alternative stays case-insensitive; the REPLACE token must be
// uppercase-only — case-insensitive matching flagged ordinary prose like
// "When to Replace Your Brake Pads" as template scaffolding.
var schemaPlaceholderRe = regexp.MustCompile(`(?i:\[(?:your |insert )?(?:business|company|brand|city|state|phone|address|email|url|name)[^\]]*\])|\bREPLACE(?:_?ME|_[A-Z]+)\b`)

func parseSchemaBlock(raw string, page *artifacts.DeepPage) artifacts.SchemaBlock {
	block := artifacts.SchemaBlock{}
	if strings.TrimSpace(raw) == "" {
		// A whitespace-only block (common CMS-plugin artifact) is inert —
		// parsers skip it; it must not drive the High "invalid JSON-LD hides
		// working markup" claim.
		block.ParseError = "empty block"
		return block
	}
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		block.ParseError = "invalid JSON"
		return block
	}
	block.Valid = true
	block.HasPlaceholder = schemaPlaceholderRe.MatchString(raw)

	// A block may be an object, an array of objects, or an @graph.
	var objs []map[string]any
	collect := func(v any) {
		if m, ok := v.(map[string]any); ok {
			objs = append(objs, m)
			if graph, ok := m["@graph"].([]any); ok {
				for _, g := range graph {
					if gm, ok := g.(map[string]any); ok {
						objs = append(objs, gm)
					}
				}
			}
		}
	}
	switch v := payload.(type) {
	case []any:
		for _, item := range v {
			collect(item)
		}
	default:
		collect(v)
	}

	hasContext := false
	for _, obj := range objs {
		if _, ok := obj["@context"]; ok {
			hasContext = true
		}
		for _, t := range schemaTypes(obj) {
			block.Types = append(block.Types, t)
			// Organization SUBTYPES (LocalBusiness, Corporation, ...) are
			// Organizations — exact-matching the literal string fired
			// "missing entity schema" on correctly marked-up sites. Person
			// counts for the sameAs/entity signals per the artifact contract.
			if orgLikeSchemaType(t) || t == "Person" {
				if orgLikeSchemaType(t) {
					page.HasOrganizationSchema = true
				}
				if n := sameAsCount(obj["sameAs"]); n > page.OrganizationSameAsCount {
					page.OrganizationSameAsCount = n
				}
				if _, ok := obj["logo"]; ok {
					page.OrganizationHasLogo = true
				}
				if u, ok := obj["url"].(string); ok && u != "" {
					page.OrganizationHasURL = true
				}
			}
		}
		if lang, ok := obj["inLanguage"].(string); ok && lang != "" && page.SchemaInLanguage == "" {
			page.SchemaInLanguage = lang
		}
		// A byline is a PERSON taking authorship. Mere key presence
		// ("author": null, an Organization author, a sitewide WebSite block)
		// scored anonymous sites as bylined.
		if name := schemaPersonAuthorName(obj["author"]); name != "" {
			page.HasByline = true
			if page.AuthorName == "" {
				page.AuthorName = name
			}
		}
		if d, ok := obj["datePublished"].(string); ok && d != "" {
			page.HasDates = true
			if page.PublishedDate == "" {
				page.PublishedDate = datePrefix(d)
			}
		}
		if d, ok := obj["dateModified"].(string); ok && d != "" {
			page.HasDates = true
			if page.ModifiedDate == "" {
				page.ModifiedDate = datePrefix(d)
			}
		}
	}
	if len(objs) > 0 && !hasContext {
		block.MissingContext = true
	}
	// Parseable-but-empty payloads ([], null, a bare string) carry no schema
	// objects — counting them as fully valid let a site of empty brackets
	// score 100 on schema validity.
	if len(objs) == 0 {
		block.Valid = false
		block.ParseError = "no schema objects"
	}
	return block
}

// orgLikeSchemaType reports whether a schema @type is an Organization or one
// of its common subtypes.
func orgLikeSchemaType(t string) bool {
	if t == "Organization" || strings.HasSuffix(t, "Organization") ||
		t == "LocalBusiness" || strings.HasSuffix(t, "Business") ||
		t == "Corporation" {
		return true
	}
	switch t {
	case "Restaurant", "Store", "Hotel", "Dentist", "Attorney", "Physician",
		"ProfessionalService", "FinancialService", "LegalService",
		"RealEstateAgent", "InsuranceAgency", "TravelAgency", "AutoRepair",
		"BeautySalon", "MedicalClinic", "Airline", "Brand":
		return true
	}
	return false
}

// sameAsCount handles both spec forms: an array of URLs or a single string.
func sameAsCount(v any) int {
	switch s := v.(type) {
	case []any:
		return len(s)
	case string:
		if strings.TrimSpace(s) != "" {
			return 1
		}
	}
	return 0
}

// schemaPersonAuthorName extracts a person author's name: a Person-typed (or
// untyped) object with a non-empty name, a non-empty string, or the first
// such entry of an array. Organization-typed authors return "".
func schemaPersonAuthorName(v any) string {
	switch a := v.(type) {
	case string:
		if looksLikePersonName(a) {
			return strings.TrimSpace(a)
		}
	case map[string]any:
		if t, ok := a["@type"].(string); ok && t != "" && t != "Person" {
			return ""
		}
		if name, ok := a["name"].(string); ok && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	case []any:
		for _, item := range a {
			if name := schemaPersonAuthorName(item); name != "" {
				return name
			}
		}
	}
	return ""
}

func schemaTypes(obj map[string]any) []string {
	switch t := obj["@type"].(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, v := range t {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// --- text statistics (readability, density, filler/AI patterns) ---

var sentenceSplitRe = regexp.MustCompile(`[.!?]+[\s"')\]]|[.!?]+$`)

// fillerPhrases + aiPatternPhrases are the deterministic content-scorer
// lexicons (decision 11). Lowercase; matched as substrings over the body.
var fillerPhrases = []string{
	"in today's fast-paced world", "in this day and age", "at the end of the day",
	"when it comes to", "it's important to note", "it is important to note",
	"needless to say", "as we all know", "in order to", "the fact of the matter",
	"first and foremost", "last but not least", "in conclusion",
}

var aiPatternPhrases = []string{
	"delve into", "delving into", "in the ever-evolving", "ever-changing landscape",
	"unlock the", "unleash the", "game-changer", "revolutionize the way",
	"seamlessly integrate", "robust and scalable", "in the realm of",
	"navigating the complexities", "a testament to", "elevate your",
	"embark on a journey", "treasure trove", "whether you're a",
	"look no further", "dive deep into", "harness the power",
}

var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "but": true,
	"of": true, "to": true, "in": true, "on": true, "for": true, "with": true,
	"is": true, "are": true, "was": true, "were": true, "be": true, "been": true,
	"it": true, "its": true, "this": true, "that": true, "these": true,
	"as": true, "at": true, "by": true, "from": true, "you": true, "your": true,
	"we": true, "our": true, "they": true, "their": true, "can": true,
	"will": true, "not": true, "have": true, "has": true, "if": true, "into": true,
	"more": true, "all": true, "also": true, "what": true, "when": true, "how": true,
}

func applyTextStats(page *artifacts.DeepPage, text string) {
	words := strings.Fields(text)
	page.WordCount = len(words)
	if len(words) == 0 {
		return
	}

	// Sentences.
	sentences := splitSentences(text)
	page.SentenceCount = len(sentences)
	if len(sentences) > 0 {
		page.AvgSentenceWords = float64(len(words)) / float64(len(sentences))
	}

	// Flesch reading ease.
	syllables := 0
	for _, w := range words {
		syllables += countSyllables(w)
	}
	if len(sentences) > 0 {
		page.FleschReadingEase = 206.835 -
			1.015*(float64(len(words))/float64(len(sentences))) -
			84.6*(float64(syllables)/float64(len(words)))
	}

	// Info density + keyword density.
	freq := map[string]int{}
	unique := map[string]bool{}
	topCount := 0
	for _, w := range words {
		lw := strings.ToLower(strings.Trim(w, `.,;:!?"'()[]{}`))
		if lw == "" {
			continue
		}
		unique[lw] = true
		if stopwords[lw] || len(lw) < 3 {
			continue
		}
		freq[lw]++
		if freq[lw] > topCount {
			topCount = freq[lw]
		}
	}
	page.UniqueWordRatio = float64(len(unique)) / float64(len(words))
	page.TopKeywordDensityPct = float64(topCount) / float64(len(words)) * 100

	// Filler / AI-pattern counts.
	lower := strings.ToLower(text)
	for _, p := range fillerPhrases {
		page.FillerPhraseCount += strings.Count(lower, p)
	}
	for _, p := range aiPatternPhrases {
		page.AIPatternCount += strings.Count(lower, p)
	}

	// Repetition: share of exact-duplicate sentences.
	if len(sentences) > 1 {
		seen := map[string]int{}
		dup := 0
		for _, s := range sentences {
			key := strings.ToLower(strings.TrimSpace(s))
			if len(key) < 20 {
				continue
			}
			seen[key]++
			if seen[key] > 1 {
				dup++
			}
		}
		page.RepeatedSentencePct = float64(dup) / float64(len(sentences)) * 100
	}
}

func splitSentences(text string) []string {
	parts := sentenceSplitRe.Split(text, -1)
	var out []string
	for _, p := range parts {
		if len(strings.Fields(p)) >= 3 {
			out = append(out, p)
		}
	}
	return out
}

func countSyllables(word string) int {
	w := strings.ToLower(strings.Trim(word, `.,;:!?"'()[]{}`))
	if w == "" {
		return 0
	}
	count, prevVowel := 0, false
	for _, r := range w {
		isVowel := strings.ContainsRune("aeiouy", r)
		if isVowel && !prevVowel {
			count++
		}
		prevVowel = isVowel
	}
	if strings.HasSuffix(w, "e") && count > 1 {
		count--
	}
	if count == 0 {
		count = 1
	}
	return count
}

// --- parasite / site-reputation-abuse markers ---

// parasiteTopics are commercial verticals that, appearing as path segments
// or heading topics on an unrelated host, pattern-match site-reputation
// abuse (Google's parasite SEO policy target).
var parasiteTopics = []string{
	"casino", "gambling", "betting", "slots", "poker",
	"coupon", "coupons", "promo-code", "promo_codes", "discount-code",
	"essay-writing", "essay-service", "paper-writing",
	"payday-loan", "payday_loans", "quick-loan",
	"crypto-signal", "forex-signal",
	"replica", "counterfeit",
	"viagra", "cialis",
}

func detectParasiteMarkers(pageURL string, headings []artifacts.DeepHeading) []string {
	var markers []string
	seen := map[string]bool{}
	add := func(m string) {
		if !seen[m] {
			seen[m] = true
			markers = append(markers, m)
		}
	}
	u, err := url.Parse(pageURL)
	pathLower := ""
	if err == nil {
		pathLower = strings.ToLower(u.Path)
	}
	for _, topic := range parasiteTopics {
		if pathLower != "" && strings.Contains(pathLower, topic) {
			add("path:" + topic)
		}
	}
	for _, h := range headings {
		hl := strings.ToLower(h.Text)
		for _, topic := range parasiteTopics {
			if strings.Contains(hl, strings.ReplaceAll(topic, "-", " ")) {
				add("heading:" + topic)
			}
		}
	}
	return markers
}
