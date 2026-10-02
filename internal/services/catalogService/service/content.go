package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Root field keys the catalog reads (D-142).
const (
	fieldName        = "name"
	fieldDescription = "description"
	fieldAddress     = "address"
	fieldLocation    = "location"
	fieldTotalArea   = "total_area"
	fieldRent        = "rent"
)

// Limits on the non-attribute parts of a content.
const (
	maxCaptionLen   = 300
	maxTaxes        = 10
	maxOtherCharges = 20
)

// rootValue decodes a root field's stored value into T.
func rootValue[T any](a models.Attributes, field string) (T, bool) {
	var zero T
	fv := a.Value(domain.RootKey, field)
	if fv == nil || fv.V == nil {
		return zero, false
	}
	return domain.DecodeValue[T](fv.V)
}

// normalizeContent validates a draft's content and returns it in stored form
// (spec 03 "save draft"): attribute values canonicalized (02; required fields
// are not enforced here), media references checked against the warehouse's
// media, rent admin terms checked.
func normalizeContent(snap *domain.Snapshot, in models.ListingContent, media []models.WarehouseMedia) (models.ListingContent, error) {
	attrs, err := domain.CanonicalizeAttributes(snap, in.Attributes)
	if err != nil {
		return in, err
	}
	out := models.ListingContent{Attributes: attrs, Media: []models.MediaRef{}}

	byID := make(map[primitive.ObjectID]*models.WarehouseMedia, len(media))
	for i := range media {
		byID[media[i].ID] = &media[i]
	}
	seen := map[primitive.ObjectID]bool{}
	covers := 0
	for i, ref := range in.Media {
		m, ok := byID[ref.MediaID]
		if !ok || m.Status != models.MediaReady {
			return in, fmt.Errorf("media %s: not an uploaded file of this warehouse", ref.MediaID.Hex())
		}
		if seen[ref.MediaID] {
			return in, fmt.Errorf("media %s: listed twice", ref.MediaID.Hex())
		}
		seen[ref.MediaID] = true
		if ref.IsCover {
			if m.Kind != models.MediaPhoto || m.Visibility != models.VisibilityPublic {
				return in, fmt.Errorf("media %s: the cover must be a public photo", ref.MediaID.Hex())
			}
			covers++
		}
		ref.Caption = strings.TrimSpace(ref.Caption)
		if len([]rune(ref.Caption)) > maxCaptionLen {
			return in, fmt.Errorf("media %s: caption longer than %d characters", ref.MediaID.Hex(), maxCaptionLen)
		}
		ref.Order = i + 1
		out.Media = append(out.Media, ref)
	}
	if covers > 1 {
		return in, fmt.Errorf("only one cover photo")
	}

	if ra := in.RentAdmin; ra != nil {
		if err := checkRentAdmin(ra, byID); err != nil {
			return in, fmt.Errorf("rentAdmin: %w", err)
		}
		cp := *ra
		out.RentAdmin = &cp
	}
	return out, nil
}

func checkRentAdmin(ra *models.RentAdmin, media map[primitive.ObjectID]*models.WarehouseMedia) error {
	for _, m := range []*models.Money{ra.Deposit, ra.CAM} {
		if m != nil && !validMoney(*m) {
			return fmt.Errorf("money needs amount ≥ 0 and a 3-letter currency")
		}
	}
	if ra.LockInMonths < 0 || ra.EscalationEveryMonths < 0 || ra.LeaseTermMonths < 0 || ra.EscalationPct < 0 || ra.EscalationPct > 100 {
		return fmt.Errorf("months and percentages must be within range")
	}
	if len(ra.Taxes) > maxTaxes || len(ra.OtherCharges) > maxOtherCharges {
		return fmt.Errorf("at most %d taxes and %d other charges", maxTaxes, maxOtherCharges)
	}
	for _, t := range ra.Taxes {
		if strings.TrimSpace(t.Name) == "" || (t.Pct == nil) == (t.Money == nil) {
			return fmt.Errorf("each tax needs a name and either pct or money")
		}
		if t.Money != nil && !validMoney(*t.Money) {
			return fmt.Errorf("tax %q: bad money", t.Name)
		}
	}
	for _, c := range ra.OtherCharges {
		if strings.TrimSpace(c.Label) == "" || !validMoney(c.Money) {
			return fmt.Errorf("each charge needs a label and valid money")
		}
	}
	if id := ra.AgreementMediaID; id != nil {
		m, ok := media[*id]
		if !ok || m.Status != models.MediaReady || m.DocType != models.DocAgreement {
			return fmt.Errorf("agreementMediaId must be an uploaded agreement of this warehouse")
		}
	}
	return nil
}

func validMoney(m models.Money) bool {
	if m.Amount < 0 || len(m.Currency) != 3 {
		return false
	}
	return strings.ToUpper(m.Currency) == m.Currency
}

// submitProblems is the submit gate (spec 03): required fields on yes nodes
// (02), a precise pin, total area > 0, a cover photo, a usable rent.
func submitProblems(snap *domain.Snapshot, c models.ListingContent, media []models.WarehouseMedia) []string {
	out := domain.SubmitProblems(snap, c.Attributes)
	a := c.Attributes
	if loc, ok := rootValue[models.Location](a, fieldLocation); ok && strings.EqualFold(loc.Accuracy, interfaces.AccuracyApproximate) {
		out = append(out, "warehouse.location: the pin is only approximate — move it onto the building (D-061)")
	}
	if area, ok := rootValue[models.Area](a, fieldTotalArea); ok && area.Sqm <= 0 {
		out = append(out, "warehouse.total_area: must be greater than 0")
	}
	if rent, ok := rootValue[models.Money](a, fieldRent); ok && !rent.OnRequest && !domain.ValidBasis(rent.Basis) {
		out = append(out, fmt.Sprintf("warehouse.rent: basis must be %s, %s or %s (or price on request)",
			domain.BasisPerSqftMonth, domain.BasisPerSqmMonth, domain.BasisFlatMonth))
	}
	if coverOf(c, media) == nil {
		out = append(out, "media: a cover photo is required (D-062)")
	}
	return out
}

// coverOf returns the content's ready cover photo, if any.
func coverOf(c models.ListingContent, media []models.WarehouseMedia) *models.WarehouseMedia {
	for _, ref := range c.Media {
		if !ref.IsCover {
			continue
		}
		for i := range media {
			if media[i].ID == ref.MediaID && media[i].Status == models.MediaReady && media[i].Kind == models.MediaPhoto {
				return &media[i]
			}
		}
	}
	return nil
}

// draftName returns the name and city shown in admin lists.
func draftName(a models.Attributes) (string, string) {
	name, _ := rootValue[string](a, fieldName)
	addr, _ := rootValue[models.Address](a, fieldAddress)
	return name, addr.City
}

// buildPublish evaluates a content and builds the live-doc patch (Approve
// steps 2–3).
func buildPublish(snap *domain.Snapshot, c models.ListingContent, media []models.WarehouseMedia, version int, shortID string, now time.Time) models.WarehousePublish {
	res := domain.Evaluate(snap, c.Attributes, now)
	comp, ver := domain.Completeness(snap, c.Attributes, res)
	p := models.WarehousePublish{
		Live:          c,
		LiveVersion:   version,
		Projection:    res.Projection,
		Completeness:  round(comp, 4),
		VerifiedRatio: round(ver, 4),
		At:            now,
	}
	a := c.Attributes
	p.Name, _ = rootValue[string](a, fieldName)
	if addr, ok := rootValue[models.Address](a, fieldAddress); ok {
		p.Country, p.PostalCode, p.City, p.Locality = addr.Country, addr.PostalCode, addr.City, addr.Locality
	}
	if loc, ok := rootValue[models.Location](a, fieldLocation); ok {
		p.Loc = domain.NewGeoPoint(loc.Lat, loc.Lng)
	}
	if area, ok := rootValue[models.Area](a, fieldTotalArea); ok {
		p.TotalSqm = area.Sqm
	}
	if rent, ok := rootValue[models.Money](a, fieldRent); ok {
		if price, ok := domain.NormalizePrice(rent, p.TotalSqm); ok {
			p.Price = &price
		}
	}
	if cover := coverOf(c, media); cover != nil {
		p.CoverKey = cover.Key
	}
	p.Slug = slugFor(p.Name, p.City, shortID)
	return p
}

func round(v float64, places int) float64 {
	f := math.Pow10(places)
	return math.Round(v*f) / f
}

// addressHash fingerprints an address for the geocoding rules (D-061).
func addressHash(a models.Address) string {
	parts := []string{a.Line1, a.Line2, a.Locality, a.City, a.Region, a.PostalCode, a.Country}
	for i := range parts {
		parts[i] = strings.ToLower(strings.Join(strings.Fields(parts[i]), " "))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:8])
}

// addressLine is the one-line address sent to the geocoder.
func addressLine(a models.Address) string {
	parts := slices.DeleteFunc([]string{a.Line1, a.Line2, a.Locality, a.City, a.Region, a.PostalCode},
		func(s string) bool { return strings.TrimSpace(s) == "" })
	return strings.Join(parts, ", ")
}
