// Package domain holds the WarehouseHub types shared across the feature
// modules (attributes, catalog, search, aisearch, enquiries, analytics):
// attribute definitions, answers, conditions, verdicts, money/area, search
// filter DTOs, the units table, the pure evaluator and the public DTO
// builders. No module owns it, and it imports no module.
//
// Cross-module calls go through small interfaces declared here (Evaluator,
// Geocoder, SearchEngine, ChangeLog, …) and are wired in
// cmd/service/modules.go, so modules never import each other.
package domain
