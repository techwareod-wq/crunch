# Atlas indexes created by hand

Regular indexes (2dsphere, text, etc.) are created by the service at boot
(`EnsureXIndexes` in `cmd/service/app_context.go`). **Atlas Vector Search
indexes can't be created that way.** Create them once per cluster (staging,
production) in the Atlas UI or with the Atlas Admin API.

## `wh_embedding` on `crunchDB.warehouses`

Used by AI search's "similar matches" fallback (spec 05, D-084). Name and
dimensions must match `warehousehub.aisearch.vectorIndex` / `embedDims` in
`values/<env>/values.yaml`.

Atlas UI: **Atlas Search → Create Search Index → Atlas Vector Search → JSON
Editor**, database `crunchDB`, collection `warehouses`, index name
`wh_embedding`:

```json
{
  "fields": [
    { "type": "vector", "path": "embedding", "numDimensions": 1024, "similarity": "cosine" },
    { "type": "filter", "path": "status" },
    { "type": "filter", "path": "country" }
  ]
}
```

- `numDimensions` = `embedDims` (1024 for `voyage-3.5-lite`).
- Changing the embedding model or dimensions: update values, rebuild this
  index with the new size, then run the backfill below.

## After creating (or rebuilding) the index

1. Set `VOYAGE_API_KEY` on the service.
2. Backfill embeddings for every live listing (superuser token):

   ```bash
   curl -X POST https://<api-host>/v1/admin/aisearch/reembed-all \
     -H "Authorization: Bearer <superuser JWT>"
   ```

   New approvals embed themselves (`aisearch.embed` job); unchanged listings
   are skipped by their content hash.

Without the index (or without a Voyage key) AI search still works: the
fallback uses the keyword (`$text`) search and responses carry
`degraded: ["semantic_unavailable"]`.
