package healthcheck

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/middleware"
)

func Handle() {
	middleware.Handle("/health", http.HandlerFunc(handleHealthcheck)).
		WithMethods("GET").
		WithLogEnabled()
}

func handleHealthcheck(w http.ResponseWriter, r *http.Request) {
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]string{
		"status": "ok",
	})
}
