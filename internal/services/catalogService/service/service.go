package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/catalogService"
	"github.com/atharva-ng/crunch/internal/services/catalogService/dto"
	"github.com/atharva-ng/crunch/internal/services/catalogService/store"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

type svc struct {
	store    store.Store
	rules    domain.Rules
	log      domain.ChangeLog
	geocoder func() interfaces.Geocoder
	dispatch func(ctx context.Context, pt pipeline.ProcessType, key string, payload any) error
	cfg      func() config.CatalogValues
	now      func() time.Time

	media  *mediaService
	public *publicSite
	search domain.SearchEngine
}

func (s *svc) record(ctx context.Context, actor domain.Actor, entity domain.ChangeEntity, id string, action domain.ChangeAction, before, after any, meta map[string]any) {
	s.log.Record(ctx, domain.ChangeEntry{Entity: entity, EntityID: id, Action: action, Actor: actor, Before: before, After: after, Meta: meta})
}

func (s *svc) preview(snap *domain.Snapshot, c models.ListingContent, media []models.WarehouseMedia) *dto.Preview {
	res := domain.Evaluate(snap, c.Attributes, s.now().UTC())
	problems := submitProblems(snap, c, media)
	if problems == nil {
		problems = []string{}
	}
	return &dto.Preview{State: res.State, Ratios: res.Ratios, NeedsInfo: res.NeedsInfo, Fit: res.Fit, SubmitProblems: problems}
}

func (s *svc) loadRevision(ctx context.Context, id primitive.ObjectID) (*models.WarehouseRevision, error) {
	return s.store.GetRevision(ctx, id)
}

// transition CAS-writes r (already mutated) over its old (state, rev),
// bumping rev, and logs it.
func (s *svc) transition(ctx context.Context, actor domain.Actor, old *models.WarehouseRevision, r *models.WarehouseRevision, action domain.ChangeAction, meta map[string]any) error {
	r.Rev = old.Rev + 1
	r.Open = r.State.IsOpen()
	r.UpdatedBy = actor.Email
	r.UpdatedAt = s.now().UTC()
	if err := s.store.ReplaceRevision(ctx, r, old.State, old.Rev); err != nil {
		if errors.Is(err, models.ErrVersionConflict) {
			return catalogService.Conflict(catalogService.CodeStale, "someone else changed this revision — reload")
		}
		return err
	}
	s.record(ctx, actor, domain.EntityRevision, r.ID.Hex(), action, old, r, meta)
	return nil
}

func cloneRevision(r *models.WarehouseRevision) models.WarehouseRevision {
	c := *r
	c.Review = append([]models.ReviewEntry(nil), r.Review...)
	return c
}

// --- create / open ---

// Create makes an unpublished warehouse with a first draft (v1). content may
// be empty.
func (s *svc) Create(ctx context.Context, actor domain.Actor, content models.ListingContent) (*models.Warehouse, catalogService.RevisionResult, error) {
	snap := s.rules.Snapshot()
	norm, err := normalizeContent(snap, content, nil)
	if err != nil {
		return nil, catalogService.RevisionResult{}, catalogService.Invalidf("%v", err)
	}
	now := s.now().UTC()
	name, city := draftName(norm.Attributes)
	w := &models.Warehouse{
		Status: models.WarehouseUnpublished, SlugHistory: []string{}, RevSeq: 1,
		Name: name, City: city, CreatedBy: actor.Email, CreatedAt: now, UpdatedAt: now,
	}
	w.Projection = models.Projection{Chips: []string{}, Unk: []string{}, Nums: []models.NumFact{}, Fit: []string{}, NeedsInfo: []string{}}
	for attempt := 0; ; attempt++ {
		if w.ShortID, err = newShortID(); err != nil {
			return nil, catalogService.RevisionResult{}, err
		}
		err = s.store.InsertWarehouse(ctx, w)
		if !errors.Is(err, models.ErrDuplicateKey) || attempt == 4 {
			break
		}
	}
	if err != nil {
		return nil, catalogService.RevisionResult{}, err
	}
	r := &models.WarehouseRevision{
		WarehouseID: w.ID, Version: 1, BaseVersion: 0, State: models.RevDraft, Open: true, Rev: 1,
		Content: norm, Review: []models.ReviewEntry{}, CreatedBy: actor.Email, UpdatedBy: actor.Email, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.InsertRevision(ctx, r); err != nil {
		return nil, catalogService.RevisionResult{}, err
	}
	if err := s.store.SetOpenRevision(ctx, w.ID, &r.ID, now); err != nil {
		return nil, catalogService.RevisionResult{}, err
	}
	w.OpenRevisionID = &r.ID
	s.record(ctx, actor, domain.EntityWarehouse, w.ID.Hex(), domain.ActionCreate, nil, w, nil)
	s.record(ctx, actor, domain.EntityRevision, r.ID.Hex(), domain.ActionCreate, nil, r, nil)
	return w, catalogService.RevisionResult{Revision: r, Preview: s.preview(snap, norm, nil)}, nil
}

// Open clones the live content into a new draft (edit live / archived).
func (s *svc) Open(ctx context.Context, actor domain.Actor, warehouseID primitive.ObjectID) (catalogService.RevisionResult, error) {
	return s.openFromLive(ctx, actor, warehouseID, false)
}

// Restore opens a draft for an archived warehouse from its last approved
// content; it goes live only when approved (D-055).
func (s *svc) Restore(ctx context.Context, actor domain.Actor, warehouseID primitive.ObjectID) (catalogService.RevisionResult, error) {
	return s.openFromLive(ctx, actor, warehouseID, true)
}

func (s *svc) openFromLive(ctx context.Context, actor domain.Actor, warehouseID primitive.ObjectID, restore bool) (catalogService.RevisionResult, error) {
	w, err := s.store.GetWarehouse(ctx, warehouseID)
	if err != nil {
		return catalogService.RevisionResult{}, err
	}
	switch {
	case restore && w.Status != models.WarehouseArchived:
		return catalogService.RevisionResult{}, catalogService.Conflict(catalogService.CodeBadState, "only an archived listing can be restored")
	case w.Status == models.WarehouseUnpublished || w.Live == nil:
		return catalogService.RevisionResult{}, catalogService.Conflict(catalogService.CodeBadState, "never published — edit its open draft instead")
	case w.OpenRevisionID != nil:
		return catalogService.RevisionResult{}, catalogService.Conflict(catalogService.CodeOpenExists, "a revision is already open")
	}
	seq, err := s.store.NextRevSeq(ctx, w.ID)
	if err != nil {
		return catalogService.RevisionResult{}, err
	}
	now := s.now().UTC()
	r := &models.WarehouseRevision{
		WarehouseID: w.ID, Version: seq, BaseVersion: w.LiveVersion, State: models.RevDraft, Open: true, Rev: 1,
		Content: cloneContent(*w.Live), Review: []models.ReviewEntry{}, CreatedBy: actor.Email, UpdatedBy: actor.Email, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.InsertRevision(ctx, r); err != nil {
		if errors.Is(err, models.ErrDuplicateKey) {
			return catalogService.RevisionResult{}, catalogService.Conflict(catalogService.CodeOpenExists, "a revision is already open")
		}
		return catalogService.RevisionResult{}, err
	}
	if err := s.store.SetOpenRevision(ctx, w.ID, &r.ID, now); err != nil {
		return catalogService.RevisionResult{}, err
	}
	action := domain.ActionCreate
	if restore {
		action = domain.ActionRestore
	}
	s.record(ctx, actor, domain.EntityRevision, r.ID.Hex(), action, nil, r, map[string]any{"baseVersion": r.BaseVersion})
	media, _ := s.store.ListMedia(ctx, w.ID)
	return catalogService.RevisionResult{Revision: r, Preview: s.preview(s.rules.Snapshot(), r.Content, media)}, nil
}

// cloneContent deep-copies the parts a draft edit may mutate.
func cloneContent(c models.ListingContent) models.ListingContent {
	out := models.ListingContent{Attributes: models.Attributes{}, Media: append([]models.MediaRef{}, c.Media...)}
	for k, st := range c.Attributes {
		ns := models.NodeState{Status: st.Status}
		if st.Fields != nil {
			ns.Fields = make(map[string]*models.FieldValue, len(st.Fields))
			for fk, fv := range st.Fields {
				if fv != nil {
					cp := *fv
					fv = &cp
				}
				ns.Fields[fk] = fv
			}
		}
		out.Attributes[k] = ns
	}
	if c.RentAdmin != nil {
		ra := *c.RentAdmin
		out.RentAdmin = &ra
	}
	return out
}

// --- save ---

// Save validates and stores a draft's content, geocodes a changed address
// (D-061) and returns the evaluator preview. Required fields are not
// enforced here.
func (s *svc) Save(ctx context.Context, actor domain.Actor, req catalogService.SaveRequest) (catalogService.RevisionResult, error) {
	old, err := s.loadRevision(ctx, req.RevisionID)
	if err != nil {
		return catalogService.RevisionResult{}, err
	}
	switch {
	case old.State == models.RevInReview:
		return catalogService.RevisionResult{}, catalogService.Conflict(catalogService.CodeInReview, "in review — withdraw it to edit (D-054)")
	case old.State != models.RevDraft:
		return catalogService.RevisionResult{}, catalogService.Conflict(catalogService.CodeBadState, "revision is %s", old.State)
	case old.Rev != req.Rev:
		return catalogService.RevisionResult{}, catalogService.Conflict(catalogService.CodeStale, "someone else saved — reload (D-052)")
	}
	media, err := s.store.ListMedia(ctx, old.WarehouseID)
	if err != nil {
		return catalogService.RevisionResult{}, err
	}
	snap := s.rules.Snapshot()
	content, err := normalizeContent(snap, req.Content, media)
	if err != nil {
		return catalogService.RevisionResult{}, catalogService.Invalidf("%v", err)
	}
	warnings := s.geocodeOnSave(ctx, old, &content)

	r := cloneRevision(old)
	r.Content = content
	if err := s.transition(ctx, actor, old, &r, domain.ActionUpdate, nil); err != nil {
		return catalogService.RevisionResult{}, err
	}
	name, city := draftName(content.Attributes)
	if err := s.store.SetDraftName(ctx, r.WarehouseID, name, city, r.UpdatedAt); err != nil {
		log.Error("catalog: hoist draft name failed", "warehouse", r.WarehouseID.Hex(), "error", err)
	}
	return catalogService.RevisionResult{Revision: &r, Preview: s.preview(snap, content, media), Warnings: warnings}, nil
}

// --- review flow ---

// Submit moves draft → in_review behind the validation gate.
func (s *svc) Submit(ctx context.Context, actor domain.Actor, id primitive.ObjectID, batchID string) (catalogService.RevisionResult, error) {
	old, err := s.loadRevision(ctx, id)
	if err != nil {
		return catalogService.RevisionResult{}, err
	}
	if old.State != models.RevDraft {
		return catalogService.RevisionResult{}, catalogService.Conflict(catalogService.CodeBadState, "only a draft can be submitted (it is %s)", old.State)
	}
	media, err := s.store.ListMedia(ctx, old.WarehouseID)
	if err != nil {
		return catalogService.RevisionResult{}, err
	}
	if problems := submitProblems(s.rules.Snapshot(), old.Content, media); len(problems) > 0 {
		return catalogService.RevisionResult{}, &catalogService.SubmitBlockedError{Problems: problems}
	}
	now := s.now().UTC()
	r := cloneRevision(old)
	r.State, r.SubmittedBy, r.SubmittedByID, r.SubmittedAt, r.BatchID = models.RevInReview, actor.Email, actor.UserID, &now, batchID
	r.Review = append(r.Review, models.ReviewEntry{Action: "submit", By: actor.Email, At: now})
	action, meta := domain.ActionSubmit, map[string]any(nil)
	if batchID != "" {
		action, meta = domain.ActionBulkSubmit, map[string]any{"batchId": batchID}
	}
	if err := s.transition(ctx, actor, old, &r, action, meta); err != nil {
		return catalogService.RevisionResult{}, err
	}
	return catalogService.RevisionResult{Revision: &r}, nil
}

// Withdraw moves in_review → draft.
func (s *svc) Withdraw(ctx context.Context, actor domain.Actor, id primitive.ObjectID) (catalogService.RevisionResult, error) {
	return s.backToDraft(ctx, actor, id, "withdraw", "", domain.ActionWithdraw)
}

// Reject moves in_review → draft with a required comment (D-054).
func (s *svc) Reject(ctx context.Context, actor domain.Actor, id primitive.ObjectID, comment string) (catalogService.RevisionResult, error) {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return catalogService.RevisionResult{}, catalogService.Invalidf("a rejection needs a comment")
	}
	return s.backToDraft(ctx, actor, id, "reject", comment, domain.ActionReject)
}

func (s *svc) backToDraft(ctx context.Context, actor domain.Actor, id primitive.ObjectID, verb, comment string, action domain.ChangeAction) (catalogService.RevisionResult, error) {
	old, err := s.loadRevision(ctx, id)
	if err != nil {
		return catalogService.RevisionResult{}, err
	}
	if old.State != models.RevInReview {
		return catalogService.RevisionResult{}, catalogService.Conflict(catalogService.CodeBadState, "only a revision in review can be %sed (it is %s)", strings.TrimSuffix(verb, "e"), old.State)
	}
	r := cloneRevision(old)
	r.State, r.BatchID = models.RevDraft, ""
	r.Review = append(r.Review, models.ReviewEntry{Action: verb, By: actor.Email, At: s.now().UTC(), Comment: comment})
	if err := s.transition(ctx, actor, old, &r, action, nil); err != nil {
		return catalogService.RevisionResult{}, err
	}
	return catalogService.RevisionResult{Revision: &r}, nil
}

// Discard moves draft → discarded and frees the warehouse for a new
// revision. A never-published warehouse stays (empty) until an approver
// deletes it.
func (s *svc) Discard(ctx context.Context, actor domain.Actor, id primitive.ObjectID) (catalogService.RevisionResult, error) {
	old, err := s.loadRevision(ctx, id)
	if err != nil {
		return catalogService.RevisionResult{}, err
	}
	if old.State != models.RevDraft {
		return catalogService.RevisionResult{}, catalogService.Conflict(catalogService.CodeBadState, "only a draft can be discarded (it is %s)", old.State)
	}
	r := cloneRevision(old)
	r.State = models.RevDiscarded
	r.Review = append(r.Review, models.ReviewEntry{Action: "discard", By: actor.Email, At: s.now().UTC()})
	if err := s.transition(ctx, actor, old, &r, domain.ActionDelete, nil); err != nil {
		return catalogService.RevisionResult{}, err
	}
	if err := s.store.SetOpenRevision(ctx, r.WarehouseID, nil, r.UpdatedAt); err != nil {
		return catalogService.RevisionResult{}, err
	}
	return catalogService.RevisionResult{Revision: &r}, nil
}

// --- approve ---

// Approve runs spec 03's ordered, retry-safe approve. A re-run on an
// already-approved revision finishes whatever steps are missing.
func (s *svc) Approve(ctx context.Context, actor domain.Actor, id primitive.ObjectID, batchID string) (catalogService.RevisionResult, error) {
	old, err := s.loadRevision(ctx, id)
	if err != nil {
		return catalogService.RevisionResult{}, err
	}
	r := cloneRevision(old)
	resumed := false
	switch old.State {
	case models.RevInReview:
		if old.SubmittedByID != "" && old.SubmittedByID == actor.UserID && !s.cfg().AllowSelfApprove {
			return catalogService.RevisionResult{}, catalogService.Conflict(catalogService.CodeSelfApprove, "you submitted this — another approver must approve it (D-053)")
		}
		// 1. CAS in_review → approved.
		now := s.now().UTC()
		r.State, r.ApprovedBy, r.ApprovedAt = models.RevApproved, actor.Email, &now
		r.Review = append(r.Review, models.ReviewEntry{Action: "approve", By: actor.Email, At: now})
		r.Rev = old.Rev + 1
		r.Open, r.UpdatedBy, r.UpdatedAt = false, actor.Email, now
		if err := s.store.ReplaceRevision(ctx, &r, old.State, old.Rev); err != nil {
			if errors.Is(err, models.ErrVersionConflict) {
				return catalogService.RevisionResult{}, catalogService.Conflict(catalogService.CodeStale, "someone else changed this revision — reload")
			}
			return catalogService.RevisionResult{}, err
		}
	case models.RevApproved:
		resumed = true
	default:
		return catalogService.RevisionResult{}, catalogService.Conflict(catalogService.CodeBadState, "only a revision in review can be approved (it is %s)", old.State)
	}

	w, err := s.store.GetWarehouse(ctx, r.WarehouseID)
	if err != nil {
		return catalogService.RevisionResult{}, err
	}
	before := w.Live
	if w.LiveVersion != r.Version {
		// 2–3. Evaluate, then CAS the live doc on the base version.
		media, err := s.store.ListMedia(ctx, w.ID)
		if err != nil {
			return catalogService.RevisionResult{}, err
		}
		patch := buildPublish(s.rules.Snapshot(), r.Content, media, r.Version, w.ShortID, s.now().UTC())
		ok, err := s.store.PublishLive(ctx, w.ID, r.BaseVersion, patch)
		if err != nil {
			return catalogService.RevisionResult{}, err
		}
		if !ok {
			s.revertApprove(ctx, actor, &r)
			return catalogService.RevisionResult{}, catalogService.Conflict(catalogService.CodeStaleBase, "another version went live first — the revision is back in review; reopen and redo the edit")
		}
	}
	// 4–7. Idempotent tail.
	s.finishApprove(ctx, actor, w, &r, before, resumed, batchID)
	return catalogService.RevisionResult{Revision: &r}, nil
}

// revertApprove puts a revision whose base went stale back in review.
func (s *svc) revertApprove(ctx context.Context, actor domain.Actor, r *models.WarehouseRevision) {
	back := cloneRevision(r)
	back.State, back.ApprovedBy, back.ApprovedAt = models.RevInReview, "", nil
	back.Review = append(back.Review, models.ReviewEntry{Action: "approve_failed", By: actor.Email, At: s.now().UTC(),
		Comment: "another version went live first"})
	if err := s.transition(ctx, actor, r, &back, domain.ActionUpdate, map[string]any{"op": "approve_failed"}); err != nil {
		log.Error("catalog: revert stale approve failed", "revision", r.ID.Hex(), "error", err)
	}
}

func (s *svc) finishApprove(ctx context.Context, actor domain.Actor, w *models.Warehouse, r *models.WarehouseRevision, before *models.ListingContent, resumed bool, batchID string) {
	ctx = context.WithoutCancel(ctx)
	// 4. Supersede older approved revisions.
	prev, err := s.store.ApprovedRevisions(ctx, w.ID)
	if err != nil {
		log.Error("catalog: list approved revisions failed", "warehouse", w.ID.Hex(), "error", err)
	}
	for i := range prev {
		p := &prev[i]
		if p.ID == r.ID {
			continue
		}
		sup := cloneRevision(p)
		sup.State = models.RevSuperseded
		if err := s.transition(ctx, actor, p, &sup, domain.ActionUpdate, map[string]any{"op": "supersede", "by": r.ID.Hex()}); err != nil {
			log.Error("catalog: supersede failed", "revision", p.ID.Hex(), "error", err)
		}
	}
	// 5. Rent terms.
	if rent, ok := rootValue[models.Money](r.Content.Attributes, fieldRent); ok {
		if err := s.store.UpsertRent(ctx, models.WarehouseRent{WarehouseID: w.ID, TermsVersion: r.Version, Headline: rent,
			Admin: r.Content.RentAdmin, EffectiveFrom: s.now().UTC()}); err != nil {
			log.Error("catalog: rent upsert failed", "warehouse", w.ID.Hex(), "error", err)
		}
	}
	// 6. Catalog version (map cache key, 04). The embedding job (05)
	// dispatches from here once aisearch lands.
	if _, err := s.store.BumpCatalogVersion(ctx); err != nil {
		log.Error("catalog: catalogVersion bump failed", "error", err)
	}
	// 7. Audit: old live → new live.
	action, meta := domain.ActionApprove, map[string]any{"revisionId": r.ID.Hex(), "version": r.Version}
	if batchID != "" {
		action, meta["batchId"] = domain.ActionBulkApprove, batchID
	}
	if resumed {
		meta["resumed"] = true
	}
	s.record(ctx, actor, domain.EntityWarehouse, w.ID.Hex(), action, before, r.Content, meta)
}

// BulkApprove approves many revisions with per-item results (same rules as
// Approve). batchID ties the change_log rows together.
func (s *svc) BulkApprove(ctx context.Context, actor domain.Actor, ids []primitive.ObjectID) (string, []catalogService.BulkItem, error) {
	if max := s.cfg().BulkApproveMax; max > 0 && len(ids) > max {
		return "", nil, catalogService.Invalidf("at most %d revisions per request", max)
	}
	batchID := primitive.NewObjectID().Hex()
	out := make([]catalogService.BulkItem, 0, len(ids))
	for _, id := range ids {
		item := catalogService.BulkItem{RevisionID: id.Hex(), OK: true}
		if _, err := s.Approve(ctx, actor, id, batchID); err != nil {
			item.OK, item.Code, item.Error = false, errorCode(err), err.Error()
		}
		out = append(out, item)
	}
	return batchID, out, nil
}

// --- warehouse-level ---

// Archive takes a live listing down (its open revision is kept).
func (s *svc) Archive(ctx context.Context, actor domain.Actor, id primitive.ObjectID) error {
	w, err := s.store.GetWarehouse(ctx, id)
	if err != nil {
		return err
	}
	ok, err := s.store.Archive(ctx, id, actor.Email, s.now().UTC())
	if err != nil {
		return err
	}
	if !ok {
		return catalogService.Conflict(catalogService.CodeBadState, "only a live listing can be archived (it is %s)", w.Status)
	}
	if _, err := s.store.BumpCatalogVersion(context.WithoutCancel(ctx)); err != nil {
		log.Error("catalog: catalogVersion bump failed", "error", err)
	}
	s.record(ctx, actor, domain.EntityWarehouse, id.Hex(), domain.ActionArchive, nil, nil, map[string]any{"liveVersion": w.LiveVersion})
	return nil
}

// Delete hard-deletes a never-published warehouse and its revisions. Its
// media become unreferenced and the media GC removes them.
func (s *svc) Delete(ctx context.Context, actor domain.Actor, id primitive.ObjectID) error {
	w, err := s.store.GetWarehouse(ctx, id)
	if err != nil {
		return err
	}
	ok, err := s.store.DeleteUnpublished(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return catalogService.Conflict(catalogService.CodeBadState, "only an unpublished warehouse can be deleted (it is %s) — archive it instead", w.Status)
	}
	if err := s.store.DeleteRevisions(ctx, id); err != nil {
		log.Error("catalog: delete revisions failed", "warehouse", id.Hex(), "error", err)
	}
	s.record(ctx, actor, domain.EntityWarehouse, id.Hex(), domain.ActionDelete, w, nil, nil)
	return nil
}

// errorCode is the machine code of a service error.
func errorCode(err error) string {
	var (
		se *catalogService.StateError
		ve *catalogService.ValidationError
	)
	switch {
	case errors.As(err, &se):
		return se.Code
	case errors.As(err, &ve):
		return "invalid"
	case errors.Is(err, catalogService.ErrNotFound):
		return "not_found"
	}
	return "internal"
}
