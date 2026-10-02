package models

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Bulk attribute writes made by the attribute engine straight onto listing
// data, outside review (D-128 new-node markers, D-129 hard-delete strips).
// They touch the live copy on `warehouses` and the content of open
// `warehouse_revisions`.

// attrPageSize bounds each find + update round.
const attrPageSize = 500

// MarkNodeUnknown writes {status: "unknown"} for node key onto every live copy
// and open revision whose parent node is yes and that doesn't hold the node
// yet (D-128). Open revisions get a rev bump so a concurrent save 409s
// instead of dropping the marker. Idempotent. Returns the warehouse ids
// touched (live and drafts, deduped).
func MarkNodeUnknown(ctx context.Context, key, parent string, at time.Time) ([]primitive.ObjectID, error) {
	marker := NodeState{Status: "unknown"}
	seen := map[primitive.ObjectID]bool{}
	var out []primitive.ObjectID
	add := func(ids []primitive.ObjectID) {
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}

	liveFilter := bson.M{
		"live.attributes." + parent + ".status": "yes",
		"live.attributes." + key:                bson.M{"$exists": false},
	}
	ids, err := updatePaged(ctx, warehousesCollection, liveFilter,
		bson.M{"$set": bson.M{"live.attributes." + key: marker}}, "_id")
	if err != nil {
		return out, err
	}
	add(ids)

	revFilter := bson.M{
		"open": true,
		"content.attributes." + parent + ".status": "yes",
		"content.attributes." + key:                bson.M{"$exists": false},
	}
	ids, err = updatePaged(ctx, warehouseRevisionsCollection, revFilter,
		bson.M{"$set": bson.M{"content.attributes." + key: marker, "updated_at": at}, "$inc": bson.M{"rev": 1}}, "warehouse_id")
	add(ids)
	return out, err
}

// updatePaged applies update to every doc matching filter, attrPageSize docs
// at a time. The update must make a doc stop matching filter (or the loop
// would not end). Returns each updated doc's idField.
func updatePaged(ctx context.Context, coll string, filter, update bson.M, idField string) ([]primitive.ObjectID, error) {
	var out []primitive.ObjectID
	for {
		rows, err := findAllDocs[bson.M](ctx, Collection(coll), filter,
			options.Find().SetLimit(attrPageSize).SetProjection(bson.M{"_id": 1, idField: 1}))
		if err != nil || len(rows) == 0 {
			return out, err
		}
		ids := make([]primitive.ObjectID, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r["_id"].(primitive.ObjectID))
			if ref, ok := r[idField].(primitive.ObjectID); ok {
				out = append(out, ref)
			}
		}
		f := bson.M{"_id": bson.M{"$in": ids}}
		for k, v := range filter {
			f[k] = v
		}
		if _, err := Collection(coll).UpdateMany(ctx, f, update); err != nil {
			return out, err
		}
	}
}

// attrPathsFilter matches docs whose attributes (under prefix "live" or
// "content") hold any of paths, each relative to `attributes` ("cold_storage"
// or "cold_storage.fields.temperature").
func attrPathsFilter(prefix string, paths []string) bson.M {
	or := make(bson.A, 0, len(paths))
	for _, p := range paths {
		or = append(or, bson.M{prefix + ".attributes." + p: bson.M{"$exists": true}})
	}
	return bson.M{"$or": or}
}

// CountWarehousesHolding counts the distinct warehouses whose live copy or
// open revision holds any of paths (hard-delete confirm screen, key reuse
// guard).
func CountWarehousesHolding(ctx context.Context, paths []string) (int64, error) {
	if len(paths) == 0 {
		return 0, nil
	}
	return countDistinctWarehouses(ctx, attrPathsFilter("live", paths), attrPathsFilter("content", paths))
}

// CountWarehousesUsingOption counts the distinct warehouses whose live copy or
// open revision has option set on node.field (pick value or multi element,
// D-138).
func CountWarehousesUsingOption(ctx context.Context, node, field, option string) (int64, error) {
	p := ".attributes." + node + ".fields." + field + ".v"
	return countDistinctWarehouses(ctx, bson.M{"live" + p: option}, bson.M{"content" + p: option})
}

func countDistinctWarehouses(ctx context.Context, liveFilter, revFilter bson.M) (int64, error) {
	ids := map[any]bool{}
	live, err := warehouses().Distinct(ctx, "_id", liveFilter)
	if err != nil {
		return 0, err
	}
	revFilter["open"] = true
	revs, err := revisions().Distinct(ctx, "warehouse_id", revFilter)
	if err != nil {
		return 0, err
	}
	for _, id := range append(live, revs...) {
		ids[id] = true
	}
	return int64(len(ids)), nil
}

// StripAttributes removes paths from every live copy and open revision
// (D-129 hard delete). Open revisions get a rev bump. Idempotent: a re-run
// matches nothing. Returns the warehouse ids touched (deduped).
func StripAttributes(ctx context.Context, paths []string, at time.Time) ([]primitive.ObjectID, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	unset := func(prefix string) bson.M {
		u := bson.M{}
		for _, p := range paths {
			u[prefix+".attributes."+p] = ""
		}
		return u
	}
	seen := map[primitive.ObjectID]bool{}
	var out []primitive.ObjectID
	add := func(ids []primitive.ObjectID) {
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	ids, err := updatePaged(ctx, warehousesCollection, attrPathsFilter("live", paths),
		bson.M{"$unset": unset("live")}, "_id")
	add(ids)
	if err != nil {
		return out, err
	}
	revFilter := attrPathsFilter("content", paths)
	revFilter["open"] = true
	ids, err = updatePaged(ctx, warehouseRevisionsCollection, revFilter,
		bson.M{"$unset": unset("content"), "$set": bson.M{"updated_at": at}, "$inc": bson.M{"rev": 1}}, "warehouse_id")
	add(ids)
	return out, err
}
