package models

import "testing"

func TestAnalyticsDimKey(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]string
		want string
	}{
		{"empty", nil, ""},
		{"single", map[string]string{"page": "https://a.com/x"}, "page=https://a.com/x"},
		{"sorted keys", map[string]string{"query": "q", "page": "p"}, "page=p|query=q"},
		{"structural chars encoded", map[string]string{"page": "https://a.com/x?a=1|b"}, "page=https://a.com/x?a%3D1%7Cb"},
		{"percent encoded first", map[string]string{"q": "100%=done"}, "q=100%25%3Ddone"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AnalyticsDimKey(c.in); got != c.want {
				t.Errorf("AnalyticsDimKey(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestAnalyticsDimKey_CollisionProof(t *testing.T) {
	// Without encoding these two would serialize identically.
	a := AnalyticsDimKey(map[string]string{"page": "x|query=y"})
	b := AnalyticsDimKey(map[string]string{"page": "x", "query": "y"})
	if a == b {
		t.Errorf("dim keys collide: %q", a)
	}
}
