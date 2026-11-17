package finder

import "testing"

// The limiter itself is tested in internal/ratelimit.
func TestSettingsFromEnv(t *testing.T) {
	t.Setenv("FINDER_RATE_PER_MIN", "120")
	t.Setenv("FINDER_RATE_BURST", "5")
	t.Setenv("FINDER_TRUST_PROXY", "true")
	if s := settingsFromEnv(); s != (limitSettings{perMin: 120, burst: 5, trustProxy: true}) {
		t.Errorf("env: %+v", s)
	}
	t.Setenv("FINDER_RATE_PER_MIN", "-3")
	t.Setenv("FINDER_RATE_BURST", "lots")
	t.Setenv("FINDER_TRUST_PROXY", "")
	if s := settingsFromEnv(); s != (limitSettings{perMin: defaultRatePerMin, burst: defaultRateBurst}) {
		t.Errorf("invalid values fall back to defaults: %+v", s)
	}
}
