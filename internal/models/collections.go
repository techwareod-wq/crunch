package models

// MongoDB collection names. One place for every collection the models package
// talks to.
const (
	usersCollection = "users"

	// WarehouseHub platform collections (feature modules keep their own
	// collection names local to the module).
	changeLogCollection    = "change_log"
	metaCountersCollection = "meta_counters"
)
