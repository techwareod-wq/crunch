package service

import (
	"crypto/rand"
	"math/big"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// shortIDAlphabet is lowercase base32 without the look-alikes 0/o, 1/l/i
// (spec 00: 8 chars).
const (
	shortIDAlphabet = "23456789abcdefghjkmnpqrstuvwxyz"
	shortIDLen      = 8
	maxSlugPart     = 60
)

// newShortID returns a random short id.
func newShortID() (string, error) {
	var b strings.Builder
	n := big.NewInt(int64(len(shortIDAlphabet)))
	for i := 0; i < shortIDLen; i++ {
		x, err := rand.Int(rand.Reader, n)
		if err != nil {
			return "", err
		}
		b.WriteByte(shortIDAlphabet[x.Int64()])
	}
	return b.String(), nil
}

// kebab lowercases s, drops accents and turns every run of other characters
// into one hyphen. Non-Latin letters are dropped (the shortId still makes
// the slug unique).
func kebab(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue // combining accent
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
	}
	out := b.String()
	if len(out) > maxSlugPart {
		out = strings.TrimRight(out[:maxSlugPart], "-")
	}
	return out
}

// slugFor builds `{name}-{city}-{shortId}` (D-056).
func slugFor(name, city, shortID string) string {
	parts := []string{}
	for _, p := range []string{kebab(name), kebab(city), shortID} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "-")
}

// shortIDFromSlug parses the trailing short id; ok is false for a malformed
// slug.
func shortIDFromSlug(slug string) (string, bool) {
	i := strings.LastIndexByte(slug, '-')
	id := slug[i+1:]
	if len(id) != shortIDLen {
		return "", false
	}
	for _, r := range id {
		if !strings.ContainsRune(shortIDAlphabet, r) {
			return "", false
		}
	}
	return id, true
}
