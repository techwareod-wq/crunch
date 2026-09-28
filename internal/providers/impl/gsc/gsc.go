// Package gsc implements the Google Search Console provider on the official
// searchconsole/v1 client (the repo's first Google API dependency — locked by
// the GSC analytics plan Step 0; SA JWT auth needs the SDK's token refresh,
// which the shared ApiClient has no notion of).
package gsc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	searchconsole "google.golang.org/api/searchconsole/v1"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/util/log"
)

var _ interfaces.GSC = (*gscProvider)(nil)

// ErrGSCDisabled: no service-account credential is configured
// (GSC_SERVICE_ACCOUNT_JSON unset or unparseable). Mirrors ErrMailerDisabled.
var ErrGSCDisabled = fmt.Errorf("gsc disabled: GSC_SERVICE_ACCOUNT_JSON not configured")

const (
	// maxRowLimit is the Search Analytics API's per-request rowLimit ceiling.
	maxRowLimit = 25000
	// maxAttempts is the total tries per API call (1 initial + retries).
	// Retries fire only on 429/5xx/quota/transport errors; the async handler's
	// own re-enqueue retry sits above this. Mirrors the Anthropic provider.
	maxAttempts = 4
	// maxRetryWait caps a single backoff sleep.
	maxRetryWait = 60 * time.Second
	// apiCallGap throttles the whole provider to one Search Console call per
	// gap (5 QPS). Google enforces a short-term QPS quota that concurrent
	// backfill chunks blow through ("Search Analytics QPS quota exceeded",
	// 2026-08-25 incident) — the provider is the singleton chokepoint every
	// caller (cron, backfill, verify) already goes through.
	apiCallGap = 200 * time.Millisecond
)

var (
	instance *gscProvider
	once     sync.Once
)

type gscProvider struct {
	svc         *searchconsole.Service
	clientEmail string

	// throttle state: nextCall is the earliest time the next API call may
	// fire; each caller claims a slot under mu then sleeps outside it.
	mu       sync.Mutex
	nextCall time.Time
}

// waitTurn blocks until this call's claimed slot arrives (or ctx ends),
// spacing all API calls apiCallGap apart across every goroutine.
func (g *gscProvider) waitTurn(ctx context.Context) error {
	g.mu.Lock()
	now := time.Now()
	if g.nextCall.Before(now) {
		g.nextCall = now
	}
	wait := g.nextCall.Sub(now)
	g.nextCall = g.nextCall.Add(apiCallGap)
	g.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// GetProvider builds the singleton from the decoded service-account key JSON.
// An empty or unparseable key yields a disabled-but-real provider (Enabled()
// false, calls return ErrGSCDisabled) — boot must not crash without GSC.
func GetProvider(serviceAccountJSON []byte) interfaces.GSC {
	once.Do(func() {
		p := &gscProvider{}
		instance = p
		if len(serviceAccountJSON) == 0 {
			return
		}
		var key struct {
			ClientEmail string `json:"client_email"`
		}
		if err := json.Unmarshal(serviceAccountJSON, &key); err != nil || key.ClientEmail == "" {
			log.Warn("gsc: service-account JSON has no parseable client_email, provider disabled", "error", err)
			return
		}
		// The typed loader (vs the deprecated WithCredentialsJSON) pins the
		// credential to the service-account type: a swapped-in
		// external_account config — which can execute arbitrary local
		// binaries — is rejected instead of honored.
		svc, err := searchconsole.NewService(context.Background(),
			option.WithAuthCredentialsJSON(option.ServiceAccount, serviceAccountJSON),
			option.WithScopes(searchconsole.WebmastersReadonlyScope))
		if err != nil {
			log.Warn("gsc: client construction failed, provider disabled", "error", err)
			return
		}
		p.svc = svc
		p.clientEmail = key.ClientEmail
		log.Info("Injected GSC provider", "saEmail", key.ClientEmail)
	})
	return instance
}

func (g *gscProvider) Enabled() bool { return g.svc != nil }

func (g *gscProvider) ClientEmail() string { return g.clientEmail }

func (g *gscProvider) ListSites(ctx context.Context) ([]dto.GSCSite, error) {
	if g.svc == nil {
		return nil, ErrGSCDisabled
	}
	var resp *searchconsole.SitesListResponse
	err := g.withRetry(ctx, func() error {
		var callErr error
		resp, callErr = g.svc.Sites.List().Context(ctx).Do()
		return callErr
	})
	if err != nil {
		return nil, fmt.Errorf("gsc: sites.list failed: %w", err)
	}
	sites := make([]dto.GSCSite, 0, len(resp.SiteEntry))
	for _, s := range resp.SiteEntry {
		sites = append(sites, dto.GSCSite{SiteURL: s.SiteUrl, PermissionLevel: s.PermissionLevel})
	}
	return sites, nil
}

func (g *gscProvider) QuerySearchAnalytics(ctx context.Context, property string, req dto.GSCQueryRequest) ([]dto.GSCRow, error) {
	if g.svc == nil {
		return nil, ErrGSCDisabled
	}
	var rows []dto.GSCRow
	startRow := 0
	for {
		limit := maxRowLimit
		if req.RowLimit > 0 && req.RowLimit-len(rows) < limit {
			limit = req.RowLimit - len(rows)
		}
		if limit <= 0 {
			break
		}
		apiReq := &searchconsole.SearchAnalyticsQueryRequest{
			StartDate:  req.StartDate,
			EndDate:    req.EndDate,
			Dimensions: req.Dimensions,
			RowLimit:   int64(limit),
			StartRow:   int64(startRow),
		}
		var resp *searchconsole.SearchAnalyticsQueryResponse
		err := g.withRetry(ctx, func() error {
			var callErr error
			resp, callErr = g.svc.Searchanalytics.Query(property, apiReq).Context(ctx).Do()
			return callErr
		})
		if err != nil {
			return nil, fmt.Errorf("gsc: searchanalytics.query %q failed: %w", property, err)
		}
		for _, r := range resp.Rows {
			rows = append(rows, dto.GSCRow{
				Keys:        r.Keys,
				Clicks:      r.Clicks,
				Impressions: r.Impressions,
				CTR:         r.Ctr,
				Position:    r.Position,
			})
		}
		// A short page means the property has no more rows for this query.
		if len(resp.Rows) < limit {
			break
		}
		startRow += len(resp.Rows)
	}
	return rows, nil
}

// withRetry runs call up to maxAttempts times with exponential backoff (2s,
// 4s, 8s, capped) on 429/5xx/quota/transport errors, waiting for a throttle
// slot before every attempt. Non-quota 4xx auth/validation errors surface
// immediately — a revoked SA must reach the caller as-is, not burn retries.
func (g *gscProvider) withRetry(ctx context.Context, call func() error) error {
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err = g.waitTurn(ctx); err != nil {
			return err
		}
		err = call()
		if err == nil {
			return nil
		}
		if !isRetryable(err) || attempt == maxAttempts {
			return err
		}
		wait := time.Duration(1<<attempt) * time.Second
		if wait > maxRetryWait {
			wait = maxRetryWait
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return err
}

func isRetryable(err error) bool {
	var ge *googleapi.Error
	if errors.As(err, &ge) {
		return ge.Code == 429 || ge.Code >= 500 || IsQuotaExceeded(err)
	}
	// No structured API error ⇒ transport-level failure; worth retrying.
	return true
}

// IsQuotaExceeded reports whether err is a quota/rate-pressure error. Google
// reports these as 429 OR as 403 with a quota reason (the 2026-08-25 backfill
// hit "Error 403 ... quotaExceeded") — transient pressure, NOT a revoked
// grant, so it must never trip the permission-denied path.
func IsQuotaExceeded(err error) bool {
	var ge *googleapi.Error
	if !errors.As(err, &ge) {
		return false
	}
	if ge.Code == 429 {
		return true
	}
	if ge.Code != 403 {
		return false
	}
	for _, item := range ge.Errors {
		switch item.Reason {
		case "quotaExceeded", "rateLimitExceeded", "userRateLimitExceeded", "dailyLimitExceeded":
			return true
		}
	}
	// Reasons aren't always populated; the message is the fallback signal.
	return strings.Contains(strings.ToLower(ge.Message), "quota")
}

// IsPermissionDenied reports whether err is a 401/403-class Google API error —
// the "user removed our service account from the property" signal the ingest
// orchestrator turns into integrations.gsc.status = "error" without retrying.
// Quota 403s are excluded: they're transient and retryable.
func IsPermissionDenied(err error) bool {
	var ge *googleapi.Error
	return errors.As(err, &ge) && (ge.Code == 401 || ge.Code == 403) && !IsQuotaExceeded(err)
}
