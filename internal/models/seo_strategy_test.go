package models

import "testing"

func TestDefaultSEOStrategyForDR(t *testing.T) {
	cases := []struct {
		dr   int
		want string
	}{
		{dr: 0, want: SEOStrategyEarlyFootholds},  // unknown rating → new/weak domain
		{dr: 19, want: SEOStrategyEarlyFootholds}, // just under the cutoff
		{dr: 20, want: SEOStrategyBalancedGrowth}, // at the cutoff
		{dr: 55, want: SEOStrategyBalancedGrowth},
	}
	for _, tc := range cases {
		if got := DefaultSEOStrategyForDR(tc.dr); got != tc.want {
			t.Errorf("DefaultSEOStrategyForDR(%d) = %q, want %q", tc.dr, got, tc.want)
		}
	}
}

func TestWebEntitySEOStrategyOrDefault(t *testing.T) {
	strPtr := func(s string) *string { return &s }

	t.Run("explicit valid choice wins over the DR default", func(t *testing.T) {
		w := &WebEntity{
			SEOStrategy:     strPtr(SEOStrategyBalancedGrowth),
			BusinessContext: &BusinessContext{UserDomainRating: 5},
		}
		if got := w.SEOStrategyOrDefault(); got != SEOStrategyBalancedGrowth {
			t.Errorf("SEOStrategyOrDefault() = %q, want explicit balanced_growth", got)
		}
	})

	t.Run("invalid stored value falls back to the DR default", func(t *testing.T) {
		w := &WebEntity{
			SEOStrategy:     strPtr("retired_strategy"),
			BusinessContext: &BusinessContext{UserDomainRating: 50},
		}
		if got := w.SEOStrategyOrDefault(); got != SEOStrategyBalancedGrowth {
			t.Errorf("SEOStrategyOrDefault() = %q, want DR-derived balanced_growth", got)
		}
	})

	t.Run("unset choice derives from the domain rating", func(t *testing.T) {
		w := &WebEntity{BusinessContext: &BusinessContext{UserDomainRating: 12}}
		if got := w.SEOStrategyOrDefault(); got != SEOStrategyEarlyFootholds {
			t.Errorf("SEOStrategyOrDefault() = %q, want early_footholds for DR 12", got)
		}
	})

	t.Run("nil entity and nil context resolve to the unknown-DR default", func(t *testing.T) {
		var w *WebEntity
		if got := w.SEOStrategyOrDefault(); got != SEOStrategyEarlyFootholds {
			t.Errorf("nil entity SEOStrategyOrDefault() = %q, want early_footholds", got)
		}
		if got := (&WebEntity{}).SEOStrategyOrDefault(); got != SEOStrategyEarlyFootholds {
			t.Errorf("no-context SEOStrategyOrDefault() = %q, want early_footholds", got)
		}
	})
}
