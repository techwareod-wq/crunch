package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/config"
	idto "github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/aiSearchService"
	"github.com/atharva-ng/crunch/internal/services/catalogService"
	"github.com/atharva-ng/crunch/internal/warehousehub/changelog"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

var ctx = context.Background()

var (
	editor   = domain.Actor{UserID: "u-ed", Email: "ed@example.com"}
	approver = domain.Actor{UserID: "u-ap", Email: "ap@example.com"}
)

// staticRules serves one snapshot.
type staticRules struct{ s *domain.Snapshot }

func (r staticRules) Snapshot() *domain.Snapshot { return r.s }
func (r staticRules) SnapshotAtLeast(context.Context, int64) (*domain.Snapshot, error) {
	return r.s, nil
}

func testSnapshot() *domain.Snapshot {
	root := domain.RootNode()
	cold := models.AttributeNode{Key: "cold_storage", ParentKey: domain.RootKey, Name: "Cold storage", Public: true, Filterable: true,
		Fields: []models.AttributeField{
			{Key: "temperature", Name: "Temperature", Type: domain.TypeNumber, Required: true, Public: true, Unit: &models.UnitSpec{Family: domain.DimTemp}},
		}}
	secret := models.AttributeNode{Key: "internal_audit", ParentKey: domain.RootKey, Name: "Internal audit", Public: false,
		Fields: []models.AttributeField{{Key: "score", Name: "Score", Type: domain.TypeNumber, Public: true}}}
	haz := models.AttributeNode{Key: "hazmat", ParentKey: domain.RootKey, Name: "Hazmat", Public: true}
	ind := models.Industry{Key: "food", Name: "Food", Required: []models.Condition{{Node: "cold_storage", Cmp: domain.CmpIsYes}}}
	return domain.NewSnapshot(3, []models.AttributeNode{root, cold, secret, haz}, []models.Industry{ind})
}

type fakeGeocoder struct {
	res   interfaces.GeocodeResult
	err   error
	calls int
}

func (g *fakeGeocoder) Geocode(context.Context, string, string) (interfaces.GeocodeResult, error) {
	g.calls++
	return g.res, g.err
}

type fakeS3 struct {
	interfaces.S3
	objects map[string]idto.S3ObjectInfo
	deleted []string
}

func (f *fakeS3) GenerateSinglepartUploadPresignedURLs(_ context.Context, _ string, reqs []idto.PresignedSinglepartPutRequest) ([]idto.PresignedSinglepartPutResponse, error) {
	return []idto.PresignedSinglepartPutResponse{{Key: reqs[0].Key, URL: "https://s3/put/" + reqs[0].Key}}, nil
}

func (f *fakeS3) HeadObject(_ context.Context, _, key string) (idto.S3ObjectInfo, bool, error) {
	info, ok := f.objects[key]
	return info, ok, nil
}

func (f *fakeS3) DeleteFile(_ context.Context, _, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

func (f *fakeS3) PresignedGetObject(_ context.Context, _, key string, _ int64) (string, error) {
	return "https://s3/get/" + key, nil
}

type sentMsg struct {
	pt      pipeline.ProcessType
	key     string
	payload any
}

type harness struct {
	store    *memStore
	cl       *changelog.Memory
	geo      *fakeGeocoder
	s3       *fakeS3
	cfg      config.CatalogValues
	svc      *svc
	media    *mediaService
	public   *publicSite
	sent     []sentMsg
	clock    time.Time
	aiSearch bool
}

func newHarness() *harness {
	h := &harness{
		store: newMemStore(), cl: &changelog.Memory{},
		geo:   &fakeGeocoder{res: interfaces.GeocodeResult{Lat: 18.52, Lng: 73.85, Accuracy: "ROOFTOP", PlaceID: "p1"}},
		s3:    &fakeS3{objects: map[string]idto.S3ObjectInfo{}},
		cfg:   config.CatalogValues{MaxPhotos: 2, MaxPhotoBytes: 10 << 20, MaxDocBytes: 20 << 20, MediaGcPendingHours: 24, MediaGcUnreferencedDays: 30, BulkApproveMax: 10, PublicPageSize: 10},
		clock: time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC),
	}
	now := func() time.Time { return h.clock }
	rules := staticRules{testSnapshot()}
	h.svc = &svc{
		store: h.store, rules: rules, log: h.cl,
		geocoder: func() interfaces.Geocoder { return h.geo },
		dispatch: func(_ context.Context, pt pipeline.ProcessType, key string, p any) error {
			h.sent = append(h.sent, sentMsg{pt, key, p})
			return nil
		},
		cfg: func() config.CatalogValues { return h.cfg }, aiSearch: func() bool { return h.aiSearch }, now: now,
	}
	h.media = &mediaService{
		store: h.store, s3: func() interfaces.S3 { return h.s3 },
		aws: func() config.AWSConfig {
			return config.AWSConfig{PublicBucket: "pub", PrivateBucket: "priv", PublicMediaBaseURL: "https://cdn"}
		},
		links: func() config.StorageValues { return config.StorageValues{PrivateLinkSeconds: 300} },
		cfg:   func() config.CatalogValues { return h.cfg }, now: now,
	}
	h.public = &publicSite{
		store: h.store, rules: rules, search: func() domain.SearchEngine { return nil },
		mediaBase: func() string { return "https://cdn" }, siteBase: func() string { return "https://site" },
		cfg: func() config.CatalogValues { return h.cfg }, logWarn: func(string, error) {},
	}
	return h
}

func fv(v any) *models.FieldValue { return &models.FieldValue{V: v} }

// fullContent is a submittable listing (minus the cover photo).
func fullContent() models.ListingContent {
	return models.ListingContent{Attributes: models.Attributes{
		domain.RootKey: {Status: domain.StatusYes, Fields: map[string]*models.FieldValue{
			"name":             fv("Pune Cold Hub"),
			"description":      fv("A cold store near the highway."),
			"address":          fv(map[string]any{"line1": "Plot 4", "locality": "Chakan", "city": "Pune", "country": "IN", "postalCode": "410501"}),
			"total_area":       fv(map[string]any{"value": 10000.0, "unit": "sqft"}),
			"rent":             fv(map[string]any{"amount": 3000.0, "currency": "INR", "basis": "per_sqft_month"}),
			"operator_company": fv("Secret Operator Pvt Ltd"),
		}},
		"cold_storage":   {Status: domain.StatusYes, Fields: map[string]*models.FieldValue{"temperature": fv(-18.0)}},
		"internal_audit": {Status: domain.StatusYes, Fields: map[string]*models.FieldValue{"score": fv(9.0)}},
		"hazmat":         {Status: domain.StatusUnknown},
	}, RentAdmin: &models.RentAdmin{LockInMonths: 12}}
}

// readyMedia inserts an uploaded file.
func (h *harness) readyMedia(wid primitive.ObjectID, kind, docType, vis string) primitive.ObjectID {
	m := &models.WarehouseMedia{WarehouseID: wid, Kind: kind, DocType: docType, Visibility: vis, Bucket: "b",
		Key: "wh/" + wid.Hex() + "/" + kind + primitive.NewObjectID().Hex(), Status: models.MediaReady, CreatedAt: h.clock}
	_ = h.store.InsertMedia(ctx, m)
	return m.ID
}

// draft creates a warehouse and saves fullContent with a cover photo.
func (h *harness) draft(t *testing.T) (*models.Warehouse, *models.WarehouseRevision) {
	t.Helper()
	w, res, err := h.svc.Create(ctx, editor, models.ListingContent{})
	if err != nil {
		t.Fatal(err)
	}
	c := fullContent()
	cover := h.readyMedia(w.ID, models.MediaPhoto, "", models.VisibilityPublic)
	staff := h.readyMedia(w.ID, models.MediaDoc, models.DocAgreement, models.VisibilityStaff)
	c.Media = []models.MediaRef{{MediaID: cover, IsCover: true}, {MediaID: staff}}
	c.RentAdmin.AgreementMediaID = &staff
	saved, err := h.svc.Save(ctx, editor, catalogService.SaveRequest{RevisionID: res.Revision.ID, Rev: res.Revision.Rev, Content: c}, "")
	if err != nil {
		t.Fatal(err)
	}
	return w, saved.Revision
}

// live takes a draft through submit + approve.
func (h *harness) live(t *testing.T) (*models.Warehouse, *models.WarehouseRevision) {
	t.Helper()
	w, r := h.draft(t)
	if _, err := h.svc.Submit(ctx, editor, r.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Approve(ctx, approver, r.ID, ""); err != nil {
		t.Fatal(err)
	}
	w, _ = h.store.GetWarehouse(ctx, w.ID)
	r, _ = h.store.GetRevision(ctx, r.ID)
	return w, r
}

func code(err error) string { return errorCode(err) }

func TestHappyPath(t *testing.T) {
	h := newHarness()
	w, r := h.draft(t)
	if r.State != models.RevDraft || r.Rev != 2 {
		t.Fatalf("draft = %s rev %d", r.State, r.Rev)
	}
	loc, ok := rootValue[models.Location](r.Content.Attributes, fieldLocation)
	if !ok || loc.Source != domain.LocationGeocoded || loc.AddressHash == "" || h.geo.calls != 1 {
		t.Fatalf("geocode on save: %+v calls=%d", loc, h.geo.calls)
	}
	if wh, _ := h.store.GetWarehouse(ctx, w.ID); wh.Name != "Pune Cold Hub" || wh.City != "Pune" {
		t.Errorf("draft name not hoisted: %q %q", wh.Name, wh.City)
	}
	if _, err := h.svc.Submit(ctx, editor, r.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Approve(ctx, approver, r.ID, ""); err != nil {
		t.Fatal(err)
	}
	wh, _ := h.store.GetWarehouse(ctx, w.ID)
	if wh.Status != models.WarehouseLive || wh.LiveVersion != 1 || wh.OpenRevisionID != nil || wh.PublishedAt == nil {
		t.Fatalf("live doc = %+v", wh)
	}
	if wh.Slug != "pune-cold-hub-pune-"+wh.ShortID || len(wh.SlugHistory) != 1 {
		t.Errorf("slug = %q %v", wh.Slug, wh.SlugHistory)
	}
	if wh.Price == nil || math.Abs(wh.Price.PerSqmMonth-32291.73) > 0.01 || wh.Loc == nil || math.Abs(wh.TotalSqm-929.0304) > 1e-6 {
		t.Errorf("hoisted price/loc/area = %+v %+v %v", wh.Price, wh.Loc, wh.TotalSqm)
	}
	if len(wh.Fit) != 1 || wh.Fit[0] != "food:F" || wh.NeedsInfoCount != 1 || wh.FitRulesVersion != 3 {
		t.Errorf("projection = %+v", wh.Projection)
	}
	if wh.CoverKey == "" {
		t.Error("cover not hoisted")
	}
	if rent, ok := h.store.rents[w.ID]; !ok || rent.TermsVersion != 1 || rent.Admin == nil {
		t.Errorf("rent = %+v", rent)
	}
	if h.store.catalogV != 1 {
		t.Errorf("catalogVersion = %d", h.store.catalogV)
	}
	last := h.cl.Entries[len(h.cl.Entries)-1]
	if last.Action != domain.ActionApprove || last.Entity != domain.EntityWarehouse {
		t.Errorf("last log = %+v", last)
	}
}

func TestSubmitGate(t *testing.T) {
	h := newHarness()
	_, res, _ := h.svc.Create(ctx, editor, models.ListingContent{})
	_, err := h.svc.Submit(ctx, editor, res.Revision.ID, "")
	var sb *catalogService.SubmitBlockedError
	if !errors.As(err, &sb) {
		t.Fatalf("empty draft submitted: %v", err)
	}
	joined := strings.Join(sb.Problems, "\n")
	for _, want := range []string{"warehouse.name", "warehouse.address", "warehouse.location", "warehouse.total_area", "warehouse.rent"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing problem %q in %v", want, sb.Problems)
		}
	}
	if strings.Contains(joined, "cover") {
		t.Errorf("cover photo should be optional: %v", sb.Problems)
	}

	// Approximate pin blocks; manual fix unblocks.
	h.geo.res.Accuracy = "APPROXIMATE"
	w, r := h.draft(t)
	if _, err := h.svc.Submit(ctx, editor, r.ID, ""); !errors.As(err, &sb) || !strings.Contains(strings.Join(sb.Problems, ""), "approximate") {
		t.Fatalf("approximate pin submitted: %v", err)
	}
	c := r.Content
	c.Attributes[domain.RootKey].Fields["location"] = fv(map[string]any{"lat": 18.5, "lng": 73.8, "source": "manual"})
	r2, err := h.svc.Save(ctx, editor, catalogService.SaveRequest{RevisionID: r.ID, Rev: r.Rev, Content: c}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Preview.SubmitProblems) != 0 {
		t.Fatalf("still blocked: %v", r2.Preview.SubmitProblems)
	}
	_ = w
}

func TestSaveRules(t *testing.T) {
	h := newHarness()
	_, r := h.draft(t)
	if _, err := h.svc.Save(ctx, editor, catalogService.SaveRequest{RevisionID: r.ID, Rev: r.Rev - 1, Content: r.Content}, ""); code(err) != catalogService.CodeStale {
		t.Errorf("stale save: %v", err)
	}
	bad := r.Content
	bad.Media = append(bad.Media, models.MediaRef{MediaID: primitive.NewObjectID()})
	if _, err := h.svc.Save(ctx, editor, catalogService.SaveRequest{RevisionID: r.ID, Rev: r.Rev, Content: bad}, ""); code(err) != "invalid" {
		t.Errorf("foreign media saved: %v", err)
	}
	h.svc.Submit(ctx, editor, r.ID, "")
	r, _ = h.store.GetRevision(ctx, r.ID)
	if _, err := h.svc.Save(ctx, editor, catalogService.SaveRequest{RevisionID: r.ID, Rev: r.Rev, Content: r.Content}, ""); code(err) != catalogService.CodeInReview {
		t.Errorf("save in review: %v", err)
	}
}

func TestIllegalTransitions(t *testing.T) {
	h := newHarness()
	_, r := h.draft(t)
	if _, err := h.svc.Approve(ctx, approver, r.ID, ""); code(err) != catalogService.CodeBadState {
		t.Errorf("approve draft: %v", err)
	}
	if _, err := h.svc.Withdraw(ctx, editor, r.ID); code(err) != catalogService.CodeBadState {
		t.Errorf("withdraw draft: %v", err)
	}
	h.svc.Submit(ctx, editor, r.ID, "")
	if _, err := h.svc.Submit(ctx, editor, r.ID, ""); code(err) != catalogService.CodeBadState {
		t.Errorf("double submit: %v", err)
	}
	if _, err := h.svc.Discard(ctx, editor, r.ID); code(err) != catalogService.CodeBadState {
		t.Errorf("discard in review: %v", err)
	}
	if _, err := h.svc.Reject(ctx, approver, r.ID, "  "); code(err) != "invalid" {
		t.Errorf("reject without comment: %v", err)
	}
	res, err := h.svc.Reject(ctx, approver, r.ID, "add photos")
	if err != nil || res.Revision.State != models.RevDraft || res.Revision.Review[len(res.Revision.Review)-1].Comment != "add photos" {
		t.Fatalf("reject: %v %+v", err, res.Revision)
	}
	if _, err := h.svc.Discard(ctx, editor, r.ID); err != nil {
		t.Fatal(err)
	}
	w, _ := h.store.GetWarehouse(ctx, r.WarehouseID)
	if w.OpenRevisionID != nil {
		t.Error("discard left the revision open")
	}
	if _, err := h.svc.Open(ctx, editor, w.ID); code(err) != catalogService.CodeBadState {
		t.Errorf("open on never-published: %v", err)
	}
}

func TestSelfApprove(t *testing.T) {
	h := newHarness()
	_, r := h.draft(t)
	h.svc.Submit(ctx, approver, r.ID, "")
	if _, err := h.svc.Approve(ctx, approver, r.ID, ""); err != nil {
		t.Fatalf("self approve: %v", err)
	}
}

func TestEditLiveAndStaleBase(t *testing.T) {
	h := newHarness()
	w, first := h.live(t)
	res, err := h.svc.Open(ctx, editor, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := res.Revision
	if r.Version != 2 || r.BaseVersion != 1 {
		t.Fatalf("open: version %d base %d", r.Version, r.BaseVersion)
	}
	if _, err := h.svc.Open(ctx, editor, w.ID); code(err) != catalogService.CodeOpenExists {
		t.Errorf("second open: %v", err)
	}
	c := r.Content
	c.Attributes[domain.RootKey].Fields["name"] = fv("Pune Mega Hub")
	saved, err := h.svc.Save(ctx, editor, catalogService.SaveRequest{RevisionID: r.ID, Rev: r.Rev, Content: c}, "")
	if err != nil {
		t.Fatal(err)
	}
	if wh, _ := h.store.GetWarehouse(ctx, w.ID); wh.Name != "Pune Cold Hub" {
		t.Error("a pending edit changed the live name")
	}
	h.svc.Submit(ctx, editor, saved.Revision.ID, "")

	// Someone else's version went live meanwhile.
	h.store.mu.Lock()
	x := h.store.warehouses[w.ID]
	x.LiveVersion = 9
	h.store.warehouses[w.ID] = x
	h.store.mu.Unlock()
	if _, err := h.svc.Approve(ctx, approver, r.ID, ""); code(err) != catalogService.CodeStaleBase {
		t.Fatalf("stale base: %v", err)
	}
	back, _ := h.store.GetRevision(ctx, r.ID)
	if back.State != models.RevInReview {
		t.Errorf("stale approve left %s", back.State)
	}

	// Restore the base and approve: slug changes, old one kept in history,
	// first approved version superseded.
	h.store.mu.Lock()
	x = h.store.warehouses[w.ID]
	x.LiveVersion = 1
	h.store.warehouses[w.ID] = x
	h.store.mu.Unlock()
	if _, err := h.svc.Approve(ctx, approver, r.ID, ""); err != nil {
		t.Fatal(err)
	}
	wh, _ := h.store.GetWarehouse(ctx, w.ID)
	if wh.LiveVersion != 2 || !strings.HasPrefix(wh.Slug, "pune-mega-hub") || len(wh.SlugHistory) != 2 {
		t.Errorf("after edit: v%d %q %v", wh.LiveVersion, wh.Slug, wh.SlugHistory)
	}
	if old, _ := h.store.GetRevision(ctx, first.ID); old.State != models.RevSuperseded {
		t.Errorf("first version = %s", old.State)
	}
}

func TestApproveResume(t *testing.T) {
	h := newHarness()
	w, r := h.draft(t)
	h.svc.Submit(ctx, editor, r.ID, "")
	r, _ = h.store.GetRevision(ctx, r.ID)

	// Crash after step 3: revision approved + live written, tail missing.
	ap := cloneRevision(r)
	ap.State, ap.Open, ap.Rev = models.RevApproved, false, r.Rev+1
	if err := h.store.ReplaceRevision(ctx, &ap, r.State, r.Rev); err != nil {
		t.Fatal(err)
	}
	media, _ := h.store.ListMedia(ctx, w.ID)
	if ok, _ := h.store.PublishLive(ctx, w.ID, 0, buildPublish(testSnapshot(), ap.Content, media, ap.Version, w.ShortID, h.clock)); !ok {
		t.Fatal("publish")
	}
	if _, ok := h.store.rents[w.ID]; ok {
		t.Fatal("rent written too early")
	}
	if _, err := h.svc.Approve(ctx, approver, r.ID, ""); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, ok := h.store.rents[w.ID]; !ok || h.store.catalogV != 1 {
		t.Error("resume did not finish the tail")
	}
	last := h.cl.Entries[len(h.cl.Entries)-1]
	if last.Meta["resumed"] != true {
		t.Errorf("resume not flagged: %+v", last.Meta)
	}
}

func TestArchiveRestoreDelete(t *testing.T) {
	h := newHarness()
	w, _ := h.live(t)
	if err := h.svc.Archive(ctx, approver, w.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Archive(ctx, approver, w.ID); code(err) != catalogService.CodeBadState {
		t.Errorf("double archive: %v", err)
	}
	if err := h.svc.Delete(ctx, approver, w.ID); code(err) != catalogService.CodeBadState {
		t.Errorf("delete archived: %v", err)
	}
	res, err := h.svc.Restore(ctx, approver, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wh, _ := h.store.GetWarehouse(ctx, w.ID); wh.Status != models.WarehouseArchived {
		t.Fatal("restore went live without approval (D-055)")
	}
	h.svc.Submit(ctx, editor, res.Revision.ID, "")
	if _, err := h.svc.Approve(ctx, approver, res.Revision.ID, ""); err != nil {
		t.Fatal(err)
	}
	if wh, _ := h.store.GetWarehouse(ctx, w.ID); wh.Status != models.WarehouseLive || wh.ArchivedAt != nil {
		t.Errorf("after restore approve: %s", wh.Status)
	}

	w2, _ := h.draft(t)
	if err := h.svc.Delete(ctx, approver, w2.ID); err != nil {
		t.Fatal(err)
	}
	if revs, _ := h.store.Revisions(ctx, w2.ID); len(revs) != 0 {
		t.Error("revisions left after delete")
	}
}

func TestBulkApprove(t *testing.T) {
	h := newHarness()
	_, a := h.draft(t)
	_, b := h.draft(t)
	h.svc.Submit(ctx, editor, a.ID, "")
	batch, items, err := h.svc.BulkApprove(ctx, approver, []primitive.ObjectID{a.ID, b.ID})
	if err != nil || batch == "" || !items[0].OK || items[1].OK || items[1].Code != catalogService.CodeBadState {
		t.Fatalf("bulk: %v %+v", err, items)
	}
}

func TestPublicLookup(t *testing.T) {
	h := newHarness()
	w, _ := h.live(t)

	res, err := h.public.Lookup(ctx, w.Slug)
	if err != nil || res.Listing == nil {
		t.Fatalf("lookup: %v %+v", err, res)
	}
	l := res.Listing
	if l.Name != "Pune Cold Hub" || l.Address.City != "Pune" || l.Rate == nil || l.Rate.Amount != 3000 || len(l.Industries) != 1 {
		t.Errorf("listing = %+v", l)
	}
	if len(l.Media) != 1 || !l.Media[0].IsCover || !strings.HasPrefix(l.Media[0].URL, "https://cdn/") {
		t.Errorf("media = %+v", l.Media)
	}
	if l.SEO.CanonicalPath != "/warehouses/"+w.Slug || l.SEO.JSONLD["url"] != "https://site/warehouses/"+w.Slug {
		t.Errorf("seo = %+v", l.SEO)
	}

	if res, _ := h.public.Lookup(ctx, "old-name-"+w.ShortID); res.RedirectTo != "/warehouses/"+w.Slug {
		t.Errorf("redirect = %+v", res)
	}
	if _, err := h.public.Lookup(ctx, "nope"); !errors.Is(err, catalogService.ErrNotFound) {
		t.Errorf("malformed: %v", err)
	}
	w2, _ := h.draft(t)
	if _, err := h.public.Lookup(ctx, "x-"+w2.ShortID); !errors.Is(err, catalogService.ErrNotFound) {
		t.Errorf("unpublished: %v", err)
	}
	h.svc.Archive(ctx, approver, w.ID)
	if res, err := h.public.Lookup(ctx, w.Slug); err != nil || !res.IsGone {
		t.Errorf("archived: %v %+v", err, res)
	}

	items, err := h.public.Slugs(ctx, 1, true)
	if err != nil || len(items) != 0 {
		t.Errorf("slugs after archive = %v %v", items, err)
	}
}

// TestPublicDTONoAdminFields walks the public JSON for anything admin-only
// (CA-12).
func TestPublicDTONoAdminFields(t *testing.T) {
	h := newHarness()
	w, _ := h.live(t)
	res, _ := h.public.Lookup(ctx, w.Slug)
	b, err := json.Marshal(res.Listing)
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, banned := range []string{
		"operator_company", "Secret Operator", "rentAdmin", "rent_admin", "lockIn", "agreement",
		"needsInfo", "needs_info", "hazmat", "internal_audit", "Plot 4", "line1", "placeId", "addressHash",
		"wh/" + w.ID.Hex() + "/doc",
	} {
		if strings.Contains(js, banned) {
			t.Errorf("public listing leaks %q: %s", banned, js)
		}
	}
	// Every top-level key is on the allowlist.
	var top map[string]any
	_ = json.Unmarshal(b, &top)
	allowed := map[string]bool{"shortId": true, "slug": true, "name": true, "description": true, "address": true, "loc": true,
		"totalArea": true, "rate": true, "attributes": true, "industries": true, "media": true, "seo": true, "updatedAt": true}
	for k := range top {
		if !allowed[k] {
			t.Errorf("unexpected public key %q", k)
		}
	}
	if !strings.Contains(js, `"cold_storage"`) {
		t.Errorf("public yes node missing: %s", js)
	}
}

func TestGeocodeRules(t *testing.T) {
	h := newHarness()
	_, r := h.draft(t)

	// A hand-placed pin is kept when the address changes, with a warning.
	c := r.Content
	c.Attributes[domain.RootKey].Fields["location"] = fv(map[string]any{"lat": 18.6, "lng": 73.9, "source": "manual"})
	res, err := h.svc.Save(ctx, editor, catalogService.SaveRequest{RevisionID: r.ID, Rev: r.Rev, Content: c}, "")
	if err != nil || len(res.Warnings) != 0 {
		t.Fatalf("manual pin: %v %v", err, res.Warnings)
	}
	c = res.Revision.Content
	c.Attributes[domain.RootKey].Fields["address"] = fv(map[string]any{"line1": "Plot 9", "city": "Pune", "country": "IN"})
	calls := h.geo.calls
	res, err = h.svc.Save(ctx, editor, catalogService.SaveRequest{RevisionID: r.ID, Rev: res.Revision.Rev, Content: c}, "")
	if err != nil || len(res.Warnings) != 1 || res.Warnings[0] != catalogService.WarnPinManual || h.geo.calls != calls {
		t.Fatalf("address change under manual pin: %v %v", err, res.Warnings)
	}
	if loc, _ := rootValue[models.Location](res.Revision.Content.Attributes, fieldLocation); loc.Lat != 18.6 {
		t.Error("manual pin moved")
	}

	// Geocoder down: pin kept, retry queued; the retry writes the pin.
	_, r2 := h.draft(t)
	c = r2.Content
	c.Attributes[domain.RootKey].Fields["address"] = fv(map[string]any{"line1": "Plot 1", "city": "Nashik", "country": "IN"})
	h.geo.err = errors.New("timeout")
	res, err = h.svc.Save(ctx, editor, catalogService.SaveRequest{RevisionID: r2.ID, Rev: r2.Rev, Content: c}, "")
	if err != nil || len(res.Warnings) != 1 || res.Warnings[0] != catalogService.WarnGeocodePending || len(h.sent) != 1 {
		t.Fatalf("geocode down: %v %v sent=%d", err, res.Warnings, len(h.sent))
	}
	h.geo.err = nil
	h.geo.res.Lat = 20.0
	if err := h.svc.GeocodeRetry(ctx, h.sent[0].payload.(catalogService.GeocodeRetryPayload)); err != nil {
		t.Fatal(err)
	}
	after, _ := h.store.GetRevision(ctx, r2.ID)
	if loc, _ := rootValue[models.Location](after.Content.Attributes, fieldLocation); loc.Lat != 20.0 || after.Rev != res.Revision.Rev {
		t.Errorf("retry: lat %v rev %d (want rev unchanged %d)", loc.Lat, after.Rev, res.Revision.Rev)
	}
}

func TestMedia(t *testing.T) {
	h := newHarness()
	w, _, _ := h.svc.Create(ctx, editor, models.ListingContent{})
	req := catalogService.UploadRequest{WarehouseID: w.ID, Kind: models.MediaPhoto, Filename: "../My Photo!.jpg", ContentType: "image/jpeg", Bytes: 1000}
	up, err := h.media.UploadURL(ctx, editor, req)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := primitive.ObjectIDFromHex(up.MediaID)
	md, _ := h.store.GetMedia(ctx, id)
	if md.Visibility != models.VisibilityPublic || md.Bucket != "pub" || !strings.HasSuffix(md.Key, "/My-Photo.jpg") {
		t.Errorf("media = %+v", md)
	}
	if _, err := h.media.Confirm(ctx, id); code(err) != "not_uploaded" {
		t.Errorf("confirm before upload: %v", err)
	}
	h.s3.objects[md.Key] = idto.S3ObjectInfo{Size: 1000, ContentType: "image/jpeg"}
	if md, err = h.media.Confirm(ctx, id); err != nil || md.Status != models.MediaReady {
		t.Fatalf("confirm: %v", err)
	}

	big := req
	big.Bytes = 11 << 20
	if _, err := h.media.UploadURL(ctx, editor, big); code(err) != "invalid" {
		t.Errorf("oversized: %v", err)
	}
	h.media.UploadURL(ctx, editor, req)
	if _, err := h.media.UploadURL(ctx, editor, req); code(err) != "invalid" {
		t.Errorf("photo cap (2): %v", err)
	}

	agr, err := h.media.UploadURL(ctx, editor, catalogService.UploadRequest{WarehouseID: w.ID, Kind: models.MediaDoc, DocType: models.DocAgreement,
		Visibility: models.VisibilityPublic, Filename: "lease.pdf", ContentType: "application/pdf", Bytes: 5000})
	if err != nil {
		t.Fatal(err)
	}
	aid, _ := primitive.ObjectIDFromHex(agr.MediaID)
	if md, _ := h.store.GetMedia(ctx, aid); md.Visibility != models.VisibilityStaff || md.Bucket != "priv" {
		t.Errorf("agreement must be staff: %+v", md)
	}
	if link, _ := h.media.Link(ctx, aid); !strings.HasPrefix(link, "https://s3/get/") {
		t.Errorf("staff link = %q", link)
	}
}

func TestMediaGC(t *testing.T) {
	h := newHarness()
	w, r := h.draft(t)
	old := h.clock.Add(-40 * 24 * time.Hour)
	stalePending := &models.WarehouseMedia{WarehouseID: w.ID, Kind: models.MediaPhoto, Status: models.MediaPending, Key: "p", CreatedAt: h.clock.Add(-25 * time.Hour)}
	unreferenced := &models.WarehouseMedia{WarehouseID: w.ID, Kind: models.MediaPhoto, Status: models.MediaReady, Key: "u", CreatedAt: old}
	h.store.InsertMedia(ctx, stalePending)
	h.store.InsertMedia(ctx, unreferenced)
	// An old but referenced file survives.
	ref := r.Content.Media[0].MediaID
	h.store.mu.Lock()
	m := h.store.media[ref]
	m.CreatedAt = old
	h.store.media[ref] = m
	h.store.mu.Unlock()

	if err := h.media.GC(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.GetMedia(ctx, stalePending.ID); err == nil {
		t.Error("stale pending upload kept")
	}
	if _, err := h.store.GetMedia(ctx, unreferenced.ID); err == nil {
		t.Error("unreferenced media kept")
	}
	if _, err := h.store.GetMedia(ctx, ref); err != nil {
		t.Error("referenced media deleted")
	}
}

func TestSlugs(t *testing.T) {
	if got := slugFor("Şhree Gödown & Co.", "Navi Mumbai", "ab23cdef"); got != "shree-godown-co-navi-mumbai-ab23cdef" {
		t.Errorf("slug = %q", got)
	}
	id, err := newShortID()
	if err != nil || len(id) != shortIDLen {
		t.Fatal(id, err)
	}
	if got, ok := shortIDFromSlug("x-y-" + id); !ok || got != id {
		t.Errorf("parse = %q %v", got, ok)
	}
	for _, bad := range []string{"", "x-short", "x-ABCDEFGH", "x-0o1lilil"} {
		if _, ok := shortIDFromSlug(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

// Values read back from Mongo serialize with the same keys the API accepts.
func TestStoredValuesRoundTripJSON(t *testing.T) {
	h := newHarness()
	_, r := h.draft(t)
	b, err := json.Marshal(r.Content.Attributes[domain.RootKey].Fields)
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{`"postalCode":"410501"`, `"placeId":"p1"`, `"addressHash":`, `"basis":"per_sqft_month"`} {
		if !strings.Contains(js, want) {
			t.Errorf("missing %s in %s", want, js)
		}
	}
	if strings.Contains(js, `"Key"`) || strings.Contains(js, "postal_code") {
		t.Errorf("BSON shape leaked into JSON: %s", js)
	}
}

func TestApproveEmbedFollowsAISearchFlag(t *testing.T) {
	embeds := func(h *harness) int {
		n := 0
		for _, m := range h.sent {
			if m.pt == aiSearchService.ProcessEmbed {
				n++
			}
		}
		return n
	}
	h := newHarness()
	h.live(t)
	if n := embeds(h); n != 0 {
		t.Errorf("flag off: %d embed jobs", n)
	}
	h = newHarness()
	h.aiSearch = true
	w, _ := h.live(t)
	if n := embeds(h); n != 1 || h.sent[len(h.sent)-1].key != aiSearchService.EmbedKey(w.ID.Hex(), 1, "") {
		t.Errorf("flag on: %d embed jobs, sent %+v", n, h.sent)
	}
}
