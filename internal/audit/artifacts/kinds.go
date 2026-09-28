// Package artifacts is the audit blackboard: collectors write typed,
// normalized artifact docs; checks declare which kinds they require and read
// them through the Bundle. Collectors never score, checks never call
// providers — the artifact structs here are the only contract between them.
//
// Normalization rule: only the fields checks consume are persisted, in the
// artifact-owned structs below — never raw provider payloads, never raw HTML
// (the deep-pass collector extracts features in-memory and discards the DOM).
// This keeps artifact docs far under the 16 MB Mongo cap at the 100-page
// tenant crawl cap and makes checks testable from fixture structs.
package artifacts

import "github.com/atharva-ng/crunch/internal/audit/core"

const (
	// KindCrawl is the normalized OnPage crawl: summary + pages + links +
	// duplicate-content data, plus the deep-pass page sample selected at
	// crawl time (sampling logic lives in exactly one place).
	KindCrawl core.Kind = "crawl"
	// KindHTMLDeep is the per-sampled-page deep pass: features extracted from
	// rendered HTML (schema, AEO, images, content scorers, agent-UX,
	// speculation, parasite markers) plus site-level probes (sitemap, robots,
	// llms.txt, IndexNow).
	KindHTMLDeep core.Kind = "html_deep"
	// KindPSI is CrUX field metrics + PSI opportunities + lab ride-along.
	KindPSI core.Kind = "psi"
	// KindAuthority is the domain rating + backlinks summary + Labs rank
	// overview.
	KindAuthority core.Kind = "authority"
	// KindSERP is the page→ranked-keyword map + SERP item types for the SXO
	// sample.
	KindSERP core.Kind = "serp"
	// KindMentions is the brand-mention footprint: third-party domains
	// mentioning the brand, from two SERP queries (quality uplift 3.1).
	KindMentions core.Kind = "mentions"
)
