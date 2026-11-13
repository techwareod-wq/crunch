package finder

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/mongo"

	"github.com/atharva-ng/crunch/internal/middleware"
)

func TestParsePlace(t *testing.T) {
	for name, tc := range map[string]struct {
		cleaned, raw, want string
		gone, bad          bool
	}{
		"trimmed":             {cleaned: "  Pune ", raw: "  Pune ", want: "Pune"},
		"entity unescaped":    {cleaned: "A&amp;B", raw: "A&B", want: "A&B"},
		"empty":               {cleaned: "", raw: ""},
		"blank":               {cleaned: "   ", raw: "   "},
		"cleaned away":        {cleaned: "", raw: "<script>alert(1)</script>", gone: true},
		"NUL byte":            {cleaned: "Pune\x00", raw: "Pune\x00", bad: true},
		"control character":   {cleaned: "Pu\x07ne", raw: "Pu\x07ne", bad: true},
		"inner newline":       {cleaned: "Pu\nne", raw: "Pu\nne", bad: true},
		"DEL":                 {cleaned: "Pune\x7f", raw: "Pune\x7f", bad: true},
		"120 chars":           {cleaned: strings.Repeat("a", 120), raw: "", want: strings.Repeat("a", 120)},
		"121 chars":           {cleaned: strings.Repeat("a", 121), bad: true},
		"120 multibyte":       {cleaned: strings.Repeat("मु", 60), want: strings.Repeat("मु", 60)},
		"33k chars":           {cleaned: strings.Repeat("A", 33000), bad: true},
		"regex metacharacter": {cleaned: ".*(", raw: ".*(", want: ".*("},
	} {
		got, gone, msg := parsePlace("place", tc.cleaned, tc.raw)
		if (msg != "") != tc.bad || gone != tc.gone || got != tc.want {
			t.Errorf("%s: got %q gone=%t msg=%q", name, got, gone, msg)
		}
	}
}

func TestParseQueryBadInput(t *testing.T) {
	features := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "dg"
		}
		return out
	}
	for name, req := range map[string]searchRequest{
		"NUL place":        {Place: "Pune\x00"},
		"33k place":        {Place: strings.Repeat("A", 33000)},
		"21 features":      {Features: features(21)},
		"area over cap":    {AreaSqft: maxAreaSqft + 1},
		"area 9e17":        {AreaSqft: 9e17},
		"pallets 9e17":     {Pallets: 900_000_000_000_000_000},
		"pallets over cap": {Pallets: maxPallets + 1},
		"negative pallets": {Pallets: -1},
		"negative area":    {AreaSqft: -0.5},
	} {
		if _, msg := parseQuery(req); msg == "" {
			t.Errorf("%s: accepted", name)
		}
	}
	q := mustQuery(t, searchRequest{Features: features(20), Pallets: maxPallets})
	if q.AreaSqft != maxPallets*sqftPerPallet || len(q.Chips) != 1 {
		t.Errorf("20 features and the pallet cap are allowed: %+v", q)
	}
	if q := mustQuery(t, searchRequest{AreaSqft: maxAreaSqft}); q.AreaSqft != maxAreaSqft {
		t.Errorf("area cap itself is allowed: %+v", q)
	}
	if q := mustQuery(t, searchRequest{AreaSqft: 200000, Pallets: 10}); q.AreaSqft != 200000 {
		t.Errorf("areaSqft must win over pallets: %v", q.AreaSqft)
	}
	if q := mustQuery(t, searchRequest{Place: "", rawPlace: "<b></b>"}); !q.PlaceGone || q.Place != "" {
		t.Errorf("a place the sanitizer emptied must be reported gone: %+v", q)
	}
	if q := mustQuery(t, searchRequest{}); q.PlaceGone {
		t.Error("no place sent is not gone")
	}
}

func TestCheckNumberNonFinite(t *testing.T) {
	for _, v := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
		if msg := checkNumber("areaSqft", v, maxAreaSqft); msg == "" {
			t.Errorf("%v accepted", v)
		}
	}
}

func TestEchoTruncated(t *testing.T) {
	long := strings.Repeat("x", 200_000)
	_, msg := parseQuery(searchRequest{Features: []string{long}})
	if !strings.HasPrefix(msg, "unknown feature ") || utf8.RuneCountInString(msg) > len(`unknown feature ""`)+maxEchoChars+1 {
		t.Errorf("message not truncated: %d chars", len(msg))
	}
	if got := clip(strings.Repeat("é", 50)); utf8.RuneCountInString(got) != maxEchoChars+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("clip = %q", got)
	}
	if clip("Pune") != "Pune" {
		t.Error("short values are kept")
	}
}

func TestParseCriteriaBadInput(t *testing.T) {
	list := func(n int, v string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = v
		}
		return out
	}
	for name, req := range map[string]matchRequest{
		"no city":       {AreaSqft: 1000},
		"NUL city":      {City: "Pune\x00", AreaSqft: 1000},
		"33k city":      {City: strings.Repeat("A", 33000), AreaSqft: 1000},
		"11 goods":      {City: "Pune", AreaSqft: 1000, Goods: list(11, "dg")},
		"11 industries": {City: "Pune", AreaSqft: 1000, Industries: list(11, "pharma")},
		"area 9e17":     {City: "Pune", AreaSqft: 9e17},
		"zero area":     {City: "Pune"},
	} {
		if _, msg := parseCriteria(req); msg == "" {
			t.Errorf("%s: accepted", name)
		}
	}
	q, msg := parseCriteria(matchRequest{City: "A&amp;B", AreaSqft: 1000, Goods: list(10, "dg"), Industries: list(10, "pharma")})
	if msg != "" || q.City != "A&B" || q.CityGone {
		t.Errorf("valid request: %+v %q", q, msg)
	}
	q, msg = parseCriteria(matchRequest{City: "", rawCity: "<script>x</script>", AreaSqft: 1000})
	if msg != "" || !q.CityGone {
		t.Errorf("a city the sanitizer emptied must be gone, not 'required': %+v %q", q, msg)
	}
}

func TestQueryRejected(t *testing.T) {
	if !queryRejected(mongo.CommandError{Code: 2, Message: "Regular expression cannot contain an embedded null byte"}) {
		t.Error("a server-side rejection is a bad request")
	}
	if queryRejected(mongo.ErrClientDisconnected) {
		t.Error("a client / network failure is not the caller's fault")
	}
}

// TestCaptureRawText runs the real chain (capture → shared deserializer) and
// checks the handler sees both the sanitized and the raw place.
func TestCaptureRawText(t *testing.T) {
	var got searchRequest
	var raw rawText
	h := captureRawText(middleware.DeserializeJson[searchRequest]()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = r.Context().Value(middleware.DeserializerContextKey).(searchRequest)
		raw = rawTextOf(r)
	})))
	for body, want := range map[string]struct{ cleaned, raw string }{
		`{"place":"<script>alert(1)</script>"}`: {"", "<script>alert(1)</script>"},
		`{"place":"A&B"}`:                       {"A&amp;B", "A&B"},
		`{"place":"Pune"}`:                      {"Pune", "Pune"},
	} {
		got, raw = searchRequest{}, rawText{}
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		if got.Place != want.cleaned || raw.Place != want.raw {
			t.Errorf("%s: cleaned %q raw %q", body, got.Place, raw.Place)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"place":"`+strings.Repeat("B", maxRawBodyBytes)+`"}`)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: %d, want 413 from the deserializer", rec.Code)
	}
}
