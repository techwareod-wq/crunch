package changelog

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

type sampleDoc struct {
	ID   primitive.ObjectID `bson:"_id"`
	Name string             `bson:"name"`
}

func TestToRow_EncodesStructsAndNils(t *testing.T) {
	id := primitive.NewObjectID()
	at := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	var nilDoc *sampleDoc

	row, err := ToRow(domain.ChangeEntry{
		Entity:   domain.EntityWarehouse,
		EntityID: id.Hex(),
		Action:   domain.ActionCreate,
		Actor:    domain.Actor{UserID: "u1", Email: "ed@x.com"},
		Before:   nilDoc,
		After:    &sampleDoc{ID: id, Name: "Bhiwandi A"},
		Meta:     map[string]any{"comment": "hi"},
	}, at)
	if err != nil {
		t.Fatal(err)
	}
	if row.Before != nil {
		t.Errorf("typed-nil before must be stored as null, got %v", row.Before)
	}
	if row.After["name"] != "Bhiwandi A" || row.After["_id"] != id {
		t.Errorf("after = %v", row.After)
	}
	if row.Entity != "warehouse" || row.Action != "create" || row.ActorEmail != "ed@x.com" || !row.At.Equal(at) || row.Meta["comment"] != "hi" {
		t.Errorf("row = %+v", row)
	}
}
