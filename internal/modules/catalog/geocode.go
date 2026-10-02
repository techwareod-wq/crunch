package catalog

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// ProcessGeocodeRetry re-geocodes a draft whose address couldn't be geocoded
// on save (D-061). SQS retries give the backoff.
const ProcessGeocodeRetry pipeline.ProcessType = "catalog.geocode_retry"

// Save warnings (D-061).
const (
	warnPinManual       = "address_changed_pin_manual"
	warnGeocodeOff      = "geocoding_unavailable"
	warnAddressNotFound = "address_not_found"
	warnGeocodePending  = "geocode_pending"
)

// GeocodeRetryPayload is the catalog.geocode_retry body.
type GeocodeRetryPayload struct {
	RevisionID  string `json:"revisionId"`
	AddressHash string `json:"addressHash"`
}

// geocodeOnSave applies D-061 to a draft being saved (content already
// normalized):
//   - a pin the editor placed by hand is kept; if the address changed since,
//     the save warns;
//   - otherwise a changed address is geocoded; on failure the old pin (or
//     none) stays and a retry is queued.
func (s *Service) geocodeOnSave(ctx context.Context, old *domain.Revision, c *domain.Content) []string {
	root, ok := c.Attributes[domain.RootKey]
	if !ok {
		return nil
	}
	addr, ok := rootValue[domain.Address](c.Attributes, fieldAddress)
	if !ok {
		return nil
	}
	h := addressHash(addr)
	loc, hasLoc := rootValue[domain.Location](c.Attributes, fieldLocation)
	prev, hadPrev := rootValue[domain.Location](old.Content.Attributes, fieldLocation)

	if hasLoc && loc.Source == domain.LocationManual {
		moved := !hadPrev || prev.Source != domain.LocationManual || prev.Lat != loc.Lat || prev.Lng != loc.Lng
		if moved || loc.AddressHash == "" {
			loc.AddressHash = h // a fresh hand-placed pin belongs to this address
			setLocation(root, loc)
			return nil
		}
		if loc.AddressHash != h {
			return []string{warnPinManual}
		}
		return nil
	}
	if hasLoc && loc.AddressHash == h {
		return nil
	}
	g := s.geocoder()
	if g == nil {
		return []string{warnGeocodeOff}
	}
	res, err := g.Geocode(ctx, addressLine(addr), addr.Country)
	switch {
	case errors.Is(err, interfaces.ErrGeocodeNotFound):
		return []string{warnAddressNotFound}
	case err != nil:
		log.Warn("catalog: geocode on save failed, queueing retry", "revision", old.ID.Hex(), "error", err)
		key := fmt.Sprintf("geocode:%s:%s", old.ID.Hex(), h)
		if derr := s.dispatch(ctx, ProcessGeocodeRetry, key, GeocodeRetryPayload{RevisionID: old.ID.Hex(), AddressHash: h}); derr != nil {
			log.Error("catalog: geocode retry dispatch failed", "revision", old.ID.Hex(), "error", derr)
		}
		return []string{warnGeocodePending}
	}
	setLocation(root, locationFrom(res, h))
	return nil
}

func locationFrom(res interfaces.GeocodeResult, hash string) domain.Location {
	return domain.Location{Lat: res.Lat, Lng: res.Lng, Accuracy: res.Accuracy, Source: domain.LocationGeocoded,
		PlaceID: res.PlaceID, AddressHash: hash}
}

func setLocation(root domain.NodeState, loc domain.Location) {
	fv := root.Fields[fieldLocation]
	if fv == nil {
		fv = &domain.FieldValue{}
	} else {
		cp := *fv
		fv = &cp
	}
	fv.V = loc
	root.Fields[fieldLocation] = fv
}

// geocodeRetry is the catalog.geocode_retry handler. It writes the pin
// without bumping rev (no editor edit is overwritten: an editor's later save
// re-checks the address anyway). Stale messages are dropped.
func (s *Service) geocodeRetry(ctx context.Context, p GeocodeRetryPayload) error {
	id, err := primitive.ObjectIDFromHex(p.RevisionID)
	if err != nil {
		return fmt.Errorf("geocode_retry: bad id: %w", pipeline.ErrPermanent)
	}
	r, err := s.store.GetRevision(ctx, id)
	if errors.Is(err, errNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !r.State.IsOpen() {
		return nil
	}
	addr, ok := rootValue[domain.Address](r.Content.Attributes, fieldAddress)
	if !ok || addressHash(addr) != p.AddressHash {
		return nil // address changed again since; that save handles it
	}
	if loc, ok := rootValue[domain.Location](r.Content.Attributes, fieldLocation); ok &&
		(loc.Source == domain.LocationManual || loc.AddressHash == p.AddressHash) {
		return nil
	}
	g := s.geocoder()
	if g == nil {
		return nil
	}
	res, err := g.Geocode(ctx, addressLine(addr), addr.Country)
	if errors.Is(err, interfaces.ErrGeocodeNotFound) {
		log.Info("catalog: geocode retry found nothing", "revision", p.RevisionID)
		return nil
	}
	if err != nil {
		return fmt.Errorf("geocode_retry: %w", err)
	}
	upd := cloneRevision(r)
	upd.Content = cloneContent(r.Content)
	setLocation(upd.Content.Attributes[domain.RootKey], locationFrom(res, p.AddressHash))
	if err := s.store.ReplaceRevision(ctx, &upd, r.State, r.Rev); err != nil {
		return fmt.Errorf("geocode_retry: write: %w", err) // retried; a CAS miss re-reads
	}
	s.record(ctx, domain.SystemActor, domain.EntityRevision, r.ID.Hex(), domain.ActionUpdate, r, &upd, map[string]any{"op": "geocode_retry"})
	return nil
}

// GeocodePreview geocodes an address for the editor's map (no write).
func (s *Service) GeocodePreview(ctx context.Context, addr domain.Address) (domain.Location, error) {
	g := s.geocoder()
	if g == nil {
		return domain.Location{}, conflict(warnGeocodeOff, "geocoding is not configured")
	}
	res, err := g.Geocode(ctx, addressLine(addr), addr.Country)
	if errors.Is(err, interfaces.ErrGeocodeNotFound) {
		return domain.Location{}, errNotFound
	}
	if err != nil {
		return domain.Location{}, err
	}
	return locationFrom(res, addressHash(addr)), nil
}
