package config

import (
	"testing"
	"time"
)

func TestLoadHTTPTimeouts(t *testing.T) {
	for _, k := range []string{"HTTP_READ_HEADER_TIMEOUT", "HTTP_READ_TIMEOUT", "HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT"} {
		t.Setenv(k, "")
	}
	var c AppConfig
	c.LoadHTTPTimeouts()
	if s := c.Server; s.ReadHeaderTimeout != 10*time.Second || s.ReadTimeout != 30*time.Second ||
		s.WriteTimeout != 60*time.Second || s.IdleTimeout != 120*time.Second {
		t.Errorf("defaults = %+v", s)
	}

	t.Setenv("HTTP_READ_HEADER_TIMEOUT", "5") // whole seconds
	t.Setenv("HTTP_READ_TIMEOUT", "45s")      // duration
	t.Setenv("HTTP_WRITE_TIMEOUT", "2m")      // duration
	t.Setenv("HTTP_IDLE_TIMEOUT", " 90 ")     // trimmed
	c.LoadHTTPTimeouts()
	if s := c.Server; s.ReadHeaderTimeout != 5*time.Second || s.ReadTimeout != 45*time.Second ||
		s.WriteTimeout != 2*time.Minute || s.IdleTimeout != 90*time.Second {
		t.Errorf("overrides = %+v", s)
	}

	t.Setenv("HTTP_READ_HEADER_TIMEOUT", "ten")
	t.Setenv("HTTP_READ_TIMEOUT", "0")
	t.Setenv("HTTP_WRITE_TIMEOUT", "-5s")
	t.Setenv("HTTP_IDLE_TIMEOUT", "1h30x")
	c.LoadHTTPTimeouts()
	if s := c.Server; s.ReadHeaderTimeout != DefaultReadHeaderTimeout || s.ReadTimeout != DefaultReadTimeout ||
		s.WriteTimeout != DefaultWriteTimeout || s.IdleTimeout != DefaultIdleTimeout {
		t.Errorf("invalid values must fall back to the defaults: %+v", s)
	}
}
