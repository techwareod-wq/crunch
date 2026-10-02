package models

// MongoDB collection names. One place for every collection the models package
// talks to.
const (
	rolesCollection = "roles"
	usersCollection = "users"

	// WarehouseHub platform collections (feature modules keep their own
	// collection names local to the module).
	changeLogCollection    = "change_log"
	staffInvitesCollection = "staff_invites"
)
