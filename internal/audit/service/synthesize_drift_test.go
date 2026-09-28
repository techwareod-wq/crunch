package service

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/models"
)

func TestDiffAuditReports(t *testing.T) {
	at := time.Now().Add(-24 * time.Hour)
	prev := &models.AuditRun{
		ID:          primitive.NewObjectID(),
		CompletedAt: &at,
		Report: &models.AuditReportDoc{
			Summary: models.AuditSummary{OverallScore: 60},
			Categories: []models.CategoryReport{
				{ID: "technical", Label: "Technical", Scored: true, Score: 80, Findings: []core.Finding{
					{CheckID: "technical.sitemap", Severity: core.SeverityLow, Title: "missing lastmod"},
					{CheckID: "technical.https", Severity: core.SeverityInfo, Title: "info-only note"},
				}},
				{ID: "content_eeat", Label: "Content", Scored: true, Score: 40},
			},
		},
	}
	current := &models.AuditReportDoc{
		Summary: models.AuditSummary{OverallScore: 73},
		Categories: []models.CategoryReport{
			{ID: "technical", Label: "Technical", Scored: true, Score: 95},
			{ID: "content_eeat", Label: "Content", Scored: true, Score: 50, Findings: []core.Finding{
				{CheckID: "content.trust_signals", Severity: core.SeverityMedium, Title: "no citations"},
			}},
			{ID: "performance", Label: "Performance", Scored: false},
		},
	}

	drift := diffAuditReports(prev, current)
	if drift.OverallDelta != 13 || drift.PreviousScore != 60 || drift.CurrentScore != 73 {
		t.Errorf("overall delta = %+v", drift)
	}
	if len(drift.CategoryDeltas) != 2 {
		t.Fatalf("category deltas = %d, want 2 (unscored performance excluded)", len(drift.CategoryDeltas))
	}
	if drift.CategoryDeltas[0].Delta != 15 {
		t.Errorf("technical delta = %d, want 15", drift.CategoryDeltas[0].Delta)
	}
	if drift.ResolvedCount != 1 || drift.ResolvedTitles[0] != "missing lastmod" {
		t.Errorf("resolved = %d %v (info finding must not count)", drift.ResolvedCount, drift.ResolvedTitles)
	}
	if drift.NewCount != 1 || drift.NewTitles[0] != "no citations" {
		t.Errorf("new = %d %v", drift.NewCount, drift.NewTitles)
	}
}
