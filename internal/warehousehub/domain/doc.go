// Package domain holds the WarehouseHub rules shared across the services
// (attributes, catalog, search, aisearch, enquiries, analytics): the attribute
// snapshot, conditions, verdicts, the units table, validators, the pure
// evaluator and price normalization. The stored shapes are in
// internal/models. No service owns it, and it imports no service.
//
// Cross-service calls go through small interfaces declared here (Rules,
// SearchEngine, ChangeLog, …) and are wired in
// cmd/service/providers, so services never import each other.
package domain
