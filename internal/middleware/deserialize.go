package middleware

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"time"

	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/microcosm-cc/bluemonday"
)

var sanitizerPolicy = bluemonday.StrictPolicy()

// maxJSONBodyBytes caps the size of a JSON request body the decoder will read
// into memory. 1 MB comfortably covers the largest legitimate payload (a full
// article draft) while preventing an oversized body from exhausting memory.
const maxJSONBodyBytes = 1 << 20 // 1 MB

// DecodeJSONBody reads the request body and unmarshals it into a value of type T.
// The read is capped at maxJSONBodyBytes; a larger body is rejected with 413
// before it is fully buffered.
func DecodeJSONBody[T any](body io.ReadCloser) (T, *apperrors.Error) {
	var zero T

	if body == nil {
		return zero, apperrors.ErrNilRequestBody
	}

	// Read one byte past the cap so we can tell "exactly at the limit" (allowed)
	// from "over the limit" (rejected) without trusting Content-Length.
	data, err := io.ReadAll(io.LimitReader(body, maxJSONBodyBytes+1))
	if err != nil {
		return zero, apperrors.ErrInvalidRequestBody
	}

	if len(data) > maxJSONBodyBytes {
		return zero, apperrors.ErrRequestBodyTooLarge
	}

	if len(data) == 0 {
		return zero, apperrors.ErrEmptyRequestBody
	}

	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return zero, apperrors.ErrInvalidRequestBody
	}

	return result, nil
}

// sanitizeStrings recursively traverses a struct and sanitizes all string fields
// to prevent XSS attacks. It handles nested structs, slices, maps, and pointers.
func sanitizeStrings(v reflect.Value) {
	switch v.Kind() {
	case reflect.Ptr:
		if !v.IsNil() {
			sanitizeStrings(v.Elem())
		}
	case reflect.Struct:
		// Skip time.Time to avoid corrupting non-string data
		if v.Type() == reflect.TypeOf(time.Time{}) {
			return
		}
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			if field.CanSet() {
				sanitizeStrings(field)
			}
		}
	case reflect.String:
		if v.CanSet() {
			v.SetString(sanitizerPolicy.Sanitize(v.String()))
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			sanitizeStrings(v.Index(i))
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			elem := v.MapIndex(key)
			// Map values are not addressable, so we need to copy and replace
			if elem.Kind() == reflect.String {
				v.SetMapIndex(key, reflect.ValueOf(sanitizerPolicy.Sanitize(elem.String())))
			}
		}
	}
}

// SanitizeStruct sanitizes all string fields of v — which must be a pointer to
// a struct — with the same policy DeserializeJson applies. For handlers that
// must decode their own body (method-dispatched routes that can't chain a
// deserializer because another verb on the path has no body).
func SanitizeStruct(v interface{}) {
	sanitizeStrings(reflect.ValueOf(v).Elem())
}

// DeserializeJson returns a middleware that decodes the JSON request body into
// a value of type T, sanitizes all string fields, and stores the result in the
// request context under DeserializerContextKey.
func DeserializeJson[T any]() func(http.Handler) http.Handler {
	return deserializeJSON[T](false)
}

// DeserializeJsonOptional is DeserializeJson for routes whose body is
// OPTIONAL: a missing/empty body decodes to T's zero value instead of a 400.
// Exists for the payment routes that historically took no body and gained an
// optional `app` field — deployed clients that still POST bodyless must keep
// working (omitted app = indexly).
func DeserializeJsonOptional[T any]() func(http.Handler) http.Handler {
	return deserializeJSON[T](true)
}

func deserializeJSON[T any](optionalBody bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Info("deserializing request body", "method", r.Method, "path", r.URL.Path)

			result, appErr := DecodeJSONBody[T](r.Body)
			if appErr != nil {
				emptyBody := appErr == apperrors.ErrEmptyRequestBody || appErr == apperrors.ErrNilRequestBody
				if !(optionalBody && emptyBody) {
					log.Error("failed to decode request body",
						"error", appErr.Message,
						"method", r.Method,
						"path", r.URL.Path,
					)
					SendJSONError(w, r, appErr)
					return
				}
				var zero T
				result = zero
			}

			sanitizeStrings(reflect.ValueOf(&result).Elem())

			ctx := context.WithValue(r.Context(), DeserializerContextKey, result)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
