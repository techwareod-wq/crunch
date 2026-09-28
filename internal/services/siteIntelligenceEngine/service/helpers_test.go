package service

import (
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestBuildClusterSummaries(t *testing.T) {
	pillar := primitive.NewObjectID()
	s1 := primitive.NewObjectID()
	s2 := primitive.NewObjectID()
	s3 := primitive.NewObjectID()
	missing := primitive.NewObjectID() // intentionally absent from the text map

	textByID := map[primitive.ObjectID]string{
		pillar: "salesforce to google sheets",
		s1:     "sync salesforce",
		s2:     "export salesforce data",
		s3:     "salesforce csv",
	}

	clusters := []models.Cluster{
		{
			ClusterID:            "salesforce-sheets",
			ClusterName:          "Salesforce + Google Sheets",
			PillarKeywordID:      pillar,
			SupportingKeywordIDs: []primitive.ObjectID{s1, missing, s2, s3},
		},
		{
			ClusterID:            "empty-cluster",
			ClusterName:          "Empty",
			PillarKeywordID:      missing, // unresolved pillar → empty string
			SupportingKeywordIDs: nil,
		},
	}

	got := buildClusterSummaries(clusters, textByID, 2)

	if len(got) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(got))
	}

	// Examples are capped at examplesPerCluster and skip IDs missing from the map.
	first := got[0]
	if first.PillarKeyword != "salesforce to google sheets" {
		t.Errorf("pillar text = %q, want resolved text", first.PillarKeyword)
	}
	if first.Examples != "sync salesforce, export salesforce data" {
		t.Errorf("examples = %q, want capped-at-2 list skipping the missing ID", first.Examples)
	}

	// An unresolved pillar ID resolves to empty string, not a panic.
	if got[1].PillarKeyword != "" {
		t.Errorf("unresolved pillar text = %q, want empty", got[1].PillarKeyword)
	}
	if got[1].Examples != "" {
		t.Errorf("no supporting IDs should yield empty examples, got %q", got[1].Examples)
	}
}
