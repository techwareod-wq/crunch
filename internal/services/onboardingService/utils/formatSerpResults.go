package utils

import (
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/dto"
)

// FormatSerpResultsForLLM flattens organic SERP items from one or more tasks
// into the line-per-result block the competitor-discovery prompt consumes.
// Results are deduped by domain (case-insensitive, first occurrence wins) so the
// multi-query competitor search merges into a single clean candidate pool
// instead of repeating a domain that ranks for several of the queries.
func FormatSerpResultsForLLM(serpResp *dto.SerpResponse) string {
	var results []string
	seen := make(map[string]struct{})

	for _, task := range serpResp.Tasks {
		for _, result := range task.Result {
			for _, item := range result.Items {
				if item.Type != "organic" {
					continue
				}
				domainKey := strings.ToLower(strings.TrimSpace(item.Domain))
				if domainKey != "" {
					if _, dup := seen[domainKey]; dup {
						continue
					}
					seen[domainKey] = struct{}{}
				}
				results = append(results, fmt.Sprintf(
					"- Domain: %s | Title: %s | Description: %s",
					item.Domain, item.Title, item.Description,
				))
			}
		}
	}

	return strings.Join(results, "\n")
}
