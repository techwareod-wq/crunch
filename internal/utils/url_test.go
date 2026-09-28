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

func TestNormalizeGSCPageURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain page", "https://abc.com/blog/post", "https://abc.com/blog/post"},
		{"trailing slash stripped", "https://abc.com/blog/post/", "https://abc.com/blog/post"},
		{"root slash kept", "https://abc.com/", "https://abc.com/"},
		{"bare host gains root slash", "https://abc.com", "https://abc.com/"},
		{"query stripped", "https://abc.com/x?utm=1", "https://abc.com/x"},
		{"fragment stripped", "https://abc.com/x#section", "https://abc.com/x"},
		{"scheme and host lowercased", "HTTPS://ABC.com/Blog", "https://abc.com/Blog"},
		{"www collapsed (one fact identity per page)", "https://www.abc.com/x", "https://abc.com/x"},
		{"real subdomain kept", "https://clerk.abc.com/x", "https://clerk.abc.com/x"},
		{"http collapses to https (one fact identity per page)", "http://abc.com/x", "https://abc.com/x"},
		{"scheme-less treated as https", "abc.com/blog", "https://abc.com/blog"},
		{"whitespace", "  https://abc.com/x  ", "https://abc.com/x"},
		{"non-http scheme rejected", "ftp://abc.com/x", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NormalizeGSCPageURL(c.in); got != c.want {
				t.Errorf("NormalizeGSCPageURL(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
