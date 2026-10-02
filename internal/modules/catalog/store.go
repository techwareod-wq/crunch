package catalog

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

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

var (
	errNotFound  = errors.New("not found")
	errDuplicate = errors.New("duplicate key")
	// errCAS: the doc changed (state / rev / live version) since it was read.
	errCAS = errors.New("changed since last read")
)

// WarehouseFilter is the admin list filter.
type WarehouseFilter struct {
	Status    string
	Q         string // case-insensitive name substring
	City      string
	NeedsInfo bool
}

// PublishPatch is what approve writes onto the live doc (spec 03 Approve
// step 3).
type PublishPatch struct {
	Live          domain.Content
	LiveVersion   int
	Name          string
	Country       string
	PostalCode    string
	City          string
	Locality      string
	Loc           *domain.GeoPoint
	TotalSqm      float64
	Price         *domain.Price
	CoverKey      string
	Completeness  float64
	VerifiedRatio float64
	Projection    domain.Projection
	Slug          string
	At            time.Time
}

// Store is the catalog persistence. Mongo in production, memStore in tests.
// Every multi-doc flow is a sequence of single-doc CAS writes (no
// transactions, spec 00).
type Store interface {
	// warehouses
	InsertWarehouse(ctx context.Context, w *domain.Warehouse) error // errDuplicate on shortId
	GetWarehouse(ctx context.Context, id primitive.ObjectID) (*domain.Warehouse, error)
	GetWarehouseByShortID(ctx context.Context, shortID string) (*domain.Warehouse, error)
	NextRevSeq(ctx context.Context, id primitive.ObjectID) (int, error)
	SetOpenRevision(ctx context.Context, id primitive.ObjectID, rev *primitive.ObjectID, at time.Time) error
	// SetDraftName updates the hoisted name/city of a never-published
	// warehouse (no-op once published).
	SetDraftName(ctx context.Context, id primitive.ObjectID, name, city string, at time.Time) error
	// PublishLive applies p when live_version == base; false = CAS miss.
	PublishLive(ctx context.Context, id primitive.ObjectID, base int, p PublishPatch) (bool, error)
	// Archive moves live → archived; false = not live.
	Archive(ctx context.Context, id primitive.ObjectID, by string, at time.Time) (bool, error)
	// DeleteUnpublished hard-deletes an unpublished warehouse; false = not
	// unpublished.
	DeleteUnpublished(ctx context.Context, id primitive.ObjectID) (bool, error)
	ListWarehouses(ctx context.Context, f WarehouseFilter, page, limit int) ([]domain.Warehouse, int64, error)
	// ListLive pages live warehouses by _id (public slugs / sitemap).
	ListLive(ctx context.Context, page, limit int) ([]domain.Warehouse, error)

	// revisions
	InsertRevision(ctx context.Context, r *domain.Revision) error // errDuplicate when one is already open
	GetRevision(ctx context.Context, id primitive.ObjectID) (*domain.Revision, error)
	// ReplaceRevision CAS-replaces r over the doc in (state, rev).
	ReplaceRevision(ctx context.Context, r *domain.Revision, state domain.RevisionState, rev int) error
	// Revisions lists a warehouse's revisions, newest first, without content.
	Revisions(ctx context.Context, warehouseID primitive.ObjectID) ([]domain.Revision, error)
	// ApprovedRevisions lists a warehouse's approved revisions (with content).
	ApprovedRevisions(ctx context.Context, warehouseID primitive.ObjectID) ([]domain.Revision, error)
	// Queue pages in-review revisions, oldest submission first (no content).
	Queue(ctx context.Context, page, limit int) ([]domain.Revision, int64, error)
	DeleteRevisions(ctx context.Context, warehouseID primitive.ObjectID) error

	// media
	InsertMedia(ctx context.Context, m *domain.Media) error
	GetMedia(ctx context.Context, id primitive.ObjectID) (*domain.Media, error)
	ReplaceMedia(ctx context.Context, m *domain.Media) error
	DeleteMedia(ctx context.Context, id primitive.ObjectID) error
	ListMedia(ctx context.Context, warehouseID primitive.ObjectID) ([]domain.Media, error)
	// CountPhotos counts a warehouse's pending + ready photos.
	CountPhotos(ctx context.Context, warehouseID primitive.ObjectID) (int64, error)
	// MediaForGC lists pending media created before pendingBefore, and
	// ready/orphaned media created before staleBefore.
	MediaForGC(ctx context.Context, pendingBefore, staleBefore time.Time) ([]domain.Media, error)
	// ReferencedMedia returns every media id a non-discarded revision
	// references (content.media or the agreement).
	ReferencedMedia(ctx context.Context) (map[primitive.ObjectID]bool, error)

	// rents
	UpsertRent(ctx context.Context, r domain.WarehouseRent) error

	BumpCatalogVersion(ctx context.Context) (int64, error)
}

// ensureIndexes creates the catalog's indexes (spec 03 Collections). Search
// indexes on `warehouses` belong to 04; `{needs_info}` to 02.
func ensureIndexes(ctx context.Context) error {
	sets := []struct {
		coll string
		idx  []mongo.IndexModel
	}{
		{domain.CollWarehouses, []mongo.IndexModel{
			{Keys: bson.D{{Key: "short_id", Value: 1}}, Options: options.Index().SetUnique(true)},
			{Keys: bson.D{{Key: "slug", Value: 1}}, Options: options.Index().SetUnique(true).
				SetPartialFilterExpression(bson.M{"slug": bson.M{"$gt": ""}})},
			{Keys: bson.D{{Key: "slug_history", Value: 1}}},
			{Keys: bson.D{{Key: "status", Value: 1}, {Key: "updated_at", Value: -1}}},
		}},
		{domain.CollRevisions, []mongo.IndexModel{
			// At most one open (draft / in_review) revision per warehouse.
			{Keys: bson.D{{Key: "warehouse_id", Value: 1}}, Options: options.Index().SetUnique(true).
				SetName("one_open_revision").SetPartialFilterExpression(bson.M{"open": true})},
			{Keys: bson.D{{Key: "state", Value: 1}, {Key: "submitted_at", Value: 1}}},
			{Keys: bson.D{{Key: "warehouse_id", Value: 1}, {Key: "version", Value: -1}}},
		}},
		{domain.CollMedia, []mongo.IndexModel{
			{Keys: bson.D{{Key: "warehouse_id", Value: 1}, {Key: "kind", Value: 1}}},
			{Keys: bson.D{{Key: "status", Value: 1}, {Key: "created_at", Value: 1}}},
		}},
		{domain.CollRents, []mongo.IndexModel{
			{Keys: bson.D{{Key: "warehouse_id", Value: 1}}, Options: options.Index().SetUnique(true)},
		}},
	}
	for _, s := range sets {
		if _, err := models.Collection(s.coll).Indexes().CreateMany(ctx, s.idx); err != nil {
			return fmt.Errorf("ensure %s indexes: %w", s.coll, err)
		}
	}
	return nil
}

type mongoStore struct{}

var _ Store = mongoStore{}

func whColl() *mongo.Collection    { return models.Collection(domain.CollWarehouses) }
func revColl() *mongo.Collection   { return models.Collection(domain.CollRevisions) }
func mediaColl() *mongo.Collection { return models.Collection(domain.CollMedia) }

func findOne[T any](ctx context.Context, coll *mongo.Collection, filter any, opts ...*options.FindOneOptions) (*T, error) {
	var out T
	err := coll.FindOne(ctx, filter, opts...).Decode(&out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func findAll[T any](ctx context.Context, coll *mongo.Collection, filter any, opts ...*options.FindOptions) ([]T, error) {
	cur, err := coll.Find(ctx, filter, opts...)
	if err != nil {
		return nil, err
	}
	out := []T{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func skip(page, limit int) int64 { return int64((page - 1) * limit) }

func (mongoStore) InsertWarehouse(ctx context.Context, w *domain.Warehouse) error {
	res, err := whColl().InsertOne(ctx, w)
	if mongo.IsDuplicateKeyError(err) {
		return errDuplicate
	}
	if err != nil {
		return err
	}
	w.ID = res.InsertedID.(primitive.ObjectID)
	return nil
}

func (mongoStore) GetWarehouse(ctx context.Context, id primitive.ObjectID) (*domain.Warehouse, error) {
	return findOne[domain.Warehouse](ctx, whColl(), bson.M{"_id": id})
}

func (mongoStore) GetWarehouseByShortID(ctx context.Context, shortID string) (*domain.Warehouse, error) {
	return findOne[domain.Warehouse](ctx, whColl(), bson.M{"short_id": shortID})
}

func (mongoStore) NextRevSeq(ctx context.Context, id primitive.ObjectID) (int, error) {
	var w struct {
		Seq int `bson:"rev_seq"`
	}
	err := whColl().FindOneAndUpdate(ctx, bson.M{"_id": id}, bson.M{"$inc": bson.M{"rev_seq": 1}},
		options.FindOneAndUpdate().SetReturnDocument(options.After).SetProjection(bson.M{"rev_seq": 1})).Decode(&w)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, errNotFound
	}
	return w.Seq, err
}

func (mongoStore) SetOpenRevision(ctx context.Context, id primitive.ObjectID, rev *primitive.ObjectID, at time.Time) error {
	_, err := whColl().UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"open_revision_id": rev, "updated_at": at}})
	return err
}

func (mongoStore) SetDraftName(ctx context.Context, id primitive.ObjectID, name, city string, at time.Time) error {
	_, err := whColl().UpdateOne(ctx, bson.M{"_id": id, "status": domain.WarehouseUnpublished},
		bson.M{"$set": bson.M{"name": name, "city": city, "updated_at": at}})
	return err
}

func (mongoStore) PublishLive(ctx context.Context, id primitive.ObjectID, base int, p PublishPatch) (bool, error) {
	set := bson.M{
		"live": p.Live, "live_version": p.LiveVersion, "status": domain.WarehouseLive, "open_revision_id": nil,
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
	res, err := whColl().UpdateOne(ctx, bson.M{"_id": id, "live_version": base}, update)
	if err != nil {
		return false, err
	}
	if res.MatchedCount == 0 {
		return false, nil
	}
	// First publish stamps published_at (idempotent).
	if _, err := whColl().UpdateOne(ctx, bson.M{"_id": id, "published_at": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"published_at": p.At}}); err != nil {
		return true, err
	}
	return true, nil
}

func (mongoStore) Archive(ctx context.Context, id primitive.ObjectID, by string, at time.Time) (bool, error) {
	res, err := whColl().UpdateOne(ctx, bson.M{"_id": id, "status": domain.WarehouseLive},
		bson.M{"$set": bson.M{"status": domain.WarehouseArchived, "archived_at": at, "archived_by": by, "updated_at": at}})
	if err != nil {
		return false, err
	}
	return res.MatchedCount == 1, nil
}

func (mongoStore) DeleteUnpublished(ctx context.Context, id primitive.ObjectID) (bool, error) {
	res, err := whColl().DeleteOne(ctx, bson.M{"_id": id, "status": domain.WarehouseUnpublished})
	if err != nil {
		return false, err
	}
	return res.DeletedCount == 1, nil
}

func (mongoStore) ListWarehouses(ctx context.Context, f WarehouseFilter, page, limit int) ([]domain.Warehouse, int64, error) {
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
	total, err := whColl().CountDocuments(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	items, err := findAll[domain.Warehouse](ctx, whColl(), q, options.Find().
		SetSort(bson.D{{Key: "updated_at", Value: -1}, {Key: "_id", Value: -1}}).
		SetSkip(skip(page, limit)).SetLimit(int64(limit)).
		SetProjection(bson.M{"live": 0, "embedding": 0}))
	return items, total, err
}

func (mongoStore) ListLive(ctx context.Context, page, limit int) ([]domain.Warehouse, error) {
	return findAll[domain.Warehouse](ctx, whColl(), bson.M{"status": domain.WarehouseLive}, options.Find().
		SetSort(bson.D{{Key: "_id", Value: 1}}).SetSkip(skip(page, limit)).SetLimit(int64(limit)).
		SetProjection(bson.M{"slug": 1, "updated_at": 1, "cover_key": 1, "short_id": 1}))
}

func (mongoStore) InsertRevision(ctx context.Context, r *domain.Revision) error {
	res, err := revColl().InsertOne(ctx, r)
	if mongo.IsDuplicateKeyError(err) {
		return errDuplicate
	}
	if err != nil {
		return err
	}
	r.ID = res.InsertedID.(primitive.ObjectID)
	return nil
}

func (mongoStore) GetRevision(ctx context.Context, id primitive.ObjectID) (*domain.Revision, error) {
	return findOne[domain.Revision](ctx, revColl(), bson.M{"_id": id})
}

func (mongoStore) ReplaceRevision(ctx context.Context, r *domain.Revision, state domain.RevisionState, rev int) error {
	res, err := revColl().ReplaceOne(ctx, bson.M{"_id": r.ID, "state": state, "rev": rev}, r)
	if mongo.IsDuplicateKeyError(err) {
		return errDuplicate
	}
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return errCAS
	}
	return nil
}

func (mongoStore) Revisions(ctx context.Context, warehouseID primitive.ObjectID) ([]domain.Revision, error) {
	return findAll[domain.Revision](ctx, revColl(), bson.M{"warehouse_id": warehouseID},
		options.Find().SetSort(bson.D{{Key: "version", Value: -1}}).SetProjection(bson.M{"content": 0}))
}

func (mongoStore) ApprovedRevisions(ctx context.Context, warehouseID primitive.ObjectID) ([]domain.Revision, error) {
	return findAll[domain.Revision](ctx, revColl(), bson.M{"warehouse_id": warehouseID, "state": domain.RevApproved})
}

func (mongoStore) Queue(ctx context.Context, page, limit int) ([]domain.Revision, int64, error) {
	q := bson.M{"state": domain.RevInReview}
	total, err := revColl().CountDocuments(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	items, err := findAll[domain.Revision](ctx, revColl(), q, options.Find().
		SetSort(bson.D{{Key: "submitted_at", Value: 1}, {Key: "_id", Value: 1}}).
		SetSkip(skip(page, limit)).SetLimit(int64(limit)).SetProjection(bson.M{"content": 0}))
	return items, total, err
}

func (mongoStore) DeleteRevisions(ctx context.Context, warehouseID primitive.ObjectID) error {
	_, err := revColl().DeleteMany(ctx, bson.M{"warehouse_id": warehouseID})
	return err
}

func (mongoStore) InsertMedia(ctx context.Context, m *domain.Media) error {
	if m.ID.IsZero() {
		m.ID = primitive.NewObjectID()
	}
	_, err := mediaColl().InsertOne(ctx, m)
	return err
}

func (mongoStore) GetMedia(ctx context.Context, id primitive.ObjectID) (*domain.Media, error) {
	return findOne[domain.Media](ctx, mediaColl(), bson.M{"_id": id})
}

func (mongoStore) ReplaceMedia(ctx context.Context, m *domain.Media) error {
	_, err := mediaColl().ReplaceOne(ctx, bson.M{"_id": m.ID}, m)
	return err
}

func (mongoStore) DeleteMedia(ctx context.Context, id primitive.ObjectID) error {
	_, err := mediaColl().DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (mongoStore) ListMedia(ctx context.Context, warehouseID primitive.ObjectID) ([]domain.Media, error) {
	return findAll[domain.Media](ctx, mediaColl(), bson.M{"warehouse_id": warehouseID, "status": bson.M{"$ne": domain.MediaOrphaned}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}))
}

func (mongoStore) CountPhotos(ctx context.Context, warehouseID primitive.ObjectID) (int64, error) {
	return mediaColl().CountDocuments(ctx, bson.M{"warehouse_id": warehouseID, "kind": domain.MediaPhoto,
		"status": bson.M{"$in": bson.A{domain.MediaPending, domain.MediaReady}}})
}

func (mongoStore) MediaForGC(ctx context.Context, pendingBefore, staleBefore time.Time) ([]domain.Media, error) {
	return findAll[domain.Media](ctx, mediaColl(), bson.M{"$or": bson.A{
		bson.M{"status": domain.MediaPending, "created_at": bson.M{"$lt": pendingBefore}},
		bson.M{"status": bson.M{"$in": bson.A{domain.MediaReady, domain.MediaOrphaned}}, "created_at": bson.M{"$lt": staleBefore}},
	}})
}

func (mongoStore) ReferencedMedia(ctx context.Context) (map[primitive.ObjectID]bool, error) {
	cur, err := revColl().Find(ctx, bson.M{"state": bson.M{"$ne": domain.RevDiscarded}},
		options.Find().SetProjection(bson.M{"content.media.media_id": 1, "content.rent_admin.agreement_media_id": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := map[primitive.ObjectID]bool{}
	for cur.Next(ctx) {
		var r domain.Revision
		if err := cur.Decode(&r); err != nil {
			return nil, err
		}
		for _, id := range referencedBy(r.Content) {
			out[id] = true
		}
	}
	return out, cur.Err()
}

func (mongoStore) UpsertRent(ctx context.Context, r domain.WarehouseRent) error {
	_, err := models.Collection(domain.CollRents).UpdateOne(ctx, bson.M{"warehouse_id": r.WarehouseID},
		bson.M{"$set": bson.M{"terms_version": r.TermsVersion, "headline": r.Headline, "admin": r.Admin, "effective_from": r.EffectiveFrom}},
		options.Update().SetUpsert(true))
	return err
}

func (mongoStore) BumpCatalogVersion(ctx context.Context) (int64, error) {
	return models.BumpCounter(ctx, models.CounterCatalogVersion)
}

// referencedBy lists the media ids a content references.
func referencedBy(c domain.Content) []primitive.ObjectID {
	out := make([]primitive.ObjectID, 0, len(c.Media)+1)
	for _, m := range c.Media {
		out = append(out, m.MediaID)
	}
	if c.RentAdmin != nil && c.RentAdmin.AgreementMediaID != nil {
		out = append(out, *c.RentAdmin.AgreementMediaID)
	}
	return out
}
