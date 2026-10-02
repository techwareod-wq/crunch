package models

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Warehouse statuses (`warehouses.status`).
const (
	WarehouseUnpublished = "unpublished"
	WarehouseLive        = "live"
	WarehouseArchived    = "archived"
)

// ListingContent is one version of a listing: the attribute data plus media
// references and admin-only rent terms. Name, address, location, total area
// and the headline rent are fields of the Warehouse root node (D-142).
type ListingContent struct {
	Attributes Attributes `bson:"attributes"           json:"attributes"`
	Media      []MediaRef `bson:"media"                json:"media"`
	RentAdmin  *RentAdmin `bson:"rent_admin,omitempty" json:"rentAdmin,omitempty"`
}

// MediaRef places one warehouse_media doc in a listing version.
type MediaRef struct {
	MediaID primitive.ObjectID `bson:"media_id"          json:"mediaId"`
	Order   int                `bson:"order"             json:"order"`
	IsCover bool               `bson:"is_cover"          json:"isCover"`
	Caption string             `bson:"caption,omitempty" json:"caption,omitempty"`
}

// RentAdmin is the admin-only part of the rent (never public).
type RentAdmin struct {
	Deposit               *Money              `bson:"deposit,omitempty"                 json:"deposit,omitempty"`
	LockInMonths          int                 `bson:"lock_in_months,omitempty"          json:"lockInMonths,omitempty"`
	EscalationPct         float64             `bson:"escalation_pct,omitempty"          json:"escalationPct,omitempty"`
	EscalationEveryMonths int                 `bson:"escalation_every_months,omitempty" json:"escalationEveryMonths,omitempty"`
	LeaseTermMonths       int                 `bson:"lease_term_months,omitempty"       json:"leaseTermMonths,omitempty"`
	CAM                   *Money              `bson:"cam,omitempty"                     json:"cam,omitempty"`
	Taxes                 []Tax               `bson:"taxes,omitempty"                   json:"taxes,omitempty"`
	AgreementMediaID      *primitive.ObjectID `bson:"agreement_media_id,omitempty"      json:"agreementMediaId,omitempty"`
	OtherCharges          []Charge            `bson:"other_charges,omitempty"           json:"otherCharges,omitempty"`
}

// Tax is one tax line: a percentage or a fixed amount.
type Tax struct {
	Name  string   `bson:"name"            json:"name"`
	Pct   *float64 `bson:"pct,omitempty"   json:"pct,omitempty"`
	Money *Money   `bson:"money,omitempty" json:"money,omitempty"`
}

// Charge is one other charge (admin-only in v1).
type Charge struct {
	Label string `bson:"label" json:"label"`
	Money Money  `bson:"money" json:"money"`
}

// Price is the normalized headline rent hoisted onto the live doc for search
// (D-058): per sq m per month in the listing's own currency, minor units.
type Price struct {
	Currency    string  `bson:"currency"      json:"currency"`
	PerSqmMonth float64 `bson:"per_sqm_month" json:"perSqmMonth"`
	Basis       string  `bson:"basis"         json:"basis"`
	Approx      bool    `bson:"approx"        json:"approx"`
	OnRequest   bool    `bson:"on_request"    json:"onRequest"`
}

// GeoPoint is a GeoJSON point ([lng, lat]) for the 2dsphere index (04).
type GeoPoint struct {
	Type        string     `bson:"type"        json:"type"`
	Coordinates [2]float64 `bson:"coordinates" json:"coordinates"`
}

// NumFact is one known number in the search projection.
type NumFact struct {
	K string  `bson:"k" json:"k"`
	V float64 `bson:"v" json:"v"`
}

// Projection is the search projection on a `warehouses` doc (spec 02
// Evaluator §6), written by approve and by the attributes recompute and read
// by search (04). Built by domain.Evaluate.
type Projection struct {
	Chips           []string  `bson:"chips"             json:"chips"`
	Unk             []string  `bson:"unk"               json:"unk"`
	Nums            []NumFact `bson:"nums"              json:"nums"`
	Fit             []string  `bson:"fit"               json:"fit"`
	NeedsInfo       []string  `bson:"needs_info"        json:"needsInfo"`
	NeedsInfoCount  int       `bson:"needs_info_count"  json:"needsInfoCount"`
	FitRulesVersion int64     `bson:"fit_rules_version" json:"fitRulesVersion"`
	EvaluatedAt     time.Time `bson:"evaluated_at"      json:"evaluatedAt"`
}

// Warehouse is a `warehouses` doc: the live copy plus everything hoisted for
// search. Only status=live docs are searchable; the projection is kept fresh
// on archived ones too so a restore needs no recompute.
type Warehouse struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	ShortID     string             `bson:"short_id"      json:"shortId"`
	Slug        string             `bson:"slug"          json:"slug"`
	SlugHistory []string           `bson:"slug_history"  json:"slugHistory"`
	Status      string             `bson:"status"        json:"status"`
	// LiveVersion is the approved revision version now live (0 = never).
	LiveVersion    int                 `bson:"live_version"     json:"liveVersion"`
	RevSeq         int                 `bson:"rev_seq"          json:"-"`
	OpenRevisionID *primitive.ObjectID `bson:"open_revision_id" json:"openRevisionId"`
	Live           *ListingContent     `bson:"live"             json:"live"`

	// Hoisted (admin list + search). Name/City follow the open draft until
	// the warehouse is first published, then the live copy.
	Name       string    `bson:"name"                  json:"name"`
	Country    string    `bson:"country,omitempty"     json:"country,omitempty"`
	PostalCode string    `bson:"postal_code,omitempty" json:"postalCode,omitempty"`
	City       string    `bson:"city,omitempty"        json:"city,omitempty"`
	Locality   string    `bson:"locality,omitempty"    json:"locality,omitempty"`
	Loc        *GeoPoint `bson:"loc,omitempty"         json:"loc,omitempty"`
	TotalSqm   float64   `bson:"total_sqm"             json:"totalSqm"`
	Price      *Price    `bson:"price,omitempty"       json:"price,omitempty"`
	// CoverKey is the live cover photo's public object key (cards, sitemap).
	CoverKey      string  `bson:"cover_key,omitempty" json:"coverKey,omitempty"`
	Completeness  float64 `bson:"completeness"        json:"completeness"`
	VerifiedRatio float64 `bson:"verified_ratio"      json:"verifiedRatio"`

	Projection `bson:",inline"`

	Embedding     []float32 `bson:"embedding,omitempty"      json:"-"`
	EmbeddingHash string    `bson:"embedding_hash,omitempty" json:"-"`

	PublishedAt *time.Time `bson:"published_at,omitempty" json:"publishedAt,omitempty"`
	ArchivedAt  *time.Time `bson:"archived_at,omitempty"  json:"archivedAt,omitempty"`
	ArchivedBy  string     `bson:"archived_by,omitempty"  json:"archivedBy,omitempty"`
	CreatedBy   string     `bson:"created_by"             json:"createdBy"`
	CreatedAt   time.Time  `bson:"created_at"             json:"createdAt"`
	UpdatedAt   time.Time  `bson:"updated_at"             json:"updatedAt"`
}

// WarehouseFilter is the admin list filter.
type WarehouseFilter struct {
	Status    string
	Q         string // case-insensitive name substring
	City      string
	NeedsInfo bool
}

// WarehousePublish is what approve writes onto the live doc (spec 03
// Approve step 3).
type WarehousePublish struct {
	Live          ListingContent
	LiveVersion   int
	Name          string
	Country       string
	PostalCode    string
	City          string
	Locality      string
	Loc           *GeoPoint
	TotalSqm      float64
	Price         *Price
	CoverKey      string
	Completeness  float64
	VerifiedRatio float64
	Projection    Projection
	Slug          string
	At            time.Time
}

// WarehouseEvalDoc is what the attributes recompute reads: the live
// attribute data.
type WarehouseEvalDoc struct {
	ID   primitive.ObjectID `bson:"_id"`
	Live *struct {
		Attributes Attributes `bson:"attributes"`
	} `bson:"live"`
}

func warehouses() *mongo.Collection { return Collection(warehousesCollection) }

// EnsureWarehouseIndexes: unique shortId / slug, slug history, admin list;
// {needs_info} for the Needs-info queue (02) and the recompute's stale-page
// scan. Search indexes are added by 04.
func EnsureWarehouseIndexes(ctx context.Context) error {
	if _, err := warehouses().Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "short_id", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "slug", Value: 1}}, Options: options.Index().SetUnique(true).
			SetPartialFilterExpression(bson.M{"slug": bson.M{"$gt": ""}})},
		{Keys: bson.D{{Key: "slug_history", Value: 1}}},
		{Keys: bson.D{{Key: "status", Value: 1}, {Key: "updated_at", Value: -1}}},
		{Keys: bson.D{{Key: "needs_info", Value: 1}}},
		{Keys: bson.D{{Key: "status", Value: 1}, {Key: "fit_rules_version", Value: 1}, {Key: "_id", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("ensure warehouses indexes: %w", err)
	}
	return nil
}

// InsertWarehouse inserts w; ErrDuplicateKey on a shortId clash.
func InsertWarehouse(ctx context.Context, w *Warehouse) error {
	return insertDoc(ctx, warehousesCollection, w, &w.ID)
}

// FindWarehouseByID reads one warehouse; ErrNotFound when missing.
func FindWarehouseByID(ctx context.Context, id primitive.ObjectID) (*Warehouse, error) {
	return findOneDoc[Warehouse](ctx, warehouses(), bson.M{"_id": id})
}

// FindWarehouseByShortID reads one warehouse by its short id.
func FindWarehouseByShortID(ctx context.Context, shortID string) (*Warehouse, error) {
	return findOneDoc[Warehouse](ctx, warehouses(), bson.M{"short_id": shortID})
}

// NextWarehouseRevSeq hands out the next revision version.
func NextWarehouseRevSeq(ctx context.Context, id primitive.ObjectID) (int, error) {
	var w struct {
		Seq int `bson:"rev_seq"`
	}
	err := warehouses().FindOneAndUpdate(ctx, bson.M{"_id": id}, bson.M{"$inc": bson.M{"rev_seq": 1}},
		options.FindOneAndUpdate().SetReturnDocument(options.After).SetProjection(bson.M{"rev_seq": 1})).Decode(&w)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, ErrNotFound
	}
	return w.Seq, err
}

// SetWarehouseOpenRevision points the warehouse at its open revision (nil
// clears it).
func SetWarehouseOpenRevision(ctx context.Context, id primitive.ObjectID, rev *primitive.ObjectID, at time.Time) error {
	_, err := warehouses().UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"open_revision_id": rev, "updated_at": at}})
	return err
}

// SetWarehouseDraftName updates the hoisted name/city of a never-published
// warehouse (no-op once published).
func SetWarehouseDraftName(ctx context.Context, id primitive.ObjectID, name, city string, at time.Time) error {
	_, err := warehouses().UpdateOne(ctx, bson.M{"_id": id, "status": WarehouseUnpublished},
		bson.M{"$set": bson.M{"name": name, "city": city, "updated_at": at}})
	return err
}

// PublishWarehouse applies p when live_version == base; false = CAS miss.
func PublishWarehouse(ctx context.Context, id primitive.ObjectID, base int, p WarehousePublish) (bool, error) {
	set := bson.M{
		"live": p.Live, "live_version": p.LiveVersion, "status": WarehouseLive, "open_revision_id": nil,
		"name": p.Name, "country": p.Country, "postal_code": p.PostalCode, "city": p.City, "locality": p.Locality,
		"loc": p.Loc, "total_sqm": p.TotalSqm, "price": p.Price, "cover_key": p.CoverKey,
		"completeness": p.Completeness, "verified_ratio": p.VerifiedRatio,
		"slug": p.Slug, "updated_at": p.At,
	}
	proj, err := bson.Marshal(p.Projection)
	if err != nil {
		return false, err
	}
	var pm bson.M
	if err := bson.Unmarshal(proj, &pm); err != nil {
		return false, err
	}
	for k, v := range pm {
		set[k] = v
	}
	// A plain update document, never a pipeline: in a pipeline, user text
	// starting with "$" would be read as a field path.
	update := bson.M{
		"$set":      set,
		"$addToSet": bson.M{"slug_history": p.Slug},
		"$unset":    bson.M{"archived_at": "", "archived_by": ""},
	}
	res, err := warehouses().UpdateOne(ctx, bson.M{"_id": id, "live_version": base}, update)
	if err != nil {
		return false, err
	}
	if res.MatchedCount == 0 {
		return false, nil
	}
	// First publish stamps published_at (idempotent).
	if _, err := warehouses().UpdateOne(ctx, bson.M{"_id": id, "published_at": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"published_at": p.At}}); err != nil {
		return true, err
	}
	return true, nil
}

// ArchiveWarehouse moves live → archived; false = not live.
func ArchiveWarehouse(ctx context.Context, id primitive.ObjectID, by string, at time.Time) (bool, error) {
	res, err := warehouses().UpdateOne(ctx, bson.M{"_id": id, "status": WarehouseLive},
		bson.M{"$set": bson.M{"status": WarehouseArchived, "archived_at": at, "archived_by": by, "updated_at": at}})
	if err != nil {
		return false, err
	}
	return res.MatchedCount == 1, nil
}

// DeleteUnpublishedWarehouse hard-deletes a never-published warehouse; false
// = not unpublished.
func DeleteUnpublishedWarehouse(ctx context.Context, id primitive.ObjectID) (bool, error) {
	res, err := warehouses().DeleteOne(ctx, bson.M{"_id": id, "status": WarehouseUnpublished})
	if err != nil {
		return false, err
	}
	return res.DeletedCount == 1, nil
}

// ListWarehouses pages the admin list, newest edit first (without the live
// content).
func ListWarehouses(ctx context.Context, f WarehouseFilter, page, limit int) ([]Warehouse, int64, error) {
	q := bson.M{}
	if f.Status != "" {
		q["status"] = f.Status
	}
	if f.Q != "" {
		q["name"] = bson.M{"$regex": regexp.QuoteMeta(f.Q), "$options": "i"}
	}
	if f.City != "" {
		q["city"] = bson.M{"$regex": "^" + regexp.QuoteMeta(f.City) + "$", "$options": "i"}
	}
	if f.NeedsInfo {
		q["needs_info_count"] = bson.M{"$gt": 0}
	}
	total, err := warehouses().CountDocuments(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	items, err := findAllDocs[Warehouse](ctx, warehouses(), q, options.Find().
		SetSort(bson.D{{Key: "updated_at", Value: -1}, {Key: "_id", Value: -1}}).
		SetSkip(skipFor(page, limit)).SetLimit(int64(limit)).
		SetProjection(bson.M{"live": 0, "embedding": 0}))
	return items, total, err
}

// ListLiveWarehouses pages live warehouses by _id (public slugs / sitemap).
func ListLiveWarehouses(ctx context.Context, page, limit int) ([]Warehouse, error) {
	return findAllDocs[Warehouse](ctx, warehouses(), bson.M{"status": WarehouseLive}, options.Find().
		SetSort(bson.D{{Key: "_id", Value: 1}}).SetSkip(skipFor(page, limit)).SetLimit(int64(limit)).
		SetProjection(bson.M{"slug": 1, "updated_at": 1, "cover_key": 1, "short_id": 1}))
}

// staleFilter matches live/archived warehouses evaluated before version
// ($not $gte also matches a missing fit_rules_version).
func staleFilter(version int64) bson.M {
	return bson.M{
		"status":            bson.M{"$in": bson.A{WarehouseLive, WarehouseArchived}},
		"fit_rules_version": bson.M{"$not": bson.M{"$gte": version}},
	}
}

// StaleWarehouseIDs pages (by _id) live/archived warehouses whose projection
// is older than version.
func StaleWarehouseIDs(ctx context.Context, version int64, after primitive.ObjectID, limit int) ([]primitive.ObjectID, error) {
	f := staleFilter(version)
	if !after.IsZero() {
		f["_id"] = bson.M{"$gt": after}
	}
	rows, err := findAllDocs[struct {
		ID primitive.ObjectID `bson:"_id"`
	}](ctx, warehouses(), f, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit)).SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return nil, err
	}
	ids := make([]primitive.ObjectID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids, nil
}

// FindWarehouseEvalDocs reads the live attribute data of ids.
func FindWarehouseEvalDocs(ctx context.Context, ids []primitive.ObjectID) ([]WarehouseEvalDoc, error) {
	return findAllDocs[WarehouseEvalDoc](ctx, warehouses(), bson.M{"_id": bson.M{"$in": ids}},
		options.Find().SetProjection(bson.M{"live.attributes": 1}))
}

// WriteWarehouseProjections sets each projection, guarded so a doc already at
// (or past) the projection's rules version is left alone. Returns how many
// docs were written.
func WriteWarehouseProjections(ctx context.Context, ps map[primitive.ObjectID]Projection) (int64, error) {
	if len(ps) == 0 {
		return 0, nil
	}
	writes := make([]mongo.WriteModel, 0, len(ps))
	for id, p := range ps {
		writes = append(writes, mongo.NewUpdateOneModel().
			SetFilter(bson.M{"_id": id, "fit_rules_version": bson.M{"$not": bson.M{"$gte": p.FitRulesVersion}}}).
			SetUpdate(bson.M{"$set": p}))
	}
	res, err := warehouses().BulkWrite(ctx, writes, options.BulkWrite().SetOrdered(false))
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

// NeedsInfoCounts counts live warehouses per Needs-info key (02 Needs-info
// summary).
func NeedsInfoCounts(ctx context.Context) (map[string]int64, error) {
	cur, err := warehouses().Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"status": WarehouseLive, "needs_info_count": bson.M{"$gt": 0}}}},
		{{Key: "$unwind", Value: "$needs_info"}},
		{{Key: "$group", Value: bson.M{"_id": "$needs_info", "n": bson.M{"$sum": 1}}}},
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := map[string]int64{}
	for cur.Next(ctx) {
		var row struct {
			Key string `bson:"_id"`
			N   int64  `bson:"n"`
		}
		if err := cur.Decode(&row); err != nil {
			return nil, err
		}
		out[row.Key] = row.N
	}
	return out, cur.Err()
}

// ListNeedsInfoWarehouses pages the live warehouses whose Needs-info holds
// key, by name (without the live content).
func ListNeedsInfoWarehouses(ctx context.Context, key string, page, limit int) ([]Warehouse, int64, error) {
	q := bson.M{"status": WarehouseLive, "needs_info": key}
	total, err := warehouses().CountDocuments(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	items, err := findAllDocs[Warehouse](ctx, warehouses(), q, options.Find().
		SetSort(bson.D{{Key: "name", Value: 1}, {Key: "_id", Value: 1}}).
		SetSkip(skipFor(page, limit)).SetLimit(int64(limit)).
		SetProjection(bson.M{"live": 0, "embedding": 0}))
	return items, total, err
}
