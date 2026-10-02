// Package seed is the day-one attribute tree and industry rules (D-043):
// the Attribute Tree doc with every attestation turned into yes/no (D-031),
// plus clear height, temperature type, CCTV, 24×7 operations, sprinklers and
// GST registration. Upserted by key (missing keys only) through
// POST /v1/admin/attributes/seed or cmd/whseed. After day one the admin panel
// is the only place the tree changes.
package seed

import "github.com/atharva-ng/crunch/internal/warehousehub/domain"

// Filter rows (chip rows on the public search page).
const (
	rowFacility       = "facility"
	rowTemperature    = "temperature"
	rowHazmat         = "hazmat"
	rowInfrastructure = "infrastructure"
	rowServices       = "services"
	rowCertifications = "certifications"
	rowCustoms        = "customs"
)

// builder assigns sibling order in declaration order.
type builder struct {
	defs  []domain.AttrDef
	order map[string]int
}

func (b *builder) add(d domain.AttrDef) {
	b.order[d.ParentKey]++
	d.Order = b.order[d.ParentKey]
	if d.Kind == "" {
		d.Kind = domain.KindAttribute
	}
	if d.Kind == domain.KindAttribute && d.Role == "" {
		d.Role = domain.RoleProperty
		switch d.Type {
		case domain.TypeNumber, domain.TypeRange, domain.TypeCalculated:
			d.Role = domain.RoleParameter
		}
	}
	b.defs = append(b.defs, d)
}

func group(key, name string) domain.AttrDef {
	return domain.AttrDef{Key: key, Kind: domain.KindGroup, Name: name}
}

// yesNo is a public, filterable bool chip.
func yesNo(key, name, parent, row string, synonyms ...string) domain.AttrDef {
	return domain.AttrDef{Key: key, Type: domain.TypeBool, Name: name, ParentKey: parent,
		Filterable: true, FilterRow: row, Public: true, Synonyms: synonyms}
}

// number is a public, filterable number (a range slider in search).
func number(key, name, parent, row string, dim domain.Dimension, input []string, synonyms ...string) domain.AttrDef {
	return domain.AttrDef{Key: key, Type: domain.TypeNumber, Name: name, ParentKey: parent,
		Unit: &domain.UnitSpec{Dimension: dim, Input: input}, Filterable: true, FilterRow: row, Public: true, Synonyms: synonyms}
}

func options(pairs ...string) []domain.AllowedValue {
	out := make([]domain.AllowedValue, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, domain.AllowedValue{Key: pairs[i], Label: pairs[i+1], Order: i/2 + 1})
	}
	return out
}

func eqTrue(attr string) domain.Condition {
	return domain.Condition{Attr: attr, Cmp: domain.CmpEq, Value: true}
}

// Defs returns the starting tree, parents before children.
func Defs() []domain.AttrDef {
	b := &builder{order: map[string]int{}}
	area := []string{domain.UnitSqft, domain.UnitSqm}
	temp := []string{domain.UnitC, domain.UnitF}

	// Storage & handling
	b.add(group("storage_handling", "Storage & handling"))
	b.add(yesNo("cold_storage", "Cold storage", "storage_handling", rowFacility, "cold chain", "refrigerated", "reefer", "cold room"))
	b.add(domain.AttrDef{Key: "temp_range", Type: domain.TypeRange, Name: "Temperature range", ParentKey: "cold_storage",
		Unit: &domain.UnitSpec{Dimension: domain.DimTemp, Input: temp}, Filterable: true, FilterRow: rowTemperature, Public: true,
		Synonyms: []string{"temperature", "degrees"}})
	tracking := yesNo("temp_tracking", "Temperature tracking", "cold_storage", rowTemperature, "temperature monitoring", "temperature logging")
	tracking.Description = "Also asked of GDP-compliant warehouses."
	tracking.AppliesWhen = &domain.CondNode{Op: domain.OpAny, Conds: []domain.Condition{eqTrue("gdp_compliant")}}
	b.add(tracking)
	b.add(yesNo("defreeze", "Defreeze", "cold_storage", rowTemperature, "defrost"))
	b.add(yesNo("deep_freeze", "Deep freeze", "cold_storage", rowTemperature, "deep freezer", "blast freezer", "-18"))
	b.add(number("min_temp", "Minimum temperature", "deep_freeze", rowTemperature, domain.DimTemp, temp, "lowest temperature"))
	b.add(domain.AttrDef{Key: "temp_type", Type: domain.TypePick, Name: "Temperature type", ParentKey: "storage_handling",
		AllowedValues: options("ambient", "Ambient", "chilled", "Chilled", "frozen", "Frozen"),
		Filterable:    true, FilterRow: rowTemperature, Public: true, Synonyms: []string{"storage temperature"}})
	b.add(yesNo("racked_storage", "Racked storage", "storage_handling", rowFacility, "racking", "racks", "pallet racking", "shelving"))
	b.add(number("pallet_positions", "Pallet positions", "racked_storage", rowFacility, domain.DimCount, nil, "pallets", "pallet capacity"))
	b.add(yesNo("bulk_storage", "Bulk storage", "storage_handling", rowFacility, "floor storage", "bulk"))
	b.add(yesNo("hazmat_storage", "Hazmat / DG storage", "storage_handling", rowFacility, "hazardous", "dangerous goods", "DG", "chemicals"))
	b.add(domain.AttrDef{Key: "dg_classes", Type: domain.TypeMulti, Name: "DG classes allowed", ParentKey: "hazmat_storage",
		AllowedValues: options("1", "Class 1 Explosives", "2", "Class 2 Gases", "3", "Class 3 Flammable liquids",
			"4", "Class 4 Flammable solids", "5", "Class 5 Oxidizers", "6", "Class 6 Toxic", "7", "Class 7 Radioactive",
			"8", "Class 8 Corrosives", "9", "Class 9 Miscellaneous"),
		Filterable: true, FilterRow: rowHazmat, Public: true, Synonyms: []string{"DG class", "hazard class"}})
	b.add(yesNo("dg_owner_consent", "Owner consent for DG classes", "hazmat_storage", rowHazmat, "owner NOC for DG"))
	b.add(yesNo("open_yard", "Open yard", "storage_handling", rowFacility, "open area", "yard", "open storage"))
	b.add(number("yard_area", "Yard area", "open_yard", rowFacility, domain.DimArea, area, "yard size"))
	b.add(domain.AttrDef{Key: "zone_segregation", Type: domain.TypeMulti, Name: "Zone segregation", ParentKey: "storage_handling",
		AllowedValues: options("quarantine", "Quarantine", "received", "Received", "waste", "Waste", "returns", "Returns"),
		Filterable:    true, FilterRow: rowFacility, Public: true, Synonyms: []string{"segregated zones", "quarantine area"}})

	// Infrastructure
	b.add(group("infrastructure", "Infrastructure"))
	b.add(number("dock_doors", "Dock doors", "infrastructure", rowInfrastructure, domain.DimCount, nil, "docks", "loading bays", "dock levellers"))
	b.add(domain.AttrDef{Key: "dock_ratio", Type: domain.TypeCalculated, Name: "Dock ratio", ParentKey: "dock_doors",
		Description: "Dock doors per 10,000 sq ft of total area.", Calc: &domain.CalcSpec{Fn: "dock_ratio"},
		Unit: &domain.UnitSpec{Dimension: domain.DimCount}, Filterable: true, FilterRow: rowInfrastructure, Public: true})
	b.add(number("clear_height", "Clear height", "infrastructure", rowInfrastructure, domain.DimLength, []string{domain.UnitFt, domain.UnitM}, "ceiling height", "eave height"))
	b.add(number("floor_strength", "Floor strength", "infrastructure", rowInfrastructure, domain.DimLoad, []string{domain.UnitTPerSqm}, "floor load", "FLC", "floor loading"))
	b.add(number("crane_capacity", "Crane / handling capacity", "infrastructure", rowInfrastructure, domain.DimMass, []string{domain.UnitMT}, "crane", "EOT crane", "gantry"))
	b.add(yesNo("power_backup", "Power backup", "infrastructure", rowInfrastructure, "DG set", "generator", "backup power"))
	b.add(yesNo("wms", "Warehouse management system", "infrastructure", rowInfrastructure, "WMS", "inventory system"))
	b.add(yesNo("cctv", "CCTV / security", "infrastructure", rowInfrastructure, "security", "surveillance", "guards"))
	b.add(yesNo("ops_24x7", "24×7 operations", "infrastructure", rowInfrastructure, "24/7", "round the clock", "24 hours"))
	b.add(yesNo("sprinklers", "Sprinkler system", "infrastructure", rowInfrastructure, "fire sprinklers", "fire fighting"))

	// Services
	b.add(group("services", "Services"))
	b.add(domain.AttrDef{Key: "vas", Type: domain.TypeMulti, Name: "Value-added services", ParentKey: "services",
		AllowedValues: options("kitting", "Kitting", "packing", "Packing", "labelling", "Labelling"),
		Filterable:    true, FilterRow: rowServices, Public: true, Synonyms: []string{"VAS", "value added services"}})
	b.add(yesNo("pick_pack_dispatch", "Pick, pack & dispatch approved", "services", rowServices, "pick pack", "fulfilment", "fulfillment", "e-commerce fulfilment"))
	b.add(yesNo("transportation", "Transportation available", "services", rowServices, "transport", "trucking", "logistics"))

	// Compliance & certifications
	b.add(group("compliance", "Compliance & certifications"))
	b.add(yesNo("fire_noc", "Fire NOC", "compliance", rowCertifications, "fire safety certificate", "fire licence"))
	b.add(yesNo("insurance_verified", "Insurance verified", "compliance", rowCertifications, "insured", "insurance"))
	b.add(yesNo("gdp_compliant", "GDP compliant", "compliance", rowCertifications, "good distribution practice", "GDP", "pharma grade"))
	b.add(yesNo("haccp", "HACCP licence", "compliance", rowCertifications, "HACCP", "food safety", "FSSAI"))
	b.add(yesNo("pest_control", "Pest control program", "compliance", rowCertifications, "pest control", "fumigation"))
	b.add(yesNo("gst_registered", "GST registered", "compliance", rowCertifications, "GST", "GSTIN"))

	// Customs
	b.add(group("customs", "Customs"))
	b.add(yesNo("bonded", "Bonded", "customs", rowCustoms, "bonded warehouse", "customs bonded", "EXIM", "free trade warehouse"))
	b.add(domain.AttrDef{Key: "bonded_licence_no", Type: domain.TypeText, Name: "Bonded licence number", ParentKey: "bonded"})

	return b.defs
}

// Industries returns the PRD §4.2.5 rule table with the locked decisions:
// Heavy/ODC floor ≥ 5 t/m² (D-036), Bonded an ordinary industry (D-037).
// Chemicals' "matches requested DG class" is not a stored rule: search adds a
// dg_classes chip when a class is asked for.
func Industries() []domain.Industry {
	gte := func(attr string, v float64) domain.Condition {
		return domain.Condition{Attr: attr, Cmp: domain.CmpGte, Value: v}
	}
	anyVAS := domain.Condition{Attr: "vas", Cmp: domain.CmpIn, Value: []string{"kitting", "packing", "labelling"}}
	return []domain.Industry{
		{Key: "ecom", Name: "E-com / D2C", Order: 1,
			Required:  []domain.Condition{eqTrue("pick_pack_dispatch")},
			Preferred: []domain.Condition{gte("dock_ratio", 1)}},
		{Key: "fmcg", Name: "FMCG / Food", Order: 2,
			Preferred: []domain.Condition{eqTrue("racked_storage"), gte("dock_ratio", 1), eqTrue("haccp"), eqTrue("pest_control"), eqTrue("cold_storage")}},
		{Key: "pharma", Name: "Pharma / Healthcare", Order: 3,
			Required: []domain.Condition{eqTrue("gdp_compliant"), eqTrue("cold_storage"), eqTrue("temp_tracking"), eqTrue("fire_noc"), eqTrue("power_backup"),
				{Attr: "zone_segregation", Cmp: domain.CmpContainsAll, Value: []string{"quarantine", "received", "waste"}}}},
		{Key: "chemicals", Name: "Chemicals / DG", Order: 4,
			Required:  []domain.Condition{eqTrue("fire_noc"), eqTrue("insurance_verified"), eqTrue("dg_owner_consent")},
			Preferred: []domain.Condition{eqTrue("cold_storage")}},
		{Key: "heavy", Name: "Heavy / ODC / Project cargo", Order: 5,
			Required:  []domain.Condition{gte("floor_strength", 5), gte("crane_capacity", 15)},
			Preferred: []domain.Condition{eqTrue("open_yard")}},
		{Key: "automotive", Name: "Automotive / Industrial", Order: 6,
			Preferred: []domain.Condition{eqTrue("racked_storage"), anyVAS}},
		{Key: "electronics", Name: "Electronics / FMCD", Order: 7,
			Required:  []domain.Condition{eqTrue("wms")},
			Preferred: []domain.Condition{anyVAS}},
		{Key: "textiles", Name: "Textiles / Apparel", Order: 8},
		{Key: "bonded", Name: "Bonded / EXIM", Order: 9,
			Required: []domain.Condition{eqTrue("bonded")}},
	}
}
