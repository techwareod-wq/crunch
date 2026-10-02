// Package changelog is the Mongo-backed domain.ChangeLog: it turns a
// domain.ChangeEntry into a change_log row. Best effort — see
// domain.ChangeLog.
package changelog

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// writeTimeout bounds one change_log insert. The insert runs on a context
// detached from the caller's cancellation so a client hanging up right after a
// successful mutation doesn't lose its audit row.
const writeTimeout = 5 * time.Second

// Mongo writes change_log rows. The zero value is ready to use.
type Mongo struct{}

var _ domain.ChangeLog = Mongo{}

// New returns the Mongo-backed change log.
func New() Mongo { return Mongo{} }

// Record inserts e. Failures are logged and swallowed (best effort, D-014 /
// platform spec P-3): the user's write already succeeded and must stand.
func (Mongo) Record(ctx context.Context, e domain.ChangeEntry) {
	row, err := ToRow(e, time.Now().UTC())
	if err != nil {
		log.Error("change_log: encode entry failed", "entity", e.Entity, "entity_id", e.EntityID, "action", e.Action, "error", err)
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	if err := models.InsertChangeLogEntry(wctx, row); err != nil {
		log.Error("change_log: insert failed", "entity", e.Entity, "entity_id", e.EntityID, "action", e.Action, "error", err)
	}
}

// ToRow converts a domain entry into the stored row, normalising Before/After
// (any bson-marshalable value) into documents.
func ToRow(e domain.ChangeEntry, at time.Time) (*models.ChangeLogEntry, error) {
	before, err := toDoc(e.Before)
	if err != nil {
		return nil, fmt.Errorf("before: %w", err)
	}
	after, err := toDoc(e.After)
	if err != nil {
		return nil, fmt.Errorf("after: %w", err)
	}
	var meta bson.M
	if len(e.Meta) > 0 {
		meta = bson.M(e.Meta)
	}
	return &models.ChangeLogEntry{
		Entity:      string(e.Entity),
		EntityID:    e.EntityID,
		Action:      string(e.Action),
		ActorUserID: e.Actor.UserID,
		ActorEmail:  e.Actor.Email,
		At:          at,
		Before:      before,
		After:       after,
		Meta:        meta,
	}, nil
}

// toDoc round-trips v through bson so any model struct is stored as a plain
// document with its bson field names. nil stays nil (stored as null).
func toDoc(v any) (bson.M, error) {
	if v == nil {
		return nil, nil
	}
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil, nil
	}
	if m, ok := v.(bson.M); ok {
		return m, nil
	}
	raw, err := bson.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m bson.M
	if err := bson.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// Memory is an in-memory domain.ChangeLog for tests: it keeps every entry.
type Memory struct {
	Entries []domain.ChangeEntry
}

var _ domain.ChangeLog = (*Memory)(nil)

// Record appends e.
func (m *Memory) Record(_ context.Context, e domain.ChangeEntry) {
	m.Entries = append(m.Entries, e)
}
