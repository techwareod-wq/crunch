package service

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/aiSearchService"
	"github.com/atharva-ng/crunch/internal/services/searchService"
	searchsvc "github.com/atharva-ng/crunch/internal/services/searchService/service"
	searchstore "github.com/atharva-ng/crunch/internal/services/searchService/store"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// rootFieldsSnap is snapshot() with the seed's fields added to the root.
func rootFieldsSnap() *domain.Snapshot {
	base := snapshot()
	nodes := slices.Clone(base.Nodes)
	root := nodes[0].Clone()
	root.Fields = append(root.Fields,
		models.AttributeField{Key: "type", Name: "Warehouse type", Type: domain.TypePick, Public: true, Filterable: true, Order: 8,
			Options: []models.FieldOption{{Key: "general", Label: "General", Order: 1}, {Key: "bonded", Label: "Bonded", Order: 2}, {Key: "ftwz", Label: "FTWZ", Order: 3}}},
		models.AttributeField{Key: "fire_noc", Name: "Fire NOC", Type: domain.TypeBool, Public: true, Filterable: true, Order: 9},
		models.AttributeField{Key: "hold_capacity_mt", Name: "Hold capacity (MT)", Type: domain.TypeNumber, Public: true, Filterable: true, Order: 10,
			Unit: &models.UnitSpec{Family: domain.DimMass}},
	)
	nodes[0] = root
	return domain.NewSnapshot(base.Version, nodes, base.Industries)
}

// TestPromptListsRootFields builds the prompt from the real search catalog:
// the added root fields are in the vocabulary and the tool's enums, the
// system root fields are not.
func TestPromptListsRootFields(t *testing.T) {
	snap := rootFieldsSnap()
	search := searchsvc.NewService(searchstore.NewStore(), staticRules{snap}, nil, config.WarehouseHubValues{}, config.AWSConfig{})
	cat, _ := search.Catalog("IN", false)
	p := buildPrompt(snap, cat)

	for _, line := range []string{"- warehouse.type:bonded: Warehouse type: Bonded\n", "- warehouse.type:general: Warehouse type: General\n",
		"- warehouse.type:ftwz: Warehouse type: FTWZ\n", "- warehouse.fire_noc: Fire NOC\n", "- warehouse.hold_capacity_mt: "} {
		if !strings.Contains(p.system, line) {
			t.Errorf("prompt lacks %q", line)
		}
	}
	var schema struct {
		Properties struct {
			Chips struct {
				Items struct {
					Enum []string `json:"enum"`
				} `json:"items"`
			} `json:"chips"`
			Ranges struct {
				Items struct {
					Properties struct {
						Key struct {
							Enum []string `json:"enum"`
						} `json:"key"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"ranges"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(p.schema, &schema); err != nil {
		t.Fatal(err)
	}
	chips, ranges := schema.Properties.Chips.Items.Enum, schema.Properties.Ranges.Items.Properties.Key.Enum
	if !slices.Contains(chips, "warehouse.type:bonded") || !slices.Contains(chips, "warehouse.fire_noc") ||
		!slices.Contains(ranges, "warehouse.hold_capacity_mt") {
		t.Errorf("schema enums: chips %v ranges %v", chips, ranges)
	}
	for _, sys := range []string{"warehouse.name", "warehouse.address", "warehouse.location", "warehouse.total_area", "warehouse.rent", "warehouse.operator_company"} {
		if strings.Contains(p.system, sys) || slices.Contains(chips, sys) || slices.Contains(ranges, sys) {
			t.Errorf("system root field %s is in the prompt", sys)
		}
	}
}

// TestAISearchRootChipReachesSearch: a model answer using warehouse.type:
// bonded is passed to the structured search.
func TestAISearchRootChipReachesSearch(t *testing.T) {
	h := newHarness()
	h.llm.input = `{"location":{"kind":"place","text":"Mumbai"},"industries":[],"chips":["warehouse.type:bonded"],"unmapped":[],"confidence":"high"}`
	if _, err := h.svc.Search(ctx, aiSearchService.Request{Q: "bonded in Mumbai"}, searchService.Viewer{}); err != nil {
		t.Fatal(err)
	}
	if f := h.search.filters[0]; !slices.Equal(f.Chips, []string{"warehouse.type:bonded"}) {
		t.Errorf("chips = %v", f.Chips)
	}
}

func TestQueryRuleNearRadius(t *testing.T) {
	for _, tc := range []struct {
		q, input string
		want     int
		note     bool
	}{
		{"50000 sqft general near Mundra", `{"location":{"kind":"place","text":"Mundra"},"industries":[],"chips":[],"unmapped":[],"confidence":"high"}`, nearRadiusKm, true},
		{"warehouse NEAR 411001", `{"location":{"kind":"pincode","text":"411001"},"industries":[],"chips":[],"unmapped":[],"confidence":"high"}`, nearRadiusKm, true},
		{"near Mundra within 10 km", `{"location":{"kind":"place","text":"Mundra"},"radiusKm":10,"industries":[],"chips":[],"unmapped":[],"confidence":"high"}`, 10, false},
		{"warehouse in Mundra", `{"location":{"kind":"place","text":"Mundra"},"industries":[],"chips":[],"unmapped":[],"confidence":"high"}`, 0, false},
		{"near the port", `{"location":{"kind":"none"},"industries":[],"chips":[],"unmapped":[],"confidence":"low"}`, 0, false},
		{"nearest godown in Pune", `{"location":{"kind":"place","text":"Pune"},"industries":[],"chips":[],"unmapped":[],"confidence":"high"}`, 0, false},
	} {
		h := newHarness()
		h.llm.input = tc.input
		resp, err := h.svc.Search(ctx, aiSearchService.Request{Q: tc.q}, searchService.Viewer{})
		if err != nil {
			t.Fatal(err)
		}
		if got := h.search.filters[0].RadiusKm; got != tc.want || slices.Contains(resp.AI.Notes, "near_radius:50") != tc.note {
			t.Errorf("%q: radius %d notes %v, want %d", tc.q, got, resp.AI.Notes, tc.want)
		}
	}
}

func TestQueryRulePallets(t *testing.T) {
	sqm := func(sqft float64) float64 { return sqft * domain.SqmPerSqft }
	t.Run("pallets only: sized at 12 sq ft each", func(t *testing.T) {
		h := newHarness()
		h.llm.input = `{"location":{"kind":"place","text":"Pune"},"industries":[],"chips":["cold_storage"],"unmapped":["200 pallets"],"confidence":"medium"}`
		resp, err := h.svc.Search(ctx, aiSearchService.Request{Q: "200 pallets cold storage in Pune"}, searchService.Viewer{})
		if err != nil {
			t.Fatal(err)
		}
		f := h.search.filters[0]
		if f.AreaSqm == nil || f.AreaSqm.Min == nil || math.Abs(*f.AreaSqm.Min-sqm(2400)) > 1e-9 || f.AreaSqm.Max != nil ||
			f.AreaInput.Value != 2400 || f.AreaInput.Unit != domain.UnitSqft {
			t.Errorf("area = %+v input %+v", f.AreaSqm, f.AreaInput)
		}
		if !slices.Contains(resp.AI.Notes, "pallets_area") || slices.ContainsFunc(resp.AI.Notes, func(n string) bool { return strings.Contains(n, "unmapped:200 pallets") }) {
			t.Errorf("notes = %v", resp.AI.Notes)
		}
	})
	t.Run("pallet qualifier and k", func(t *testing.T) {
		h := newHarness()
		h.llm.input = `{"location":{"kind":"none"},"industries":[],"chips":[],"unmapped":[],"confidence":"low"}`
		if _, err := h.svc.Search(ctx, aiSearchService.Request{Q: "space for up to 1.5k pallets"}, searchService.Viewer{}); err != nil {
			t.Fatal(err)
		}
		f := h.search.filters[0]
		if f.AreaSqm == nil || f.AreaSqm.Max == nil || math.Abs(*f.AreaSqm.Max-sqm(18000)) > 1e-6 || f.AreaSqm.Min != nil {
			t.Errorf("area = %+v", f.AreaSqm)
		}
	})
	t.Run("explicit area wins", func(t *testing.T) {
		h := newHarness()
		h.llm.input = `{"location":{"kind":"none"},"area":{"value":5000,"unit":"sqft","intent":"min"},"industries":[],"chips":[],"unmapped":[],"confidence":"high"}`
		resp, err := h.svc.Search(ctx, aiSearchService.Request{Q: "5000 sqft for 200 pallets"}, searchService.Viewer{})
		if err != nil {
			t.Fatal(err)
		}
		f := h.search.filters[0]
		if f.AreaSqm == nil || math.Abs(*f.AreaSqm.Min-sqm(5000)) > 1e-9 || !slices.Contains(resp.AI.Notes, "pallets_ignored") {
			t.Errorf("area = %+v notes %v", f.AreaSqm, resp.AI.Notes)
		}
	})
	t.Run("explicit area the regex can't read still wins", func(t *testing.T) {
		h := newHarness()
		h.llm.input = `{"location":{"kind":"none"},"area":{"value":200000,"unit":"sqft","intent":"min"},"industries":[],"chips":[],"unmapped":[],"confidence":"high"}`
		resp, _ := h.svc.Search(ctx, aiSearchService.Request{Q: "2 lakh sqft for 300 pallets"}, searchService.Viewer{})
		if f := h.search.filters[0]; f.AreaInput == nil || f.AreaInput.Value != 200000 || !slices.Contains(resp.AI.Notes, "pallets_ignored") {
			t.Errorf("area input = %+v notes %v", f.AreaInput, resp.AI.Notes)
		}
	})
	t.Run("basic search fallback converts too", func(t *testing.T) {
		h := newHarness()
		h.llm.err = aiSearchService.ErrNoLLM
		resp, err := h.svc.Search(ctx, aiSearchService.Request{Q: "100000 pallets near Pune"}, searchService.Viewer{})
		if err != nil {
			t.Fatal(err)
		}
		f := h.search.filters[0]
		if resp.AI.Parsed || f.AreaInput == nil || f.AreaInput.Value != 1_200_000 || f.Location.PostalCode != "" {
			t.Errorf("fallback: area %+v location %+v (a pallet count must not be a pincode)", f.AreaInput, f.Location)
		}
	})
}

func TestPreparsePallets(t *testing.T) {
	for q, want := range map[string]*palletHint{
		"200 pallets in Pune":          {Count: 200, Intent: intentMin},
		"about 1,000 pallet positions": {Count: 1000, Intent: intentApprox},
		"2k pallets":                   {Count: 2000, Intent: intentMin},
		"pallet racking in 411001":     nil,
		"10000 sqft":                   nil,
	} {
		got := preparse(q).Pallets
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("%q: %+v, want %+v", q, got, want)
		}
	}
	if h := preparse("10000 sqft and 200 pallets"); h.Area == nil || h.Area.Value != 10000 || h.Pallets == nil || h.Pallets.Count != 200 {
		t.Errorf("area + pallets: %+v %+v", h.Area, h.Pallets)
	}
}
