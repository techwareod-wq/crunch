package collectors

import (
	"testing"

	"github.com/atharva-ng/crunch/internal/locations"
	"github.com/atharva-ng/crunch/internal/models"
)

func testCatalog(t *testing.T) *locations.Catalog {
	t.Helper()
	cat, err := locations.NewCatalog([]locations.Country{
		{Name: "India", LocationCode: 2356, ISOCode: "IN"},
		{Name: "United Kingdom", LocationCode: 2826, ISOCode: "GB"},
		{Name: "United States", LocationCode: 2840, ISOCode: "US"},
		{Name: "Germany", LocationCode: 2276, ISOCode: "DE"},
	})
	if err != nil {
		t.Fatalf("test catalog: %v", err)
	}
	return cat
}

func TestInferLocationFromDomain(t *testing.T) {
	cat := testCatalog(t)
	for domain, want := range map[string]int{
		"example.in":    2356,
		"example.co.in": 2356,
		"example.co.uk": 2826, // uk aliases to the GB ISO entry
		"example.de":    2276,
		"example.com":   0, // generic
		"tryelip.ai":    0, // ccTLD-as-tech-TLD carries no market signal
		"example.io":    0,
		"example":       0,
	} {
		if got := InferLocationFromDomain(cat, domain); got != want {
			t.Errorf("InferLocationFromDomain(%q) = %d, want %d", domain, got, want)
		}
	}
	if got := InferLocationFromDomain(nil, "example.in"); got != 0 {
		t.Errorf("nil catalog must yield no signal, got %d", got)
	}
}

func TestInferLocationFromLang(t *testing.T) {
	cat := testCatalog(t)
	for lang, want := range map[string]int{
		"en-IN": 2356,
		"en-GB": 2826,
		"en-US": 2840,
		"en":    0, // no region subtag
		"hi-IN": 2356,
		"":      0,
	} {
		if got := InferLocationFromLang(cat, lang); got != want {
			t.Errorf("InferLocationFromLang(%q) = %d, want %d", lang, got, want)
		}
	}
}

func TestRunLocationCode_DefaultsToUS(t *testing.T) {
	if got := RunLocationCode(&models.AuditRun{}); got != DefaultLocationCode {
		t.Errorf("unstamped run location = %d, want US default %d", got, DefaultLocationCode)
	}
	if got := RunLocationCode(&models.AuditRun{LocationCode: 2356}); got != 2356 {
		t.Errorf("stamped run location = %d, want 2356", got)
	}
}

func TestHTMLLangRegex(t *testing.T) {
	body := []byte(`<!DOCTYPE html><html class="dark" lang="en-IN"><head></head></html>`)
	m := htmlLangRe.FindSubmatch(body)
	if m == nil || string(m[1]) != "en-IN" {
		t.Fatalf("lang extraction failed: %v", m)
	}
	if htmlLangRe.FindSubmatch([]byte(`<html><head></head></html>`)) != nil {
		t.Error("lang extraction must not match when the attribute is absent")
	}
}
