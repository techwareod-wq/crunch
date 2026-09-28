package service

import (
	"sort"
	"strings"

	"github.com/kljensen/snowball/english"
)

// canonicalKeywordKey reduces a keyword to a variant-insensitive identity:
// normalized text, each token snowball-stemmed, tokens sorted. Morphological
// variants ("crm software" / "crm softwares") and word-order variants
// ("email marketing tools" / "tools email marketing") share a key; distinct
// terms ("crm software" / "crm platform") do not. Stopwords are kept
// unstemmed so intent-bearing phrasings like "how to X" stay distinct from
// "X". Returns "" for keywords that normalize to nothing.
func canonicalKeywordKey(text string) string {
	norm := normalizeKeywordText(text)
	if norm == "" {
		return ""
	}
	tokens := strings.Fields(norm)
	for i, tok := range tokens {
		tokens[i] = english.Stem(tok, false)
	}
	sort.Strings(tokens)
	return strings.Join(tokens, " ")
}
