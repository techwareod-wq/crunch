package middleware

import (
	"context"
	"log/slog"
	"net/http"

	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/util/log"
)

var routes = make(map[string]http.Handler)

type pattern string

// contextKey is used for typed context keys to avoid collisions.
type contextKey int

const (
	UserContextKey contextKey = iota
	UserEmailKey
	AuthTypeKey
	DeserializerContextKey
	LoggerContextKey
	ActiveCompanyContextKey
)

const JWTAuthIdentifier = "jwt"

func Handle(p string, h http.Handler) pattern {
	_, exists := routes[p]
	if exists {
		log.Error("route already registered, exiting", "path", p)
		panic("route " + p + " is already registered")
	}
	routes[p] = h
	return pattern(p)
}

// Commonly used to inject AppContext into the request context.
func (p pattern) With(mw func(http.Handler) http.Handler) pattern {
	ro := routes[string(p)]
	routes[string(p)] = mw(ro)
	return p
}

// WithMethods restricts the route to the given HTTP methods.
// OPTIONS is always allowed for CORS preflight.
func (p pattern) WithMethods(methods ...string) pattern {
	ro := routes[string(p)]
	routes[string(p)] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			ro.ServeHTTP(w, r)
			return
		}
		for _, m := range methods {
			if r.Method == m {
				ro.ServeHTTP(w, r)
				return
			}
		}
		SendJSONError(w, r, apperrors.ErrMethodNotAllowed)
	})
	return p
}

func InjectContext(key, val interface{}) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), key, val)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func (p pattern) WithLogEnabled() pattern {
	ro := routes[string(p)]
	routes[string(p)] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Info("incoming request",
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
		)
		ro.ServeHTTP(w, r)
	})
	return p
}

func (p pattern) WithLogger() pattern {
	ro := routes[string(p)]
	routes[string(p)] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := log.Logger.With(
			"method", r.Method,
			"path", r.URL.Path,
			"remote", r.RemoteAddr,
		)
		ctx := context.WithValue(r.Context(), LoggerContextKey, logger)
		ro.ServeHTTP(w, r.WithContext(ctx))
	})
	return p
}

// GetLogger returns the request-scoped *slog.Logger that WithLogger stored on
// the context (falling back to the base logger when no wrapper ran). Returning
// the *slog.Logger directly is intentional: it is exactly what the logging
// wrapper wraps, so callers log against the same request-scoped fields.
func GetLogger(r *http.Request) *slog.Logger {
	if l, ok := r.Context().Value(LoggerContextKey).(*slog.Logger); ok {
		return l
	}
	return log.Logger
}

// If mux is nil, http.DefaultServeMux is used.
func RegisterAll(mux *http.ServeMux) {
	if mux == nil {
		mux = http.DefaultServeMux
	}
	for path, handler := range routes {
		mux.Handle(path, handler)
	}
}
