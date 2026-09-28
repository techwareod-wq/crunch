package service

import (
	"testing"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
)

// prodScoring mirrors values/production/values.yaml siteIntelligence.scoring so
// the goldens below track the shipped formula: DR-adjusted difficulty
// (adjustedKD = kd − (userDR − 15) × 0.5) and the concave final curve
// (final = 100 × (raw/100)^0.7), rounded to one decimal.
var prodScoring = config.ScoringValues{
	VolumeLogAnchor:      100000.0,
	VolumeScoreCap:       1.0,
	VolumeScoreFloor:     1.0,
	IntentScoreBOFU:      1.00,
	IntentScoreMOFU:      0.85,
	IntentScoreTOFU:      0.70,
	IntentScoreDefault:   0.75,
	DifficultyScoreNull:  0.5,
	DifficultyScoreFloor: 0.0,
	DifficultyScoreCap:   1.0,
	DifficultyKDScale:    100.0,
	DifficultyDRAnchor:   15.0,
	DifficultyDRSlope:    0.5,
	WeightVolume:         0.40,
	WeightDifficulty:     0.40,
	WeightFunnel:         0.20,
	OpportunityScale:     100.0,
	OpportunityGamma:     0.7,
	ScorePrecision:       10.0,
}

// Goldens computed with an independent implementation of the spec (not by
// running this package's code), so a regression in the shared helpers cannot
// silently regenerate matching expectations.
func TestComputeOpportunityScore(t *testing.T) {
	cases := []struct {
		name   string
		volume int
		kd     int
		funnel models.FunnelStage
		userDR int
		want   float64
	}{
		{name: "neutral DR anchor", volume: 10000, kd: 30, funnel: models.FunnelBOFU, userDR: 15, want: 85.5},
		{name: "strong domain discounts KD", volume: 50000, kd: 20, funnel: models.FunnelBOFU, userDR: 45, want: 96.9},
		{name: "weak domain penalizes KD", volume: 50000, kd: 20, funnel: models.FunnelBOFU, userDR: 5, want: 91.1},
		{name: "mid keyword lifted by DR60", volume: 1000, kd: 50, funnel: models.FunnelMOFU, userDR: 60, want: 77.9},
		{name: "hard keyword, DR90", volume: 100, kd: 70, funnel: models.FunnelTOFU, userDR: 90, want: 67.5},
		{name: "perfect score endpoint fixed", volume: 100000, kd: 0, funnel: models.FunnelBOFU, userDR: 15, want: 100.0},
		{name: "difficulty cap binds", volume: 80000, kd: 15, funnel: models.FunnelBOFU, userDR: 70, want: 99.5},
		{name: "DR saturates past the cap", volume: 80000, kd: 15, funnel: models.FunnelBOFU, userDR: 100, want: 99.5},
		{name: "unknown KD stays neutral", volume: 5000, kd: -1, funnel: models.FunnelMOFU, userDR: 15, want: 75.2},
		{name: "unknown KD ignores DR", volume: 5000, kd: -1, funnel: models.FunnelMOFU, userDR: 45, want: 75.2},
		{name: "zero volume floor", volume: 0, kd: 40, funnel: models.FunnelMOFU, userDR: 15, want: 53.6},
		{name: "unclassified funnel default", volume: 300, kd: 60, funnel: "", userDR: 55, want: 69.0},
		{name: "worst-case keyword", volume: 1, kd: 95, funnel: models.FunnelBOFU, userDR: 15, want: 34.6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeOpportunityScore(prodScoring, tc.volume, tc.kd, tc.funnel, tc.userDR)
			if got != tc.want {
				t.Errorf("computeOpportunityScore(vol=%d, kd=%d, funnel=%q, dr=%d) = %v, want %v",
					tc.volume, tc.kd, tc.funnel, tc.userDR, got, tc.want)
			}
		})
	}
}

// A gamma of 1.0 must reproduce the pre-curve raw score exactly, so the knob
// can be dialed back to the old curve without a code change.
func TestComputeOpportunityScoreGammaIdentity(t *testing.T) {
	sc := prodScoring
	sc.OpportunityGamma = 1.0
	// vol 10000, kd 30, BOFU, DR at anchor: raw = (0.4·0.8 + 0.4·0.7 + 0.2·1)·100 = 80.0
	if got := computeOpportunityScore(sc, 10000, 30, models.FunnelBOFU, 15); got != 80.0 {
		t.Errorf("gamma=1.0 score = %v, want 80.0 (raw)", got)
	}
}
