package middleware

import (
	"net/http/httptest"
	"strings"
	"testing"

	apperrors "github.com/atharva-ng/crunch/internal/errors"
)

// The error envelope grew `code`/`data` for the entitlement 402s. Legacy
// errors must stay byte-identical on the wire — clients parse this envelope
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

func TestSendJSONError_SubscriptionRequiredCarriesCode(t *testing.T) {
	rec := httptest.NewRecorder()
	SendJSONError(rec, nil, apperrors.ErrSubscriptionRequired)

	if rec.Code != 402 {
		t.Errorf("status = %d, want 402", rec.Code)
	}
	got := strings.TrimSpace(rec.Body.String())
	want := `{"success":false,"error":"active subscription required","code":"subscription_required"}`
	if got != want {
		t.Errorf("envelope:\n got %s\nwant %s", got, want)
	}
}

func TestSendJSONError_FeatureNotIncludedCarriesCodeAndData(t *testing.T) {
	rec := httptest.NewRecorder()
	SendJSONError(rec, nil, apperrors.FeatureNotIncluded("indexly", "cms.publish"))

	if rec.Code != 402 {
		t.Errorf("status = %d, want 402", rec.Code)
	}
	body := rec.Body.String()
	for _, fragment := range []string{
		`"code":"feature_not_included"`,
		`"app":"indexly"`,
		`"feature":"cms.publish"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("envelope missing %s: %s", fragment, body)
		}
	}
}
