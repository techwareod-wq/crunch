package service

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/net/html"

	"github.com/atharva-ng/crunch/internal/models"
	sr "github.com/atharva-ng/crunch/internal/services/styleReplicationService"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/utils"
)

// HandleScrape processes one URL of the scrape fan-out: rendered fetch
// (DataForSEO primary, direct fallback), body-text extraction when article
// learning is on, image extraction when image learning is on. Failures mark
// the URL failed and the run proceeds — partial results are by design. The
// worker whose atomic decrement hits 0 flips the run to awaiting_review (or
// errors it when every URL failed).
func (s *styleReplicationService) HandleScrape(ctx context.Context, userID string, payload sr.StyleScrapePayload) error {
	found, run, err := models.GetStyleReplicationRun(ctx, payload.RunID)
	if err != nil {
		return fmt.Errorf("style scrape: load run: %w", err)
	}
	if !found {
		return fmt.Errorf("style scrape: run not found: %s", payload.RunID)
	}
	// A canceled run's in-flight messages no-op.
	if run.Status != models.StyleRunStatusScraping {
		return nil
	}

	doc, _, fetchErr := utils.FetchRenderedWebsiteDoc(ctx, s.dfs, utils.DataForSEOFetchValues{
		TaskOKStatusCode: s.dfsValues.TaskOkStatusCode,
		FetchTimeout:     fetchTimeout(s.values.FetchTimeoutSeconds),
	}, payload.URL)

	if fetchErr != nil {
		log.Warn("style scrape: fetch failed", "url", payload.URL, "error", fetchErr, "runId", payload.RunID)
		if err := models.MarkStyleRunURLScraped(ctx, payload.RunID, payload.URL, true, nil, nil); err != nil {
			return fmt.Errorf("style scrape: mark failed: %w (original: %v)", err, fetchErr)
		}
	} else {
		var content *models.StyleScrapedContent
		if run.Learn.Articles() {
			body := utils.ExtractArticleBodyText(doc, s.values.MaxContentCharsPerURL)
			content = &models.StyleScrapedContent{
				URL:     payload.URL,
				Title:   pageTitle(doc),
				Content: body,
			}
		}

		var images []models.StyleScrapedImage
		if run.Learn.Images() {
			for _, img := range utils.ExtractContentImages(doc, payload.URL, s.values.MaxImagesPerURL) {
				// og:image is the page's hero/thumbnail representative;
				// everything else is in-article imagery — each bucket feeds
				// its own synthesis call. Only requested buckets are kept, so
				// a mid-article-only re-run never surfaces thumbnail refs.
				position := models.ImagePositionMidArticle
				if img.OG {
					position = models.ImagePositionThumbnail
				}
				if !run.Learn.WantsPosition(position) {
					continue
				}
				images = append(images, models.StyleScrapedImage{
					URL:           img.URL,
					Position:      position,
					SourceArticle: payload.URL,
					Selected:      true,
				})
			}
		}

		if err := models.MarkStyleRunURLScraped(ctx, payload.RunID, payload.URL, false, content, images); err != nil {
			return fmt.Errorf("style scrape: save result: %w", err)
		}
	}

	// Atomic fan-in — only the worker that hits 0 finalizes.
	remaining, err := models.DecrementStyleRunScrapesRemaining(ctx, payload.RunID)
	if err != nil {
		return fmt.Errorf("style scrape: decrement remaining: %w", err)
	}
	if remaining > 0 {
		return nil
	}

	found, run, err = models.GetStyleReplicationRun(ctx, payload.RunID)
	if err != nil {
		return fmt.Errorf("style scrape: reload run: %w", err)
	}
	if !found {
		return fmt.Errorf("style scrape: run vanished: %s", payload.RunID)
	}

	successes := 0
	for _, st := range run.ScrapeStatuses {
		if st.Done {
			successes++
		}
	}
	if successes == 0 {
		return models.SetStyleRunError(ctx, payload.RunID, "every source URL failed to scrape")
	}

	_, err = models.TryAdvanceStyleRunStatus(ctx, payload.RunID,
		[]int{models.StyleRunStatusScraping},
		models.StyleRunStatusAwaitingReview, nil)
	if err != nil {
		return fmt.Errorf("style scrape: advance to review: %w", err)
	}
	return nil
}

// pageTitle prefers the first <h1>'s text, falling back to <title> — the
// label the review card shows for a scraped article.
func pageTitle(doc *html.Node) string {
	if h1 := firstElementText(doc, "h1"); h1 != "" {
		return h1
	}
	return firstElementText(doc, "title")
}

func firstElementText(n *html.Node, element string) string {
	var walk func(*html.Node) (string, bool)
	walk = func(n *html.Node) (string, bool) {
		if n.Type == html.ElementNode && n.Data == element {
			return strings.TrimSpace(utils.ExtractText(n)), true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if v, ok := walk(c); ok {
				return v, true
			}
		}
		return "", false
	}
	text, _ := walk(n)
	return text
}
