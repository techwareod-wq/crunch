package utils

import (
	"reflect"
	"testing"

	"github.com/atharva-ng/crunch/internal/config"
)

// balancedFilters mirrors values.yaml siteIntelligence.keywordFilters — the
// balanced_growth defaults with the udr-relative difficulty band.
var balancedFilters = config.KeywordFilterValues{
	MaxRankPosition:   30,
	MinSearchVolume:   100,
	DifficultyFloor:   5,
	DifficultyBelowDR: 15,
	DifficultyAboveDR: 50,
}

// earlyFootholdsFilters mirrors the strategies.early_footholds entry — a
// volume band plus a fixed difficulty window.
var earlyFootholdsFilters = config.KeywordFilterValues{
	MaxRankPosition:       30,
	MinSearchVolume:       10,
	MaxSearchVolume:       1000,
	DifficultyFloor:       0,
	DifficultyMaxAbsolute: 15,
}

// hasFilter reports whether the flat AndFilters output contains the exact
// [field, op, value] triple.
func hasFilter(filters []interface{}, field, op string, value interface{}) bool {
	for _, entry := range filters {
		triple, ok := entry.([]interface{})
		if !ok || len(triple) != 3 {
			continue
		}
		if triple[0] == field && triple[1] == op && reflect.DeepEqual(triple[2], value) {
			return true
		}
	}
	return false
}

// countFilterField counts how many filter triples target the given field.
func countFilterField(filters []interface{}, field string) int {
	n := 0
	for _, entry := range filters {
		if triple, ok := entry.([]interface{}); ok && len(triple) == 3 && triple[0] == field {
			n++
		}
	}
	return n
}

func TestKeywordDifficultyRange(t *testing.T) {
	t.Run("udr-relative band with floor", func(t *testing.T) {
		if min, max := keywordDifficultyRange(10, balancedFilters); min != 5 || max != 60 {
			t.Errorf("range(udr=10) = [%d, %d], want [5, 60] (floor binds)", min, max)
		}
		if min, max := keywordDifficultyRange(40, balancedFilters); min != 25 || max != 90 {
			t.Errorf("range(udr=40) = [%d, %d], want [25, 90]", min, max)
		}
	})

	t.Run("fixed band ignores the domain rating", func(t *testing.T) {
		for _, udr := range []int{0, 15, 80} {
			if min, max := keywordDifficultyRange(udr, earlyFootholdsFilters); min != 0 || max != 15 {
				t.Errorf("range(udr=%d, fixed) = [%d, %d], want [0, 15]", udr, min, max)
			}
		}
	})
}

func TestGetKeywordIdeasRequestObject_Filters(t *testing.T) {
	t.Run("early footholds applies the volume band and fixed KD window", func(t *testing.T) {
		req := GetKeywordIdeasRequestObject(900, []string{"seed"}, 2840, 55, earlyFootholdsFilters)

		if !req.CloselyVariants {
			t.Error("closely_variants must be true")
		}
		if !hasFilter(req.Filters, "keyword_info.search_volume", ">=", 10) {
			t.Errorf("missing volume >= 10 filter: %#v", req.Filters)
		}
		if !hasFilter(req.Filters, "keyword_info.search_volume", "<=", 1000) {
			t.Errorf("missing volume <= 1000 filter: %#v", req.Filters)
		}
		// udr 55 must not widen the fixed band.
		if !hasFilter(req.Filters, "keyword_properties.keyword_difficulty", ">=", 0) ||
			!hasFilter(req.Filters, "keyword_properties.keyword_difficulty", "<=", 15) {
			t.Errorf("missing fixed KD [0, 15] filters: %#v", req.Filters)
		}
	})

	t.Run("balanced keeps the uncapped volume and udr-relative band", func(t *testing.T) {
		req := GetKeywordIdeasRequestObject(900, []string{"seed"}, 2840, 25, balancedFilters)

		if !hasFilter(req.Filters, "keyword_info.search_volume", ">=", 100) {
			t.Errorf("missing volume >= 100 filter: %#v", req.Filters)
		}
		if got := countFilterField(req.Filters, "keyword_info.search_volume"); got != 1 {
			t.Errorf("balanced must not cap volume: %d volume filters, want 1", got)
		}
		if !hasFilter(req.Filters, "keyword_properties.keyword_difficulty", ">=", 10) ||
			!hasFilter(req.Filters, "keyword_properties.keyword_difficulty", "<=", 75) {
			t.Errorf("missing udr-relative KD [10, 75] filters for udr=25: %#v", req.Filters)
		}
	})
}

func TestGetkeywordRequestObject_Filters(t *testing.T) {
	t.Run("early footholds applies rank cap, volume band, fixed KD window", func(t *testing.T) {
		req := GetkeywordRequestObject(200, "example.com", 2840, 60, earlyFootholdsFilters)

		if !hasFilter(req.Filters, "ranked_serp_element.serp_item.rank_group", "<=", 30) {
			t.Errorf("missing rank_group <= 30 filter: %#v", req.Filters)
		}
		if !hasFilter(req.Filters, "keyword_data.keyword_info.search_volume", ">=", 10) ||
			!hasFilter(req.Filters, "keyword_data.keyword_info.search_volume", "<=", 1000) {
			t.Errorf("missing volume band [10, 1000] filters: %#v", req.Filters)
		}
		if !hasFilter(req.Filters, "keyword_data.keyword_properties.keyword_difficulty", ">=", 0) ||
			!hasFilter(req.Filters, "keyword_data.keyword_properties.keyword_difficulty", "<=", 15) {
			t.Errorf("missing fixed KD [0, 15] filters: %#v", req.Filters)
		}
	})

	t.Run("balanced keeps the uncapped volume", func(t *testing.T) {
		req := GetkeywordRequestObject(200, "example.com", 2840, 20, balancedFilters)

		if !hasFilter(req.Filters, "keyword_data.keyword_info.search_volume", ">=", 100) {
			t.Errorf("missing volume >= 100 filter: %#v", req.Filters)
		}
		if got := countFilterField(req.Filters, "keyword_data.keyword_info.search_volume"); got != 1 {
			t.Errorf("balanced must not cap volume: %d volume filters, want 1", got)
		}
	})
}
