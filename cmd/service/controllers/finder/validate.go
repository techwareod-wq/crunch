package finder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/mongo"
)

// Input validation shared by /search and /match. Every string is trimmed and
// checked before it reaches a Mongo regex, so a bad value is a 400, never a
// 500.

const (
	// maxPlaceChars caps place (search) and city (match), in characters.
	maxPlaceChars = 120
	// maxEchoChars caps any request value repeated in a message.
	maxEchoChars = 40

	maxFeatures   = 20
	maxIndustries = 10
	maxGoods      = 10

	// maxAreaSqft / maxPallets bound the area inputs (pallets × sqftPerPallet
	// can then never overflow).
	maxAreaSqft = 1_000_000_000
	maxPallets  = 10_000_000

	// maxRawBodyBytes matches the shared JSON decoder's cap.
	maxRawBodyBytes = 1 << 20
)

// msgPlaceCleanedAway answers a place that the shared request cleaner
// emptied (e.g. "<script>…"): zero results, without echoing the input.
const msgPlaceCleanedAway = "We don't have live warehouses in that place yet."

// clip shortens s to maxEchoChars characters for echoing in a message.
func clip(s string) string {
	if utf8.RuneCountInString(s) <= maxEchoChars {
		return s
	}
	return string([]rune(s)[:maxEchoChars]) + "…"
}

// parsePlace validates a place (search) or city (match). cleaned is the
// value after the shared deserializer's HTML sanitizer, raw the value as
// sent (see captureRawText). The sanitizer escapes entities ("A&B" arrives
// as "A&amp;B"), so they are unescaped first; it also strips tags, so a raw
// value that was non-empty but cleaned to nothing is reported as gone (the
// caller answers zero results instead of searching everywhere).
func parsePlace(name, cleaned, raw string) (place string, gone bool, msg string) {
	place = strings.TrimSpace(html.UnescapeString(cleaned))
	if place == "" {
		return "", strings.TrimSpace(raw) != "", ""
	}
	if strings.ContainsFunc(place, unicode.IsControl) || !utf8.ValidString(place) {
		return "", false, name + " can't contain control characters"
	}
	if utf8.RuneCountInString(place) > maxPlaceChars {
		return "", false, fmt.Sprintf("%s must be at most %d characters", name, maxPlaceChars)
	}
	return place, false, ""
}

// checkList caps a list input's length.
func checkList(name string, n, max int) string {
	if n > max {
		return fmt.Sprintf("at most %d %s", max, name)
	}
	return ""
}

// checkNumber requires a finite number in [0, max].
func checkNumber(name string, v, max float64) string {
	switch {
	case math.IsNaN(v) || math.IsInf(v, 0):
		return name + " must be a finite number"
	case v < 0:
		return name + " can't be negative"
	case v > max:
		return fmt.Sprintf("%s must be at most %d", name, int64(max))
	}
	return ""
}

// queryRejected reports whether Mongo refused the query itself (e.g. an
// invalid regex) rather than failing to answer: a bad input, so a 400.
func queryRejected(err error) bool {
	var se mongo.ServerError
	return errors.As(err, &se)
}

// rawText is the place / city as sent, before the shared sanitizer.
type rawText struct {
	Place string `json:"place"`
	City  string `json:"city"`
}

type rawTextKey struct{}

// captureRawText keeps the request's place / city as sent, so the handler
// can tell a value the shared deserializer's sanitizer emptied from one that
// was never given. It must wrap the deserializer (chain it after); the body
// is restored untouched for it.
func captureRawText(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, maxRawBodyBytes+1))
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(data), r.Body), r.Body}
		var raw rawText
		if err == nil && len(data) <= maxRawBodyBytes {
			_ = json.Unmarshal(data, &raw) // a bad body is the deserializer's 400
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), rawTextKey{}, raw)))
	})
}

func rawTextOf(r *http.Request) rawText {
	raw, _ := r.Context().Value(rawTextKey{}).(rawText)
	return raw
}
