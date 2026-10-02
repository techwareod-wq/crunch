package models

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// The WarehouseHub attribute tree (spec 02, D-120…D-126): admin-defined
// nodes, each owning typed fields, one `attribute_nodes` doc per node with
// its fields embedded. The rules that act on these shapes (validation,
// evaluation) live in internal/warehousehub/domain.

// FieldType is a field's value type (D-137). Immutable after creation (D-130).
type FieldType string

// Dimension is the unit family of a number/range field (canonical units in
// domain/units.go).
type Dimension string

// UnitSpec pins a number/range field to a unit family. Stored values are in
// the family's canonical unit; Input lists the units the admin form offers.
type UnitSpec struct {
	Family Dimension `bson:"family"          json:"family"`
	Input  []string  `bson:"input,omitempty" json:"input,omitempty"`
}

// FieldOption is one choice of a pick/multi field. Values link by Key; an
// option in use can't be deleted (D-138).
type FieldOption struct {
	Key   string `bson:"key"   json:"key"`
	Label string `bson:"label" json:"label"`
	Order int    `bson:"order" json:"order"`
}

// RatioSpec defines a ratio field: Top ÷ (Bottom ÷ Per) (D-134). Top and
// Bottom are full field paths "<node>.<field>" of numeric fields.
// BottomUnit expresses Bottom in that unit of its family before dividing
// (dock ratio = doors per 10,000 sq ft: bottomUnit "sqft"); empty = canonical.
type RatioSpec struct {
	Top        string  `bson:"top"                   json:"top"`
	Bottom     string  `bson:"bottom"                json:"bottom"`
	Per        float64 `bson:"per"                   json:"per"`
	BottomUnit string  `bson:"bottom_unit,omitempty" json:"bottomUnit,omitempty"`
}

// FieldValidation is one entry of a field's validation list (D-137). Kind
// names a registry entry (domain/validators.go); Value holds its parameter.
// Message, when set, replaces the default error text shown to editors.
type FieldValidation struct {
	Kind    string `bson:"kind"              json:"kind"`
	Value   any    `bson:"value"             json:"value"`
	Message string `bson:"message,omitempty" json:"message,omitempty"`
}

// AttributeField is one typed value a node carries. Its full path is
// "<node>.<field>".
type AttributeField struct {
	Key         string    `bson:"key"                   json:"key"`
	Name        string    `bson:"name"                  json:"name"`
	Description string    `bson:"description,omitempty" json:"description,omitempty"`
	Order       int       `bson:"order"                 json:"order"`
	Type        FieldType `bson:"type"                  json:"type"`
	// Required: the node can be "yes" on a submitted revision only with a
	// real value here (D-124). Never on a ratio.
	Required bool `bson:"required" json:"required"`
	// Locked: a root system field. Rename, re-describe and reorder only
	// (D-140). Set by the boot bootstrap, never through the API.
	Locked      bool              `bson:"locked"                json:"locked"`
	Unit        *UnitSpec         `bson:"unit,omitempty"        json:"unit,omitempty"`
	Options     []FieldOption     `bson:"options,omitempty"     json:"options,omitempty"`
	Ratio       *RatioSpec        `bson:"ratio,omitempty"       json:"ratio,omitempty"`
	Validations []FieldValidation `bson:"validations,omitempty" json:"validations,omitempty"`
	Public      bool              `bson:"public"                json:"public"`
	Filterable  bool              `bson:"filterable"            json:"filterable"`
	FilterRow   string            `bson:"filter_row,omitempty"  json:"filterRow,omitempty"`
	FilterPos   int               `bson:"filter_pos"            json:"filterPos"`
}

// HasOption reports whether key is one of f's options.
func (f *AttributeField) HasOption(key string) bool {
	return slices.ContainsFunc(f.Options, func(o FieldOption) bool { return o.Key == key })
}

// Clone deep-copies f.
func (f AttributeField) Clone() AttributeField {
	f.Options = slices.Clone(f.Options)
	f.Validations = slices.Clone(f.Validations)
	if f.Unit != nil {
		u := *f.Unit
		u.Input = slices.Clone(u.Input)
		f.Unit = &u
	}
	if f.Ratio != nil {
		r := *f.Ratio
		f.Ratio = &r
	}
	return f
}

// AttributeNode is one `attribute_nodes` doc: a node and its fields, read and
// written together under one CAS version.
type AttributeNode struct {
	ID  primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Key string             `bson:"key"           json:"key"`
	// ParentKey is "" only on the root.
	ParentKey   string `bson:"parent_key"            json:"parentKey"`
	Name        string `bson:"name"                  json:"name"`
	Description string `bson:"description,omitempty" json:"description,omitempty"`
	Order       int    `bson:"order"                 json:"order"`
	// System marks the root: undeletable, unmovable.
	System     bool   `bson:"system"               json:"system"`
	Public     bool   `bson:"public"               json:"public"`
	Filterable bool   `bson:"filterable"           json:"filterable"`
	FilterRow  string `bson:"filter_row,omitempty" json:"filterRow,omitempty"`
	FilterPos  int    `bson:"filter_pos"           json:"filterPos"`
	// Synonyms feed the AI search vocabulary (spec 05).
	Synonyms []string         `bson:"synonyms,omitempty" json:"synonyms,omitempty"`
	Fields   []AttributeField `bson:"fields"             json:"fields"`
	// Version is the per-doc CAS counter (expectedVersion on update).
	Version   int       `bson:"version"    json:"version"`
	UpdatedBy string    `bson:"updated_by" json:"updatedBy"`
	UpdatedAt time.Time `bson:"updated_at" json:"updatedAt"`
}

// Field looks up one of n's fields.
func (n *AttributeNode) Field(key string) (*AttributeField, bool) {
	for i := range n.Fields {
		if n.Fields[i].Key == key {
			return &n.Fields[i], true
		}
	}
	return nil, false
}

// Clone deep-copies n so a cached node can be edited safely.
func (n AttributeNode) Clone() AttributeNode {
	n.Synonyms = slices.Clone(n.Synonyms)
	n.Fields = slices.Clone(n.Fields)
	for i := range n.Fields {
		n.Fields[i] = n.Fields[i].Clone()
	}
	return n
}

// EnsureAttributeNodeIndexes: unique key, sibling order.
func EnsureAttributeNodeIndexes(ctx context.Context) error {
	if _, err := Collection(attributeNodesCollection).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "key", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "parent_key", Value: 1}, {Key: "order", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("ensure attribute_nodes indexes: %w", err)
	}
	return nil
}

// ListAttributeNodes reads the whole tree.
func ListAttributeNodes(ctx context.Context) ([]AttributeNode, error) {
	return findAllDocs[AttributeNode](ctx, Collection(attributeNodesCollection), bson.M{})
}

// InsertAttributeNode inserts n; ErrDuplicateKey when the key exists.
func InsertAttributeNode(ctx context.Context, n *AttributeNode) error {
	return insertDoc(ctx, attributeNodesCollection, n, &n.ID)
}

// ReplaceAttributeNode CAS-replaces the node whose version is expected;
// ErrVersionConflict on a mismatch.
func ReplaceAttributeNode(ctx context.Context, n *AttributeNode, expected int) error {
	return casReplaceByKey(ctx, attributeNodesCollection, n.Key, expected, n)
}

// DeleteAttributeNode CAS-deletes a node on version; ErrVersionConflict on a
// mismatch.
func DeleteAttributeNode(ctx context.Context, key string, expected int) error {
	res, err := Collection(attributeNodesCollection).DeleteOne(ctx, bson.M{"key": key, "version": expected})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrVersionConflict
	}
	return nil
}

// --- shared helpers for the WarehouseHub models ---

func findAllDocs[T any](ctx context.Context, coll *mongo.Collection, filter any, opts ...*options.FindOptions) ([]T, error) {
	cur, err := coll.Find(ctx, filter, opts...)
	if err != nil {
		return nil, err
	}
	out := []T{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func findOneDoc[T any](ctx context.Context, coll *mongo.Collection, filter any) (*T, error) {
	var out T
	err := coll.FindOne(ctx, filter).Decode(&out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// insertDoc inserts doc and writes the new _id into id.
func insertDoc(ctx context.Context, coll string, doc any, id *primitive.ObjectID) error {
	res, err := Collection(coll).InsertOne(ctx, doc)
	if mongo.IsDuplicateKeyError(err) {
		return ErrDuplicateKey
	}
	if err != nil {
		return err
	}
	*id = res.InsertedID.(primitive.ObjectID)
	return nil
}

func casReplaceByKey(ctx context.Context, coll, key string, expected int, doc any) error {
	res, err := Collection(coll).ReplaceOne(ctx, bson.M{"key": key, "version": expected}, doc)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrVersionConflict
	}
	return nil
}

func skipFor(page, limit int) int64 { return int64((page - 1) * limit) }
