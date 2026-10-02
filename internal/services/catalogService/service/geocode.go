package service

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/catalogService"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// geocodeOnSave applies D-061 to a draft being saved (content already
// normalized):
//   - a pin the editor placed by hand is kept; if the address changed since,
//     the save warns;
//   - otherwise a changed address is geocoded; on failure the old pin (or
//     none) stays and a retry is queued.
func (s *svc) geocodeOnSave(ctx context.Context, old *models.WarehouseRevision, c *models.ListingContent) []string {
	root, ok := c.Attributes[domain.RootKey]
	if !ok {
		return nil
	}
	addr, ok := rootValue[models.Address](c.Attributes, fieldAddress)
	if !ok {
		return nil
	}
	h := addressHash(addr)
	loc, hasLoc := rootValue[models.Location](c.Attributes, fieldLocation)
	prev, hadPrev := rootValue[models.Location](old.Content.Attributes, fieldLocation)

	if hasLoc && loc.Source == domain.LocationManual {
		moved := !hadPrev || prev.Source != domain.LocationManual || prev.Lat != loc.Lat || prev.Lng != loc.Lng
		if moved || loc.AddressHash == "" {
			loc.AddressHash = h // a fresh hand-placed pin belongs to this address
			setLocation(root, loc)
			return nil
		}
		if loc.AddressHash != h {
			return []string{catalogService.WarnPinManual}
		}
		return nil
	}
	if hasLoc && loc.AddressHash == h {
		return nil
	}
	g := s.geocoder()
	if g == nil {
		return []string{catalogService.WarnGeocodeOff}
	}
	res, err := g.Geocode(ctx, addressLine(addr), addr.Country)
	switch {
	case errors.Is(err, interfaces.ErrGeocodeNotFound):
		return []string{catalogService.WarnAddressNotFound}
	case err != nil:
		log.Warn("catalog: geocode on save failed, queueing retry", "revision", old.ID.Hex(), "error", err)
		key := fmt.Sprintf("geocode:%s:%s", old.ID.Hex(), h)
		if derr := s.dispatch(ctx, catalogService.ProcessGeocodeRetry, key, catalogService.GeocodeRetryPayload{RevisionID: old.ID.Hex(), AddressHash: h}); derr != nil {
			log.Error("catalog: geocode retry dispatch failed", "revision", old.ID.Hex(), "error", derr)
		}
		return []string{catalogService.WarnGeocodePending}
	}
	setLocation(root, locationFrom(res, h))
	return nil
}

func locationFrom(res interfaces.GeocodeResult, hash string) models.Location {
	return models.Location{Lat: res.Lat, Lng: res.Lng, Accuracy: res.Accuracy, Source: domain.LocationGeocoded,
		PlaceID: res.PlaceID, AddressHash: hash}
}

func setLocation(root models.NodeState, loc models.Location) {
	fv := root.Fields[fieldLocation]
	if fv == nil {
		fv = &models.FieldValue{}
	} else {
		cp := *fv
		fv = &cp
	}
	fv.V = loc
	root.Fields[fieldLocation] = fv
}

// GeocodeRetry is the catalog.geocode_retry handler. It writes the pin
// without bumping rev (no editor edit is overwritten: an editor's later save
// re-checks the address anyway). Stale messages are dropped.
func (s *svc) GeocodeRetry(ctx context.Context, p catalogService.GeocodeRetryPayload) error {
	id, err := primitive.ObjectIDFromHex(p.RevisionID)
	if err != nil {
		return fmt.Errorf("geocode_retry: bad id: %w", pipeline.ErrPermanent)
	}
	r, err := s.store.GetRevision(ctx, id)
	if errors.Is(err, catalogService.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !r.State.IsOpen() {
		return nil
	}
	addr, ok := rootValue[models.Address](r.Content.Attributes, fieldAddress)
	if !ok || addressHash(addr) != p.AddressHash {
		return nil // address changed again since; that save handles it
	}
	if loc, ok := rootValue[models.Location](r.Content.Attributes, fieldLocation); ok &&
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
func (s *svc) GeocodePreview(ctx context.Context, addr models.Address) (models.Location, error) {
	g := s.geocoder()
	if g == nil {
		return models.Location{}, catalogService.Conflict(catalogService.CodeGeocodingOff, "geocoding is not configured")
	}
	res, err := g.Geocode(ctx, addressLine(addr), addr.Country)
	if errors.Is(err, interfaces.ErrGeocodeNotFound) {
		return models.Location{}, catalogService.ErrNotFound
	}
	if err != nil {
		return models.Location{}, err
	}
	return locationFrom(res, addressHash(addr)), nil
}
