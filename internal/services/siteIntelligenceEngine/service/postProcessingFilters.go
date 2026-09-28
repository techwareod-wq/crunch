package service

import (
	"sort"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
)

type keywordProcessingSteps func([]models.Keyword) []models.Keyword

func FilterByMinVolume(minVolume int) keywordProcessingSteps {
	return func(keywords []models.Keyword) []models.Keyword {
		var filtered []models.Keyword
		for _, kw := range keywords {
			if kw.Volume > minVolume {
				filtered = append(filtered, kw)
			}
		}
		return filtered
	}
}

func FilterByMinCPC(minCpc float64) keywordProcessingSteps {
	return func(keywords []models.Keyword) []models.Keyword {
		var filtered []models.Keyword
		for _, kw := range keywords {
			if kw.CPC > minCpc {
				filtered = append(filtered, kw)
			}
		}
		return filtered
	}
}
func FilterByRankingPosition(minRP int) keywordProcessingSteps {
	return func(keywords []models.Keyword) []models.Keyword {
		var filtered []models.Keyword
		for _, kw := range keywords {
			if kw.RankingPosition > minRP {
				filtered = append(filtered, kw)
			}
		}
		return filtered
	}
}

func FilterByKeywordDifficulty(minKD int) keywordProcessingSteps {
	return func(keywords []models.Keyword) []models.Keyword {
		var filtered []models.Keyword
		for _, kw := range keywords {
			if kw.KeywordDifficulty >= minKD {
				filtered = append(filtered, kw)
			}
		}
		return filtered
	}
}

// Deduplicate collapses keywords that share a canonical key (stemmed, sorted
// tokens), so morphological and word-order variants merge alongside exact
// case/whitespace dupes. The variant with the highest provisional opportunity
// score wins its group (funnel is unclassified at this stage, so the intent
// term is the default weight — variants of the same phrase would land in the
// same funnel anyway); volume breaks score ties. Output preserves first-seen
// order.
func Deduplicate(sc config.ScoringValues, userDR int) keywordProcessingSteps {
	return func(keywords []models.Keyword) []models.Keyword {
		best := make(map[string]int)
		result := make([]models.Keyword, 0, len(keywords))
		scores := make([]float64, 0, len(keywords))
		for _, kw := range keywords {
			key := canonicalKeywordKey(kw.Keyword)
			if key == "" {
				continue
			}
			score := computeOpportunityScore(sc, kw.Volume, kw.KeywordDifficulty, kw.Funnel, userDR)
			if at, ok := best[key]; ok {
				if score > scores[at] || (score == scores[at] && kw.Volume > result[at].Volume) {
					result[at] = kw
					scores[at] = score
				}
				continue
			}
			best[key] = len(result)
			result = append(result, kw)
			scores = append(scores, score)
		}
		return result
	}
}

// CountRemoved wraps a processing step and accumulates how many keywords the
// step dropped into *removed. Used to report dedupe effectiveness at the end
// of post-processing.
func CountRemoved(step keywordProcessingSteps, removed *int) keywordProcessingSteps {
	return func(keywords []models.Keyword) []models.Keyword {
		out := step(keywords)
		*removed += len(keywords) - len(out)
		return out
	}
}

func FilterNull(keywords []models.Keyword) []models.Keyword {
	var filtered []models.Keyword
	for _, kw := range keywords {
		if kw.Volume >= 0 && kw.KeywordDifficulty >= 0 && kw.RankingPosition >= 0 && kw.Keyword != "" {
			filtered = append(filtered, kw)
		}
	}
	return filtered
}

func Sequentialise(keywords []models.Keyword) []models.Keyword {
	maxID := 0
	for _, kw := range keywords {
		if kw.SequenceID > maxID {
			maxID = kw.SequenceID
		}
	}

	nextID := maxID + 1
	for i := range keywords {
		if keywords[i].SequenceID == 0 {
			keywords[i].SequenceID = nextID
			nextID++
		}
	}
	return keywords
}

// FlagRefreshCandidates marks keywords already ranking inside the "striking
// distance" band [minPos, maxPos] — cheap wins to re-optimise rather than
// net-new articles to write. The band comes from
// values.siteIntelligence.refreshCandidate.
func FlagRefreshCandidates(minPos, maxPos int) keywordProcessingSteps {
	return func(keywords []models.Keyword) []models.Keyword {
		for i := range keywords {
			if keywords[i].RankingPosition >= minPos && keywords[i].RankingPosition <= maxPos {
				keywords[i].Status = "refresh_candidate"
			}
		}
		return keywords
	}
}

// TruncateTopKeywords caps the persisted keyword set for trial-mode runs:
// sort volume desc (CPC desc as tiebreak) and keep the top n. Runs after the
// filter chain and before Sequentialise so trial users get the strongest n
// keywords, not an arbitrary prefix. n <= 0 is a no-op (full mode).
func TruncateTopKeywords(n int) keywordProcessingSteps {
	return func(keywords []models.Keyword) []models.Keyword {
		if n <= 0 || len(keywords) <= n {
			return keywords
		}
		sort.SliceStable(keywords, func(i, j int) bool {
			if keywords[i].Volume != keywords[j].Volume {
				return keywords[i].Volume > keywords[j].Volume
			}
			return keywords[i].CPC > keywords[j].CPC
		})
		return keywords[:n]
	}
}

func filterNavigationalIntent(keywords []models.Keyword) []models.Keyword {
	var filtered []models.Keyword
	for _, kw := range keywords {
		if kw.Intent != "navigational" {
			filtered = append(filtered, kw)
		}
	}
	return filtered
}
