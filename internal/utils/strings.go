package utils

// Truncate returns s unchanged when it has at most n runes; otherwise the
// first n runes followed by "…". Meant to keep log fields bounded, not for
// user-facing display.
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
