package service

import (
	"context"
	"errors"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/catalogService"
	"github.com/atharva-ng/crunch/internal/services/catalogService/dto"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Needs-info queue (spec 02, D-039). Keys are the live projection's
// needs_info entries: "<node>" for an unknown node, "<node>.<field>" for a
// required field missing on a yes node (D-127).

// NeedsInfoSummary counts live warehouses per key, most first.
func (s *svc) NeedsInfoSummary(ctx context.Context) ([]dto.NeedsInfoKey, error) {
	counts, err := s.store.NeedsInfoCounts(ctx)
	if err != nil {
		return nil, err
	}
	snap := s.rules.Snapshot()
	out := make([]dto.NeedsInfoKey, 0, len(counts))
	for k, n := range counts {
		kind, name := needsInfoLabel(snap, k)
		out = append(out, dto.NeedsInfoKey{Key: k, Kind: kind, Name: name, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}

// needsInfoLabel names a key from the tree; a key the tree no longer knows
// (deleted, recompute pending) is shown as is.
func needsInfoLabel(snap *domain.Snapshot, key string) (string, string) {
	if node, field, ok := strings.Cut(key, "."); ok {
		if n, f, found := snap.Field(key); found {
			return dto.NeedsInfoField, n.Name + " › " + f.Name
		}
		return dto.NeedsInfoField, node + " › " + field
	}
	if n, ok := snap.Node(key); ok {
		return dto.NeedsInfoNode, n.Name
	}
	return dto.NeedsInfoNode, key
}

// NeedsInfoList pages the live warehouses missing key, each with its open
// revision (an in-review one can't be answered until it is decided).
func (s *svc) NeedsInfoList(ctx context.Context, key string, page, limit int) ([]dto.NeedsInfoWarehouse, int64, error) {
	ws, total, err := s.store.NeedsInfoWarehouses(ctx, key, page, limit)
	if err != nil {
		return nil, 0, err
	}
	var open []primitive.ObjectID
	for _, w := range ws {
		if w.OpenRevisionID != nil {
			open = append(open, *w.OpenRevisionID)
		}
	}
	states, err := s.store.RevisionStates(ctx, open)
	if err != nil {
		return nil, 0, err
	}
	out := make([]dto.NeedsInfoWarehouse, 0, len(ws))
	for _, w := range ws {
		item := dto.NeedsInfoWarehouse{ID: w.ID.Hex(), ShortID: w.ShortID, Name: w.Name, City: w.City, NeedsInfo: w.NeedsInfo}
		if item.NeedsInfo == nil {
			item.NeedsInfo = []string{}
		}
		if w.OpenRevisionID != nil {
			if st, ok := states[*w.OpenRevisionID]; ok {
				item.OpenRevision = &dto.OpenRevision{ID: w.OpenRevisionID.Hex(), State: st}
			}
		}
		out = append(out, item)
	}
	return out, total, nil
}

// AnswerNeedsInfo groups the answers by warehouse (request order kept) and,
// per warehouse: gets or opens its draft, applies every answer, saves once
// (same validation as a normal save) and optionally submits. One warehouse's
// failure never stops the others. All change_log rows share the batchId.
func (s *svc) AnswerNeedsInfo(ctx context.Context, actor domain.Actor, req catalogService.AnswerRequest) (string, []catalogService.AnswerItem, error) {
	if len(req.Items) == 0 {
		return "", nil, catalogService.Invalidf("no answers")
	}
	if max := s.cfg().BulkApproveMax; max > 0 && len(req.Items) > max {
		return "", nil, catalogService.Invalidf("at most %d answers per request", max)
	}
	var order []primitive.ObjectID
	byWarehouse := map[primitive.ObjectID][]catalogService.NeedsInfoAnswer{}
	for i, a := range req.Items {
		if a.WarehouseID.IsZero() || a.Node == "" {
			return "", nil, catalogService.Invalidf("item %d: warehouseId and node are required", i)
		}
		switch a.Status {
		case domain.StatusYes, domain.StatusNo:
		case "":
			if len(a.Fields) == 0 {
				return "", nil, catalogService.Invalidf("item %d: give a status (yes/no) or field values", i)
			}
		default:
			return "", nil, catalogService.Invalidf("item %d: status must be yes or no", i)
		}
		if _, seen := byWarehouse[a.WarehouseID]; !seen {
			order = append(order, a.WarehouseID)
		}
		byWarehouse[a.WarehouseID] = append(byWarehouse[a.WarehouseID], a)
	}

	batchID := primitive.NewObjectID().Hex()
	out := make([]catalogService.AnswerItem, 0, len(order))
	for _, wid := range order {
		item := catalogService.AnswerItem{WarehouseID: wid.Hex()}
		rev, err := s.answerOne(ctx, actor, wid, byWarehouse[wid], batchID)
		if rev != nil {
			item.RevisionID = rev.ID.Hex()
		}
		if err == nil {
			item.OK = true
			if req.Submit {
				_, err = s.Submit(ctx, actor, rev.ID, batchID)
				item.Submitted = err == nil
			}
		}
		if err != nil {
			item.Code, item.Error = errorCode(err), err.Error()
			var sb *catalogService.SubmitBlockedError
			if errors.As(err, &sb) {
				item.Code, item.Error = "submit_blocked", strings.Join(sb.Problems, "; ")
			}
		}
		out = append(out, item)
	}
	return batchID, out, nil
}

// answerOne applies one warehouse's answers to its open draft and saves.
func (s *svc) answerOne(ctx context.Context, actor domain.Actor, wid primitive.ObjectID, answers []catalogService.NeedsInfoAnswer, batchID string) (*models.WarehouseRevision, error) {
	w, err := s.store.GetWarehouse(ctx, wid)
	if err != nil {
		return nil, err
	}
	var r *models.WarehouseRevision
	base := w.Live
	if w.OpenRevisionID != nil {
		if r, err = s.loadRevision(ctx, *w.OpenRevisionID); err != nil {
			return nil, err
		}
		if r.State == models.RevInReview {
			return r, catalogService.Conflict(catalogService.CodeInReview, "in review — skipped (decide or withdraw it first)")
		}
		base = &r.Content
	}
	if base == nil {
		return nil, catalogService.Conflict(catalogService.CodeBadState, "never published and no open draft")
	}
	// Check the answers before opening a draft, so a bad one leaves nothing
	// behind.
	content := cloneContent(*base)
	for _, a := range answers {
		if err := applyAnswer(content.Attributes, a); err != nil {
			return r, err
		}
	}
	if r == nil {
		res, err := s.Open(ctx, actor, wid)
		if err != nil {
			return nil, err
		}
		r = res.Revision
	}
	res, err := s.Save(ctx, actor, catalogService.SaveRequest{RevisionID: r.ID, Rev: r.Rev, Content: content}, batchID)
	if err != nil {
		return r, err
	}
	return res.Revision, nil
}

// applyAnswer sets a node's state and merges field values. "no" drops the
// node (absent = no, D-133); its children's data is kept but ignored
// (D-123). Field values alone need the node to be yes already. The save
// that follows validates everything.
func applyAnswer(attrs models.Attributes, a catalogService.NeedsInfoAnswer) error {
	if a.Status == domain.StatusNo {
		delete(attrs, a.Node)
		return nil
	}
	st := attrs[a.Node]
	switch {
	case a.Status == domain.StatusYes:
		st.Status = domain.StatusYes
	case st.Status != domain.StatusYes:
		return catalogService.Invalidf("%s is not yes on this listing — answer it with status yes or no", a.Node)
	}
	if st.Fields == nil {
		st.Fields = map[string]*models.FieldValue{}
	}
	for k, v := range a.Fields {
		st.Fields[k] = v
	}
	attrs[a.Node] = st
	return nil
}
