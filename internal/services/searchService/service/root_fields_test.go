package service

import (
	"slices"
	"strings"
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/searchService"
	"github.com/atharva-ng/crunch/internal/services/searchService/dto"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// rootFieldsSnapshot is testSnapshot with fields added to the Warehouse root
// (as the seed / an admin does): a pick, a bool and a number that are public
// + filterable, plus a staff-only and a non-filterable one.
func rootFieldsSnapshot() *domain.Snapshot {
	base := testSnapshot()
	nodes := slices.Clone(base.Nodes)
	root := nodes[0].Clone()
	root.Fields = append(root.Fields,
		models.AttributeField{Key: "type", Name: "Warehouse type", Type: domain.TypePick, Public: true, Filterable: true, Order: 8,
			Options: []models.FieldOption{{Key: "general", Label: "General", Order: 1}, {Key: "bonded", Label: "Bonded", Order: 2}, {Key: "ftwz", Label: "FTWZ", Order: 3}}},
		models.AttributeField{Key: "food_grade", Name: "Food grade", Type: domain.TypeBool, Public: true, Filterable: true, Order: 9},
		models.AttributeField{Key: "hold_capacity_mt", Name: "Hold capacity", Type: domain.TypeNumber, Public: true, Filterable: true, Order: 10,
			Unit: &models.UnitSpec{Family: domain.DimMass}},
		models.AttributeField{Key: "staff_flag", Name: "Staff flag", Type: domain.TypeBool, Public: false, Filterable: true, Order: 11},
		models.AttributeField{Key: "quiet_flag", Name: "Quiet flag", Type: domain.TypeBool, Public: true, Filterable: false, Order: 12},
	)
	nodes[0] = root
	return domain.NewSnapshot(base.Version, nodes, base.Industries)
}

func TestNormalizeRootFields(t *testing.T) {
	snap, cfg := rootFieldsSnapshot(), testConfig()
	in := domain.SearchFilters{
		Chips: []string{"warehouse.type:bonded", "warehouse.food_grade", "warehouse.type:cold", "warehouse",
			"warehouse.staff_flag", "warehouse.quiet_flag", "warehouse.name", "warehouse.operator_company"},
		Ranges: map[string]domain.MinMax{
			"warehouse.hold_capacity_mt": {Min: fp(100)},
			"warehouse.total_area":       {Min: fp(100)},
		},
	}
	n, q, dropped, err := normalize(snap, in, cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(n.Chips, []string{"warehouse.type:bonded", "warehouse.food_grade"}) {
		t.Errorf("public chips = %v", n.Chips)
	}
	if len(q.Chips) != 2 || !slices.Equal(q.Chips[0].Unk, []string{"warehouse.type", domain.RootKey}) {
		t.Errorf("query chips = %+v", q.Chips)
	}
	if _, ok := n.Ranges["warehouse.hold_capacity_mt"]; !ok || len(n.Ranges) != 1 {
		t.Errorf("public ranges = %v", n.Ranges)
	}
	for _, w := range []string{"chip:warehouse.type:cold", "chip:warehouse", "chip:warehouse.staff_flag", "chip:warehouse.quiet_flag",
		"chip:warehouse.name", "chip:warehouse.operator_company", "range:warehouse.total_area"} {
		if !slices.Contains(dropped, w) {
			t.Errorf("%s not dropped (dropped = %v)", w, dropped)
		}
	}

	// Admin: staff-only / non-filterable root fields too, but still never the
	// root chip or a system root field.
	n, _, dropped, err = normalize(snap, in, cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(n.Chips, []string{"warehouse.type:bonded", "warehouse.food_grade", "warehouse.staff_flag", "warehouse.quiet_flag"}) {
		t.Errorf("admin chips = %v", n.Chips)
	}
	for _, w := range []string{"chip:warehouse", "chip:warehouse.name", "chip:warehouse.operator_company", "range:warehouse.total_area"} {
		if !slices.Contains(dropped, w) {
			t.Errorf("admin: %s not dropped (dropped = %v)", w, dropped)
		}
	}
}

// TestSearchRootChip: a structured search with warehouse.type:bonded keeps
// the chip, reaches the store query and counts it in the facets.
func TestSearchRootChip(t *testing.T) {
	h := newHarness()
	h.svc.rules = staticRules{rootFieldsSnapshot()}
	h.store.result = models.SearchResult{Total: 1, Hits: []models.SearchHit{{ShortID: "b1", Slug: "s", Name: "Bonded hub",
		Chips: []string{"warehouse.type:bonded"}}}, ChipCounts: map[string]int64{"warehouse.type:bonded": 1, "warehouse.name": 1}}
	resp, err := h.svc.Search(ctx, domain.SearchFilters{Chips: []string{"warehouse.type:bonded"}}, searchService.Viewer{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.Applied.Filters.Chips, []string{"warehouse.type:bonded"}) || len(resp.Applied.Dropped) != 0 {
		t.Errorf("applied = %+v dropped = %v", resp.Applied.Filters.Chips, resp.Applied.Dropped)
	}
	if len(h.store.execs) == 0 || !slices.ContainsFunc(h.store.execs[0].Query.Chips, func(c models.SearchChip) bool { return c.Chip == "warehouse.type:bonded" }) {
		t.Errorf("store query lacks the chip: %+v", h.store.execs)
	}
	if resp.Facets.Chips["warehouse.type:bonded"] != 1 {
		t.Errorf("facets = %v", resp.Facets.Chips)
	}
	if _, ok := resp.Facets.Chips["warehouse.name"]; ok {
		t.Error("a system root field must not be a facet")
	}
}

func catalogChips(c dto.PublicCatalog) []string {
	var keys []string
	for _, r := range c.ChipRows {
		for _, ch := range r.Chips {
			keys = append(keys, ch.Key)
		}
	}
	return keys
}

func catalogRanges(c dto.PublicCatalog) []string {
	var keys []string
	for _, r := range c.Ranges {
		keys = append(keys, r.Key)
	}
	return keys
}

// TestCatalogRootFields: the catalog (which the AI prompt is built from)
// lists the added root fields, never the system ones or a root chip.
func TestCatalogRootFields(t *testing.T) {
	h := newHarness()
	h.svc.rules = staticRules{rootFieldsSnapshot()}
	c, _ := h.svc.Catalog("", false)
	chips, ranges := catalogChips(c), catalogRanges(c)
	for _, want := range []string{"warehouse.type:general", "warehouse.type:bonded", "warehouse.type:ftwz", "warehouse.food_grade"} {
		if !slices.Contains(chips, want) {
			t.Errorf("chip %s missing: %v", want, chips)
		}
	}
	if !slices.Contains(ranges, "warehouse.hold_capacity_mt") {
		t.Errorf("range missing: %v", ranges)
	}
	for _, k := range append(slices.Clone(chips), ranges...) {
		path, _, _ := strings.Cut(k, ":")
		_, field, _ := strings.Cut(path, ".")
		if k == domain.RootKey || (strings.HasPrefix(path, domain.RootKey+".") && domain.IsSystemRootField(field)) ||
			strings.Contains(k, "staff_flag") || strings.Contains(k, "quiet_flag") {
			t.Errorf("catalog lists %s", k)
		}
	}
	gi := slices.IndexFunc(c.Groups, func(g dto.FilterGroup) bool { return g.Key == domain.RootKey })
	if gi < 0 || c.Groups[gi].Selectable || len(c.Groups[gi].Fields) != 3 {
		t.Errorf("root group = %+v", c.Groups)
	}

	// Admin lists the staff-only and non-filterable root fields, still no
	// system field.
	a, _ := h.svc.Catalog("", true)
	ac := catalogChips(a)
	if !slices.Contains(ac, "warehouse.staff_flag") || !slices.Contains(ac, "warehouse.quiet_flag") {
		t.Errorf("admin chips = %v", ac)
	}
	if slices.Contains(catalogRanges(a), "warehouse.total_area") || slices.Contains(ac, domain.RootKey) {
		t.Errorf("admin catalog lists a system root field: %v %v", ac, catalogRanges(a))
	}
}

// TestCatalogSystemRootUnchanged: with only the system root fields (the
// shipped tree), the catalog has no root group or chips, as before.
func TestCatalogSystemRootUnchanged(t *testing.T) {
	h := newHarness()
	for _, admin := range []bool{false, true} {
		c, _ := h.svc.Catalog("", admin)
		if slices.ContainsFunc(c.Groups, func(g dto.FilterGroup) bool { return g.Key == domain.RootKey }) {
			t.Errorf("admin=%t: root group listed", admin)
		}
		for _, k := range append(catalogChips(c), catalogRanges(c)...) {
			if strings.HasPrefix(k, domain.RootKey) {
				t.Errorf("admin=%t: lists %s", admin, k)
			}
		}
	}
	for _, k := range []string{"name", "description", "address", "location", "total_area", "rent", "operator_company"} {
		if !domain.IsSystemRootField(k) {
			t.Errorf("%s should be a system root field", k)
		}
	}
	if domain.IsSystemRootField("type") || domain.IsSystemRootField("food_grade") {
		t.Error("added root fields are not system fields")
	}
}
