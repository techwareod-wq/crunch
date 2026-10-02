package models

// MongoDB collection names. One place for every collection the models package
// talks to.
const (
	usersCollection = "users"

	// WarehouseHub collections.
	changeLogCollection          = "change_log"
	metaCountersCollection       = "meta_counters"
	attributeNodesCollection     = "attribute_nodes"
	industriesCollection         = "industries"
	warehousesCollection         = "warehouses"
	warehouseRevisionsCollection = "warehouse_revisions"
	warehouseMediaCollection     = "warehouse_media"
	warehouseRentsCollection     = "warehouse_rents"
	geocodeCacheCollection       = "geocode_cache"
	pincodesCollection           = "pincodes"
)
