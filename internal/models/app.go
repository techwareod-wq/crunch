package models

// AppID values identify a product within central. Every subscription and
// entitlement projection is scoped to exactly one app.
//
// This is the single canonical definition of the product identifier — all
// services, controllers, and middleware reference AppIDIndexly rather than
// hardcoding the raw string, so introducing another app (or renaming this one)
// is a one-line change here.
//
// AppIDIndexly is the original single product (previously referred to as
// "app1"). Subscriptions created before the multi-app split have no app_id at
// all; appScopeFilter maps that absence to Indexly (see the backfill in
// entitlements_migration.go).
const AppIDIndexly = "indexly"
