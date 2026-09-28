package gsc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"google.golang.org/api/googleapi"
)

// The 2026-08-25 backfill incident: Google reported QPS quota pressure as a
// 403, which the old classifier read as "SA removed" and flipped the
// integration to error without retrying. These tests pin the quota/permission
// split.

func quota403() error {
	return &googleapi.Error{
		Code:    403,
		Message: "Search Analytics QPS quota exceeded. Learn about usage limits",
		Errors:  []googleapi.ErrorItem{{Reason: "quotaExceeded"}},
	}
}

func TestQuota403IsQuotaNotPermission(t *testing.T) {
	err := fmt.Errorf("gsc: searchanalytics.query failed: %w", quota403())
	if !IsQuotaExceeded(err) {
		t.Fatal("quota 403 not classified as quota")
	}
	if IsPermissionDenied(err) {
		t.Fatal("quota 403 must NOT classify as permission denied")
	}
	if !isRetryable(err) {
		t.Fatal("quota 403 must be retryable")
	}
}

func TestQuota403ByMessageOnly(t *testing.T) {
	// Reasons aren't always populated; the message is the fallback.
	err := &googleapi.Error{Code: 403, Message: "Search Analytics QPS quota exceeded."}
	if !IsQuotaExceeded(err) {
		t.Fatal("quota-message 403 not classified as quota")
	}
	if IsPermissionDenied(err) {
		t.Fatal("quota-message 403 must NOT classify as permission denied")
	}
}

func TestRealPermission403(t *testing.T) {
	err := &googleapi.Error{
		Code:    403,
		Message: "User does not have sufficient permission for site",
		Errors:  []googleapi.ErrorItem{{Reason: "forbidden"}},
	}
	if IsQuotaExceeded(err) {
		t.Fatal("permission 403 misclassified as quota")
	}
	if !IsPermissionDenied(err) {
		t.Fatal("permission 403 not classified as permission denied")
	}
	if isRetryable(err) {
		t.Fatal("permission 403 must not burn retries")
	}
}

func Test401IsPermissionDenied(t *testing.T) {
	err := &googleapi.Error{Code: 401, Message: "Invalid Credentials"}
	if !IsPermissionDenied(err) {
		t.Fatal("401 not classified as permission denied")
	}
	if IsQuotaExceeded(err) {
		t.Fatal("401 misclassified as quota")
	}
}

func Test429IsQuotaAndRetryable(t *testing.T) {
	err := &googleapi.Error{Code: 429, Message: "Rate limit exceeded"}
	if !IsQuotaExceeded(err) || !isRetryable(err) {
		t.Fatal("429 must be quota + retryable")
	}
	if IsPermissionDenied(err) {
		t.Fatal("429 misclassified as permission denied")
	}
}

func TestWaitTurnSpacesCalls(t *testing.T) {
	p := &gscProvider{}
	ctx := context.Background()
	if err := p.waitTurn(ctx); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := p.waitTurn(ctx); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < apiCallGap-50*time.Millisecond {
		t.Fatalf("second call not throttled: elapsed %v, want ≥ ~%v", elapsed, apiCallGap)
	}
}

func TestWaitTurnHonorsContext(t *testing.T) {
	p := &gscProvider{}
	if err := p.waitTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.waitTurn(ctx); err == nil {
		t.Fatal("canceled context must abort the throttle wait")
	}
}
