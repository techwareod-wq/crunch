package utils

import "testing"

func TestExtractDomain(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare domain", "abc.com", "abc.com"},
		{"scheme + www + path", "https://www.abc.com/blog/post", "abc.com"},
		{"http + trailing slash", "http://abc.com/", "abc.com"},
		{"www no scheme", "www.pqr.ai", "pqr.ai"},
		{"scheme only", "https://pqr.ai", "pqr.ai"},
		{"uppercase + trailing slash", "PQR.ai/", "pqr.ai"},
		{"port and path", "https://www.abc.com:8080/x", "abc.com"},
		{"userinfo", "https://user:pass@abc.com/x", "abc.com"},
		{"query and fragment", "https://abc.com/x?y=1#z", "abc.com"},
		{"whitespace", "  https://www.abc.com/  ", "abc.com"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExtractDomain(c.in); got != c.want {
				t.Errorf("ExtractDomain(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
