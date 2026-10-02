package domain

import (
	"github.com/atharva-ng/crunch/internal/models"
	"math"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// mustD turns a map into the primitive.D shape the Mongo driver decodes
// nested documents into.
func mustD(m map[string]any) primitive.D {
	b, err := bson.Marshal(m)
	if err != nil {
		panic(err)
	}
	var d primitive.D
	if err := bson.Unmarshal(b, &d); err != nil {
		panic(err)
	}
	return d
}

func TestValidationParams(t *testing.T) {
	cases := []struct {
		name string
		f    models.AttributeField
		ok   bool
	}{
		{"min on number", models.AttributeField{Type: TypeNumber, Validations: []models.FieldValidation{{Kind: ValidMin, Value: 0.0}}}, true},
		{"min needs a number", models.AttributeField{Type: TypeNumber, Validations: []models.FieldValidation{{Kind: ValidMin, Value: "x"}}}, false},
		{"min not on text", models.AttributeField{Type: TypeText, Validations: []models.FieldValidation{{Kind: ValidMin, Value: 1.0}}}, false},
		{"maxLength ok", models.AttributeField{Type: TypeText, Validations: []models.FieldValidation{{Kind: ValidMaxLength, Value: 20.0}}}, true},
		{"maxLength above cap", models.AttributeField{Type: TypeText, Validations: []models.FieldValidation{{Kind: ValidMaxLength, Value: 501.0}}}, false},
		{"maxLength fractional", models.AttributeField{Type: TypeLongtext, Validations: []models.FieldValidation{{Kind: ValidMaxLength, Value: 2.5}}}, false},
		{"regex ok", models.AttributeField{Type: TypeText, Validations: []models.FieldValidation{{Kind: ValidRegex, Value: `^[A-Z]{2}\d+$`}}}, true},
		{"bad regex rejected at save", models.AttributeField{Type: TypeText, Validations: []models.FieldValidation{{Kind: ValidRegex, Value: `(`}}}, false},
		{"regex too long", models.AttributeField{Type: TypeText, Validations: []models.FieldValidation{{Kind: ValidRegex, Value: strings.Repeat("a", MaxRegexLen+1)}}}, false},
		{"units of family", models.AttributeField{Type: TypeNumber, Unit: &models.UnitSpec{Family: DimLength}, Validations: []models.FieldValidation{{Kind: ValidUnits, Value: []any{"m", "ft"}}}}, true},
		{"units wrong family", models.AttributeField{Type: TypeNumber, Unit: &models.UnitSpec{Family: DimLength}, Validations: []models.FieldValidation{{Kind: ValidUnits, Value: []any{"sqft"}}}}, false},
		{"units on unitless number", models.AttributeField{Type: TypeNumber, Validations: []models.FieldValidation{{Kind: ValidUnits, Value: []any{"m"}}}}, false},
		{"units on area", models.AttributeField{Type: TypeArea, Validations: []models.FieldValidation{{Kind: ValidUnits, Value: []any{"sqft"}}}}, true},
		{"currencies", models.AttributeField{Type: TypeMoney, Validations: []models.FieldValidation{{Kind: ValidCurrencies, Value: []any{"inr"}}}}, true},
		{"currencies bad", models.AttributeField{Type: TypeMoney, Validations: []models.FieldValidation{{Kind: ValidCurrencies, Value: []any{"RUPEE"}}}}, false},
		{"unknown kind", models.AttributeField{Type: TypeText, Validations: []models.FieldValidation{{Kind: "nope"}}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NormalizeValidations(&c.f)
			if (err == nil) != c.ok {
				t.Fatalf("err = %v, want ok=%v", err, c.ok)
			}
		})
	}
}

func normalized(t *testing.T, f models.AttributeField) *models.AttributeField {
	t.Helper()
	vs, err := NormalizeValidations(&f)
	if err != nil {
		t.Fatal(err)
	}
	f.Validations = vs
	return &f
}

func TestValidationChecks(t *testing.T) {
	num := normalized(t, models.AttributeField{Type: TypeNumber, Unit: &models.UnitSpec{Family: DimLength}, Validations: []models.FieldValidation{
		{Kind: ValidMin, Value: 1.0}, {Kind: ValidMax, Value: 30.0}, {Kind: ValidUnits, Value: []any{"m"}}}})
	if _, err := CanonicalizeValue(num, &models.FieldValue{Raw: &models.RawValue{Value: 12.0, Unit: "m"}, V: 0}); err != nil {
		t.Errorf("12 m: %v", err)
	}
	if _, err := CanonicalizeValue(num, &models.FieldValue{V: 0.5}); err == nil {
		t.Error("below min accepted")
	}
	if _, err := CanonicalizeValue(num, &models.FieldValue{Raw: &models.RawValue{Value: 20.0, Unit: "ft"}, V: 0}); err == nil {
		t.Error("ft accepted although units = [m]")
	}

	txt := normalized(t, models.AttributeField{Type: TypeText, Validations: []models.FieldValidation{
		{Kind: ValidRegex, Value: `^\d{6}$`, Message: "6-digit PIN"}, {Kind: ValidMaxLength, Value: 6.0}}})
	if _, err := CanonicalizeValue(txt, val("411001")); err != nil {
		t.Errorf("pin: %v", err)
	}
	if _, err := CanonicalizeValue(txt, val("41100")); err == nil || err.Error() != "6-digit PIN" {
		t.Errorf("custom message: %v", err)
	}

	money := normalized(t, models.AttributeField{Type: TypeMoney, Validations: []models.FieldValidation{{Kind: ValidCurrencies, Value: []any{"INR"}}, {Kind: ValidMin, Value: 1.0}}})
	if _, err := CanonicalizeValue(money, val(map[string]any{"amount": 0.0, "currency": "inr", "onRequest": true})); err != nil {
		t.Errorf("on request skips min: %v", err)
	}
	if _, err := CanonicalizeValue(money, val(map[string]any{"amount": 500.0, "currency": "USD"})); err == nil {
		t.Error("USD accepted")
	}
}

func TestCanonicalizeValue(t *testing.T) {
	temp := &models.AttributeField{Type: TypeNumber, Unit: &models.UnitSpec{Family: DimTemp}}
	fv, err := CanonicalizeValue(temp, &models.FieldValue{V: 0, Raw: &models.RawValue{Value: 32.0, Unit: UnitF}})
	if err != nil || fv.V.(float64) != 0 || fv.Raw.Unit != UnitF {
		t.Fatalf("32 F → %v %+v", err, fv)
	}

	area := &models.AttributeField{Type: TypeArea}
	fv, err = CanonicalizeValue(area, val(map[string]any{"value": 10000.0, "unit": "sqft"}))
	if err != nil || math.Abs(fv.V.(models.Area).Sqm-929.0304) > 1e-9 {
		t.Fatalf("area: %v %+v", err, fv)
	}

	addr := &models.AttributeField{Type: TypeAddress, Required: true}
	fv, err = CanonicalizeValue(addr, val(map[string]any{"line1": " Plot 4 ", "city": "Pune", "country": "in", "postalCode": "411001"}))
	if err != nil || fv.V.(models.Address).Country != "IN" || fv.V.(models.Address).PostalCode != "411001" || fv.V.(models.Address).Line1 != "Plot 4" {
		t.Fatalf("address: %v %+v", err, fv)
	}
	if _, err := CanonicalizeValue(addr, val(map[string]any{"city": "Pune", "country": "IN"})); err == nil {
		t.Error("address without line1 accepted")
	}

	loc := &models.AttributeField{Type: TypeLocation}
	if fv, err = CanonicalizeValue(loc, val(map[string]any{"lat": 18.5, "lng": 73.8})); err != nil || fv.V.(models.Location).Source != LocationManual {
		t.Fatalf("location: %v %+v", err, fv)
	}
	if _, err := CanonicalizeValue(loc, val(map[string]any{"lat": 98.0, "lng": 73.8})); err == nil {
		t.Error("lat 98 accepted")
	}

	date := &models.AttributeField{Type: TypeDate}
	if _, err := CanonicalizeValue(date, val("2026-02-30")); err == nil {
		t.Error("bad date accepted")
	}

	pick := &models.AttributeField{Type: TypeMulti, Options: []models.FieldOption{{Key: "a"}, {Key: "b"}}}
	if fv, err = CanonicalizeValue(pick, val([]any{"a", "a", "b"})); err != nil || len(fv.V.([]string)) != 2 {
		t.Fatalf("multi dedupe: %v %+v", err, fv)
	}
	if _, err := CanonicalizeValue(pick, val([]any{"z"})); err == nil {
		t.Error("unknown option accepted")
	}

	if _, err := CanonicalizeValue(&models.AttributeField{Type: TypeRatio}, val(1.0)); err == nil {
		t.Error("ratio took a value")
	}
	if _, err := CanonicalizeValue(&models.AttributeField{Type: TypeText, Required: true}, nil); err == nil {
		t.Error("required null accepted (D-124)")
	}
	if fv, err := CanonicalizeValue(&models.AttributeField{Type: TypeText}, val("  ")); err != nil || fv != nil {
		t.Errorf("blank optional text should be null: %v %+v", err, fv)
	}
}

func TestCanonicalizeAttributes(t *testing.T) {
	s := fixtureSnap()
	out, err := CanonicalizeAttributes(s, models.Attributes{
		"hazmat":       {Status: StatusNo},
		"temp_control": {Status: StatusUnknown, Fields: map[string]*models.FieldValue{"temperature_range": val(models.Range{Min: 1, Max: 2})}},
		"cold_storage": {Status: StatusYes, Fields: map[string]*models.FieldValue{"humidity": nil}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["hazmat"]; ok {
		t.Error("no must not be stored (D-133)")
	}
	if out["temp_control"].Fields != nil {
		t.Error("unknown node kept field values")
	}
	if v, ok := out["cold_storage"].Fields["humidity"]; !ok || v != nil {
		t.Error("optional null must be stored as null (D-125)")
	}
	if out[RootKey].Status != StatusYes {
		t.Error("root missing")
	}
	if _, err := CanonicalizeAttributes(s, models.Attributes{"nope": {Status: StatusYes}}); err == nil {
		t.Error("unknown node accepted")
	}
	if _, err := CanonicalizeAttributes(s, models.Attributes{"cold_storage": {Status: StatusYes, Fields: map[string]*models.FieldValue{"temperature": val("hot")}}}); err == nil ||
		!strings.HasPrefix(err.Error(), "cold_storage.temperature:") {
		t.Errorf("error should name the path: %v", err)
	}
	// The snapshot's field must not have been mutated by the draft relaxation.
	if _, f, _ := s.Field("cold_storage.temperature"); !f.Required {
		t.Fatal("snapshot mutated")
	}
}

func TestSubmitProblems(t *testing.T) {
	s := fixtureSnap()
	a := baseAttrs()
	a["temp_control"] = models.NodeState{Status: StatusYes}
	a["cold_storage"] = models.NodeState{Status: StatusUnknown}
	got := SubmitProblems(s, a)
	want := []string{"warehouse.name", "warehouse.address", "warehouse.location", "warehouse.rent", "temp_control"}
	if len(got) != len(want) {
		t.Fatalf("problems = %v", got)
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], w+":") {
			t.Errorf("problem %d = %q, want %s…", i, got[i], w)
		}
	}
}

func TestDecodeBSONShapes(t *testing.T) {
	m, ok := decodeDoc[models.Money](mustD(map[string]any{"amount": int64(5000), "currency": "INR", "onRequest": true}))
	if !ok || m.Amount != 5000 || !m.OnRequest {
		t.Fatalf("money from BSON = %+v %v", m, ok)
	}
	if v, err := FromCanonical(DimTemp, 0, UnitF); err != nil || math.Abs(v-32) > 1e-9 {
		t.Fatalf("0 C in F = %v %v", v, err)
	}
}
