package domain

import (
	"context"
	"math"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Catalog shapes shared across modules (spec 03). The catalog module owns the
// collections; search (04), aisearch (05) and the attributes module's
// needs-info / strip jobs read or patch them through these names.

// Collections owned by the catalog module.
const (
	CollRevisions = "warehouse_revisions"
	CollRents     = "warehouse_rents"
	CollMedia     = "warehouse_media"
)

// Content is one version of a listing: the attribute data (02) plus media
// references and admin-only rent terms. Name, address, location, total area
// and the headline rent are fields of the Warehouse root node (D-142).
type Content struct {
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

// Rent bases (the root `rent` field's Money.Basis).
const (
	BasisPerSqftMonth = "per_sqft_month"
	BasisPerSqmMonth  = "per_sqm_month"
	BasisFlatMonth    = "flat_month"
)

// ValidBasis reports whether b is a rent basis.
func ValidBasis(b string) bool {
	return b == BasisPerSqftMonth || b == BasisPerSqmMonth || b == BasisFlatMonth
}

// Price is the normalized headline rent hoisted onto the live doc for search
// (D-058): per sq m per month in the listing's own currency, minor units.
type Price struct {
	Currency    string  `bson:"currency"       json:"currency"`
	PerSqmMonth float64 `bson:"per_sqm_month"  json:"perSqmMonth"`
	Basis       string  `bson:"basis"          json:"basis"`
	Approx      bool    `bson:"approx"         json:"approx"`
	OnRequest   bool    `bson:"on_request"     json:"onRequest"`
}

// NormalizePrice converts the headline rent (D-058, D-131): per sq ft × 10.7639,
// per sq m as is, flat ÷ total area (approx). ok is false when there is no
// usable price (unknown basis, or flat rent without a total area).
func NormalizePrice(m Money, totalSqm float64) (Price, bool) {
	p := Price{Currency: m.Currency, Basis: m.Basis}
	if m.OnRequest {
		p.OnRequest = true
		return p, true
	}
	amt := float64(m.Amount)
	switch m.Basis {
	case BasisPerSqmMonth:
		p.PerSqmMonth = amt
	case BasisPerSqftMonth:
		p.PerSqmMonth = amt / SqmPerSqft
	case BasisFlatMonth:
		if totalSqm <= 0 {
			return Price{}, false
		}
		p.PerSqmMonth, p.Approx = amt/totalSqm, true
	default:
		return Price{}, false
	}
	p.PerSqmMonth = math.Round(p.PerSqmMonth*100) / 100
	return p, true
}

// GeoPoint is a GeoJSON point ([lng, lat]) for the 2dsphere index (04).
type GeoPoint struct {
	Type        string     `bson:"type"        json:"type"`
	Coordinates [2]float64 `bson:"coordinates" json:"coordinates"`
}

// NewGeoPoint builds a GeoJSON point.
func NewGeoPoint(lat, lng float64) *GeoPoint {
	return &GeoPoint{Type: "Point", Coordinates: [2]float64{lng, lat}}
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
	Live           *Content            `bson:"live"             json:"live"`

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

// RevisionState is a revision's lifecycle state (spec 03).
type RevisionState string

const (
	RevDraft      RevisionState = "draft"
	RevInReview   RevisionState = "in_review"
	RevApproved   RevisionState = "approved"
	RevSuperseded RevisionState = "superseded"
	RevDiscarded  RevisionState = "discarded"
)

// IsOpen reports whether s is draft or in review (at most one per warehouse).
func (s RevisionState) IsOpen() bool { return s == RevDraft || s == RevInReview }

// ReviewEntry is one append-only review-history line (D-054).
type ReviewEntry struct {
	Action  string    `bson:"action"            json:"action"`
	By      string    `bson:"by"                json:"by"`
	At      time.Time `bson:"at"                json:"at"`
	Comment string    `bson:"comment,omitempty" json:"comment,omitempty"`
}

// Revision is a `warehouse_revisions` doc.
type Revision struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	WarehouseID primitive.ObjectID `bson:"warehouse_id"  json:"warehouseId"`
	Version     int                `bson:"version"       json:"version"`
	// BaseVersion is the live version this revision was cloned from (0 for
	// a never-published warehouse); approve CASes on it.
	BaseVersion int           `bson:"base_version" json:"baseVersion"`
	State       RevisionState `bson:"state"        json:"state"`
	// Open mirrors State.IsOpen() for the unique partial index.
	Open bool `bson:"open" json:"-"`
	// Rev is the save CAS counter (D-052).
	Rev         int           `bson:"rev"                json:"rev"`
	BatchID     string        `bson:"batch_id,omitempty" json:"batchId,omitempty"`
	Content     Content       `bson:"content"            json:"content"`
	Review      []ReviewEntry `bson:"review"             json:"review"`
	CreatedBy   string        `bson:"created_by"         json:"createdBy"`
	UpdatedBy   string        `bson:"updated_by"         json:"updatedBy"`
	CreatedAt   time.Time     `bson:"created_at"         json:"createdAt"`
	UpdatedAt   time.Time     `bson:"updated_at"         json:"updatedAt"`
	SubmittedBy string        `bson:"submitted_by,omitempty" json:"submittedBy,omitempty"`
	// SubmittedByID is the submitter's user id (self-approve check, D-053).
	SubmittedByID string     `bson:"submitted_by_id,omitempty" json:"-"`
	SubmittedAt   *time.Time `bson:"submitted_at,omitempty"    json:"submittedAt,omitempty"`
	ApprovedBy    string     `bson:"approved_by,omitempty"     json:"approvedBy,omitempty"`
	ApprovedAt    *time.Time `bson:"approved_at,omitempty"     json:"approvedAt,omitempty"`
}

// Media kinds, doc types and visibility (D-062).
const (
	MediaPhoto = "photo"
	MediaDoc   = "doc"

	DocFloorPlan   = "floor_plan"
	DocAgreement   = "agreement"
	DocCertificate = "certificate"
	DocOther       = "other"

	VisibilityPublic = "public"
	VisibilityStaff  = "staff"

	MediaPending  = "pending"
	MediaReady    = "ready"
	MediaOrphaned = "orphaned"
)

// Media is a `warehouse_media` doc. Keys are immutable and shared across
// revisions; nothing is copied on approve.
type Media struct {
	ID          primitive.ObjectID `bson:"_id,omitempty"      json:"id"`
	WarehouseID primitive.ObjectID `bson:"warehouse_id"       json:"warehouseId"`
	Kind        string             `bson:"kind"               json:"kind"`
	DocType     string             `bson:"doc_type,omitempty" json:"docType,omitempty"`
	Visibility  string             `bson:"visibility"         json:"visibility"`
	Bucket      string             `bson:"bucket"             json:"-"`
	Key         string             `bson:"key"                json:"key"`
	Filename    string             `bson:"filename"           json:"filename"`
	ContentType string             `bson:"content_type"       json:"contentType"`
	Bytes       int64              `bson:"bytes"              json:"bytes"`
	Status      string             `bson:"status"             json:"status"`
	UploadedBy  string             `bson:"uploaded_by"        json:"uploadedBy"`
	CreatedAt   time.Time          `bson:"created_at"         json:"createdAt"`
}

// WarehouseRent is a `warehouse_rents` doc: the live rent terms, copied on
// approve. Payments later reference warehouseId + termsVersion (PRD §4.4).
type WarehouseRent struct {
	ID            primitive.ObjectID `bson:"_id,omitempty"        json:"id"`
	WarehouseID   primitive.ObjectID `bson:"warehouse_id"         json:"warehouseId"`
	TermsVersion  int                `bson:"terms_version"        json:"termsVersion"`
	Headline      Money              `bson:"headline"             json:"headline"`
	Admin         *RentAdmin         `bson:"admin,omitempty"      json:"admin,omitempty"`
	EffectiveFrom time.Time          `bson:"effective_from"       json:"effectiveFrom"`
}

// ListingCard is the compact public listing used by the 410 page's nearby
// list (03) and search results (04).
type ListingCard struct {
	ShortID  string      `json:"shortId"`
	Slug     string      `json:"slug"`
	Name     string      `json:"name"`
	City     string      `json:"city,omitempty"`
	Locality string      `json:"locality,omitempty"`
	CoverURL string      `json:"coverUrl,omitempty"`
	TotalSqm float64     `json:"totalSqm"`
	Rate     *PublicRate `json:"rate,omitempty"`
}

// PublicRate is the public view of the headline rent.
type PublicRate struct {
	Amount      int64   `json:"amount"`
	Currency    string  `json:"currency"`
	Basis       string  `json:"basis,omitempty"`
	PerSqmMonth float64 `json:"perSqmMonth,omitempty"`
	Approx      bool    `json:"approx"`
	OnRequest   bool    `json:"onRequest"`
}

// SearchEngine is what the catalog needs from search (04): the nearest live
// listings for an archived listing's 410 page. Wired in cmd/service once
// search lands; nil means "no nearby list".
type SearchEngine interface {
	Nearest(ctx context.Context, loc GeoPoint, limit int, exclude primitive.ObjectID) ([]ListingCard, error)
}
