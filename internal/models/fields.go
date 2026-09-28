package models

// High-frequency BSON field names used as query/update keys across the models
// package. Only the snake_case keys that repeat across files live here; struct
// tags and one-off keys stay literal at their use sites.
const (
	fieldID        = "_id"
	fieldUpdatedAt = "updated_at"
	fieldCreatedAt = "created_at"
	fieldEmail     = "email"
)
