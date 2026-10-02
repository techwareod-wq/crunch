package utils

import "testing"

func TestNormalizePhone(t *testing.T) {
	ok := map[string]string{
		"98765 43210":       "+919876543210",
		"+91 98765-43210":   "+919876543210",
		"098765 43210":      "+919876543210",
		"022 2345 6789":     "+912223456789", // Mumbai landline
		"+1 (415) 555-2671": "+14155552671",  // explicit foreign prefix wins
	}
	for in, want := range ok {
		got, err := NormalizePhone(in, DefaultPhoneRegion)
		if err != nil || got != want {
			t.Errorf("NormalizePhone(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", "12345", "abcdefghij", "+91 98765", "+91 98765 432109"} {
		if got, err := NormalizePhone(in, DefaultPhoneRegion); err == nil {
			t.Errorf("NormalizePhone(%q) = %q, want error", in, got)
		}
	}
}
