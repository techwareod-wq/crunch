package models

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// SearchEventTTL is how long raw search events are kept (D-106).
const SearchEventTTL = 90 * 24 * time.Hour

// Search event kinds.
const (
	SearchKindStructured = "structured"
	SearchKindAI         = "ai"
)

// SearchEvent is a `search_events` doc (spec 07): one page-1 search. The _id
// is the searchId returned to the client; enquiries carry it back (D-105).
// Dropped by the TTL index after 90 days.
type SearchEvent struct {
	ID        primitive.ObjectID  `bson:"_id"                  json:"id"`
	At        time.Time           `bson:"at"                   json:"at"`
	SessionID string              `bson:"session_id,omitempty" json:"sessionId,omitempty"`
	UserID    *primitive.ObjectID `bson:"user_id,omitempty"    json:"userId,omitempty"`
	// IsStaff: an admin or superuser searched; written but left out of
	// rollups and dashboards (D-106).
	IsStaff bool   `bson:"is_staff" json:"isStaff"`
	Kind    string `bson:"kind"     json:"kind"`
	// RawText is what was typed; NormQuery its lowercased, collapsed form
	// (the dashboards group on it).
	RawText   string `bson:"raw_text,omitempty"   json:"rawText,omitempty"`
	NormQuery string `bson:"norm_query,omitempty" json:"normQuery,omitempty"`
	// Filters are the applied SearchFilters (camelCase keys, as the API
	// returns them).
	Filters       bson.M             `bson:"filters"                  json:"filters"`
	ResolvedPoint *SearchEventPoint  `bson:"resolved_point,omitempty" json:"resolvedPoint,omitempty"`
	GeocodeSource string             `bson:"geocode_source,omitempty" json:"geocodeSource,omitempty"`
	PlaceLabel    string             `bson:"place_label,omitempty"    json:"placeLabel,omitempty"`
	Radius        *SearchEventRadius `bson:"radius,omitempty"         json:"radius,omitempty"`
	ResultCount   int64              `bson:"result_count"             json:"resultCount"`
	FallbackUsed  bool               `bson:"fallback_used"            json:"fallbackUsed"`
	// FallbackReason / FallbackCount describe the similar matches (05).
	FallbackReason string         `bson:"fallback_reason,omitempty" json:"fallbackReason,omitempty"`
	FallbackCount  int            `bson:"fallback_count,omitempty"  json:"fallbackCount,omitempty"`
	AI             *SearchEventAI `bson:"ai,omitempty"              json:"ai,omitempty"`
	LatencyMs      int64          `bson:"latency_ms"                json:"latencyMs"`
	Page           int            `bson:"page"                      json:"page"`
	Country        string         `bson:"country"                   json:"country"`
	Degraded       []string       `bson:"degraded"                  json:"degraded"`
}

// SearchEventPoint is the resolved search location.
type SearchEventPoint struct {
	Lat float64 `bson:"lat" json:"lat"`
	Lng float64 `bson:"lng" json:"lng"`
}

// SearchEventRadius is the ring the search used (D-071).
type SearchEventRadius struct {
	RequestedKm int  `bson:"requested_km" json:"requestedKm"`
	UsedKm      int  `bson:"used_km"      json:"usedKm"`
	Expanded    bool `bson:"expanded"     json:"expanded"`
	Exhausted   bool `bson:"exhausted"    json:"exhausted"`
}

// SearchEventAI is the natural-language parse (05).
type SearchEventAI struct {
	Parsed    bool     `bson:"parsed"     json:"parsed"`
	Model     string   `bson:"model"      json:"model"`
	LatencyMs int64    `bson:"latency_ms" json:"latencyMs"`
	Notes     []string `bson:"notes"      json:"notes"`
}

// SearchEventRef is the slice of an event the conversion join reads.
type SearchEventRef struct {
	ID         primitive.ObjectID `bson:"_id"`
	NormQuery  string             `bson:"norm_query"`
	PlaceLabel string             `bson:"place_label"`
	IsStaff    bool               `bson:"is_staff"`
}

// SearchEventFilter narrows the raw log. Zero fields are ignored.
type SearchEventFilter struct {
	// Q is a case-insensitive substring of the normalized query.
	Q    string
	Zero bool
	From *time.Time
	To   *time.Time
}

func searchEvents() *mongo.Collection { return Collection(searchEventsCollection) }

// EnsureSearchEventIndexes: the 90-day TTL (D-106), the query / zero-result
// views and the per-session trail.
func EnsureSearchEventIndexes(ctx context.Context) error {
	_, err := searchEvents().Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "at", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(int32(SearchEventTTL / time.Second))},
		{Keys: bson.D{{Key: "norm_query", Value: 1}, {Key: "at", Value: -1}}},
		{Keys: bson.D{{Key: "result_count", Value: 1}, {Key: "at", Value: -1}}},
		{Keys: bson.D{{Key: "session_id", Value: 1}, {Key: "at", Value: -1}}},
		{Keys: bson.D{{Key: "user_id", Value: 1}}},
	})
	if err != nil {
		return fmt.Errorf("ensure search_events indexes: %w", err)
	}
	return nil
}

// InsertSearchEvent writes one event (its _id is the searchId).
func InsertSearchEvent(ctx context.Context, e *SearchEvent) error {
	_, err := searchEvents().InsertOne(ctx, e)
	return err
}

// FindSearchEventByID reads one event; ErrNotFound when missing or expired.
func FindSearchEventByID(ctx context.Context, id primitive.ObjectID) (*SearchEvent, error) {
	return findOneDoc[SearchEvent](ctx, searchEvents(), bson.M{"_id": id})
}

// EachSearchEvent streams every event with at in [from, to), oldest first.
func EachSearchEvent(ctx context.Context, from, to time.Time, fn func(SearchEvent) error) error {
	cur, err := searchEvents().Find(ctx, bson.M{"at": bson.M{"$gte": from.UTC(), "$lt": to.UTC()}},
		options.Find().SetSort(bson.D{{Key: "at", Value: 1}}).SetProjection(bson.M{"filters": 0}))
	if err != nil {
		return err
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var e SearchEvent
		if err := cur.Decode(&e); err != nil {
			return err
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return cur.Err()
}

// SearchEventRefs reads the query + place of each event in ids (expired
// ones are simply missing).
func SearchEventRefs(ctx context.Context, ids []primitive.ObjectID) ([]SearchEventRef, error) {
	if len(ids) == 0 {
		return []SearchEventRef{}, nil
	}
	return findAllDocs[SearchEventRef](ctx, searchEvents(), bson.M{"_id": bson.M{"$in": ids}},
		options.Find().SetProjection(bson.M{"norm_query": 1, "place_label": 1, "is_staff": 1}))
}

// ListSearchEvents pages raw events newest first (staff included, flagged).
func ListSearchEvents(ctx context.Context, f SearchEventFilter, page, limit int) ([]SearchEvent, int64, error) {
	q := bson.M{}
	if f.Q != "" {
		q["norm_query"] = primitive.Regex{Pattern: regexp.QuoteMeta(f.Q), Options: "i"}
	}
	if f.Zero {
		q["result_count"] = 0
	}
	if f.From != nil || f.To != nil {
		at := bson.M{}
		if f.From != nil {
			at["$gte"] = f.From.UTC()
		}
		if f.To != nil {
			at["$lt"] = f.To.UTC()
		}
		q["at"] = at
	}
	total, err := searchEvents().CountDocuments(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	items, err := findAllDocs[SearchEvent](ctx, searchEvents(), q, options.Find().
		SetSort(bson.D{{Key: "at", Value: -1}, {Key: "_id", Value: -1}}).
		SetSkip(skipFor(page, limit)).SetLimit(int64(limit)))
	return items, total, err
}

// UnsetSearchEventsUser drops the user id from the user's events (D-019).
// Zero-match-OK; returns how many were changed.
func UnsetSearchEventsUser(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	res, err := searchEvents().UpdateMany(ctx, bson.M{"user_id": userID}, bson.M{"$unset": bson.M{"user_id": ""}})
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}
