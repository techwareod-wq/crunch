package utils

import (
	"encoding/json"
	"fmt"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/dto"
	commonutils "github.com/atharva-ng/crunch/internal/utils"
)

func GetHeadersForDataForSEO(basicAuthKey string) (map[string]string, error) {
	headers := make(map[string]string)
	headers["Authorization"] = "Basic " + basicAuthKey
	headers["Content-Type"] = "application/json"
	return headers, nil
}

type expandedKeywordsResponse struct {
	Seeds []string `json:"seeds"`
}

func ParseExpandedKeywordsLLMResponse(response string) ([]string, error) {
	var parsed expandedKeywordsResponse
	if err := json.Unmarshal([]byte(response), &parsed); err != nil {
		return nil, fmt.Errorf("failed to unmarshal expanded keywords response: %w", err)
	}

	keywords := make([]string, len(parsed.Seeds))
	for i, seed := range parsed.Seeds {
		keywords[i] = seed
	}
	return keywords, nil
}

// keywordDifficultyRange derives the [min, max] keyword-difficulty window.
// When the filter set pins a fixed band (DifficultyMaxAbsolute > 0 — the
// early_footholds strategy), that band applies regardless of domain rating.
// Otherwise it is udr-relative:
//
//	min = max(f.DifficultyFloor, udr - f.DifficultyBelowDR)
//	max = udr + f.DifficultyAboveDR
//
// The floor keeps the low end sane for zero/near-zero-authority domains; the
// wide upper bound still admits ambitious targets.
func keywordDifficultyRange(udr int, f config.KeywordFilterValues) (int, int) {
	if f.DifficultyMaxAbsolute > 0 {
		return f.DifficultyFloor, f.DifficultyMaxAbsolute
	}
	min := udr - f.DifficultyBelowDR
	if min < f.DifficultyFloor {
		min = f.DifficultyFloor
	}
	return min, udr + f.DifficultyAboveDR
}

// GetkeywordRequestObject builds a ranked_keywords task for a domain target. The
// filters narrow the domain's ranking keywords to organic top-MaxRankPosition
// results within the strategy's volume band, its difficulty band (udr-relative
// or fixed — see keywordDifficultyRange), non-navigational intent, and no
// brand/login noise, sorted best-ranked-first then by volume.
// brand (derived from the target host) drops the domain's own branded terms when
// present.
func GetkeywordRequestObject(count int, url string, locationCode int, udr int, f config.KeywordFilterValues) dto.GetKeywordDataTask {
	kdMin, kdMax := keywordDifficultyRange(udr, f)

	req := dto.GetKeywordDataTask{
		Target:           url,
		LanguageName:     "English",
		LocationCode:     locationCode,
		Limit:            count,
		LoadRankAbsolute: true,
		ItemTypes:        []string{"organic"},
	}

	filters := [][]interface{}{
		dto.NewFilter(dto.FilterRankGroup, dto.FilterOpLessOrEqual, f.MaxRankPosition),
		dto.NewFilter(dto.FilterRankedSearchVolume, dto.FilterOpGreaterOrEqual, f.MinSearchVolume),
	}
	if f.MaxSearchVolume > 0 {
		filters = append(filters, dto.NewFilter(dto.FilterRankedSearchVolume, dto.FilterOpLessOrEqual, f.MaxSearchVolume))
	}
	filters = append(filters,
		dto.NewFilter(dto.FilterRankedKeywordDifficulty, dto.FilterOpGreaterOrEqual, kdMin),
		dto.NewFilter(dto.FilterRankedKeywordDifficulty, dto.FilterOpLessOrEqual, kdMax),
		dto.NewFilter(dto.FilterSerpItemURL, dto.FilterOpNotLike, "%login%"),
		dto.NewFilter(dto.FilterRankedMainIntent, dto.FilterOpIn, []string{"informational", "commercial", "transactional"}),
	)
	if brand := commonutils.BrandFromURL(url); brand != "" {
		filters = append(filters, dto.NewFilter(dto.FilterRankedKeyword, dto.FilterOpNotLike, "%"+brand+"%"))
	}
	req.Filters = dto.AndFilters(filters...)

	req.OrderBy = []string{
		dto.NewOrderBy(dto.OrderByRankGroup, dto.OrderAsc),
		dto.NewOrderBy(dto.OrderBySearchVolume, dto.OrderDesc),
	}
	return req
}

// GetKeywordIdeasRequestObject builds a keyword_ideas task from seed keywords.
// closely_variants keeps the expansion tight; the filters apply the strategy's
// volume band and difficulty band (udr-relative or fixed — see
// keywordDifficultyRange), sorted by relevance then volume.
func GetKeywordIdeasRequestObject(count int, keywords []string, locationCode int, udr int, f config.KeywordFilterValues) dto.GetKeywordDataTask {
	kdMin, kdMax := keywordDifficultyRange(udr, f)

	req := dto.GetKeywordDataTask{
		Keywords:        keywords,
		LanguageName:    "English",
		LocationCode:    locationCode,
		Limit:           count,
		CloselyVariants: true,
	}

	filters := [][]interface{}{
		dto.NewFilter(dto.FilterSearchVolume, dto.FilterOpGreaterOrEqual, f.MinSearchVolume),
	}
	if f.MaxSearchVolume > 0 {
		filters = append(filters, dto.NewFilter(dto.FilterSearchVolume, dto.FilterOpLessOrEqual, f.MaxSearchVolume))
	}
	filters = append(filters,
		dto.NewFilter(dto.FilterKeywordDifficulty, dto.FilterOpGreaterOrEqual, kdMin),
		dto.NewFilter(dto.FilterKeywordDifficulty, dto.FilterOpLessOrEqual, kdMax),
	)
	req.Filters = dto.AndFilters(filters...)

	req.OrderBy = []string{
		dto.NewOrderBy(dto.OrderByRelevance, dto.OrderDesc),
		dto.NewOrderBy(dto.OrderByIdeasSearchVolume, dto.OrderDesc),
	}
	return req
}
