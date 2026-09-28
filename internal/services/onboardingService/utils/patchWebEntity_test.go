package utils

import (
	"encoding/json"
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/onboardingService/dto"
	"go.mongodb.org/mongo-driver/bson"
)

func replaceOp(field string, raw string) dto.PatchOp {
	return dto.PatchOp{Op: dto.PatchOpReplace, Field: field, Value: json.RawMessage(raw)}
}

func TestApplyPatchOps_PublishAsLive(t *testing.T) {
	t.Run("accepts a bool and sets the field", func(t *testing.T) {
		set, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{replaceOp("publish_as_live", "true")})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v, ok := set["publish_as_live"].(bool); !ok || !v {
			t.Fatalf("publish_as_live not set to true: %#v", set["publish_as_live"])
		}
	})

	t.Run("sets false explicitly", func(t *testing.T) {
		set, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{replaceOp("publish_as_live", "false")})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v, ok := set["publish_as_live"].(bool); !ok || v {
			t.Fatalf("publish_as_live not set to false: %#v", set["publish_as_live"])
		}
	})

	t.Run("rejects a non-bool value", func(t *testing.T) {
		if _, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{replaceOp("publish_as_live", `"yes"`)}); err == nil {
			t.Fatal("expected error for a non-bool value, got nil")
		}
	})
}

func TestApplyPatchOps_ThumbnailStyle(t *testing.T) {
	t.Run("accepts a known style id", func(t *testing.T) {
		set, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{replaceOp("thumbnail_style", `"blueprint"`)})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v, ok := set["thumbnail_style"].(string); !ok || v != "blueprint" {
			t.Fatalf("thumbnail_style not set to blueprint: %#v", set["thumbnail_style"])
		}
	})

	t.Run("rejects an unknown style id", func(t *testing.T) {
		if _, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{replaceOp("thumbnail_style", `"nope"`)}); err == nil {
			t.Fatal("expected error for an unknown style id, got nil")
		}
	})

	t.Run("remove unsets the field", func(t *testing.T) {
		removeOp := dto.PatchOp{Op: dto.PatchOpRemove, Field: "thumbnail_style"}
		set, unset, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{removeOp})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := set["thumbnail_style"]; ok {
			t.Fatalf("remove must not set thumbnail_style: %#v", set["thumbnail_style"])
		}
		if _, ok := unset["thumbnail_style"]; !ok {
			t.Fatalf("remove must unset thumbnail_style, unset=%#v", unset)
		}
	})
}

func TestApplyPatchOps_SEOStrategy(t *testing.T) {
	t.Run("accepts a known strategy id", func(t *testing.T) {
		set, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{replaceOp("seo_strategy", `"early_footholds"`)})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v, ok := set["seo_strategy"].(string); !ok || v != models.SEOStrategyEarlyFootholds {
			t.Fatalf("seo_strategy not set to early_footholds: %#v", set["seo_strategy"])
		}
	})

	t.Run("rejects an unknown strategy id", func(t *testing.T) {
		if _, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{replaceOp("seo_strategy", `"moonshot"`)}); err == nil {
			t.Fatal("expected error for an unknown strategy id, got nil")
		}
	})

	t.Run("remove unsets the field", func(t *testing.T) {
		removeOp := dto.PatchOp{Op: dto.PatchOpRemove, Field: "seo_strategy"}
		set, unset, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{removeOp})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := set["seo_strategy"]; ok {
			t.Fatalf("remove must not set seo_strategy: %#v", set["seo_strategy"])
		}
		if _, ok := unset["seo_strategy"]; !ok {
			t.Fatalf("remove must unset seo_strategy, unset=%#v", unset)
		}
	})
}

func competitors(set bson.M, t *testing.T) []models.Competitor {
	t.Helper()
	comps, ok := set["competitors"].([]models.Competitor)
	if !ok {
		t.Fatalf("competitors not staged: %#v", set["competitors"])
	}
	return comps
}

func TestApplyPatchOps_CompetitorDomain(t *testing.T) {
	t.Run("normalizes a pasted URL to the bare host", func(t *testing.T) {
		op := replaceOp("competitors", `[{"domain":"HTTPS://www.Example.com/blog?a=1","reason":"r"}]`)
		set, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{op})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := competitors(set, t)[0].Domain; got != "example.com" {
			t.Fatalf("domain not normalized: %q", got)
		}
	})

	t.Run("rejects a non-domain value on replace", func(t *testing.T) {
		op := replaceOp("competitors", `[{"domain":"hello world"}]`)
		if _, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{op}); err == nil {
			t.Fatal("expected error for a non-domain value, got nil")
		}
	})

	t.Run("rejects an empty domain on replace", func(t *testing.T) {
		op := replaceOp("competitors", `[{"companyName":"Acme"}]`)
		if _, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{op}); err == nil {
			t.Fatal("expected error for an empty domain, got nil")
		}
	})

	t.Run("rejects a non-domain value on add", func(t *testing.T) {
		op := dto.PatchOp{Op: dto.PatchOpAdd, Field: "competitors", Value: json.RawMessage(`{"domain":"not a domain"}`)}
		if _, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{op}); err == nil {
			t.Fatal("expected error for a non-domain value, got nil")
		}
	})

	t.Run("rejects a non-domain value on indexed replace", func(t *testing.T) {
		idx := 0
		current := &models.WebEntity{Competitors: []models.Competitor{{Domain: "example.com"}}}
		op := dto.PatchOp{Op: dto.PatchOpReplace, Field: "competitors", Index: &idx, Value: json.RawMessage(`{"domain":"nope"}`)}
		if _, _, err := ApplyPatchOps(current, []dto.PatchOp{op}); err == nil {
			t.Fatal("expected error for a non-domain value, got nil")
		}
	})

	t.Run("add normalizes and keeps existing entries", func(t *testing.T) {
		current := &models.WebEntity{Competitors: []models.Competitor{{Domain: "one.com"}}}
		op := dto.PatchOp{Op: dto.PatchOpAdd, Field: "competitors", Value: json.RawMessage(`{"domain":"www.two.com/"}`)}
		set, _, err := ApplyPatchOps(current, []dto.PatchOp{op})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		comps := competitors(set, t)
		if len(comps) != 2 || comps[0].Domain != "one.com" || comps[1].Domain != "two.com" {
			t.Fatalf("unexpected competitors: %#v", comps)
		}
	})

	t.Run("remove by domain still works on a stored non-domain value", func(t *testing.T) {
		current := &models.WebEntity{Competitors: []models.Competitor{{Domain: "hello world"}, {Domain: "keep.com"}}}
		op := dto.PatchOp{Op: dto.PatchOpRemove, Field: "competitors", Value: json.RawMessage(`{"domain":"hello world"}`)}
		set, _, err := ApplyPatchOps(current, []dto.PatchOp{op})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		comps := competitors(set, t)
		if len(comps) != 1 || comps[0].Domain != "keep.com" {
			t.Fatalf("unexpected competitors: %#v", comps)
		}
	})
}

func removeOp(field string) dto.PatchOp {
	return dto.PatchOp{Op: dto.PatchOpRemove, Field: field}
}

func TestApplyPatchOps_CoreContextStrings(t *testing.T) {
	t.Run("accepts a non-empty replace", func(t *testing.T) {
		set, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{replaceOp("context.business_name", `"Acme"`)})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v, ok := set["context.business_name"].(string); !ok || v != "Acme" {
			t.Fatalf("business_name not set: %#v", set["context.business_name"])
		}
	})

	t.Run("rejects a blank replace on a core field", func(t *testing.T) {
		for _, field := range []string{"context.business_name", "context.product_type", "context.key_differentiator"} {
			if _, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{replaceOp(field, `"  "`)}); err == nil {
				t.Fatalf("expected error blanking %s, got nil", field)
			}
		}
	})

	t.Run("rejects remove on a core field", func(t *testing.T) {
		if _, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{removeOp("context.product_type")}); err == nil {
			t.Fatal("expected error removing context.product_type, got nil")
		}
	})

	t.Run("still allows blanking and removing non-core strings", func(t *testing.T) {
		if _, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{replaceOp("context.primary_use_case", `""`)}); err != nil {
			t.Fatalf("unexpected error blanking primary_use_case: %v", err)
		}
		if _, unset, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{removeOp("context.primary_use_case")}); err != nil || len(unset) != 1 {
			t.Fatalf("expected primary_use_case unset, got unset=%#v err=%v", unset, err)
		}
	})
}

func TestApplyPatchOps_ICPSignals(t *testing.T) {
	t.Run("accepts a replace with roles and pain points", func(t *testing.T) {
		set, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{replaceOp("context.icp_signals", `{"roles":["founder"],"pain_points":["churn"]}`)})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := set["context.icp_signals"].(models.ICPSignals); !ok {
			t.Fatalf("icp_signals not staged: %#v", set["context.icp_signals"])
		}
	})

	t.Run("rejects a replace missing roles or pain points", func(t *testing.T) {
		if _, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{replaceOp("context.icp_signals", `{"roles":["founder"]}`)}); err == nil {
			t.Fatal("expected error for icp_signals without pain points, got nil")
		}
		if _, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{replaceOp("context.icp_signals", `{"pain_points":["churn"]}`)}); err == nil {
			t.Fatal("expected error for icp_signals without roles, got nil")
		}
	})

	t.Run("rejects remove of the whole object", func(t *testing.T) {
		if _, _, err := ApplyPatchOps(&models.WebEntity{}, []dto.PatchOp{removeOp("context.icp_signals")}); err == nil {
			t.Fatal("expected error removing icp_signals, got nil")
		}
	})

	t.Run("rejects emptying the roles or pain point lists", func(t *testing.T) {
		entity := &models.WebEntity{BusinessContext: &models.BusinessContext{
			ICPSignals: &models.ICPSignals{Roles: []string{"founder"}, PainPoints: []string{"churn"}},
		}}
		if _, _, err := ApplyPatchOps(entity, []dto.PatchOp{replaceOp("context.icp_signals.roles", `[]`)}); err == nil {
			t.Fatal("expected error emptying icp roles, got nil")
		}
		if _, _, err := ApplyPatchOps(entity, []dto.PatchOp{replaceOp("context.icp_signals.pain_points", `[]`)}); err == nil {
			t.Fatal("expected error emptying icp pain points, got nil")
		}
	})

	t.Run("still allows emptying industries", func(t *testing.T) {
		entity := &models.WebEntity{BusinessContext: &models.BusinessContext{
			ICPSignals: &models.ICPSignals{Industries: []string{"saas"}},
		}}
		if _, _, err := ApplyPatchOps(entity, []dto.PatchOp{replaceOp("context.icp_signals.industries", `[]`)}); err != nil {
			t.Fatalf("unexpected error emptying industries: %v", err)
		}
	})
}

func TestEffectiveBusinessContext(t *testing.T) {
	strPtr := func(s string) *string { return &s }
	complete := func() *models.BusinessContext {
		return &models.BusinessContext{
			BusinessName:      strPtr("Acme"),
			ProductType:       strPtr("CRM"),
			KeyDifferentiator: strPtr("fast"),
			KeyFeatures:       []string{"a"},
			ICPSignals:        &models.ICPSignals{Roles: []string{"founder"}, PainPoints: []string{"churn"}},
		}
	}

	t.Run("complete stored context has no missing fields", func(t *testing.T) {
		if missing := EffectiveBusinessContext(&models.WebEntity{BusinessContext: complete()}, bson.M{}).MissingCoreFields(); len(missing) != 0 {
			t.Fatalf("expected no missing fields, got %v", missing)
		}
	})

	t.Run("nil context reports everything missing", func(t *testing.T) {
		if missing := EffectiveBusinessContext(&models.WebEntity{}, bson.M{}).MissingCoreFields(); len(missing) != 6 {
			t.Fatalf("expected 6 missing fields, got %v", missing)
		}
	})

	t.Run("staged whole-object icp fills a stored hole", func(t *testing.T) {
		bc := complete()
		bc.ICPSignals = nil
		set := bson.M{"context.icp_signals": models.ICPSignals{Roles: []string{"founder"}, PainPoints: []string{"churn"}}}
		if missing := EffectiveBusinessContext(&models.WebEntity{BusinessContext: bc}, set).MissingCoreFields(); len(missing) != 0 {
			t.Fatalf("expected no missing fields, got %v", missing)
		}
	})

	t.Run("staged dotted icp lists fill a stored hole", func(t *testing.T) {
		bc := complete()
		bc.ICPSignals = nil
		set := bson.M{
			"context.icp_signals.roles":       []string{"founder"},
			"context.icp_signals.pain_points": []string{"churn"},
		}
		if missing := EffectiveBusinessContext(&models.WebEntity{BusinessContext: bc}, set).MissingCoreFields(); len(missing) != 0 {
			t.Fatalf("expected no missing fields, got %v", missing)
		}
	})

	t.Run("staged strings fill stored holes", func(t *testing.T) {
		bc := complete()
		bc.BusinessName = nil
		bc.ProductType = strPtr("  ")
		set := bson.M{
			"context.business_name": "Acme",
			"context.product_type":  "CRM",
		}
		if missing := EffectiveBusinessContext(&models.WebEntity{BusinessContext: bc}, set).MissingCoreFields(); len(missing) != 0 {
			t.Fatalf("expected no missing fields, got %v", missing)
		}
	})
}
