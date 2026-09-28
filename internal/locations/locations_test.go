package locations

import "testing"

func TestCatalogLookups(t *testing.T) {
	cat, err := NewCatalog([]Country{
		{Name: "India", LocationCode: 2356, ISOCode: "IN"},
		{Name: "United States", LocationCode: 2840, ISOCode: "US"},
	})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}

	if ct, ok := cat.ByName("  india "); !ok || ct.LocationCode != 2356 || ct.ISOCode != "IN" {
		t.Errorf("ByName(india) = %+v, %v", ct, ok)
	}
	if _, ok := cat.ByName("atlantis"); ok {
		t.Error("unknown country must not resolve")
	}
	if got := cat.CodeForISO("in"); got != 2356 {
		t.Errorf("CodeForISO(in) = %d, want 2356", got)
	}
	if got := cat.CodeForISO("IN"); got != 2356 {
		t.Errorf("CodeForISO is case-sensitive; got %d", got)
	}
	if got := cat.CodeForISO("xx"); got != 0 {
		t.Errorf("unknown ISO must yield 0, got %d", got)
	}
	if got := cat.NameForCode(2356); got != "India" {
		t.Errorf("NameForCode(2356) = %q", got)
	}
	if got := cat.NameForCode(9999); got != "" {
		t.Errorf("unknown code must yield empty, got %q", got)
	}
	if names := cat.Countries(); len(names) != 2 || names[0].Name != "India" {
		t.Errorf("Countries() order not preserved: %+v", names)
	}
}

func TestCatalogNilSafety(t *testing.T) {
	var cat *Catalog
	if _, ok := cat.ByName("india"); ok {
		t.Error("nil catalog ByName must miss")
	}
	if cat.CodeForISO("in") != 0 || cat.NameForCode(2356) != "" || cat.Countries() != nil {
		t.Error("nil catalog lookups must degrade to unknown")
	}
}

func TestNewCatalog_Empty(t *testing.T) {
	if _, err := NewCatalog(nil); err == nil {
		t.Error("empty country list must error")
	}
}
