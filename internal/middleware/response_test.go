package middleware

import (
	"net/http/httptest"
	"strings"
	"testing"

	apperrors "github.com/atharva-ng/crunch/internal/errors"
)

// The error envelope carries optional `code`/`data`. Errors without them must
// stay byte-identical on the wire — clients parse this envelope
// everywhere, and an unexpected new field on unrelated errors is a silent
// contract change.

func TestSendJSONError_LegacyEnvelopeUnchanged(t *testing.T) {
	rec := httptest.NewRecorder()
	SendJSONError(rec, nil, apperrors.ErrUserNotFound)

	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	got := strings.TrimSpace(rec.Body.String())
	want := `{"success":false,"error":"user not found"}`
	if got != want {
		t.Errorf("legacy envelope changed:\n got %s\nwant %s", got, want)
	}
}

func TestSendJSONError_CodeAndDataCarried(t *testing.T) {
	rec := httptest.NewRecorder()
	SendJSONError(rec, nil, &apperrors.Error{
		Code:    409,
		Message: "thing conflicted",
		ErrCode: "thing_conflict",
		Data:    map[string]string{"thing": "x"},
	})

	if rec.Code != 409 {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	body := rec.Body.String()
	for _, fragment := range []string{
		`"code":"thing_conflict"`,
		`"thing":"x"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("envelope missing %s: %s", fragment, body)
		}
	}
}
