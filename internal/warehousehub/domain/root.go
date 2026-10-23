package domain

import "github.com/atharva-ng/crunch/internal/models"

// RootNode is the Warehouse root and its system fields (D-142). The
// attributes module inserts whatever is missing on boot and never overwrites
// an admin's rename, description or order. Every field is locked (D-140)
// except operator_company (admin-only, never public).
func RootNode() models.AttributeNode {
	locked := func(key, name string, t models.FieldType, required, public bool, order int) models.AttributeField {
		return models.AttributeField{Key: key, Name: name, Type: t, Required: required, Locked: true, Public: public, Order: order}
	}
	area := locked("total_area", "Total area", TypeArea, true, true, 5)
	area.Filterable, area.FilterRow = true, "size"
	return models.AttributeNode{
		Key:    RootKey,
		Name:   "Warehouse",
		Order:  1,
		System: true,
		Public: true,
		Fields: []models.AttributeField{
			locked("name", "Name", TypeText, true, true, 1),
			locked("description", "Description", TypeLongtext, false, true, 2),
			locked("address", "Address", TypeAddress, true, true, 3),
			locked("location", "Location", TypeLocation, true, true, 4),
			area,
			locked("rent", "Rent", TypeMoney, true, true, 6),
			{Key: "operator_company", Name: "Operator company", Type: TypeText, Order: 7},
		},
	}
}

// systemRootFields are RootNode's own field keys.
var systemRootFields = func() map[string]bool {
	m := map[string]bool{}
	for _, f := range RootNode().Fields {
		m[f.Key] = true
	}
	return m
}()

// IsSystemRootField reports whether key is one of the Warehouse root's
// system fields (RootNode: name, address, location, total area, rent, …).
// Search leaves these out (area and price have their own filters); fields
// an admin adds to the root are searchable like any other node's.
func IsSystemRootField(key string) bool { return systemRootFields[key] }
