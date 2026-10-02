package domain

// RootNode is the Warehouse root and its system fields (D-142). The
// attributes module inserts whatever is missing on boot and never overwrites
// an admin's rename, description or order. Every field is locked (D-140)
// except operator_company (admin-only, never public).
func RootNode() Node {
	locked := func(key, name string, t FieldType, required, public bool, order int) Field {
		return Field{Key: key, Name: name, Type: t, Required: required, Locked: true, Public: public, Order: order}
	}
	area := locked("total_area", "Total area", TypeArea, true, true, 5)
	area.Filterable, area.FilterRow = true, "size"
	return Node{
		Key:    RootKey,
		Name:   "Warehouse",
		Order:  1,
		System: true,
		Public: true,
		Fields: []Field{
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
