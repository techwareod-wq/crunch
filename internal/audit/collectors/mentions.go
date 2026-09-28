package collectors

import (
	"context"
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// mentionsCollector builds the brand-mention footprint (quality uplift 3.1):
// two SERP organic queries — the quoted registrable domain, and the quoted
// domain label excluding the site itself — normalized into the distinct
// third-party root domains that mention the brand. Zero third-party footprint
// makes AI citation structurally impossible; mentions correlate ~3× stronger
// with AI visibility than backlinks. Non-critical: a failure degrades to a
// constraint and aisearch.brand_footprint skips/renormalizes.
type mentionsCollector struct {
	// enabled mirrors audit.mentions.enabled — a disabled collector opts out
	// of the fan-out entirely (AppliesTo false) so no artifact is produced
	// and the dependent check renormalizes away.
	enabled bool
}

func (mentionsCollector) ID() core.CollectorID              { return CollectorMentions }
func (mentionsCollector) Produces() []core.Kind             { return []core.Kind{artifacts.KindMentions} }
func (m mentionsCollector) AppliesTo(*models.AuditRun) bool { return m.enabled }
func (mentionsCollector) Critical() bool                    { return false }

// mentionsDefaultDepth is the page-1 depth per query when values leave it 0.
const mentionsDefaultDepth = 20

// mentionKeySurfaces are the third-party surfaces AI engines actually cite —
// presence on any of them is the cheapest citability signal there is.
var mentionKeySurfaces = []string{
	"reddit.com", "producthunt.com", "g2.com", "capterra.com",
	"alternativeto.net", "trustpilot.com", "wikipedia.org", "wikidata.org",
	"github.com", "ycombinator.com", "news.ycombinator.com", "linkedin.com",
	// The strongest single AI-citation correlate in published mention studies.
	"youtube.com",
}

func (m mentionsCollector) Collect(ctx context.Context, deps Deps, run *models.AuditRun) error {
	artifact, err := BuildMentionsArtifact(ctx, deps, run)
	if err != nil {
		return fmt.Errorf("mentions collector: %w", err)
	}
	return models.UpsertAuditArtifact(ctx, run.ID, artifacts.KindMentions, artifact)
}

// BuildMentionsArtifact runs the two mention queries and normalizes the
// third-party domain set. Exported for the re-check engine's scoped mode
// (mentions re-query is allowed — it's 2 calls).
func BuildMentionsArtifact(ctx context.Context, deps Deps, run *models.AuditRun) (*artifacts.MentionsArtifact, error) {
	domain := run.TargetDomain
	label := domainLabel(domain)
	queries := []string{
		fmt.Sprintf("%q", domain),
	}
	if label != "" && label != domain {
		queries = append(queries, fmt.Sprintf("%q -site:%s", label, domain))
	}

	depth := deps.Values.Mentions.SerpDepth
	if depth <= 0 {
		depth = mentionsDefaultDepth
	}

	artifact := &artifacts.MentionsArtifact{
		QueriedTerms: queries,
		KeySurfaces:  map[string]bool{},
	}
	thirdParty := map[string]bool{}
	for _, q := range queries {
		resp, err := deps.DFS.GetAdvancedSerpResults(ctx, dto.GetAdvancedSerpRequest{Tasks: []dto.AdvancedSerpTask{{
			Keyword:      q,
			LocationCode: RunLocationCode(run),
			LanguageCode: "en",
			Depth:        depth,
		}}})
		if err != nil {
			// Both queries matter for the "zero footprint" claim — a partial
			// read must not score as absence.
			return nil, fmt.Errorf("serp query %s: %w", q, err)
		}
		if len(resp.Tasks) == 0 || len(resp.Tasks[0].Result) == 0 {
			log.Warn("audit mentions: serp query returned no result", "query", q, "runId", run.ID.Hex())
			continue
		}
		for _, item := range resp.Tasks[0].Result[0].Items {
			if item.Type != "organic" || item.Domain == "" {
				continue
			}
			root := rootDomain(item.Domain)
			if root == "" || root == domain || strings.HasSuffix(root, "."+domain) || strings.HasSuffix(domain, "."+root) {
				continue
			}
			thirdParty[root] = true
		}
	}

	for d := range thirdParty {
		artifact.ThirdPartyDomains = append(artifact.ThirdPartyDomains, d)
	}
	sortStrings(artifact.ThirdPartyDomains)
	artifact.ThirdPartyCount = len(artifact.ThirdPartyDomains)
	for _, surface := range mentionKeySurfaces {
		if thirdParty[surface] {
			artifact.KeySurfaces[surface] = true
		}
	}

	// Category occupancy: one extra query for the phrase the site exists to
	// answer. Best-effort — the two brand queries above carry the footprint
	// claim; a category failure degrades to "not probed", never fails the
	// artifact.
	probeCategoryOccupancy(ctx, deps, run, artifact, depth)

	return artifact, nil
}

// categoryTopDomainsCap bounds the occupant list persisted per run.
const categoryTopDomainsCap = 10

// probeCategoryOccupancy derives the category phrase from the homepage title
// and records who occupies its SERP. Every failure path leaves
// CategoryProbed false — checks treat that as "not assessed".
//
// Gap-closure round 4: the probe used to search the site's OWN category
// phrase verbatim ("AI-Native Audit Workspace") — a self-coined term any site
// trivially ranks #1 for, which read as a footprint strength while the site
// was absent from every query buyers actually type. The phrase is now
// de-hyped into a buyer-intent query ("best ai audit software") when it names
// a product; the artifact records which intent ran so the check weighs a
// self-phrase result far more weakly.
func probeCategoryOccupancy(ctx context.Context, deps Deps, run *models.AuditRun, artifact *artifacts.MentionsArtifact, depth int) {
	crawl, err := loadCrawlArtifact(ctx, run)
	if err != nil {
		log.Warn("audit mentions: crawl load for category probe failed", "runId", run.ID.Hex(), "error", err)
		return
	}
	title := homepageTitle(crawl)
	phrase := deriveCategoryQuery(title, domainLabel(run.TargetDomain))
	if phrase == "" {
		return
	}
	query, intent := phrase, artifacts.CategoryIntentSelf
	if buyer, ok := buyerIntentQuery(phrase); ok {
		query, intent = buyer, artifacts.CategoryIntentBuyer
	}

	resp, err := deps.DFS.GetAdvancedSerpResults(ctx, dto.GetAdvancedSerpRequest{Tasks: []dto.AdvancedSerpTask{{
		Keyword:      query,
		LocationCode: RunLocationCode(run),
		LanguageCode: "en",
		Depth:        depth,
	}}})
	if err != nil {
		log.Warn("audit mentions: category serp query failed", "query", query, "runId", run.ID.Hex(), "error", err)
		return
	}
	if len(resp.Tasks) == 0 || len(resp.Tasks[0].Result) == 0 {
		log.Warn("audit mentions: category serp query returned no result", "query", query, "runId", run.ID.Hex())
		return
	}

	target := rootDomain(run.TargetDomain)
	seen := map[string]bool{}
	rank := 0
	artifact.CategoryProbed = true
	artifact.CategoryQuery = query
	artifact.CategoryQueryIntent = intent
	artifact.CategorySourcePhrase = phrase
	for _, item := range resp.Tasks[0].Result[0].Items {
		if item.Type != "organic" || item.Domain == "" {
			continue
		}
		rank++
		root := rootDomain(item.Domain)
		if root == target && artifact.CategoryTargetPosition == 0 {
			artifact.CategoryTargetPosition = rank
		}
		if !seen[root] && len(artifact.CategoryTopDomains) < categoryTopDomainsCap {
			seen[root] = true
			artifact.CategoryTopDomains = append(artifact.CategoryTopDomains, root)
		}
	}
}

// homepageTitle finds the crawl's entry-page title.
func homepageTitle(crawl *artifacts.CrawlArtifact) string {
	start := normalizeForMatch(crawl.StartURL)
	for _, p := range crawl.Pages {
		if normalizeForMatch(p.URL) == start {
			return p.Title
		}
	}
	return ""
}

// brandPrefixes are the vanity prefixes domain labels bolt onto brand names
// ("tryelip" → brand "Elip"); stripping them lets the brand token be removed
// from the title even when the site never uses the full label.
var brandPrefixes = []string{"try", "get", "use", "join", "hey", "go", "my"}

// categoryLeadFillers are title openers that precede the category phrase.
var categoryLeadFillers = []string{"meet", "welcome to", "introducing", "your", "the"}

// deriveCategoryQuery extracts the site's category phrase from its homepage
// title: split on title separators, drop brand tokens, trim lead-in fillers,
// keep the wordiest remaining segment. "" = no derivable phrase (skip the
// probe rather than search garbage).
func deriveCategoryQuery(title, label string) string {
	if strings.TrimSpace(title) == "" || label == "" {
		return ""
	}
	brandTokens := map[string]bool{strings.ToLower(label): true}
	for _, p := range brandPrefixes {
		if rest := strings.TrimPrefix(strings.ToLower(label), p); len(rest) >= 3 && rest != strings.ToLower(label) {
			brandTokens[rest] = true
		}
	}

	best := ""
	bestWords := 0
	// " - " is a separator; a bare hyphen inside a word is not.
	title = strings.ReplaceAll(title, " - ", " | ")
	for _, seg := range strings.FieldsFunc(title, func(r rune) bool {
		return r == '|' || r == '—' || r == '–' || r == '·' || r == ':'
	}) {
		var kept []string
		for _, w := range strings.Fields(seg) {
			clean := strings.ToLower(strings.Trim(w, ".,!?;()\"'"))
			if brandTokens[clean] {
				continue
			}
			kept = append(kept, strings.Trim(w, ".,!?;()\"'"))
		}
		phrase := strings.Join(kept, " ")
		// Trim lead-in fillers repeatedly ("Meet your ..." → "...").
		for changed := true; changed; {
			changed = false
			for _, f := range categoryLeadFillers {
				if l := strings.ToLower(phrase); strings.HasPrefix(l, f+" ") {
					phrase = strings.TrimSpace(phrase[len(f)+1:])
					changed = true
				}
			}
		}
		words := len(strings.Fields(phrase))
		if words > bestWords {
			best = phrase
			bestWords = words
		}
	}
	if bestWords < 2 || len(best) < 8 || len(best) > 80 {
		return ""
	}
	return best
}

// hypeTokenMap normalizes marketing compounds to their information-bearing
// core ("AI-Native" → "ai"); hypeDropTokens vanish entirely. Both operate on
// lowercased, punctuation-trimmed tokens.
var hypeTokenMap = map[string]string{
	"ai-native": "ai", "ai-powered": "ai", "ai-first": "ai",
	"ai-driven": "ai", "ai-enabled": "ai", "ai-based": "ai",
}

var hypeDropTokens = map[string]bool{
	"next-gen": true, "next-generation": true, "all-in-one": true,
	"modern": true, "smart": true, "intelligent": true, "leading": true,
	"revolutionary": true, "ultimate": true, "complete": true,
	"powerful": true, "seamless": true, "effortless": true, "best": true,
	"#1": true, "no.1": true, "the": true, "your": true, "new": true,
}

// productNouns are the trailing nouns that mark the phrase as naming a
// product; buyers search the category as "... software".
var productNouns = map[string]bool{
	"workspace": true, "platform": true, "suite": true, "tool": true,
	"tools": true, "toolkit": true, "solution": true, "solutions": true,
	"system": true, "software": true, "app": true, "application": true,
	"hub": true, "engine": true, "assistant": true, "copilot": true,
	"crm": true, "erp": true, "saas": true,
}

// buyerIntentQuery turns a self-described category phrase into the query a
// buyer actually types: hype stripped, the product noun normalized to
// "software", "best" prefixed — "AI-Native Audit Workspace" → "best ai audit
// software". ok=false means the phrase names no product (a blog, a service
// description) and the raw phrase should be searched as-is instead.
func buyerIntentQuery(phrase string) (string, bool) {
	var kept []string
	nounIdx := -1
	for _, raw := range strings.Fields(strings.ToLower(phrase)) {
		tok := strings.Trim(raw, ".,!?;()\"'")
		if mapped, ok := hypeTokenMap[tok]; ok {
			tok = mapped
		}
		if tok == "" || hypeDropTokens[tok] {
			continue
		}
		kept = append(kept, tok)
		if productNouns[tok] {
			nounIdx = len(kept) - 1
		}
	}
	if nounIdx < 1 || nounIdx > 4 {
		// No product noun (or no qualifier before it) — not a product phrase.
		return "", false
	}
	core := append(append([]string{}, kept[:nounIdx]...), "software")
	query := "best " + strings.Join(core, " ")
	if len(query) > 60 {
		return "", false
	}
	return query, true
}

// domainLabel extracts the brand label from a registrable domain
// ("tryelip.ai" → "tryelip").
func domainLabel(domain string) string {
	if i := strings.Index(domain, "."); i > 0 {
		return domain[:i]
	}
	return domain
}

// compoundSuffixes are common two-part public suffixes for the root-domain
// heuristic (a full PSL pull is overkill for a footprint count).
var compoundSuffixes = map[string]bool{
	"co.uk": true, "org.uk": true, "ac.uk": true, "gov.uk": true,
	"com.au": true, "net.au": true, "org.au": true,
	"co.in": true, "co.nz": true, "co.jp": true, "co.za": true,
	"com.br": true, "com.mx": true, "com.sg": true,
}

// rootDomain approximates the registrable domain of a host.
func rootDomain(host string) string {
	host = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "www.")
	parts := strings.Split(host, ".")
	if len(parts) <= 2 {
		return host
	}
	if compoundSuffixes[strings.Join(parts[len(parts)-2:], ".")] && len(parts) >= 3 {
		return strings.Join(parts[len(parts)-3:], ".")
	}
	return strings.Join(parts[len(parts)-2:], ".")
}
