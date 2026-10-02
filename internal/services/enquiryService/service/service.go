package service

import (
	"context"
	"errors"
	"html"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/accountService"
	"github.com/atharva-ng/crunch/internal/services/enquiryService"
	"github.com/atharva-ng/crunch/internal/services/enquiryService/dto"
	"github.com/atharva-ng/crunch/internal/services/enquiryService/store"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/utils"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

var _ enquiryService.EnquiryService = (*svc)(nil)

// Field limits (characters).
const (
	maxNameLen      = 200
	maxCompanyLen   = 200
	minMessageLen   = 10
	maxKeyLen       = 128
	maxSessionLen   = 128
	maxCloseNoteLen = 1000
	maxNoteLen      = 4000
	// defaultCountry is the enquiry country without a listing (India-only
	// launch, D-003).
	defaultCountry = "IN"
	// listingPathPrefix is the public listing path (spec 03 Slugs).
	listingPathPrefix = "/warehouses/"
)

type svc struct {
	store      store.Store
	log        domain.ChangeLog
	cfg        func() config.EnquiriesValues
	publicBase func() string
	mediaBase  func() string
	now        func() time.Time
}

// NewService builds the enquiry service.
func NewService(st store.Store, changeLog domain.ChangeLog, wh config.WarehouseHubValues, aws config.AWSConfig) enquiryService.EnquiryService {
	return &svc{
		store: st, log: changeLog,
		cfg:        func() config.EnquiriesValues { return wh.Enquiries },
		publicBase: func() string { return wh.PublicBaseURL },
		mediaBase:  func() string { return aws.PublicMediaBaseURL },
		now:        time.Now,
	}
}

// stamp is a write time at Mongo's millisecond precision, so the
// updatedAt a client reads back matches the CAS filter exactly.
func (s *svc) stamp() time.Time {
	return s.now().UTC().Truncate(time.Millisecond)
}

func (s *svc) record(ctx context.Context, actor domain.Actor, id primitive.ObjectID, action domain.ChangeAction, before, after any, meta map[string]any) {
	s.log.Record(ctx, domain.ChangeEntry{Entity: domain.EntityEnquiry, EntityID: id.Hex(), Action: action, Actor: actor,
		Before: before, After: after, Meta: meta})
}

// clean undoes the request deserializer's HTML escaping, trims and
// collapses whitespace (single-line fields).
func clean(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}

// --- submit ---

// Submit stores a visitor's enquiry.
//
// D-018: no captcha and no rate limit — Clerk sign-in is the only gate.
func (s *svc) Submit(ctx context.Context, u *models.User, req enquiryService.SubmitRequest) (enquiryService.SubmitResult, error) {
	email := strings.ToLower(strings.TrimSpace(u.Email))
	if email == "" {
		return enquiryService.SubmitResult{}, enquiryService.Invalidf("your account has no email address — add one to send an enquiry")
	}
	key := strings.TrimSpace(req.IdempotencyKey)
	if key == "" || len(key) > maxKeyLen {
		return enquiryService.SubmitResult{}, enquiryService.Invalidf("idempotencyKey is required (at most %d characters)", maxKeyLen)
	}
	existing, err := s.store.GetByIdempotencyKey(ctx, u.ID, key)
	if err == nil {
		return enquiryService.SubmitResult{ID: existing.ID.Hex()}, nil
	}
	if !errors.Is(err, models.ErrNotFound) {
		return enquiryService.SubmitResult{}, err
	}

	name := clean(req.Name)
	if name == "" {
		name = clean(u.Name)
	}
	if name == "" {
		return enquiryService.SubmitResult{}, enquiryService.Invalidf("name is required")
	}
	if utf8.RuneCountInString(name) > maxNameLen {
		return enquiryService.SubmitResult{}, enquiryService.Invalidf("name is too long (at most %d characters)", maxNameLen)
	}
	company := clean(req.Company)
	if utf8.RuneCountInString(company) > maxCompanyLen {
		return enquiryService.SubmitResult{}, enquiryService.Invalidf("company is too long (at most %d characters)", maxCompanyLen)
	}
	phone := clean(req.Phone)
	if phone == "" {
		return enquiryService.SubmitResult{}, enquiryService.Invalidf("phone is required")
	}
	phoneE164, err := utils.NormalizePhone(phone, utils.DefaultPhoneRegion)
	if err != nil {
		return enquiryService.SubmitResult{}, enquiryService.Invalidf("phone is not a valid number")
	}
	message := strings.TrimSpace(html.UnescapeString(req.Message))
	maxMessage := s.cfg().MessageMaxLen
	if maxMessage <= 0 {
		maxMessage = 4000
	}
	if n := utf8.RuneCountInString(message); n < minMessageLen || n > maxMessage {
		return enquiryService.SubmitResult{}, enquiryService.Invalidf("message must be %d to %d characters", minMessageLen, maxMessage)
	}

	now := s.stamp()
	e := &models.Enquiry{
		UserID: u.ID, ClerkID: u.ClerkID,
		Name: name, Company: company, Email: email, Phone: phone, PhoneE164: phoneE164, Message: message,
		Country: defaultCountry, Status: models.EnquiryNew,
		Notes: []models.EnquiryNote{}, History: []models.EnquiryChange{},
		IdempotencyKey: key, CreatedAt: now, UpdatedAt: now,
	}
	if sid := strings.ToLower(strings.TrimSpace(req.ListingShortID)); sid != "" {
		w, err := s.store.WarehouseByShortID(ctx, sid)
		if err != nil {
			return enquiryService.SubmitResult{}, err
		}
		switch w.Status {
		case models.WarehouseLive:
		case models.WarehouseArchived:
			return enquiryService.SubmitResult{}, enquiryService.ErrListingGone
		default:
			return enquiryService.SubmitResult{}, enquiryService.ErrNotFound
		}
		e.Listing = &models.EnquiryListing{WarehouseID: w.ID, ShortID: w.ShortID, Slug: w.Slug, Name: w.Name, City: w.City}
		if w.Country != "" {
			e.Country = w.Country
		}
	}
	// The search / session ids are analytics only: a malformed one is
	// dropped, never refused (D-105).
	if id, err := primitive.ObjectIDFromHex(strings.TrimSpace(req.SearchID)); err == nil {
		e.SearchID = &id
	}
	if sess := strings.TrimSpace(req.SessionID); len(sess) <= maxSessionLen {
		e.SessionID = sess
	}

	if err := s.store.Insert(ctx, e); err != nil {
		if !errors.Is(err, models.ErrDuplicateKey) {
			return enquiryService.SubmitResult{}, err
		}
		// A concurrent retry with the same key won the insert.
		existing, err := s.store.GetByIdempotencyKey(ctx, u.ID, key)
		if err != nil {
			return enquiryService.SubmitResult{}, err
		}
		return enquiryService.SubmitResult{ID: existing.ID.Hex()}, nil
	}

	// Prefill for next time (P-4). The enquiry stands even if this fails.
	if phoneE164 != u.PhoneE164 || company != u.Company {
		if err := s.store.UpdateUserProfile(ctx, u.ID, phone, phoneE164, company, now); err != nil {
			log.Warn("enquiries: profile save failed", "user", u.ID.Hex(), "error", err)
		}
	}
	// Sales alert + visitor confirmation emails (D-103) are deferred until
	// email is re-added; enquiries land in the inbox only.
	s.record(ctx, domain.Actor{UserID: u.ID.Hex(), Email: email}, e.ID, domain.ActionCreate, nil, e, nil)
	return enquiryService.SubmitResult{ID: e.ID.Hex(), Created: true}, nil
}

// --- inbox reads ---

func (s *svc) List(ctx context.Context, f models.EnquiryFilter, page, limit int) ([]models.Enquiry, int64, error) {
	return s.store.List(ctx, f, page, limit)
}

func (s *svc) Export(ctx context.Context, f models.EnquiryFilter) ([]models.Enquiry, error) {
	limit := s.cfg().ExportMax
	if limit <= 0 {
		limit = 10000
	}
	return s.store.Export(ctx, f, limit)
}

func (s *svc) Detail(ctx context.Context, id primitive.ObjectID) (*dto.EnquiryDetail, error) {
	e, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	d := &dto.EnquiryDetail{Enquiry: e}
	if e.Listing != nil {
		w, err := s.store.Warehouse(ctx, e.Listing.WarehouseID)
		switch {
		case err == nil:
			d.Listing = s.listingCard(w)
		case !errors.Is(err, models.ErrNotFound):
			return nil, err
		}
	}
	if e.SearchID != nil {
		ev, err := s.store.SearchEvent(ctx, *e.SearchID)
		switch {
		case err == nil:
			d.Search = &dto.OriginSearch{SearchID: ev.ID.Hex(), At: ev.At, Kind: ev.Kind, Text: ev.RawText,
				Place: ev.PlaceLabel, Filters: ev.Filters, ResultCount: ev.ResultCount}
		case !errors.Is(err, models.ErrNotFound):
			return nil, err
		}
	}
	return d, nil
}

func (s *svc) listingCard(w *models.Warehouse) *dto.ListingCard {
	c := &dto.ListingCard{
		WarehouseID: w.ID.Hex(), ShortID: w.ShortID, Slug: w.Slug, Name: w.Name, City: w.City, Status: w.Status,
		TotalSqm: w.TotalSqm, CoverURL: domain.PublicMediaURL(s.mediaBase(), w.CoverKey),
	}
	if base := s.publicBase(); base != "" && w.Status == models.WarehouseLive {
		c.PublicURL = base + listingPathPrefix + w.Slug
	}
	return c
}

// --- inbox writes ---

// load reads an enquiry for a CAS write and checks the caller's
// expectedUpdatedAt.
func (s *svc) load(ctx context.Context, id primitive.ObjectID, expected time.Time) (*models.Enquiry, error) {
	if id.IsZero() {
		return nil, enquiryService.Invalidf("id is required")
	}
	if expected.IsZero() {
		return nil, enquiryService.Invalidf("expectedUpdatedAt is required")
	}
	e, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !e.UpdatedAt.Equal(expected) {
		return nil, enquiryService.ErrStale
	}
	return e, nil
}

// replace CAS-writes e over before and logs the change.
func (s *svc) replace(ctx context.Context, actor domain.Actor, before, e *models.Enquiry, meta map[string]any) error {
	if err := s.store.Replace(ctx, e, before.UpdatedAt); err != nil {
		if errors.Is(err, models.ErrVersionConflict) {
			return enquiryService.ErrStale
		}
		return err
	}
	s.record(ctx, actor, e.ID, domain.ActionUpdate, before, e, meta)
	return nil
}

func cloneEnquiry(e *models.Enquiry) *models.Enquiry {
	c := *e
	c.Notes = slices.Clone(e.Notes)
	c.History = slices.Clone(e.History)
	return &c
}

// SetStatus moves the status (free in any direction, D-104); closing needs
// a reason. Re-sending the current state is a no-op.
func (s *svc) SetStatus(ctx context.Context, actor domain.Actor, req enquiryService.StatusRequest) (*models.Enquiry, error) {
	reason, note := strings.TrimSpace(req.CloseReason), strings.TrimSpace(html.UnescapeString(req.CloseNote))
	switch req.Status {
	case models.EnquiryNew, models.EnquiryContacted:
		reason, note = "", ""
	case models.EnquiryClosed:
		if reason != models.CloseWon && reason != models.CloseLost && reason != models.CloseOther {
			return nil, enquiryService.Invalidf("closeReason must be won, lost or other when closing")
		}
		if utf8.RuneCountInString(note) > maxCloseNoteLen {
			return nil, enquiryService.Invalidf("closeNote is too long (at most %d characters)", maxCloseNoteLen)
		}
	default:
		return nil, enquiryService.Invalidf("status must be new, contacted or closed")
	}
	before, err := s.load(ctx, req.ID, req.ExpectedUpdatedAt)
	if err != nil {
		return nil, err
	}
	if before.Status == req.Status && before.CloseReason == reason && before.CloseNote == note {
		return before, nil
	}
	e := cloneEnquiry(before)
	now := s.stamp()
	if e.Status != req.Status {
		e.History = append(e.History, models.EnquiryChange{At: now, By: actor.UserID, ByEmail: actor.Email,
			Field: models.EnquiryFieldStatus, From: e.Status, To: req.Status})
	}
	e.Status, e.CloseReason, e.CloseNote = req.Status, reason, note
	e.UpdatedAt = now
	if err := s.replace(ctx, actor, before, e, map[string]any{"field": models.EnquiryFieldStatus}); err != nil {
		return nil, err
	}
	return e, nil
}

// Assign sets or clears the assignee, who must hold the editor permission
// (the inbox permission, D-112).
func (s *svc) Assign(ctx context.Context, actor domain.Actor, req enquiryService.AssignRequest) (*models.Enquiry, error) {
	var assignee *models.User
	if req.AssigneeUserID != nil {
		u, err := s.store.User(ctx, *req.AssigneeUserID)
		if errors.Is(err, models.ErrNotFound) {
			return nil, enquiryService.Invalidf("assignee not found")
		}
		if err != nil {
			return nil, err
		}
		if !authz.Has(u, authz.PermEditor) {
			return nil, enquiryService.Invalidf("the assignee must hold the editor permission")
		}
		assignee = u
	}
	before, err := s.load(ctx, req.ID, req.ExpectedUpdatedAt)
	if err != nil {
		return nil, err
	}
	if (assignee == nil && before.AssigneeUserID == nil) ||
		(assignee != nil && before.AssigneeUserID != nil && *before.AssigneeUserID == assignee.ID) {
		return before, nil
	}
	e := cloneEnquiry(before)
	now := s.stamp()
	change := models.EnquiryChange{At: now, By: actor.UserID, ByEmail: actor.Email, Field: models.EnquiryFieldAssignee,
		From: before.AssigneeEmail}
	e.AssigneeUserID, e.AssigneeEmail = nil, ""
	if assignee != nil {
		id := assignee.ID
		e.AssigneeUserID, e.AssigneeEmail = &id, strings.ToLower(assignee.Email)
	}
	change.To = e.AssigneeEmail
	e.History = append(e.History, change)
	e.UpdatedAt = now
	if err := s.replace(ctx, actor, before, e, map[string]any{"field": models.EnquiryFieldAssignee}); err != nil {
		return nil, err
	}
	return e, nil
}

// AddNote appends a staff note (no CAS: notes only ever accumulate).
func (s *svc) AddNote(ctx context.Context, actor domain.Actor, req enquiryService.NoteRequest) (*models.Enquiry, error) {
	if req.ID.IsZero() {
		return nil, enquiryService.Invalidf("id is required")
	}
	body := strings.TrimSpace(html.UnescapeString(req.Body))
	if body == "" || utf8.RuneCountInString(body) > maxNoteLen {
		return nil, enquiryService.Invalidf("body must be 1 to %d characters", maxNoteLen)
	}
	before, err := s.store.Get(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	n := models.EnquiryNote{ID: primitive.NewObjectID(), ByUserID: actor.UserID, ByEmail: actor.Email, At: s.stamp(), Body: body}
	e, err := s.store.PushNote(ctx, req.ID, n)
	if err != nil {
		return nil, err
	}
	s.record(ctx, actor, e.ID, domain.ActionUpdate, before, e, map[string]any{"note": n.ID.Hex()})
	return e, nil
}

// --- account deletion (D-019) ---

func (s *svc) Cleaner() accountService.DataCleaner { return cleaner{s} }

type cleaner struct{ s *svc }

func (c cleaner) Name() string { return "enquiries" }

// DeleteUserData soft-deletes: the enquiries leave the inbox but keep their
// contact details (D-019).
func (c cleaner) DeleteUserData(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	return c.s.store.SoftDeleteUser(ctx, userID, c.s.stamp())
}
