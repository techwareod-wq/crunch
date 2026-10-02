package domain

import "context"

// ChangeEntity names what a change_log entry is about (D-014).
type ChangeEntity string

const (
	EntityWarehouse    ChangeEntity = "warehouse"
	EntityRevision     ChangeEntity = "revision"
	EntityRent         ChangeEntity = "rent"
	EntityMedia        ChangeEntity = "media"
	EntityAttributeDef ChangeEntity = "attributeDef"
	EntityIndustry     ChangeEntity = "industry"
	EntityEnquiry      ChangeEntity = "enquiry"
)

// KnownEntity reports whether e is one of the ChangeEntity constants.
func KnownEntity(e string) bool {
	switch ChangeEntity(e) {
	case EntityWarehouse, EntityRevision, EntityRent, EntityMedia,
		EntityAttributeDef, EntityIndustry, EntityEnquiry:
		return true
	}
	return false
}

// ChangeAction is the verb of a change_log entry. Bulk variants prefix
// "bulk_" (e.g. ActionBulkApprove) and carry a batchId in Meta.
type ChangeAction string

const (
	ActionCreate      ChangeAction = "create"
	ActionUpdate      ChangeAction = "update"
	ActionSubmit      ChangeAction = "submit"
	ActionWithdraw    ChangeAction = "withdraw"
	ActionApprove     ChangeAction = "approve"
	ActionReject      ChangeAction = "reject"
	ActionArchive     ChangeAction = "archive"
	ActionRestore     ChangeAction = "restore"
	ActionRetire      ChangeAction = "retire"
	ActionMove        ChangeAction = "move"
	ActionDelete      ChangeAction = "delete"
	ActionBulkApprove ChangeAction = "bulk_approve"
	ActionBulkSubmit  ChangeAction = "bulk_submit"
	ActionBulkAnswer  ChangeAction = "bulk_answer"
)

// Actor identifies who made a change. Empty UserID means the system (cron,
// async job, migration).
type Actor struct {
	UserID string
	Email  string
}

// SystemActor is the actor for platform-initiated changes.
var SystemActor = Actor{UserID: "", Email: "system"}

// ChangeEntry is one change_log record: the full document before and after a
// mutation (D-014). Before is nil on create, After is nil on delete. Before and
// After must be bson-marshalable (a model struct or a bson.M).
type ChangeEntry struct {
	Entity   ChangeEntity
	EntityID string
	Action   ChangeAction
	Actor    Actor
	Before   any
	After    any
	// Meta carries free-form context: comment, batchId, rulesVersion, …
	Meta map[string]any
}

// ChangeLog records change_log entries. Services call Record AFTER a
// successful write.
//
// Best effort by design: Record never fails the caller. A write failure is
// logged and the user's action stands — a lost audit row is preferable to a
// refused edit (platform spec P-3).
type ChangeLog interface {
	Record(ctx context.Context, e ChangeEntry)
}
