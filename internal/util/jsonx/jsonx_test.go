package jsonx

import (
	"encoding/json"
	"testing"
)

func TestExtractObjectPlain(t *testing.T) {
	raw, err := ExtractObject(`Sure, here you go: {"say": "hi", "action": "none", "args": {}} hope that helps`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out["say"] != "hi" {
		t.Fatalf("out = %v err %v", out, err)
	}
}

func TestExtractObjectNestedAndStrings(t *testing.T) {
	raw, err := ExtractObject(`{"a": {"b": "brace } in string", "c": [1, 2]}, "d": "\" escaped"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !json.Valid(raw) {
		t.Fatalf("invalid extraction: %s", raw)
	}
}

// The SIE production failure, ported with its duplicate extractor: the model
// appended a trailing sign-off that itself contained a brace. A naive
// first-'{'-to-last-'}' slice swallowed the prose, leaving "...} Let me
// {adjust}..." which made json.Unmarshal fail with "invalid character 'L'
// after top-level value". The matching-delimiter scan must stop at the
// object's close.
func TestExtractObjectTrailingSignoffWithStrayBrace(t *testing.T) {
	object := `{"clusters":[{"cluster_id":"c","pillar_keyword":"k"}]}`
	cases := []string{
		object + "\n\nLet me know if you'd like me to {adjust} any cluster boundaries.",
		object + "\n\nLet me know if you need anything else.",
		"Here is the clustering result:\n" + object,
		`{"clusters":[{"cluster_id":"c","cluster_name":"a {weird} name"}]} trailing {junk}`,
	}
	for _, input := range cases {
		raw, err := ExtractObject(input)
		if err != nil {
			t.Fatalf("%q: unexpected error: %v", input, err)
		}
		// The extracted body must be exactly one JSON value with nothing
		// trailing, i.e. it must unmarshal the way the real handlers do.
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("extracted body did not unmarshal cleanly: %v\nbody: %s", err, raw)
		}
	}
}

func TestExtractObjectFenced(t *testing.T) {
	text := "```json\n{\"action\": \"write_post\"}\n```"
	raw, err := ExtractObject(text)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out["action"] != "write_post" {
		t.Fatalf("out = %v", out)
	}
}

func TestExtractObjectErrors(t *testing.T) {
	if _, err := ExtractObject("no json here"); err == nil {
		t.Fatal("expected error on no object")
	}
	if _, err := ExtractObject(`{"truncated": tru`); err == nil {
		t.Fatal("expected error on unbalanced object")
	}
	if _, err := ExtractObject(`{"clusters":[{"cluster_id":"c"`); err == nil {
		t.Fatal("expected error on truncated output")
	}
}

func TestExtractArray(t *testing.T) {
	raw, err := ExtractArray(`The competitors are: [{"name": "Acme", "note": "big ] player"}, {"name": "Globex"}] — hope that helps.`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out []struct{ Name string }
	if err := json.Unmarshal(raw, &out); err != nil || len(out) != 2 || out[0].Name != "Acme" {
		t.Fatalf("out = %+v err %v", out, err)
	}

	if _, err := ExtractArray("no list here"); err == nil {
		t.Fatal("expected error on no array")
	}
	if _, err := ExtractArray(`[1, 2`); err == nil {
		t.Fatal("expected error on unbalanced array")
	}
}

func TestExtractObjectFromLastMarker(t *testing.T) {
	text := `thinking... {"news_ideas": []} was a draft. Final answer:
{"news_ideas": [{"angle": "real one"}], "keywords": ["ai"]}`
	raw, err := ExtractObjectFromLastMarker(text, `"news_ideas"`, `"keywords"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out struct {
		NewsIdeas []struct{ Angle string } `json:"news_ideas"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.NewsIdeas) != 1 || out.NewsIdeas[0].Angle != "real one" {
		t.Fatalf("must pick the LAST marker occurrence: %+v", out)
	}
}

// The scout pretty-prints inside a fence, so the key never sits flush against
// the brace. Matching the literal `{"news_ideas"` missed it and fell through to
// ExtractObject, which takes the FIRST object — here a draft in the preamble.
func TestExtractObjectFromLastMarkerPrettyPrinted(t *testing.T) {
	text := "Here is a draft: {\"news_ideas\": [{\"angle\": \"draft\"}]}\n\nFinal:\n```json\n{\n  \"news_ideas\": [\n    {\n      \"angle\": \"real one\"\n    }\n  ],\n  \"keywords\": [\"ai\"]\n}\n```"
	raw, err := ExtractObjectFromLastMarker(text, `"news_ideas"`, `"keywords"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out struct {
		NewsIdeas []struct{ Angle string } `json:"news_ideas"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.NewsIdeas) != 1 || out.NewsIdeas[0].Angle != "real one" {
		t.Fatalf("must pick the pretty-printed LAST object: %+v", out)
	}
}

// A key named in prose is not an object opening; the search must keep walking
// back rather than stopping on it.
func TestExtractObjectFromLastMarkerSkipsProseMention(t *testing.T) {
	text := `{"news_ideas": [{"angle": "real one"}]} — note the "news_ideas" field above.`
	raw, err := ExtractObjectFromLastMarker(text, `"news_ideas"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out struct {
		NewsIdeas []struct{ Angle string } `json:"news_ideas"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.NewsIdeas) != 1 || out.NewsIdeas[0].Angle != "real one" {
		t.Fatalf("out = %+v", out)
	}
}

// A response cut off mid-object must error, never yield a half-parsed one.
func TestExtractObjectFromLastMarkerTruncated(t *testing.T) {
	text := "Story 1 — a long preamble that ate the budget.\n\n```json\n{\n  \"news_ideas\": [\n    {\n      \"angle\": \"good one\",\n      \"stats\": \""
	if _, err := ExtractObjectFromLastMarker(text, `"news_ideas"`, `"keywords"`); err == nil {
		t.Fatal("expected error on truncated object")
	}
}

func TestExtractObjectFromLastMarkerFallsBack(t *testing.T) {
	raw, err := ExtractObjectFromLastMarker(`just {"plain": true} object`, `"news_ideas"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out["plain"] != true {
		t.Fatalf("out = %v", out)
	}
}
