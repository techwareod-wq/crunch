package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
)

// TestMain neuters the audit insert seam for the whole package: without a
// live Mongo, models.InsertAdminAction would nil-deref, and most middleware
// tests (including the pre-existing 403 cases in admin_test.go) now cross the
// audit path. Audit tests swap in a recording stub via captureAuditInserts.
func TestMain(m *testing.M) {
	insertAdminAction = func(ctx context.Context, action *models.AdminAction) error { return nil }
	os.Exit(m.Run())
}

// captureAuditInserts swaps the insert seam for a recording stub and resets
// the denial throttle so each test starts from a clean window.
func captureAuditInserts(t *testing.T) *[]models.AdminAction {
	t.Helper()
	var rows []models.AdminAction
	orig := insertAdminAction
	insertAdminAction = func(ctx context.Context, action *models.AdminAction) error {
		rows = append(rows, *action)
		return nil
	}
	denialLog.Lock()
	denialLog.last = map[string]time.Time{}
	denialLog.Unlock()
	t.Cleanup(func() { insertAdminAction = orig })
	return &rows
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestAudit_MutatingRequestByAdminIsRecorded(t *testing.T) {
	rows := captureAuditInserts(t)

	Handle("/test/audit-mutation", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})).WithAdminAuthorization()

	body := struct {
		UserID string `json:"userId"`
		Note   string `json:"note"`
	}{UserID: "0123456789abcdef01234567", Note: "retry"}

	r := httptest.NewRequest(http.MethodPost, "/test/audit-mutation", nil)
	r.Header.Set(adminActionHeader, "demo.dispatch")
	ctx := context.WithValue(r.Context(), UserContextKey, &models.User{Role: models.RoleAdmin, Email: "admin@x.com"})
	ctx = context.WithValue(ctx, DeserializerContextKey, body)
	rec := httptest.NewRecorder()
	routes["/test/audit-mutation"].ServeHTTP(rec, r.WithContext(ctx))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if len(*rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(*rows))
	}
	row := (*rows)[0]
	if row.AdminEmail != "admin@x.com" || row.Method != http.MethodPost || row.Path != "/test/audit-mutation" {
		t.Errorf("row identity = %+v", row)
	}
	if row.Status != http.StatusAccepted {
		t.Errorf("row.Status = %d, want 202", row.Status)
	}
	if row.ActionID != "demo.dispatch" {
		t.Errorf("row.ActionID = %q", row.ActionID)
	}
	if row.TargetUserID != "0123456789abcdef01234567" {
		t.Errorf("row.TargetUserID = %q, want the body userId", row.TargetUserID)
	}
	want := sha256Hex([]byte(`{"userId":"0123456789abcdef01234567","note":"retry"}`))
	if row.PayloadHash != want {
		t.Errorf("row.PayloadHash = %q, want sha256 of the deserialized payload", row.PayloadHash)
	}
}

func TestAudit_AdminGetIsNotRecorded(t *testing.T) {
	rows := captureAuditInserts(t)
	h := buildAdminRoute(t, "/test/audit-get-skip")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestWithAccess(models.RoleAdmin, "admin@x.com"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(*rows) != 0 {
		t.Fatalf("audit rows = %d, want 0 for an allowed GET", len(*rows))
	}
}

func TestAudit_DenialIsRecordedAndThrottled(t *testing.T) {
	rows := captureAuditInserts(t)
	h := buildAdminRoute(t, "/test/audit-denial")

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, requestWithAccess(models.RoleUser, "intruder@x.com"))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	}

	if len(*rows) != 1 {
		t.Fatalf("audit rows = %d, want 1 (denials throttled per email)", len(*rows))
	}
	row := (*rows)[0]
	if row.Status != http.StatusForbidden || row.AdminEmail != "intruder@x.com" {
		t.Errorf("denial row = %+v", row)
	}
}

func TestAudit_SelfDecodedBodyIsCapturedWhenHandlerReadsIt(t *testing.T) {
	rows := captureAuditInserts(t)

	raw := `{"userId":"aaaabbbbccccddddeeeeffff","itemId":"w1"}`
	Handle("/test/audit-selfdecode", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mirrors handlers that decode their own body instead of using
		// DeserializeJson.
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Errorf("handler body read: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})).WithAdminAuthorization()

	r := httptest.NewRequest(http.MethodPatch, "/test/audit-selfdecode", strings.NewReader(raw))
	ctx := context.WithValue(r.Context(), UserContextKey, &models.User{Role: models.RoleAdmin, Email: "admin@x.com"})
	rec := httptest.NewRecorder()
	routes["/test/audit-selfdecode"].ServeHTTP(rec, r.WithContext(ctx))

	if len(*rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(*rows))
	}
	row := (*rows)[0]
	if row.TargetUserID != "aaaabbbbccccddddeeeeffff" {
		t.Errorf("row.TargetUserID = %q, want value probed from raw body", row.TargetUserID)
	}
	if row.PayloadHash != sha256Hex([]byte(raw)) {
		t.Errorf("row.PayloadHash = %q, want sha256 of the raw body", row.PayloadHash)
	}
}

func TestAudit_QueryTargetIsRecordedOnDelete(t *testing.T) {
	rows := captureAuditInserts(t)

	Handle("/test/audit-query-target", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).WithAdminAuthorization()

	r := httptest.NewRequest(http.MethodDelete, "/test/audit-query-target?userId=1234567890abcdef12345678&scheduledArticleId=s1", nil)
	ctx := context.WithValue(r.Context(), UserContextKey, &models.User{Role: models.RoleAdmin, Email: "admin@x.com"})
	rec := httptest.NewRecorder()
	routes["/test/audit-query-target"].ServeHTTP(rec, r.WithContext(ctx))

	if len(*rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(*rows))
	}
	if got := (*rows)[0].TargetUserID; got != "1234567890abcdef12345678" {
		t.Errorf("TargetUserID = %q, want the ?userId= value", got)
	}
}

func TestAudit_PanickingHandlerIsRecordedAs500AndRepanics(t *testing.T) {
	rows := captureAuditInserts(t)

	Handle("/test/audit-panic", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("mid-mutation crash")
	})).WithAdminAuthorization()

	r := httptest.NewRequest(http.MethodPost, "/test/audit-panic", nil)
	ctx := context.WithValue(r.Context(), UserContextKey, &models.User{Role: models.RoleAdmin, Email: "admin@x.com"})

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Error("panic did not propagate through the audit wrapper")
			}
		}()
		routes["/test/audit-panic"].ServeHTTP(httptest.NewRecorder(), r.WithContext(ctx))
	}()

	if len(*rows) != 1 {
		t.Fatalf("audit rows = %d, want 1 even when the handler panics", len(*rows))
	}
	if got := (*rows)[0].Status; got != http.StatusInternalServerError {
		t.Errorf("panic row status = %d, want 500", got)
	}
}

func TestAudit_ActionIDHeaderIsTruncated(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	r.Header.Set(adminActionHeader, strings.Repeat("a", maxActionIDLen+50))
	if got := auditActionID(r); len(got) != maxActionIDLen {
		t.Errorf("action id length = %d, want %d", len(got), maxActionIDLen)
	}
}

func TestStatusRecorder_DefaultsTo200OnWrite(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	if _, err := rec.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if rec.status != http.StatusOK {
		t.Errorf("status = %d, want implicit 200", rec.status)
	}
}

func TestStatusRecorder_FirstWriteHeaderWins(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	rec.WriteHeader(http.StatusConflict)
	rec.WriteHeader(http.StatusOK) // superfluous, mirrors stdlib behavior
	if rec.status != http.StatusConflict {
		t.Errorf("status = %d, want first-write 409", rec.status)
	}
}
