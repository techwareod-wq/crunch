package artifacts

import (
	"fmt"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/atharva-ng/crunch/internal/audit/core"
)

// Bundle is the typed read-side over the auditArtifact docs loaded for a run.
// Has(kind) gates check execution; the typed getters return (v, ok) — never
// panic — so a check can only ever see a kind it declared (or defensively
// probed for).
type Bundle struct {
	artifacts map[core.Kind]any
}

// NewBundle builds an empty bundle (fixture tests add artifacts via Put).
func NewBundle() *Bundle {
	return &Bundle{artifacts: map[core.Kind]any{}}
}

// Put stores a typed artifact under its kind. The value must be a pointer to
// the kind's struct (as produced by BuildBundle / test fixtures).
func (b *Bundle) Put(kind core.Kind, v any) {
	b.artifacts[kind] = v
}

// Has reports whether the run produced the given artifact kind.
func (b *Bundle) Has(kind core.Kind) bool {
	_, ok := b.artifacts[kind]
	return ok
}

// HasAll reports whether every kind is present — the engine's skip gate for a
// check's Requires().
func (b *Bundle) HasAll(kinds []core.Kind) bool {
	for _, k := range kinds {
		if !b.Has(k) {
			return false
		}
	}
	return true
}

// Kinds lists the kinds present (stable output not guaranteed).
func (b *Bundle) Kinds() []core.Kind {
	out := make([]core.Kind, 0, len(b.artifacts))
	for k := range b.artifacts {
		out = append(out, k)
	}
	return out
}

func (b *Bundle) Crawl() (*CrawlArtifact, bool) {
	v, ok := b.artifacts[KindCrawl].(*CrawlArtifact)
	return v, ok
}

func (b *Bundle) HTMLDeep() (*HTMLDeepArtifact, bool) {
	v, ok := b.artifacts[KindHTMLDeep].(*HTMLDeepArtifact)
	return v, ok
}

func (b *Bundle) PSI() (*PSIArtifact, bool) {
	v, ok := b.artifacts[KindPSI].(*PSIArtifact)
	return v, ok
}

func (b *Bundle) Authority() (*AuthorityArtifact, bool) {
	v, ok := b.artifacts[KindAuthority].(*AuthorityArtifact)
	return v, ok
}

func (b *Bundle) SERP() (*SERPArtifact, bool) {
	v, ok := b.artifacts[KindSERP].(*SERPArtifact)
	return v, ok
}

func (b *Bundle) Mentions() (*MentionsArtifact, bool) {
	v, ok := b.artifacts[KindMentions].(*MentionsArtifact)
	return v, ok
}

// BuildBundle decodes raw artifact payloads (as loaded from Mongo) into the
// typed bundle. An unknown kind is skipped (forward compat: an older binary
// reading a newer run's artifacts); a payload that fails to decode is an
// error — a half-read blackboard must not silently score.
func BuildBundle(raw map[core.Kind]bson.Raw) (*Bundle, error) {
	b := NewBundle()
	for kind, payload := range raw {
		var v any
		switch kind {
		case KindCrawl:
			v = &CrawlArtifact{}
		case KindHTMLDeep:
			v = &HTMLDeepArtifact{}
		case KindPSI:
			v = &PSIArtifact{}
		case KindAuthority:
			v = &AuthorityArtifact{}
		case KindSERP:
			v = &SERPArtifact{}
		case KindMentions:
			v = &MentionsArtifact{}
		default:
			continue
		}
		if err := bson.Unmarshal(payload, v); err != nil {
			return nil, fmt.Errorf("decode %s artifact: %w", kind, err)
		}
		b.Put(kind, v)
	}
	return b, nil
}
