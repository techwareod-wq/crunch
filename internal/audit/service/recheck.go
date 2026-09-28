package service

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/atharva-ng/crunch/internal/audit"
	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/checks"
	"github.com/atharva-ng/crunch/internal/audit/collectors"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/audit/engine"
	"github.com/atharva-ng/crunch/internal/audit/targetcheck"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// recheckScopePageCap bounds how many affected pages a scoped re-collection
// fetches (spot check, never a re-crawl).
const recheckScopePageCap = 10

// recheckExcludedChecks are deterministic checks a scoped re-collection can't
// honestly reproduce — duplicate/programmatic need the full crawl corpus, SXO
// needs SERP context. They render "verified on your next audit" instead of a
// button. (LLM checks are excluded by kind: nondeterministic + gated budget.)
var recheckExcludedChecks = map[core.CheckID]bool{
	"onpage.sxo_mismatch":       true,
	"onpage.duplicate_content":  true,
	"onpage.programmatic_gates": true,
}

// buildRecheckableIDs computes the eligible set at boot from the registry:
// deterministic, not excluded, and no SERP dependency.
func buildRecheckableIDs(reg *checks.Registry) []string {
	var out []string
	for _, c := range reg.Deterministic() {
		if recheckExcludedChecks[c.ID()] {
			continue
		}
		serp := false
		for _, k := range c.Requires() {
			if k == artifacts.KindSERP {
				serp = true
				break
			}
		}
		if serp {
			continue
		}
		out = append(out, string(c.ID()))
	}
	sort.Strings(out)
	return out
}

// RecheckableCheckIDs lists the check ids the re-check button may target.
func (s *auditService) RecheckableCheckIDs() []string {
	return append([]string(nil), s.recheckable...)
}

// StartRecheck guards and dispatches one per-check re-verification. The
// result is a verification OVERLAY: the report and its score never change —
// the full score refresh is the next weekly audit (score-honesty contract).
func (s *auditService) StartRecheck(ctx context.Context, entity *models.WebEntity, runID, checkID string) (*models.AuditRecheck, error) {
	found, run, err := models.FindAuditRunByID(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("audit recheck: load run: %w", err)
	}
	if !found || run.WebEntityID == nil || *run.WebEntityID != entity.ID {
		return nil, audit.ErrAuditReportNotFound
	}
	if run.Status != models.AuditStatusComplete {
		return nil, audit.ErrAuditRecheckStale
	}
	latestFound, latest, err := models.FindLatestCompletedAuditRunForEntity(ctx, entity.ID)
	if err != nil {
		return nil, fmt.Errorf("audit recheck: latest run: %w", err)
	}
	if !latestFound || latest.ID != run.ID {
		return nil, audit.ErrAuditRecheckStale
	}

	if !s.recheckableSet[checkID] {
		return nil, audit.ErrAuditRecheckIneligible
	}
	outcome := findCheckOutcome(run, checkID)
	if outcome == nil || outcome.Skipped {
		// Never ran (or its data was missing) — there's nothing to verify
		// against; the next audit measures it.
		return nil, audit.ErrAuditRecheckIneligible
	}

	if lastFound, last, err := models.FindLatestAuditRecheckForCheck(ctx, run.ID, checkID); err != nil {
		return nil, fmt.Errorf("audit recheck: last recheck: %w", err)
	} else if lastFound {
		if last.Status < models.AuditRecheckStatusComplete {
			return last, nil // already verifying — idempotent re-entry
		}
		cooldown := time.Duration(s.values.Recheck.PerCheckCooldownMinutes) * time.Minute
		if cooldown > 0 && last.Status == models.AuditRecheckStatusComplete &&
			last.CompletedAt != nil && time.Since(*last.CompletedAt) < cooldown {
			return nil, audit.ErrAuditRecheckCooldown
		}
	}
	if dayCap := s.values.Recheck.MaxPerRunPerDay; dayCap > 0 {
		n, err := models.CountAuditRechecksForRunSince(ctx, run.ID, time.Now().Add(-24*time.Hour))
		if err != nil {
			return nil, fmt.Errorf("audit recheck: daily count: %w", err)
		}
		if n >= int64(dayCap) {
			return nil, audit.ErrAuditRecheckLimitReached
		}
	}

	rc := &models.AuditRecheck{
		RunID:       run.ID,
		WebEntityID: entity.ID,
		CheckID:     checkID,
		Status:      models.AuditRecheckStatusCreated,
		Before:      recheckSideFromOutcome(outcome),
	}
	if err := models.CreateAuditRecheck(ctx, rc); err != nil {
		return nil, err
	}

	if err := s.dispatcher.Dispatch(ctx, string(audit.ProcessAuditRecheck), dispatchUserID(run),
		audit.AuditRecheckPayload{RecheckID: rc.ID.Hex()}); err != nil {
		if markErr := models.SetAuditRecheckError(ctx, rc.ID.Hex(), "dispatch failed"); markErr != nil {
			log.Error("audit recheck: mark dispatch failure", "error", markErr, "recheckId", rc.ID.Hex())
		}
		return nil, fmt.Errorf("audit recheck: dispatch: %w", err)
	}
	return rc, nil
}

// HandleRecheck is the AUDIT_RECHECK stage: scoped re-collection of the
// check's required kinds into an IN-MEMORY bundle (the blackboard is never
// touched — the stored run must stay byte-identical), one check re-run, and
// the pure verdict. A data source that can't be re-collected completes as
// `inconclusive` — the honesty rule, never a guess.
func (s *auditService) HandleRecheck(ctx context.Context, userID string, p audit.AuditRecheckPayload) error {
	found, rc, err := models.FindAuditRecheckByID(ctx, p.RecheckID)
	if err != nil {
		return fmt.Errorf("audit recheck: load: %w", err)
	}
	if !found {
		return fmt.Errorf("audit recheck: not found %s: %w", p.RecheckID, pipeline.ErrPermanent)
	}
	if rc.Status >= models.AuditRecheckStatusComplete {
		return nil // redelivery after the terminal write
	}
	runFound, run, err := models.FindAuditRunByID(ctx, rc.RunID.Hex())
	if err != nil {
		return fmt.Errorf("audit recheck: load run: %w", err)
	}
	if !runFound {
		return fmt.Errorf("audit recheck: run %s gone: %w", rc.RunID.Hex(), pipeline.ErrPermanent)
	}

	check, ok := s.registry.ByID(core.CheckID(rc.CheckID))
	if !ok {
		return fmt.Errorf("audit recheck: check %s not registered: %w", rc.CheckID, pipeline.ErrPermanent)
	}

	if err := models.MarkAuditRecheckRunning(ctx, p.RecheckID); err != nil {
		return fmt.Errorf("audit recheck: mark running: %w", err)
	}

	// SSRF posture — validate close to use: the scoped collection fetches
	// the target host directly from this process.
	if _, _, err := targetcheck.Validate(ctx, run.TargetURL); err != nil {
		return s.completeRecheckInconclusive(ctx, p.RecheckID, "the target failed safety re-validation")
	}

	scoped := recheckScopedPages(run, rc)
	bundle := artifacts.NewBundle()
	spot := false
	for _, kind := range check.Requires() {
		switch kind {
		case artifacts.KindPSI:
			art, err := collectors.BuildPSIArtifact(ctx, s.deps(), run)
			if err != nil {
				log.Warn("audit recheck: psi re-collect failed", "recheckId", p.RecheckID, "error", err)
				return s.completeRecheckInconclusive(ctx, p.RecheckID, "PageSpeed data was unavailable")
			}
			bundle.Put(kind, art)

		case artifacts.KindAuthority:
			art, err := collectors.BuildAuthorityArtifact(ctx, s.deps(), run)
			if err != nil {
				log.Warn("audit recheck: authority re-collect failed", "recheckId", p.RecheckID, "error", err)
				return s.completeRecheckInconclusive(ctx, p.RecheckID, "authority data was unavailable")
			}
			bundle.Put(kind, art)

		case artifacts.KindHTMLDeep:
			deepPages := scoped
			if len(deepPages) == 0 {
				deepPages = s.recheckSamplePages(ctx, run)
			}
			art := collectors.BuildHTMLDeepScoped(ctx, s.deps(), run, deepPages)
			if len(deepPages) > 0 && noDeepPageFetched(art) {
				return s.completeRecheckInconclusive(ctx, p.RecheckID, "the affected pages could not be fetched")
			}
			spot = spot || len(deepPages) > 0
			bundle.Put(kind, art)

		case artifacts.KindCrawl:
			art, err := collectors.BuildCrawlSpotCheck(ctx, run, scoped)
			if err != nil {
				log.Warn("audit recheck: spot check failed", "recheckId", p.RecheckID, "error", err)
				return s.completeRecheckInconclusive(ctx, p.RecheckID, "the affected pages could not be fetched")
			}
			spot = true
			bundle.Put(kind, art)

		case artifacts.KindMentions:
			// Re-querying mentions is cheap (2 SERP calls) and explicitly
			// allowed in scoped mode.
			art, err := collectors.BuildMentionsArtifact(ctx, s.deps(), run)
			if err != nil {
				log.Warn("audit recheck: mentions re-collect failed", "recheckId", p.RecheckID, "error", err)
				return s.completeRecheckInconclusive(ctx, p.RecheckID, "brand-mention data was unavailable")
			}
			bundle.Put(kind, art)

		default:
			return s.completeRecheckInconclusive(ctx, p.RecheckID, "this check's data source can't be re-collected on its own")
		}
	}

	result, err := check.Run(ctx, checks.Input{Bundle: bundle, Run: run, Values: s.values})
	if err != nil {
		log.Warn("audit recheck: check re-run failed", "recheckId", p.RecheckID, "check", rc.CheckID, "error", err)
		return s.completeRecheckInconclusive(ctx, p.RecheckID, "the check could not be evaluated on the fresh data")
	}

	after := &models.AuditRecheckSide{
		Findings:  result.Findings,
		HasScore:  result.Score != nil,
		Pages:     actionablePageCount(result.Findings),
		SpotCheck: spot,
	}
	if result.Score != nil {
		after.Earned = result.Score.Earned
		after.Possible = result.Score.Possible
	}
	verdict := engine.RecheckVerdict(rc.Before, after)
	note := ""
	// A scoped spot-check re-fetches only the finding's LISTED pages (capped
	// at 10 by the emitting check). When the original finding counted more
	// pages than it listed, a clean re-run proves the sample, not the claim —
	// "Fixed" must not overclaim what was verified.
	if spot && verdict == models.AuditRecheckVerdictFixed && beforeFindingsWereSampled(rc.Before) {
		verdict = models.AuditRecheckVerdictImproved
		note = "The re-checked sample pages are clean, but the original finding covered more pages than were re-fetched — verified on the listed pages only."
	}
	if err := models.CompleteAuditRecheck(ctx, p.RecheckID, verdict, note, after); err != nil {
		return fmt.Errorf("audit recheck: complete: %w", err)
	}
	return nil
}

// beforeFindingsWereSampled reports whether any before-side finding's title
// count exceeds its listed pages — the capped-page-list signature.
func beforeFindingsWereSampled(before *models.AuditRecheckSide) bool {
	if before == nil {
		return false
	}
	for _, f := range before.Findings {
		if m := leadingCountRe.FindString(f.Title); m != "" {
			if n, err := strconv.Atoi(m); err == nil && n > len(f.Pages) && len(f.Pages) > 0 {
				return true
			}
		}
	}
	return false
}

var leadingCountRe = regexp.MustCompile(`\d+`)

// completeRecheckInconclusive is an HONEST terminal state, not an error: the
// source was unavailable, so we say so instead of guessing.
func (s *auditService) completeRecheckInconclusive(ctx context.Context, recheckID, note string) error {
	if err := models.CompleteAuditRecheck(ctx, recheckID, models.AuditRecheckVerdictInconclusive, note, nil); err != nil {
		return fmt.Errorf("audit recheck: complete inconclusive: %w", err)
	}
	return nil
}

// recheckScopedPages resolves the pages a scoped re-collection may fetch: the
// original finding's affected pages, restricted to the audited domain (SSRF
// posture — never fetch an off-target URL), deduped, capped.
func recheckScopedPages(run *models.AuditRun, rc *models.AuditRecheck) []string {
	if rc.Before == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range rc.Before.Findings {
		for _, p := range f.Pages {
			if seen[p] || !pageOnDomain(p, run.TargetDomain) {
				continue
			}
			seen[p] = true
			out = append(out, p)
			if len(out) >= recheckScopePageCap {
				return out
			}
		}
	}
	return out
}

// pageOnDomain reports whether the URL's host is the audited eTLD+1 or a
// subdomain of it.
func pageOnDomain(raw, domain string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	domain = strings.ToLower(domain)
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// recheckSamplePages falls back to the run's stored deep-pass sample for
// findings that name no pages (site-level checks still get their probes; the
// sample keeps page-based checks honest). Empty when the crawl artifact
// TTL-expired.
func (s *auditService) recheckSamplePages(ctx context.Context, run *models.AuditRun) []string {
	raws, err := models.LoadAuditArtifacts(ctx, run.ID)
	if err != nil {
		return nil
	}
	bundle, err := artifacts.BuildBundle(raws)
	if err != nil {
		return nil
	}
	crawl, ok := bundle.Crawl()
	if !ok {
		return nil
	}
	sample := crawl.DeepPassSample
	if len(sample) > recheckScopePageCap {
		sample = sample[:recheckScopePageCap]
	}
	return sample
}

func noDeepPageFetched(art *artifacts.HTMLDeepArtifact) bool {
	for _, p := range art.Pages {
		if p.Fetched {
			return false
		}
	}
	return true
}

// findCheckOutcome pulls one check's persisted outcome off the run.
func findCheckOutcome(run *models.AuditRun, checkID string) *models.AuditCheckOutcome {
	for i := range run.CheckOutcomes {
		if run.CheckOutcomes[i].CheckID == checkID {
			return &run.CheckOutcomes[i]
		}
	}
	return nil
}

func recheckSideFromOutcome(o *models.AuditCheckOutcome) *models.AuditRecheckSide {
	return &models.AuditRecheckSide{
		Findings: o.Findings,
		HasScore: o.HasScore,
		Earned:   o.Earned,
		Possible: o.Possible,
		Pages:    actionablePageCount(o.Findings),
	}
}

// actionablePageCount counts distinct pages across NON-info findings — info
// findings are transparency notes and must not keep a clean re-run from
// reading "fixed".
func actionablePageCount(findings []core.Finding) int {
	seen := map[string]bool{}
	for _, f := range findings {
		if f.Severity == core.SeverityInfo {
			continue
		}
		for _, p := range f.Pages {
			seen[p] = true
		}
	}
	return len(seen)
}
