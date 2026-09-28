package models

import "testing"

// deriveArtifactStatus is the pure decision behind the data-repair migration's
// status reconciliation. The DB-bound counting/CAS helpers it sits behind need
// an integration harness (none exists in this repo yet), but this mapping is the
// part most worth pinning down: it decides how far forward a WEC's status is
// reconciled from its persisted artifacts, and a wrong rung either strands a WEC
// or skips a stage.
func TestDeriveArtifactStatus(t *testing.T) {
	tests := []struct {
		name        string
		hasClusters bool
		scored      int
		settled     int
		total       int
		want        int
	}{
		{
			name:        "clusters present wins over everything",
			hasClusters: true,
			scored:      100,
			settled:     100,
			total:       100,
			want:        SIEStatusClusteringDone,
		},
		{
			name:   "scores present but no clusters -> opportunity score",
			scored: 100,
			// scores are written only after funnel completes, so settled==total too
			settled: 100,
			total:   100,
			want:    SIEStatusOpportunityScoreCalculated,
		},
		{
			name:    "all keywords settled, no scores -> funnel done",
			settled: 100,
			total:   100,
			want:    SIEStatusFunnelClassificationDone,
		},
		{
			name:    "settled exceeds total (legacy inflation) still reads as funnel done",
			settled: 250,
			total:   100,
			want:    SIEStatusFunnelClassificationDone,
		},
		{
			name:    "keywords present but funnel incomplete -> post processing done",
			settled: 40,
			total:   100,
			want:    SIEStatusPostProcessingDone,
		},
		{
			name: "no keywords at all -> created (no forward reconcile)",
			want: SIEStatusCreated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveArtifactStatus(tt.hasClusters, tt.scored, tt.settled, tt.total)
			if got != tt.want {
				t.Errorf("deriveArtifactStatus(clusters=%v, scored=%d, settled=%d, total=%d) = %d, want %d",
					tt.hasClusters, tt.scored, tt.settled, tt.total, got, tt.want)
			}
		})
	}
}
