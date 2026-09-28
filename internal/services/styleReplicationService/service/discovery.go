package service

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/analytics"
	"github.com/atharva-ng/crunch/internal/analytics/queries"
	"github.com/atharva-ng/crunch/internal/models"
	sr "github.com/atharva-ng/crunch/internal/services/styleReplicationService"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/utils"
)

// gscDiscoveryWindowDays is how far back the GSC top-performers read looks.
const gscDiscoveryWindowDays = 90

// discoverySitemapCap bounds the sitemap entry pool discovery works from —
// both the sitemap candidate source and the GSC intersection set.
const discoverySitemapCap = 200

// HandleDiscovery builds the candidate URL list for a run: GSC top performers
// when the integration is connected (Indexly-written articles excluded —
// locked decision 6), the sitemap's newest article-like URLs otherwise.
// Either path may legitimately yield zero candidates — the wizard then shows
// the manual-entry state, so an empty result is success, not an error.
func (s *styleReplicationService) HandleDiscovery(ctx context.Context, userID string, payload sr.StyleRunPayload) error {
	found, run, err := models.GetStyleReplicationRun(ctx, payload.RunID)
	if err != nil {
		return fmt.Errorf("style discovery: load run: %w", err)
	}
	if !found {
		return fmt.Errorf("style discovery: run not found: %s", payload.RunID)
	}
	// Only a fresh (or retried) run proceeds; a canceled/completed run's
	// redelivery no-ops.
	if run.Status != models.StyleRunStatusCreated && run.Status != models.StyleRunStatusDiscovering {
		return nil
	}
	if err := models.SetStyleRunStatus(ctx, payload.RunID, models.StyleRunStatusDiscovering); err != nil {
		return fmt.Errorf("style discovery: mark discovering: %w", err)
	}

	foundEntity, entity, err := models.FindWebEntityByID(ctx, run.WebEntityID.Hex())
	if err != nil {
		return fmt.Errorf("style discovery: load web entity: %w", err)
	}
	if !foundEntity {
		return fmt.Errorf("style discovery: web entity not found: %s", run.WebEntityID.Hex())
	}

	sitemapEntries := utils.FetchSitemapEntries(ctx, entity.WebsiteUrl, utils.SitemapFetchOptions{
		FetchTimeout: time.Duration(s.values.FetchTimeoutSeconds) * time.Second,
		MaxBytes:     5 << 20,
		MaxURLs:      discoverySitemapCap,
	})

	var candidates []models.StyleCandidateURL
	if state, ok := entity.Integration("gsc"); ok && state.Status == models.IntegrationStatusConnected {
		candidates, err = s.discoverFromGSC(ctx, entity, sitemapEntries)
		if err != nil {
			// GSC being down shouldn't kill discovery — the sitemap path below
			// still serves.
			log.Warn("style discovery: gsc path failed, falling back to sitemap", "error", err, "runId", payload.RunID)
			candidates = nil
		}
	}
	if len(candidates) == 0 {
		candidates = s.discoverFromSitemap(sitemapEntries)
	}

	return models.UpdateStyleReplicationRun(ctx, payload.RunID, map[string]interface{}{
		"status":     models.StyleRunStatusAwaitingURLs,
		"candidates": candidates,
	})
}

// discoverFromGSC returns the entity's top-clicked article-like pages over the
// last 90 settled days, minus Indexly-generated articles (IsArticle).
func (s *styleReplicationService) discoverFromGSC(ctx context.Context, entity *models.WebEntity, sitemapEntries []utils.SitemapEntry) ([]models.StyleCandidateURL, error) {
	src, ok := analytics.Get("gsc")
	if !ok {
		return nil, fmt.Errorf("gsc source not registered")
	}
	settled := analytics.LatestSettledDate(src)
	end, err := time.Parse(analytics.DateLayout, settled)
	if err != nil {
		return nil, fmt.Errorf("parse settled date %q: %w", settled, err)
	}
	from := end.AddDate(0, 0, -(gscDiscoveryWindowDays - 1)).Format(analytics.DateLayout)

	rows, err := queries.PagesTable(ctx, entity.ID, from, settled, settled)
	if err != nil {
		return nil, fmt.Errorf("pages table: %w", err)
	}

	sitemapSet := map[string]bool{}
	for _, e := range sitemapEntries {
		if n := utils.NormalizeGSCPageURL(e.URL); n != "" {
			sitemapSet[n] = true
		}
	}

	var out []models.StyleCandidateURL
	for _, row := range rows {
		if row.IsArticle {
			continue // Indexly-written — learning from our own output is circular
		}
		if !isArticleLikeURL(row.Page, sitemapSet) {
			continue
		}
		out = append(out, models.StyleCandidateURL{URL: row.Page, Source: models.StyleCandidateSourceGSC})
		if len(out) >= s.values.MaxUrls {
			break
		}
	}
	return out, nil
}

// discoverFromSitemap returns the newest article-like sitemap URLs by
// <lastmod> desc. An entirely undated sitemap yields nothing — recency is the
// whole signal, and guessing would surface pricing pages over posts.
func (s *styleReplicationService) discoverFromSitemap(entries []utils.SitemapEntry) []models.StyleCandidateURL {
	var dated []utils.SitemapEntry
	for _, e := range entries {
		if e.LastMod.IsZero() {
			continue
		}
		if !isArticleLikeURL(e.URL, nil) {
			continue
		}
		dated = append(dated, e)
	}
	sort.SliceStable(dated, func(i, j int) bool { return dated[i].LastMod.After(dated[j].LastMod) })

	var out []models.StyleCandidateURL
	for _, e := range dated {
		out = append(out, models.StyleCandidateURL{URL: e.URL, Source: models.StyleCandidateSourceSitemap})
		if len(out) >= s.values.MaxUrls {
			break
		}
	}
	return out
}

// articlePathMarkers are path segments that positively mark a blog article.
var articlePathMarkers = []string{"/blog/", "/post/", "/posts/", "/article/", "/articles/", "/news/", "/guides/", "/resources/"}

// excludedPathMarkers are site-chrome pages that are never articles, whatever
// the sitemap says.
var excludedPathMarkers = []string{
	"/pricing", "/about", "/contact", "/privacy", "/terms", "/legal",
	"/login", "/signin", "/sign-in", "/signup", "/sign-up", "/careers",
	"/category/", "/tag/", "/author/", "/page/",
}

// datePathRe matches date segments like /2026/08/ or /2026-08-12- in a path.
var datePathRe = regexp.MustCompile(`/(19|20)\d{2}[/-](0[1-9]|1[0-2])[/-]`)

// isArticleLikeURL applies the article heuristics: never an excluded chrome
// page; then either present in the sitemap set (when one is available) or
// matching path patterns — a marker segment, a date segment, or path depth
// ≥ 2. The root page always fails (depth 0).
func isArticleLikeURL(raw string, sitemapSet map[string]bool) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	path := strings.ToLower(u.Path)
	if path == "" || path == "/" {
		return false
	}
	for _, marker := range excludedPathMarkers {
		if strings.Contains(path, marker) {
			return false
		}
	}
	if len(sitemapSet) > 0 {
		if n := utils.NormalizeGSCPageURL(raw); n != "" && sitemapSet[n] {
			return true
		}
	}
	for _, marker := range articlePathMarkers {
		if strings.Contains(path, marker) {
			return true
		}
	}
	if datePathRe.MatchString(path) {
		return true
	}
	segments := 0
	for _, seg := range strings.Split(path, "/") {
		if seg != "" {
			segments++
		}
	}
	return segments >= 2
}

func parseObjectID(id string) (primitive.ObjectID, error) {
	return primitive.ObjectIDFromHex(id)
}
