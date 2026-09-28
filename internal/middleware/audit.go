package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// Persisted admin audit trail (the adminActions collection). This file is the
// write side; it is invoked exclusively from WithAdminAuthorization so that
// every /v1/admin/* route is audited by construction — a route cannot opt out
// without also losing the allowlist gate. Policy:
//
//   - mutating requests (anything but GET/HEAD/OPTIONS) by an allowlisted
//     admin: always recorded, whatever the response status — failed attempts
//     are evidence too, and a panicking handler is recorded as a 500.
//   - allowlist denials (403): recorded for any method, throttled per email so
//     an authenticated non-admin cannot flood the collection.
//   - unauthenticated requests never get here (JWT rejects them first), so
//     there is no unauthenticated write path into the audit collection.
//
// Audit writes are fail-open: a Mongo hiccup is logged loudly but never turns
// a completed admin action into an error response.

// insertAdminAction is a seam for tests (mirrors findUserByID in the admin
// controllers) so the audit path runs without a live Mongo.
var insertAdminAction = models.InsertAdminAction

const (
	adminActionHeader   = "X-Admin-Action"
	maxActionIDLen      = 128
	maxAuditBodyCapture = 1 << 20 // matches the DeserializeJson body cap
	auditWriteTimeout   = 5 * time.Second
	denialWriteWindow   = time.Minute
	// maxDenialThrottleEntries size-caps the denialLog map so a stream of
	// distinct attacker emails (each a valid Clerk JWT) cannot grow it without
	// bound: at the cap it is dropped wholesale, which at worst lets one extra
	// denial per email through — the log line is never lost, only the write
	// dedup resets.
	maxDenialThrottleEntries = 4096
)

// statusRecorder is the codebase's first ResponseWriter wrapper: it only
// captures the status code. No admin handler uses Flusher/Hijacker (all go
// through SendJSONResponse/SendJSONError), so plain embedding is safe.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (rec *statusRecorder) WriteHeader(status int) {
	if rec.status == 0 {
		rec.status = status
	}
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *statusRecorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.ResponseWriter.Write(b)
}

// denialLog throttles persisted 403 rows to one per email per window. Every
// denial still emits a log line; the throttle only bounds collection writes.
var denialLog = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

func shouldPersistDenial(email string) bool {
	denialLog.Lock()
	defer denialLog.Unlock()
	now := time.Now()
	if last, ok := denialLog.last[email]; ok && now.Sub(last) < denialWriteWindow {
		return false
	}
	if len(denialLog.last) >= maxDenialThrottleEntries {
		denialLog.last = make(map[string]time.Time, maxDenialThrottleEntries)
	}
	denialLog.last[email] = now
	return true
}

// auditBodyCapture tees the raw request body into a capped buffer for the few
// admin routes that decode their own JSON (web-entity PATCH, plans upsert) —
// for every other route the deserialized value is already in the context and
// no capture is needed.
type auditBodyCapture struct {
	io.Reader
	io.Closer
	buf *bytes.Buffer
}

func captureAuditBody(r *http.Request) *auditBodyCapture {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}
	if r.Body == nil || r.Context().Value(DeserializerContextKey) != nil {
		return nil
	}
	buf := &bytes.Buffer{}
	capture := &auditBodyCapture{
		Reader: io.TeeReader(io.LimitReader(r.Body, maxAuditBodyCapture), buf),
		Closer: r.Body,
		buf:    buf,
	}
	r.Body = capture
	return capture
}

// auditPayload returns the request payload as JSON bytes: the sanitized
// deserialized value when the route used DeserializeJson, else the captured
// raw body.
func auditPayload(r *http.Request, captured *auditBodyCapture) []byte {
	if decoded := r.Context().Value(DeserializerContextKey); decoded != nil {
		if b, err := json.Marshal(decoded); err == nil {
			return b
		}
	}
	if captured != nil && captured.buf.Len() > 0 {
		return captured.buf.Bytes()
	}
	return nil
}

// auditTarget extracts the requested target user from wherever this route's
// convention put it (?userId= / ?targetUserId= on GET and DELETE, userId /
// targetUserId JSON fields on body routes). It records what was REQUESTED —
// resolution failures still show up as rows with a non-2xx status.
func auditTarget(r *http.Request, payload []byte) string {
	q := r.URL.Query()
	if v := q.Get("userId"); v != "" {
		return v
	}
	if v := q.Get("targetUserId"); v != "" {
		return v
	}
	if len(payload) > 0 {
		var probe struct {
			UserID       string `json:"userId"`
			TargetUserID string `json:"targetUserId"`
		}
		if err := json.Unmarshal(payload, &probe); err == nil {
			if probe.UserID != "" {
				return probe.UserID
			}
			return probe.TargetUserID
		}
	}
	return ""
}

func auditActionID(r *http.Request) string {
	id := r.Header.Get(adminActionHeader)
	if len(id) > maxActionIDLen {
		id = id[:maxActionIDLen]
	}
	return id
}

// recordAdminAction persists one audit row. The write context is detached
// from the request (an admin closing the tab must not lose the row) but
// keeps a hard timeout.
func recordAdminAction(r *http.Request, email string, status int, payload []byte) {
	action := &models.AdminAction{
		AdminEmail:   email,
		Method:       r.Method,
		Path:         r.URL.Path,
		ActionID:     auditActionID(r),
		TargetUserID: auditTarget(r, payload),
		Status:       status,
	}
	if len(payload) > 0 {
		sum := sha256.Sum256(payload)
		action.PayloadHash = hex.EncodeToString(sum[:])
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditWriteTimeout)
	defer cancel()
	if err := insertAdminAction(ctx, action); err != nil {
		log.Error("ADMIN AUDIT WRITE FAILED — action executed but not recorded",
			"error", err,
			"admin_email", email,
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"target_user_id", action.TargetUserID,
		)
	}
}

func auditsMutation(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}
