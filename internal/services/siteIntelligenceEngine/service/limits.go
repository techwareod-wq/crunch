package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// sieLimits is the per-run view of the pipeline's size knobs, resolved once
// from the WEC's sie_mode so individual stages don't sprinkle mode checks.
// Zero MaxPersistedKeywords / ClusterCount mean "no cap" / "prompt default
// (6–10 range)" — the full-mode behavior.
type sieLimits struct {
	UserKeywordsLimit       int
	CompetitorKeywordsLimit int
	ExpandedKeywordsLimit   int
	MaxPersistedKeywords    int
	ClusterCount            int
}

// limitsFor resolves the effective pipeline limits for a WEC: trial-mode WECs
// get values.siteIntelligence.trial, everything else the full-mode values.
func (s *seoBlogGeneratorSiteIntelligence) limitsFor(wec *models.WebEntityContext) sieLimits {
	if wec.EffectiveSIEMode() == models.SIEModeTrial {
		t := s.values.Trial
		return sieLimits{
			UserKeywordsLimit:       t.UserKeywordsLimit,
			CompetitorKeywordsLimit: t.CompetitorKeywordsLimit,
			ExpandedKeywordsLimit:   t.ExpandedKeywordsLimit,
			MaxPersistedKeywords:    t.MaxPersistedKeywords,
			ClusterCount:            t.ClusterCount,
		}
	}
	return fullLimits(s.values)
}

// fullLimits is the full-mode view of the top-level siteIntelligence values —
// also what the upgrade expand pipeline re-fetches at.
func fullLimits(v config.SiteIntelligenceValues) sieLimits {
	return sieLimits{
		UserKeywordsLimit:       v.UserKeywordsLimit,
		CompetitorKeywordsLimit: v.CompetitorKeywordsLimit,
		ExpandedKeywordsLimit:   v.ExpandedKeywordsLimit,
	}
}

// limitsForWECID loads the WEC and resolves its limits — for stages that
// don't otherwise need the WEC document.
func (s *seoBlogGeneratorSiteIntelligence) limitsForWECID(ctx context.Context, webEntityContextID string) (sieLimits, error) {
	ok, wec, err := models.GetWebEntityContext(ctx, webEntityContextID)
	if err != nil {
		return sieLimits{}, err
	}
	if !ok {
		return sieLimits{}, fmt.Errorf("web entity context not found: %s", webEntityContextID)
	}
	return s.limitsFor(wec), nil
}

// resolveSIEMode derives the mode stamped onto a new WEC from the user's
// indexly entitlement status at creation time: trialing → trial, anything
// else → full. Lookup failures default to full — mis-sizing a trial run up is
// recoverable (the expand pipeline handles it); blocking orchestration is not.
func resolveSIEMode(ctx context.Context, userID string) string {
	found, user, err := models.FindUserByID(ctx, userID)
	if err != nil || !found {
		log.Error("SIE: could not resolve user for sie_mode stamp — defaulting to full",
			"error", err, "userId", userID)
		return models.SIEModeFull
	}
	if ent, ok := user.Entitlements[models.AppIDIndexly]; ok && ent.Status == models.SubStatusTrialing {
		return models.SIEModeTrial
	}
	return models.SIEModeFull
}
