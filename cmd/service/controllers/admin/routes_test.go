package admin

import (
	"testing"

	"github.com/atharva-ng/crunch/internal/config"
)

// TestHandle_RegistersWithoutPanic is the route-registration smoke test: the
// middleware registry panics on duplicate paths, so a bad mirror path fails
// loudly here instead of at server boot.
func TestHandle_RegistersWithoutPanic(t *testing.T) {
	Handle(&config.AppContext{})
}
