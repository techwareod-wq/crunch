package collectors

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/utils"
)

// Spot-check bounds: cheap, honest, never a re-crawl.
const (
	spotCheckTimeout = 20 * time.Second
	spotCheckBodyCap = 1 << 20 // 1 MiB per page
)

// BuildCrawlSpotCheck fetches the given URLs LIVE (status + head parse) and
// builds a minimal CrawlArtifact covering only those pages — the re-check's
// spot-check mode for crawl-derived checks. The verdict computed over it is a
// verdict over these pages only; the caller flags the result spotCheck so the
// FE copy reads "spot-checked N affected pages". Never touches the
// blackboard, never re-posts an OnPage task.
func BuildCrawlSpotCheck(ctx context.Context, run *models.AuditRun, pages []string) (*artifacts.CrawlArtifact, error) {
	if len(pages) == 0 {
		pages = []string{run.TargetURL}
	}

	// First-hop client (no redirect follow — redirects_status wants the raw
	// status) + a following client for the final document.
	firstHop := &http.Client{
		Timeout:       spotCheckTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	follower := &http.Client{Timeout: spotCheckTimeout}

	artifact := &artifacts.CrawlArtifact{
		StartURL:     run.TargetURL,
		PagesCrawled: len(pages),
		PageCap:      run.PageCap,
	}

	httpsOK := false
	for _, pageURL := range pages {
		page := spotFetchPage(ctx, firstHop, follower, pageURL)
		if page.IsHTTPS && page.StatusCode > 0 && !page.IsBroken {
			httpsOK = true
		}
		if page.IsBroken {
			artifact.BrokenLinks = append(artifact.BrokenLinks, artifacts.BrokenLink{ToURL: page.URL})
		}
		artifact.InternalLinksTotal += page.InternalLinksCount
		artifact.ExternalLinksTotal += page.ExternalLinksCount
		artifact.Pages = append(artifact.Pages, page)
	}
	artifact.ValidCertificate = httpsOK

	// Site-level ride-alongs the technical checks read.
	probeClient := &http.Client{Timeout: siteProbeTimeout}
	if body, err := utils.FetchURLBody(ctx, probeClient, "https://"+run.TargetDomain+"/robots.txt", 512<<10); err == nil {
		artifact.RobotsTxtFound = true
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "sitemap:") {
				artifact.SitemapInRobots = true
				break
			}
		}
	}

	if len(artifact.Pages) == 0 {
		return nil, fmt.Errorf("spot check: no pages could be examined")
	}
	return artifact, nil
}

// spotFetchPage fetches one URL and extracts the head/body fields the
// crawl-derived checks consume. Fields a single live fetch can't honestly
// derive (canonical chains, duplicate flags, inbound links) stay zero.
func spotFetchPage(ctx context.Context, firstHop, follower *http.Client, pageURL string) artifacts.CrawlPage {
	page := artifacts.CrawlPage{
		URL:     pageURL,
		IsHTTPS: strings.HasPrefix(strings.ToLower(pageURL), "https://"),
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		page.IsBroken = true
		return page
	}
	resp, err := firstHop.Do(req)
	if err != nil {
		page.IsBroken = true
		return page
	}
	page.StatusCode = resp.StatusCode
	page.IsRedirect = resp.StatusCode >= 300 && resp.StatusCode < 400
	page.IsBroken = resp.StatusCode >= 400

	// Resolve the document to parse: the page itself, or the redirect target
	// (followed) so title/meta/canonical reflect what a visitor lands on.
	docBody := resp.Body
	if page.IsBroken {
		resp.Body.Close()
		return page
	}
	if page.IsRedirect {
		resp.Body.Close()
		followReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return page
		}
		docResp, err := follower.Do(followReq)
		if err != nil {
			return page
		}
		docBody = docResp.Body
	}
	defer docBody.Close()

	doc, err := html.Parse(io.LimitReader(docBody, spotCheckBodyCap))
	if err != nil {
		return page
	}
	extractSpotPageFields(doc, &page)
	return page
}

// extractSpotPageFields walks the parsed document once, filling the head +
// structure fields.
func extractSpotPageFields(doc *html.Node, page *artifacts.CrawlPage) {
	pageHost := ""
	if u, err := url.Parse(page.URL); err == nil {
		pageHost = strings.ToLower(u.Hostname())
	}
	words := 0

	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "title":
				if page.Title == "" {
					page.Title = strings.TrimSpace(nodeText(n))
				}
			case "meta":
				name := strings.ToLower(attrValue(n, "name"))
				content := attrValue(n, "content")
				if name == "description" && page.MetaDescription == "" {
					page.MetaDescription = strings.TrimSpace(content)
				}
				if (name == "robots" || name == "googlebot") &&
					strings.Contains(strings.ToLower(content), "noindex") {
					page.NoIndex = true
				}
			case "link":
				if strings.EqualFold(attrValue(n, "rel"), "canonical") && page.Canonical == "" {
					page.Canonical = strings.TrimSpace(attrValue(n, "href"))
				}
			case "h1":
				page.H1Count++
			case "img":
				page.ImagesCount++
			case "a":
				href := attrValue(n, "href")
				if href == "" || strings.HasPrefix(href, "#") {
					break
				}
				if u, err := url.Parse(href); err == nil {
					host := strings.ToLower(u.Hostname())
					if host == "" || host == pageHost {
						page.InternalLinksCount++
					} else {
						page.ExternalLinksCount++
					}
				}
			case "script", "style", "noscript":
				return // skip non-content text
			}
		}
		if n.Type == html.TextNode {
			words += len(strings.Fields(n.Data))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	page.WordCount = words
}
