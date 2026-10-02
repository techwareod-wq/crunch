package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Service errors the handlers map to HTTP.

// validationError is a 400.
type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }

func invalidf(format string, a ...any) error { return &validationError{msg: fmt.Sprintf(format, a...)} }

// stateError is a 409 with a machine code: the action isn't allowed in the
// current state (in_review lock, already open, not live…).
type stateError struct{ code, msg string }

func (e *stateError) Error() string { return e.msg }

func conflict(code, format string, a ...any) error {
	return &stateError{code: code, msg: fmt.Sprintf(format, a...)}
}

// submitBlockedError is a 400 listing what stops a submit.
type submitBlockedError struct{ Problems []string }

func (e *submitBlockedError) Error() string {
	return "submit blocked: " + strings.Join(e.Problems, "; ")
}

// Conflict codes.
const (
	codeInReview    = "in_review"
	codeBadState    = "bad_state"
	codeStale       = "version_conflict"
	codeOpenExists  = "open_revision_exists"
	codeSelfApprove = "self_approve"
	codeStaleBase   = "stale_base"
)

// Actor is the acting staff member.
type Actor = domain.Actor

// Service runs the listing lifecycle (spec 03). Every transition is a CAS on
// the revision (state + rev) and writes change_log.
type Service struct {
	store    Store
	rules    domain.Rules
	log      domain.ChangeLog
	geocoder func() interfaces.Geocoder
	dispatch func(ctx context.Context, pt pipeline.ProcessType, key string, payload any) error
	cfg      func() config.CatalogValues
	now      func() time.Time
}

func (s *Service) record(ctx context.Context, actor Actor, entity domain.ChangeEntity, id string, action domain.ChangeAction, before, after any, meta map[string]any) {
	s.log.Record(ctx, domain.ChangeEntry{Entity: entity, EntityID: id, Action: action, Actor: actor, Before: before, After: after, Meta: meta})
}

// Preview is the evaluator output returned with a saved draft.
type Preview struct {
	State     map[string]domain.NodeStatus `json:"state"`
	Ratios    map[string]float64           `json:"ratios"`
	NeedsInfo []string                     `json:"needsInfo"`
	Fit       map[string]domain.Verdict    `json:"fit"`
	// SubmitProblems is what a submit would reject right now.
	SubmitProblems []string `json:"submitProblems"`
}

// RevisionResult is returned by every revision write.
type RevisionResult struct {
	Revision *domain.Revision `json:"revision"`
	Preview  *Preview         `json:"preview,omitempty"`
	Warnings []string         `json:"warnings,omitempty"`
}

func (s *Service) preview(snap *domain.Snapshot, c domain.Content, media []domain.Media) *Preview {
	res := domain.Evaluate(snap, c.Attributes, s.now().UTC())
	problems := submitProblems(snap, c, media)
	if problems == nil {
		problems = []string{}
	}
	return &Preview{State: res.State, Ratios: res.Ratios, NeedsInfo: res.NeedsInfo, Fit: res.Fit, SubmitProblems: problems}
}

func (s *Service) loadRevision(ctx context.Context, id primitive.ObjectID) (*domain.Revision, error) {
	return s.store.GetRevision(ctx, id)
}

// transition CAS-writes r (already mutated) over its old (state, rev),
// bumping rev, and logs it.
func (s *Service) transition(ctx context.Context, actor Actor, old *domain.Revision, r *domain.Revision, action domain.ChangeAction, meta map[string]any) error {
	r.Rev = old.Rev + 1
	r.Open = r.State.IsOpen()
	r.UpdatedBy = actor.Email
	r.UpdatedAt = s.now().UTC()
	if err := s.store.ReplaceRevision(ctx, r, old.State, old.Rev); err != nil {
		if errors.Is(err, errCAS) {
			return conflict(codeStale, "someone else changed this revision — reload")
		}
		return err
	}
	s.record(ctx, actor, domain.EntityRevision, r.ID.Hex(), action, old, r, meta)
	return nil
}

func cloneRevision(r *domain.Revision) domain.Revision {
	c := *r
	c.Review = append([]domain.ReviewEntry(nil), r.Review...)
	return c
}

// --- create / open ---

// Create makes an unpublished warehouse with a first draft (v1). content may
// be empty.
func (s *Service) Create(ctx context.Context, actor Actor, content domain.Content) (*domain.Warehouse, RevisionResult, error) {
	snap := s.rules.Snapshot()
	norm, err := normalizeContent(snap, content, nil)
	if err != nil {
		return nil, RevisionResult{}, invalidf("%v", err)
	}
	now := s.now().UTC()
	name, city := draftName(norm.Attributes)
	w := &domain.Warehouse{
		Status: domain.WarehouseUnpublished, SlugHistory: []string{}, RevSeq: 1,
		Name: name, City: city, CreatedBy: actor.Email, CreatedAt: now, UpdatedAt: now,
	}
	w.Projection = domain.Projection{Chips: []string{}, Unk: []string{}, Nums: []domain.NumFact{}, Fit: []string{}, NeedsInfo: []string{}}
	for attempt := 0; ; attempt++ {
		if w.ShortID, err = newShortID(); err != nil {
			return nil, RevisionResult{}, err
		}
		err = s.store.InsertWarehouse(ctx, w)
		if !errors.Is(err, errDuplicate) || attempt == 4 {
			break
		}
	}
	if err != nil {
		return nil, RevisionResult{}, err
	}
	r := &domain.Revision{
		WarehouseID: w.ID, Version: 1, BaseVersion: 0, State: domain.RevDraft, Open: true, Rev: 1,
		Content: norm, Review: []domain.ReviewEntry{}, CreatedBy: actor.Email, UpdatedBy: actor.Email, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.InsertRevision(ctx, r); err != nil {
		return nil, RevisionResult{}, err
	}
	if err := s.store.SetOpenRevision(ctx, w.ID, &r.ID, now); err != nil {
		return nil, RevisionResult{}, err
	}
	w.OpenRevisionID = &r.ID
	s.record(ctx, actor, domain.EntityWarehouse, w.ID.Hex(), domain.ActionCreate, nil, w, nil)
	s.record(ctx, actor, domain.EntityRevision, r.ID.Hex(), domain.ActionCreate, nil, r, nil)
	return w, RevisionResult{Revision: r, Preview: s.preview(snap, norm, nil)}, nil
}

// Open clones the live content into a new draft (edit live / archived).
func (s *Service) Open(ctx context.Context, actor Actor, warehouseID primitive.ObjectID) (RevisionResult, error) {
	return s.openFromLive(ctx, actor, warehouseID, false)
}

// Restore opens a draft for an archived warehouse from its last approved
// content; it goes live only when approved (D-055).
func (s *Service) Restore(ctx context.Context, actor Actor, warehouseID primitive.ObjectID) (RevisionResult, error) {
	return s.openFromLive(ctx, actor, warehouseID, true)
}

func (s *Service) openFromLive(ctx context.Context, actor Actor, warehouseID primitive.ObjectID, restore bool) (RevisionResult, error) {
	w, err := s.store.GetWarehouse(ctx, warehouseID)
	if err != nil {
		return RevisionResult{}, err
	}
	switch {
	case restore && w.Status != domain.WarehouseArchived:
		return RevisionResult{}, conflict(codeBadState, "only an archived listing can be restored")
	case w.Status == domain.WarehouseUnpublished || w.Live == nil:
		return RevisionResult{}, conflict(codeBadState, "never published — edit its open draft instead")
	case w.OpenRevisionID != nil:
		return RevisionResult{}, conflict(codeOpenExists, "a revision is already open")
	}
	seq, err := s.store.NextRevSeq(ctx, w.ID)
	if err != nil {
		return RevisionResult{}, err
	}
	now := s.now().UTC()
	r := &domain.Revision{
		WarehouseID: w.ID, Version: seq, BaseVersion: w.LiveVersion, State: domain.RevDraft, Open: true, Rev: 1,
		Content: cloneContent(*w.Live), Review: []domain.ReviewEntry{}, CreatedBy: actor.Email, UpdatedBy: actor.Email, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.InsertRevision(ctx, r); err != nil {
		if errors.Is(err, errDuplicate) {
			return RevisionResult{}, conflict(codeOpenExists, "a revision is already open")
		}
		return RevisionResult{}, err
	}
	if err := s.store.SetOpenRevision(ctx, w.ID, &r.ID, now); err != nil {
		return RevisionResult{}, err
	}
	action := domain.ActionCreate
	if restore {
		action = domain.ActionRestore
	}
	s.record(ctx, actor, domain.EntityRevision, r.ID.Hex(), action, nil, r, map[string]any{"baseVersion": r.BaseVersion})
	media, _ := s.store.ListMedia(ctx, w.ID)
	return RevisionResult{Revision: r, Preview: s.preview(s.rules.Snapshot(), r.Content, media)}, nil
}

// cloneContent deep-copies the parts a draft edit may mutate.
func cloneContent(c domain.Content) domain.Content {
	out := domain.Content{Attributes: domain.Attributes{}, Media: append([]domain.MediaRef{}, c.Media...)}
	for k, st := range c.Attributes {
		ns := domain.NodeState{Status: st.Status}
		if st.Fields != nil {
			ns.Fields = make(map[string]*domain.FieldValue, len(st.Fields))
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

// SaveRequest is a draft save (CAS on rev, D-052).
type SaveRequest struct {
	RevisionID primitive.ObjectID `json:"revisionId"`
	Rev        int                `json:"rev"`
	Content    domain.Content     `json:"content"`
}

// Save validates and stores a draft's content, geocodes a changed address
// (D-061) and returns the evaluator preview. Required fields are not
// enforced here.
func (s *Service) Save(ctx context.Context, actor Actor, req SaveRequest) (RevisionResult, error) {
	old, err := s.loadRevision(ctx, req.RevisionID)
	if err != nil {
		return RevisionResult{}, err
	}
	switch {
	case old.State == domain.RevInReview:
		return RevisionResult{}, conflict(codeInReview, "in review — withdraw it to edit (D-054)")
	case old.State != domain.RevDraft:
		return RevisionResult{}, conflict(codeBadState, "revision is %s", old.State)
	case old.Rev != req.Rev:
		return RevisionResult{}, conflict(codeStale, "someone else saved — reload (D-052)")
	}
	media, err := s.store.ListMedia(ctx, old.WarehouseID)
	if err != nil {
		return RevisionResult{}, err
	}
	snap := s.rules.Snapshot()
	content, err := normalizeContent(snap, req.Content, media)
	if err != nil {
		return RevisionResult{}, invalidf("%v", err)
	}
	warnings := s.geocodeOnSave(ctx, old, &content)

	r := cloneRevision(old)
	r.Content = content
	if err := s.transition(ctx, actor, old, &r, domain.ActionUpdate, nil); err != nil {
		return RevisionResult{}, err
	}
	name, city := draftName(content.Attributes)
	if err := s.store.SetDraftName(ctx, r.WarehouseID, name, city, r.UpdatedAt); err != nil {
		log.Error("catalog: hoist draft name failed", "warehouse", r.WarehouseID.Hex(), "error", err)
	}
	return RevisionResult{Revision: &r, Preview: s.preview(snap, content, media), Warnings: warnings}, nil
}

// --- review flow ---

// Submit moves draft → in_review behind the validation gate.
func (s *Service) Submit(ctx context.Context, actor Actor, id primitive.ObjectID, batchID string) (RevisionResult, error) {
	old, err := s.loadRevision(ctx, id)
	if err != nil {
		return RevisionResult{}, err
	}
	if old.State != domain.RevDraft {
		return RevisionResult{}, conflict(codeBadState, "only a draft can be submitted (it is %s)", old.State)
	}
	media, err := s.store.ListMedia(ctx, old.WarehouseID)
	if err != nil {
		return RevisionResult{}, err
	}
	if problems := submitProblems(s.rules.Snapshot(), old.Content, media); len(problems) > 0 {
		return RevisionResult{}, &submitBlockedError{Problems: problems}
	}
	now := s.now().UTC()
	r := cloneRevision(old)
	r.State, r.SubmittedBy, r.SubmittedByID, r.SubmittedAt, r.BatchID = domain.RevInReview, actor.Email, actor.UserID, &now, batchID
	r.Review = append(r.Review, domain.ReviewEntry{Action: "submit", By: actor.Email, At: now})
	action, meta := domain.ActionSubmit, map[string]any(nil)
	if batchID != "" {
		action, meta = domain.ActionBulkSubmit, map[string]any{"batchId": batchID}
	}
	if err := s.transition(ctx, actor, old, &r, action, meta); err != nil {
		return RevisionResult{}, err
	}
	return RevisionResult{Revision: &r}, nil
}

// Withdraw moves in_review → draft.
func (s *Service) Withdraw(ctx context.Context, actor Actor, id primitive.ObjectID) (RevisionResult, error) {
	return s.backToDraft(ctx, actor, id, "withdraw", "", domain.ActionWithdraw)
}

// Reject moves in_review → draft with a required comment (D-054).
func (s *Service) Reject(ctx context.Context, actor Actor, id primitive.ObjectID, comment string) (RevisionResult, error) {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return RevisionResult{}, invalidf("a rejection needs a comment")
	}
	return s.backToDraft(ctx, actor, id, "reject", comment, domain.ActionReject)
}

func (s *Service) backToDraft(ctx context.Context, actor Actor, id primitive.ObjectID, verb, comment string, action domain.ChangeAction) (RevisionResult, error) {
	old, err := s.loadRevision(ctx, id)
	if err != nil {
		return RevisionResult{}, err
	}
	if old.State != domain.RevInReview {
		return RevisionResult{}, conflict(codeBadState, "only a revision in review can be %sed (it is %s)", strings.TrimSuffix(verb, "e"), old.State)
	}
	r := cloneRevision(old)
	r.State, r.BatchID = domain.RevDraft, ""
	r.Review = append(r.Review, domain.ReviewEntry{Action: verb, By: actor.Email, At: s.now().UTC(), Comment: comment})
	if err := s.transition(ctx, actor, old, &r, action, nil); err != nil {
		return RevisionResult{}, err
	}
	return RevisionResult{Revision: &r}, nil
}

// Discard moves draft → discarded and frees the warehouse for a new
// revision. A never-published warehouse stays (empty) until an approver
// deletes it.
func (s *Service) Discard(ctx context.Context, actor Actor, id primitive.ObjectID) (RevisionResult, error) {
	old, err := s.loadRevision(ctx, id)
	if err != nil {
		return RevisionResult{}, err
	}
	if old.State != domain.RevDraft {
		return RevisionResult{}, conflict(codeBadState, "only a draft can be discarded (it is %s)", old.State)
	}
	r := cloneRevision(old)
	r.State = domain.RevDiscarded
	r.Review = append(r.Review, domain.ReviewEntry{Action: "discard", By: actor.Email, At: s.now().UTC()})
	if err := s.transition(ctx, actor, old, &r, domain.ActionDelete, nil); err != nil {
		return RevisionResult{}, err
	}
	if err := s.store.SetOpenRevision(ctx, r.WarehouseID, nil, r.UpdatedAt); err != nil {
		return RevisionResult{}, err
	}
	return RevisionResult{Revision: &r}, nil
}

// --- approve ---

// Approve runs spec 03's ordered, retry-safe approve. A re-run on an
// already-approved revision finishes whatever steps are missing.
func (s *Service) Approve(ctx context.Context, actor Actor, id primitive.ObjectID, batchID string) (RevisionResult, error) {
	old, err := s.loadRevision(ctx, id)
	if err != nil {
		return RevisionResult{}, err
	}
	r := cloneRevision(old)
	resumed := false
	switch old.State {
	case domain.RevInReview:
		if old.SubmittedByID != "" && old.SubmittedByID == actor.UserID && !s.cfg().AllowSelfApprove {
			return RevisionResult{}, conflict(codeSelfApprove, "you submitted this — another approver must approve it (D-053)")
		}
		// 1. CAS in_review → approved.
		now := s.now().UTC()
		r.State, r.ApprovedBy, r.ApprovedAt = domain.RevApproved, actor.Email, &now
		r.Review = append(r.Review, domain.ReviewEntry{Action: "approve", By: actor.Email, At: now})
		r.Rev = old.Rev + 1
		r.Open, r.UpdatedBy, r.UpdatedAt = false, actor.Email, now
		if err := s.store.ReplaceRevision(ctx, &r, old.State, old.Rev); err != nil {
			if errors.Is(err, errCAS) {
				return RevisionResult{}, conflict(codeStale, "someone else changed this revision — reload")
			}
			return RevisionResult{}, err
		}
	case domain.RevApproved:
		resumed = true
	default:
		return RevisionResult{}, conflict(codeBadState, "only a revision in review can be approved (it is %s)", old.State)
	}

	w, err := s.store.GetWarehouse(ctx, r.WarehouseID)
	if err != nil {
		return RevisionResult{}, err
	}
	before := w.Live
	if w.LiveVersion != r.Version {
		// 2–3. Evaluate, then CAS the live doc on the base version.
		media, err := s.store.ListMedia(ctx, w.ID)
		if err != nil {
			return RevisionResult{}, err
		}
		patch := buildPublish(s.rules.Snapshot(), r.Content, media, r.Version, w.ShortID, s.now().UTC())
		ok, err := s.store.PublishLive(ctx, w.ID, r.BaseVersion, patch)
		if err != nil {
			return RevisionResult{}, err
		}
		if !ok {
			s.revertApprove(ctx, actor, &r)
			return RevisionResult{}, conflict(codeStaleBase, "another version went live first — the revision is back in review; reopen and redo the edit")
		}
	}
	// 4–7. Idempotent tail.
	s.finishApprove(ctx, actor, w, &r, before, resumed, batchID)
	return RevisionResult{Revision: &r}, nil
}

// revertApprove puts a revision whose base went stale back in review.
func (s *Service) revertApprove(ctx context.Context, actor Actor, r *domain.Revision) {
	back := cloneRevision(r)
	back.State, back.ApprovedBy, back.ApprovedAt = domain.RevInReview, "", nil
	back.Review = append(back.Review, domain.ReviewEntry{Action: "approve_failed", By: actor.Email, At: s.now().UTC(),
		Comment: "another version went live first"})
	if err := s.transition(ctx, actor, r, &back, domain.ActionUpdate, map[string]any{"op": "approve_failed"}); err != nil {
		log.Error("catalog: revert stale approve failed", "revision", r.ID.Hex(), "error", err)
	}
}

func (s *Service) finishApprove(ctx context.Context, actor Actor, w *domain.Warehouse, r *domain.Revision, before *domain.Content, resumed bool, batchID string) {
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
		sup.State = domain.RevSuperseded
		if err := s.transition(ctx, actor, p, &sup, domain.ActionUpdate, map[string]any{"op": "supersede", "by": r.ID.Hex()}); err != nil {
			log.Error("catalog: supersede failed", "revision", p.ID.Hex(), "error", err)
		}
	}
	// 5. Rent terms.
	if rent, ok := rootValue[domain.Money](r.Content.Attributes, fieldRent); ok {
		if err := s.store.UpsertRent(ctx, domain.WarehouseRent{WarehouseID: w.ID, TermsVersion: r.Version, Headline: rent,
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

// BulkItem is one bulk-approve result.
type BulkItem struct {
	RevisionID string `json:"revisionId"`
	OK         bool   `json:"ok"`
	Code       string `json:"code,omitempty"`
	Error      string `json:"error,omitempty"`
}

// BulkApprove approves many revisions with per-item results (same rules as
// Approve). batchID ties the change_log rows together.
func (s *Service) BulkApprove(ctx context.Context, actor Actor, ids []primitive.ObjectID) (string, []BulkItem, error) {
	if max := s.cfg().BulkApproveMax; max > 0 && len(ids) > max {
		return "", nil, invalidf("at most %d revisions per request", max)
	}
	batchID := primitive.NewObjectID().Hex()
	out := make([]BulkItem, 0, len(ids))
	for _, id := range ids {
		item := BulkItem{RevisionID: id.Hex(), OK: true}
		if _, err := s.Approve(ctx, actor, id, batchID); err != nil {
			item.OK, item.Code, item.Error = false, errorCode(err), err.Error()
		}
		out = append(out, item)
	}
	return batchID, out, nil
}

// --- warehouse-level ---

// Archive takes a live listing down (its open revision is kept).
func (s *Service) Archive(ctx context.Context, actor Actor, id primitive.ObjectID) error {
	w, err := s.store.GetWarehouse(ctx, id)
	if err != nil {
		return err
	}
	ok, err := s.store.Archive(ctx, id, actor.Email, s.now().UTC())
	if err != nil {
		return err
	}
	if !ok {
		return conflict(codeBadState, "only a live listing can be archived (it is %s)", w.Status)
	}
	if _, err := s.store.BumpCatalogVersion(context.WithoutCancel(ctx)); err != nil {
		log.Error("catalog: catalogVersion bump failed", "error", err)
	}
	s.record(ctx, actor, domain.EntityWarehouse, id.Hex(), domain.ActionArchive, nil, nil, map[string]any{"liveVersion": w.LiveVersion})
	return nil
}

// Delete hard-deletes a never-published warehouse and its revisions. Its
// media become unreferenced and the media GC removes them.
func (s *Service) Delete(ctx context.Context, actor Actor, id primitive.ObjectID) error {
	w, err := s.store.GetWarehouse(ctx, id)
	if err != nil {
		return err
	}
	ok, err := s.store.DeleteUnpublished(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return conflict(codeBadState, "only an unpublished warehouse can be deleted (it is %s) — archive it instead", w.Status)
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
		se *stateError
		ve *validationError
	)
	switch {
	case errors.As(err, &se):
		return se.code
	case errors.As(err, &ve):
		return "invalid"
	case errors.Is(err, errNotFound):
		return "not_found"
	}
	return "internal"
}
