package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/enquiryService"
	"github.com/atharva-ng/crunch/internal/warehousehub/changelog"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// memStore is an in-memory Store with Mongo's CAS and soft-delete
// semantics.
type memStore struct {
	mu         sync.Mutex
	enquiries  map[primitive.ObjectID]models.Enquiry
	warehouses map[string]models.Warehouse
	users      map[primitive.ObjectID]models.User
	events     map[primitive.ObjectID]models.SearchEvent
	profiles   int
}

func newMemStore() *memStore {
	return &memStore{
		enquiries:  map[primitive.ObjectID]models.Enquiry{},
		warehouses: map[string]models.Warehouse{},
		users:      map[primitive.ObjectID]models.User{},
		events:     map[primitive.ObjectID]models.SearchEvent{},
	}
}

func (m *memStore) Insert(_ context.Context, e *models.Enquiry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.enquiries {
		if x.UserID == e.UserID && x.IdempotencyKey == e.IdempotencyKey {
			return models.ErrDuplicateKey
		}
	}
	e.ID = primitive.NewObjectID()
	m.enquiries[e.ID] = *cloneEnquiry(e)
	return nil
}

func (m *memStore) Get(_ context.Context, id primitive.ObjectID) (*models.Enquiry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.enquiries[id]
	if !ok || e.DeletedAt != nil {
		return nil, models.ErrNotFound
	}
	return cloneEnquiry(&e), nil
}

func (m *memStore) GetByIdempotencyKey(_ context.Context, userID primitive.ObjectID, key string) (*models.Enquiry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.enquiries {
		if e.UserID == userID && e.IdempotencyKey == key {
			return cloneEnquiry(&e), nil
		}
	}
	return nil, models.ErrNotFound
}

func (m *memStore) Replace(_ context.Context, e *models.Enquiry, expected time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.enquiries[e.ID]
	if !ok || cur.DeletedAt != nil || !cur.UpdatedAt.Equal(expected) {
		return models.ErrVersionConflict
	}
	m.enquiries[e.ID] = *cloneEnquiry(e)
	return nil
}

func (m *memStore) PushNote(_ context.Context, id primitive.ObjectID, n models.EnquiryNote) (*models.Enquiry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.enquiries[id]
	if !ok || e.DeletedAt != nil {
		return nil, models.ErrNotFound
	}
	e.Notes = append(e.Notes, n)
	e.UpdatedAt = n.At
	m.enquiries[id] = e
	return cloneEnquiry(&e), nil
}

func (m *memStore) List(_ context.Context, f models.EnquiryFilter, _, _ int) ([]models.Enquiry, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []models.Enquiry{}
	for _, e := range m.enquiries {
		if e.DeletedAt != nil || (f.Status != "" && e.Status != f.Status) {
			continue
		}
		out = append(out, e)
	}
	return out, int64(len(out)), nil
}

func (m *memStore) Export(ctx context.Context, f models.EnquiryFilter, _ int) ([]models.Enquiry, error) {
	items, _, err := m.List(ctx, f, 1, 0)
	return items, err
}

func (m *memStore) SoftDeleteUser(_ context.Context, userID primitive.ObjectID, at time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, e := range m.enquiries {
		if e.UserID == userID && e.DeletedAt == nil {
			e.DeletedAt = &at
			m.enquiries[id] = e
			n++
		}
	}
	return n, nil
}

func (m *memStore) WarehouseByShortID(_ context.Context, shortID string) (*models.Warehouse, error) {
	w, ok := m.warehouses[shortID]
	if !ok {
		return nil, models.ErrNotFound
	}
	return &w, nil
}

func (m *memStore) Warehouse(_ context.Context, id primitive.ObjectID) (*models.Warehouse, error) {
	for _, w := range m.warehouses {
		if w.ID == id {
			return &w, nil
		}
	}
	return nil, models.ErrNotFound
}

func (m *memStore) SearchEvent(_ context.Context, id primitive.ObjectID) (*models.SearchEvent, error) {
	ev, ok := m.events[id]
	if !ok {
		return nil, models.ErrNotFound
	}
	return &ev, nil
}

func (m *memStore) User(_ context.Context, id primitive.ObjectID) (*models.User, error) {
	u, ok := m.users[id]
	if !ok {
		return nil, models.ErrNotFound
	}
	return &u, nil
}

func (m *memStore) UpdateUserProfile(_ context.Context, id primitive.ObjectID, phone, phoneE164, company string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.users[id]
	u.Phone, u.PhoneE164, u.Company = phone, phoneE164, company
	m.users[id] = u
	m.profiles++
	return nil
}

// --- fixtures ---

var t0 = time.Date(2026, 10, 2, 10, 0, 0, 123456789, time.UTC)

type fixture struct {
	svc   *svc
	store *memStore
	log   *changelog.Memory
	clock *time.Time
}

func newFixture() *fixture {
	st := newMemStore()
	cl := &changelog.Memory{}
	clock := t0
	s := &svc{
		store: st, log: cl,
		cfg:        func() config.EnquiriesValues { return config.EnquiriesValues{MessageMaxLen: 4000, ExportMax: 100} },
		publicBase: func() string { return "https://wh.example" },
		mediaBase:  func() string { return "" },
		now:        func() time.Time { return clock },
	}
	return &fixture{svc: s, store: st, log: cl, clock: &clock}
}

func (f *fixture) tick() { *f.clock = f.clock.Add(time.Second) }

func visitor() *models.User {
	return &models.User{ID: primitive.NewObjectID(), ClerkID: "user_1", Email: "Asha@Example.com", Name: "Asha", Role: models.RoleUser}
}

func validReq(key string) enquiryService.SubmitRequest {
	return enquiryService.SubmitRequest{Name: "Asha Rao", Company: "Rao Logistics", Phone: "98200 12345",
		Message: "Need 20,000 sq ft near Bhiwandi from next month.", IdempotencyKey: key}
}

func isValidation(err error) bool {
	var ve *enquiryService.ValidationError
	return errors.As(err, &ve)
}

var staff = domain.Actor{UserID: primitive.NewObjectID().Hex(), Email: "ed@wh.example"}

// --- submit ---

func TestSubmitValidation(t *testing.T) {
	f := newFixture()
	noEmail := visitor()
	noEmail.Email = ""
	cases := []struct {
		name string
		user *models.User
		mut  func(*enquiryService.SubmitRequest)
	}{
		{"missing phone", visitor(), func(r *enquiryService.SubmitRequest) { r.Phone = " " }},
		{"bad phone", visitor(), func(r *enquiryService.SubmitRequest) { r.Phone = "12345" }},
		{"user without email", noEmail, func(*enquiryService.SubmitRequest) {}},
		{"short message", visitor(), func(r *enquiryService.SubmitRequest) { r.Message = "hi" }},
		{"long message", visitor(), func(r *enquiryService.SubmitRequest) { r.Message = strings.Repeat("x", 4001) }},
		{"missing key", visitor(), func(r *enquiryService.SubmitRequest) { r.IdempotencyKey = "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := validReq("k1")
			c.mut(&req)
			if _, err := f.svc.Submit(context.Background(), c.user, req); !isValidation(err) {
				t.Fatalf("err = %v, want a validation error", err)
			}
		})
	}
	if len(f.store.enquiries) != 0 {
		t.Fatalf("nothing may be stored, got %d", len(f.store.enquiries))
	}
}

func TestSubmitStoresAndSavesProfile(t *testing.T) {
	f := newFixture()
	u := visitor()
	f.store.users[u.ID] = *u
	searchID := primitive.NewObjectID()
	req := validReq("k1")
	req.SearchID = searchID.Hex()
	req.SessionID = "sess-1"
	req.Message = "Need cold storage &amp; racking." // as the deserializer escapes it

	res, err := f.svc.Submit(context.Background(), u, req)
	if err != nil || !res.Created {
		t.Fatalf("submit = %+v, %v", res, err)
	}
	id, _ := primitive.ObjectIDFromHex(res.ID)
	e := f.store.enquiries[id]
	if e.Email != "asha@example.com" || e.PhoneE164 != "+919820012345" || e.Status != models.EnquiryNew || e.Country != "IN" {
		t.Errorf("stored = %+v", e)
	}
	if e.Message != "Need cold storage & racking." {
		t.Errorf("message = %q, want unescaped", e.Message)
	}
	if e.SearchID == nil || *e.SearchID != searchID || e.SessionID != "sess-1" {
		t.Errorf("conversion ids = %v %q", e.SearchID, e.SessionID)
	}
	if got := f.store.users[u.ID]; got.PhoneE164 != "+919820012345" || got.Company != "Rao Logistics" {
		t.Errorf("profile not saved: %+v", got)
	}
	if len(f.log.Entries) != 1 || f.log.Entries[0].Action != domain.ActionCreate || f.log.Entries[0].Entity != domain.EntityEnquiry {
		t.Errorf("change log = %+v", f.log.Entries)
	}

	// Unchanged profile: no second write.
	u2 := f.store.users[u.ID]
	if _, err := f.svc.Submit(context.Background(), &u2, validReq("k2")); err != nil {
		t.Fatal(err)
	}
	if f.store.profiles != 1 {
		t.Errorf("profile writes = %d, want 1", f.store.profiles)
	}
}

func TestSubmitIdempotent(t *testing.T) {
	f := newFixture()
	u := visitor()
	first, err := f.svc.Submit(context.Background(), u, validReq("same"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.svc.Submit(context.Background(), u, validReq("same"))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || again.Created {
		t.Errorf("retry = %+v, want the first id and Created=false", again)
	}
	if len(f.store.enquiries) != 1 {
		t.Errorf("stored %d enquiries, want 1", len(f.store.enquiries))
	}
}

func TestSubmitListing(t *testing.T) {
	f := newFixture()
	live := models.Warehouse{ID: primitive.NewObjectID(), ShortID: "abcd2345", Slug: "bhiwandi-abcd2345", Name: "Bhiwandi A", City: "Bhiwandi", Status: models.WarehouseLive, Country: "IN"}
	f.store.warehouses[live.ShortID] = live
	f.store.warehouses["gone2345"] = models.Warehouse{ID: primitive.NewObjectID(), ShortID: "gone2345", Status: models.WarehouseArchived}
	f.store.warehouses["draft234"] = models.Warehouse{ID: primitive.NewObjectID(), ShortID: "draft234", Status: models.WarehouseUnpublished}

	req := validReq("a")
	req.ListingShortID = "gone2345"
	if _, err := f.svc.Submit(context.Background(), visitor(), req); !errors.Is(err, enquiryService.ErrListingGone) {
		t.Errorf("archived listing: err = %v, want ErrListingGone", err)
	}
	req.ListingShortID = "draft234"
	if _, err := f.svc.Submit(context.Background(), visitor(), req); !errors.Is(err, enquiryService.ErrNotFound) {
		t.Errorf("unpublished listing: err = %v, want ErrNotFound", err)
	}
	req.ListingShortID = "ABCD2345"
	res, err := f.svc.Submit(context.Background(), visitor(), req)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := primitive.ObjectIDFromHex(res.ID)
	if l := f.store.enquiries[id].Listing; l == nil || l.WarehouseID != live.ID || l.Name != "Bhiwandi A" {
		t.Errorf("listing snapshot = %+v", l)
	}

	d, err := f.svc.Detail(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if d.Listing == nil || d.Listing.PublicURL != "https://wh.example/warehouses/bhiwandi-abcd2345" || d.Search != nil {
		t.Errorf("detail = %+v", d)
	}
}

// --- inbox ---

func submitted(t *testing.T, f *fixture) *models.Enquiry {
	t.Helper()
	res, err := f.svc.Submit(context.Background(), visitor(), validReq(primitive.NewObjectID().Hex()))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := primitive.ObjectIDFromHex(res.ID)
	e, err := f.store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestStatusCASAndHistory(t *testing.T) {
	f := newFixture()
	e := submitted(t, f)
	f.tick()

	_, err := f.svc.SetStatus(context.Background(), staff, enquiryService.StatusRequest{ID: e.ID, Status: models.EnquiryClosed, ExpectedUpdatedAt: e.UpdatedAt})
	if !isValidation(err) {
		t.Fatalf("close without reason: err = %v, want validation", err)
	}

	got, err := f.svc.SetStatus(context.Background(), staff, enquiryService.StatusRequest{ID: e.ID, Status: models.EnquiryContacted, ExpectedUpdatedAt: e.UpdatedAt})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.EnquiryContacted || len(got.History) != 1 || got.History[0].From != "new" || got.History[0].To != "contacted" || got.History[0].ByEmail != staff.Email {
		t.Errorf("after contacted: %+v", got)
	}

	// The old updatedAt is stale now.
	f.tick()
	if _, err := f.svc.SetStatus(context.Background(), staff, enquiryService.StatusRequest{ID: e.ID, Status: models.EnquiryNew, ExpectedUpdatedAt: e.UpdatedAt}); !errors.Is(err, enquiryService.ErrStale) {
		t.Errorf("stale write: err = %v, want ErrStale", err)
	}

	closed, err := f.svc.SetStatus(context.Background(), staff, enquiryService.StatusRequest{ID: e.ID, Status: models.EnquiryClosed, CloseReason: models.CloseWon, ExpectedUpdatedAt: got.UpdatedAt})
	if err != nil {
		t.Fatal(err)
	}
	if closed.CloseReason != models.CloseWon || len(closed.History) != 2 {
		t.Errorf("after close: %+v", closed)
	}
	// Reopen clears the close reason (moves are free, D-104).
	f.tick()
	reopened, err := f.svc.SetStatus(context.Background(), staff, enquiryService.StatusRequest{ID: e.ID, Status: models.EnquiryNew, ExpectedUpdatedAt: closed.UpdatedAt})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.CloseReason != "" || len(reopened.History) != 3 {
		t.Errorf("after reopen: %+v", reopened)
	}
	// create + 3 updates
	if len(f.log.Entries) != 4 {
		t.Errorf("change log entries = %d, want 4", len(f.log.Entries))
	}
}

func TestAssign(t *testing.T) {
	f := newFixture()
	e := submitted(t, f)
	editor := models.User{ID: primitive.NewObjectID(), Email: "Ed@wh.example", Role: models.RoleAdmin, Permissions: []string{string(authz.PermEditor)}}
	approver := models.User{ID: primitive.NewObjectID(), Email: "ap@wh.example", Role: models.RoleAdmin, Permissions: []string{string(authz.PermApprover)}}
	f.store.users[editor.ID] = editor
	f.store.users[approver.ID] = approver
	f.tick()

	if _, err := f.svc.Assign(context.Background(), staff, enquiryService.AssignRequest{ID: e.ID, AssigneeUserID: &approver.ID, ExpectedUpdatedAt: e.UpdatedAt}); !isValidation(err) {
		t.Errorf("non-editor assignee: err = %v, want validation", err)
	}
	got, err := f.svc.Assign(context.Background(), staff, enquiryService.AssignRequest{ID: e.ID, AssigneeUserID: &editor.ID, ExpectedUpdatedAt: e.UpdatedAt})
	if err != nil {
		t.Fatal(err)
	}
	if got.AssigneeUserID == nil || *got.AssigneeUserID != editor.ID || got.AssigneeEmail != "ed@wh.example" ||
		len(got.History) != 1 || got.History[0].Field != models.EnquiryFieldAssignee || got.History[0].To != "ed@wh.example" {
		t.Errorf("assigned = %+v", got)
	}
	f.tick()
	cleared, err := f.svc.Assign(context.Background(), staff, enquiryService.AssignRequest{ID: e.ID, ExpectedUpdatedAt: got.UpdatedAt})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.AssigneeUserID != nil || cleared.History[1].From != "ed@wh.example" || cleared.History[1].To != "" {
		t.Errorf("unassigned = %+v", cleared)
	}
}

func TestAddNote(t *testing.T) {
	f := newFixture()
	e := submitted(t, f)
	if _, err := f.svc.AddNote(context.Background(), staff, enquiryService.NoteRequest{ID: e.ID, Body: "  "}); !isValidation(err) {
		t.Errorf("empty note: err = %v", err)
	}
	f.tick()
	got, err := f.svc.AddNote(context.Background(), staff, enquiryService.NoteRequest{ID: e.ID, Body: "Called, sending options"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Notes) != 1 || got.Notes[0].Body != "Called, sending options" || !got.UpdatedAt.After(e.UpdatedAt) {
		t.Errorf("notes = %+v", got)
	}
}

func TestSoftDeleteHidesButKeepsContact(t *testing.T) {
	f := newFixture()
	u := visitor()
	res, err := f.svc.Submit(context.Background(), u, validReq("k"))
	if err != nil {
		t.Fatal(err)
	}
	other := submitted(t, f)

	c := f.svc.Cleaner()
	n, err := c.DeleteUserData(context.Background(), u.ID)
	if err != nil || n != 1 {
		t.Fatalf("cleaner = %d, %v", n, err)
	}
	if n, _ := c.DeleteUserData(context.Background(), u.ID); n != 0 {
		t.Errorf("re-run must be zero-match, got %d", n)
	}
	id, _ := primitive.ObjectIDFromHex(res.ID)
	if _, err := f.svc.Detail(context.Background(), id); !errors.Is(err, enquiryService.ErrNotFound) {
		t.Errorf("deleted detail: err = %v, want not found", err)
	}
	items, total, _ := f.svc.List(context.Background(), models.EnquiryFilter{}, 1, 25)
	if total != 1 || items[0].ID != other.ID {
		t.Errorf("inbox = %d items", total)
	}
	if raw := f.store.enquiries[id]; raw.DeletedAt == nil || raw.PhoneE164 == "" || raw.Email == "" {
		t.Errorf("contact must be kept: %+v", raw)
	}
}
