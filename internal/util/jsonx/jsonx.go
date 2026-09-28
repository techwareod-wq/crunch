// Package jsonx extracts JSON values from LLM output that may wrap them in
// prose or code fences — the one shared extractor behind every structured
// LLM response in the repo (the agent brain's decision parse, the SIE
// clustering response, and any future caller).
//
// Why not a naive slice: models wrap output in preambles and trailing
// sign-offs that survive fence stripping, and a first-'{'-to-last-'}' slice
// breaks the moment that prose contains a brace of its own ("...} Let me
// {adjust}..." → json.Unmarshal fails with "invalid character 'L' after
// top-level value"). Walking the delimiters while respecting string literals
// and escapes stops exactly at the value's close; truncated output errors
// loudly instead of parsing garbage.
package jsonx

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ExtractObject returns the first complete top-level JSON object in text,
// tolerating surrounding prose and ```json fences. It validates the result
// parses before returning it.
func ExtractObject(text string) (json.RawMessage, error) {
	return extract(text, '{', '}')
}

// ExtractArray is ExtractObject for top-level JSON arrays (some LLM contracts
// return a bare list).
func ExtractArray(text string) (json.RawMessage, error) {
	return extract(text, '[', ']')
}

func extract(text string, open, close byte) (json.RawMessage, error) {
	if v, err := scanBalanced(text, strings.IndexByte(text, open), open, close); err == nil {
		return v, nil
	}
	// Fenced fallback: the value may start inside a fence whose leading
	// characters confused the direct scan (rare, but cheap to try).
	if inner, ok := innerFence(text); ok {
		if v, err := scanBalanced(inner, strings.IndexByte(inner, open), open, close); err == nil {
			return v, nil
		}
	}
	return nil, fmt.Errorf("jsonx: no complete JSON value found")
}

// ExtractObjectFromLastMarker scans backwards for the last object whose first
// key is one of keys (each given quoted, e.g. `"news_ideas"`) and extracts the
// balanced object starting at that object's brace. Used on tool-using output
// where the model may print draft JSON earlier in its reasoning — the LAST
// occurrence is the final answer. Falls back to ExtractObject when no key
// matches.
//
// Whitespace between the brace and the key is skipped, so a pretty-printed
// object matches as readily as a compact one: matching the literal `{"key"`
// silently missed every `{\n  "key"` the model produced, which dropped the
// caller onto ExtractObject and gave up the last-occurrence guarantee.
func ExtractObjectFromLastMarker(text string, keys ...string) (json.RawMessage, error) {
	best := -1
	for _, k := range keys {
		if open := lastObjectOpenedByKey(text, k); open > best {
			best = open
		}
	}
	if best >= 0 {
		if obj, err := scanBalanced(text, best, '{', '}'); err == nil {
			return obj, nil
		}
	}
	return ExtractObject(text)
}

// lastObjectOpenedByKey returns the index of the brace opening the last object
// whose first key is key, or -1. Occurrences that are not in first-key position
// (the key named in prose, or nested deeper in the object) are skipped rather
// than ending the search.
func lastObjectOpenedByKey(text, key string) int {
	for idx := strings.LastIndex(text, key); idx >= 0; idx = strings.LastIndex(text[:idx], key) {
		i := idx - 1
		for i >= 0 && (text[i] == ' ' || text[i] == '\t' || text[i] == '\n' || text[i] == '\r') {
			i--
		}
		if i >= 0 && text[i] == '{' {
			return i
		}
	}
	return -1
}

// scanBalanced walks from the opening delimiter at or after start, tracking
// depth and string/escape state, and returns the balanced value.
func scanBalanced(text string, start int, open, close byte) (json.RawMessage, error) {
	if start < 0 || start >= len(text) {
		return nil, fmt.Errorf("jsonx: no JSON value found")
	}
	if text[start] != open {
		off := strings.IndexByte(text[start:], open)
		if off < 0 {
			return nil, fmt.Errorf("jsonx: no JSON value found")
		}
		start += off
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		c := text[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inString:
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
		case c == open:
			depth++
		case c == close:
			depth--
			if depth == 0 {
				candidate := json.RawMessage(text[start : i+1])
				if !json.Valid(candidate) {
					return nil, fmt.Errorf("jsonx: extracted value is not valid JSON")
				}
				return candidate, nil
			}
		}
	}
	return nil, fmt.Errorf("jsonx: unbalanced JSON value (truncated output?)")
}

// innerFence returns the contents of the first ``` fence, if any.
func innerFence(text string) (string, bool) {
	open := strings.Index(text, "```")
	if open < 0 {
		return "", false
	}
	rest := text[open+3:]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:] // drop the language tag line
	}
	close := strings.Index(rest, "```")
	if close < 0 {
		return "", false
	}
	return rest[:close], true
}
