package attributes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/util/log"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Service errors the handlers map to HTTP.
var (
	errNotFound = errors.New("not found")
)

// validationError is a 400: the write breaks a tree / rule invariant.
type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }

func invalid(err error) error { return &validationError{msg: err.Error()} }

// retireBlockedError is the D-042 guard (409): live rules still reference the
// node or a descendant.
type retireBlockedError struct {
	Industries []string `json:"industries"`
	Attributes []string `json:"attributes"`
}

func (e *retireBlockedError) Error() string { return "retire blocked: still referenced by rules" }

// confirmRequiredError (409): retiring a parent also retires these
// descendants; resend with confirm=true.
type confirmRequiredError struct {
	Descendants []string `json:"descendants"`
}

func (e *confirmRequiredError) Error() string { return "retire would also retire descendants" }

// Service performs every tree and industry write: validate against a fresh
// snapshot, CAS-write, record change_log, then bump rulesVersion, reload the
// cache and dispatch the recompute (spec 02 Recompute, D-040). There are no
// transactions; each step is ordered and a failed tail step is logged — the
// cache ticker and the nightly safety net catch up.
type Service struct {
	store    Store
	cache    *Cache
	log      domain.ChangeLog
	dispatch func(ctx context.Context, rulesVersion int64) error
	now      func() time.Time
}

// WriteResult is returned by every write.
type WriteResult struct {
	// RulesVersion after the write (0 when the bump failed).
	RulesVersion int64 `json:"rulesVersion"`
	// Changed lists the keys written.
	Changed []string `json:"changed"`
}

func (s *Service) afterWrite(ctx context.Context, changed []string) WriteResult {
	res := WriteResult{Changed: changed}
	if len(changed) == 0 {
		res.RulesVersion = s.cache.Snapshot().Version
		return res
	}
	ctx = context.WithoutCancel(ctx)
	v, err := s.store.BumpRulesVersion(ctx)
	if err != nil {
		log.Error("attributes: rulesVersion bump failed — recompute deferred to the next write or safety net", "error", err)
	}
	res.RulesVersion = v
	if err := s.cache.Reload(ctx); err != nil {
		log.Error("attributes: cache reload after write failed", "error", err)
	}
	if v > 0 && s.dispatch != nil {
		if err := s.dispatch(ctx, v); err != nil {
			log.Error("attributes: recompute dispatch failed", "rules_version", v, "error", err)
		}
	}
	return res
}

func (s *Service) record(ctx context.Context, actor domain.Actor, entity domain.ChangeEntity, id string, action domain.ChangeAction, before, after any, meta map[string]any) {
	s.log.Record(ctx, domain.ChangeEntry{Entity: entity, EntityID: id, Action: action, Actor: actor, Before: before, After: after, Meta: meta})
}

func cloneDef(d domain.AttrDef) domain.AttrDef {
	d.AllowedValues = slices.Clone(d.AllowedValues)
	d.Synonyms = slices.Clone(d.Synonyms)
	if d.Unit != nil {
		u := *d.Unit
		u.Input = slices.Clone(u.Input)
		d.Unit = &u
	}
	if d.Calc != nil {
		c := *d.Calc
		d.Calc = &c
	}
	if d.AppliesWhen != nil {
		n := *d.AppliesWhen
		n.Conds = slices.Clone(n.Conds)
		d.AppliesWhen = &n
	}
	return d
}

func nextOrder(snap *domain.Snapshot, parent string) int {
	n := 0
	for _, k := range snap.Children(parent) {
		d, _ := snap.Def(k)
		n = max(n, d.Order)
	}
	return n + 1
}

func (s *Service) stamp(d *domain.AttrDef, actor domain.Actor) {
	d.UpdatedBy = actor.Email
	d.UpdatedAt = s.now().UTC()
}

// replaceDef bumps the version and CAS-writes against old.
func (s *Service) replaceDef(ctx context.Context, actor domain.Actor, old, upd *domain.AttrDef, action domain.ChangeAction, meta map[string]any) error {
	upd.ID = old.ID
	upd.Version = old.Version + 1
	s.stamp(upd, actor)
	if err := s.store.ReplaceDef(ctx, upd, old.Version); err != nil {
		return err
	}
	s.record(ctx, actor, domain.EntityAttributeDef, upd.Key, action, old, upd, meta)
	return nil
}

// --- attribute definitions ---

// CreateDef adds a node. Order 0 appends after the last sibling.
func (s *Service) CreateDef(ctx context.Context, actor domain.Actor, d domain.AttrDef) (domain.AttrDef, WriteResult, error) {
	snap, err := s.store.Load(ctx)
	if err != nil {
		return d, WriteResult{}, err
	}
	if _, exists := snap.Def(d.Key); exists {
		return d, WriteResult{}, errKeyExists
	}
	if d.Order <= 0 {
		d.Order = nextOrder(snap, d.ParentKey)
	}
	d.Retired, d.RetiredAt = false, nil
	d, err = domain.ValidateDef(snap, d)
	if err != nil {
		return d, WriteResult{}, invalid(err)
	}
	d.ID = primitive.NilObjectID
	d.Version = 1
	s.stamp(&d, actor)
	if err := s.store.InsertDef(ctx, &d); err != nil {
		return d, WriteResult{}, err
	}
	s.record(ctx, actor, domain.EntityAttributeDef, d.Key, domain.ActionCreate, nil, d, nil)
	return d, s.afterWrite(ctx, []string{d.Key}), nil
}

// DefPatch is an update: nil fields are unchanged. Key, kind, type and parent
// are immutable here (parent → MoveDef).
type DefPatch struct {
	Key             string                 `json:"key"`
	ExpectedVersion int                    `json:"expectedVersion"`
	Name            *string                `json:"name,omitempty"`
	Description     *string                `json:"description,omitempty"`
	Role            *domain.AttrRole       `json:"role,omitempty"`
	Unit            *domain.UnitSpec       `json:"unit,omitempty"`
	AllowedValues   *[]domain.AllowedValue `json:"allowedValues,omitempty"`
	Filterable      *bool                  `json:"filterable,omitempty"`
	FilterRow       *string                `json:"filterRow,omitempty"`
	FilterPos       *int                   `json:"filterPos,omitempty"`
	Public          *bool                  `json:"public,omitempty"`
	Synonyms        *[]string              `json:"synonyms,omitempty"`
	AppliesWhen     *domain.CondNode       `json:"appliesWhen,omitempty"`
	// ClearAppliesWhen removes the rule (AppliesWhen nil means "unchanged").
	ClearAppliesWhen bool `json:"clearAppliesWhen,omitempty"`
}

// UpdateDef applies p with an expectedVersion CAS.
func (s *Service) UpdateDef(ctx context.Context, actor domain.Actor, p DefPatch) (domain.AttrDef, WriteResult, error) {
	snap, err := s.store.Load(ctx)
	if err != nil {
		return domain.AttrDef{}, WriteResult{}, err
	}
	old, ok := snap.Def(p.Key)
	if !ok {
		return domain.AttrDef{}, WriteResult{}, errNotFound
	}
	if old.Version != p.ExpectedVersion {
		return domain.AttrDef{}, WriteResult{}, errVersionConflict
	}
	upd := cloneDef(*old)
	if p.Name != nil {
		upd.Name = *p.Name
	}
	if p.Description != nil {
		upd.Description = *p.Description
	}
	if p.Role != nil {
		upd.Role = *p.Role
	}
	if p.Unit != nil {
		u := *p.Unit
		upd.Unit = &u
	}
	if p.AllowedValues != nil {
		upd.AllowedValues = slices.Clone(*p.AllowedValues)
	}
	if p.Filterable != nil {
		upd.Filterable = *p.Filterable
	}
	if p.FilterRow != nil {
		upd.FilterRow = *p.FilterRow
	}
	if p.FilterPos != nil {
		upd.FilterPos = *p.FilterPos
	}
	if p.Public != nil {
		upd.Public = *p.Public
	}
	if p.Synonyms != nil {
		upd.Synonyms = slices.Clone(*p.Synonyms)
	}
	if p.ClearAppliesWhen {
		upd.AppliesWhen = nil
	} else if p.AppliesWhen != nil {
		n := *p.AppliesWhen
		upd.AppliesWhen = &n
	}
	if err := domain.CheckDefUpdate(*old, upd); err != nil {
		return upd, WriteResult{}, invalid(err)
	}
	if upd, err = domain.ValidateDef(snap, upd); err != nil {
		return upd, WriteResult{}, invalid(err)
	}
	if err := s.replaceDef(ctx, actor, old, &upd, domain.ActionUpdate, nil); err != nil {
		return upd, WriteResult{}, err
	}
	return upd, s.afterWrite(ctx, []string{upd.Key}), nil
}

// MoveDef re-parents a node. order ≤ 0 appends after the new siblings.
func (s *Service) MoveDef(ctx context.Context, actor domain.Actor, key, newParent string, order int) (domain.AttrDef, WriteResult, error) {
	snap, err := s.store.Load(ctx)
	if err != nil {
		return domain.AttrDef{}, WriteResult{}, err
	}
	old, ok := snap.Def(key)
	if !ok {
		return domain.AttrDef{}, WriteResult{}, errNotFound
	}
	upd := cloneDef(*old)
	upd.ParentKey = newParent
	if order <= 0 {
		order = nextOrder(snap, newParent)
	}
	upd.Order = order
	if upd, err = domain.ValidateDef(snap, upd); err != nil {
		return upd, WriteResult{}, invalid(err)
	}
	meta := map[string]any{"fromParent": old.ParentKey, "toParent": newParent}
	if err := s.replaceDef(ctx, actor, old, &upd, domain.ActionMove, meta); err != nil {
		return upd, WriteResult{}, err
	}
	return upd, s.afterWrite(ctx, []string{key}), nil
}

// Reorder sets the sibling order under parentKey; keys must be exactly the
// current children.
func (s *Service) Reorder(ctx context.Context, actor domain.Actor, parentKey string, keys []string) (WriteResult, error) {
	snap, err := s.store.Load(ctx)
	if err != nil {
		return WriteResult{}, err
	}
	cur := snap.Children(parentKey)
	if len(cur) != len(keys) {
		return WriteResult{}, invalid(fmt.Errorf("keys must list exactly the %d children of %q", len(cur), parentKey))
	}
	for _, k := range keys {
		if !slices.Contains(cur, k) {
			return WriteResult{}, invalid(fmt.Errorf("%q is not a child of %q", k, parentKey))
		}
	}
	var changed []string
	for i, k := range keys {
		old, _ := snap.Def(k)
		if old.Order == i+1 {
			continue
		}
		upd := cloneDef(*old)
		upd.Order = i + 1
		if err := s.replaceDef(ctx, actor, old, &upd, domain.ActionUpdate, map[string]any{"op": "reorder"}); err != nil {
			s.afterWrite(ctx, changed)
			return WriteResult{Changed: changed}, err
		}
		changed = append(changed, k)
	}
	return s.afterWrite(ctx, changed), nil
}

// RetireDef retires key and its non-retired descendants (D-042 guard first;
// descendants need confirm). Descendants are retired bottom-up and the node
// last, so a failed run is finished by a retry.
func (s *Service) RetireDef(ctx context.Context, actor domain.Actor, key string, confirm bool) (WriteResult, error) {
	snap, err := s.store.Load(ctx)
	if err != nil {
		return WriteResult{}, err
	}
	d, ok := snap.Def(key)
	if !ok {
		return WriteResult{}, errNotFound
	}
	// Reversed pre-order puts every node after its descendants: bottom-up.
	var descendants, targets []string
	for _, k := range snap.Descendants(key) {
		if x, _ := snap.Def(k); !x.Retired {
			descendants = append(descendants, k)
		}
	}
	targets = slices.Clone(descendants)
	slices.Reverse(targets)
	if !d.Retired {
		targets = append(targets, key)
	}
	if len(targets) == 0 {
		return s.afterWrite(ctx, nil), nil
	}
	set := map[string]bool{}
	for _, k := range targets {
		set[k] = true
	}

	blocked := &retireBlockedError{Industries: []string{}, Attributes: []string{}}
	for i := range snap.Industries {
		ind := &snap.Industries[i]
		if !ind.Retired && ind.References(set) {
			blocked.Industries = append(blocked.Industries, ind.Key)
		}
	}
	for i := range snap.Defs {
		x := &snap.Defs[i]
		if x.Retired || set[x.Key] || x.AppliesWhen == nil {
			continue
		}
		for _, c := range x.AppliesWhen.Conds {
			if set[c.Attr] {
				blocked.Attributes = append(blocked.Attributes, x.Key)
				break
			}
		}
	}
	if len(blocked.Industries)+len(blocked.Attributes) > 0 {
		return WriteResult{}, blocked
	}
	if len(descendants) > 0 && !confirm {
		return WriteResult{}, &confirmRequiredError{Descendants: descendants}
	}

	now := s.now().UTC()
	var changed []string
	for _, k := range targets {
		old, _ := snap.Def(k)
		upd := cloneDef(*old)
		upd.Retired, upd.RetiredAt = true, &now
		meta := map[string]any(nil)
		if k != key {
			meta = map[string]any{"via": key}
		}
		if err := s.replaceDef(ctx, actor, old, &upd, domain.ActionRetire, meta); err != nil {
			s.afterWrite(ctx, changed)
			return WriteResult{Changed: changed}, err
		}
		changed = append(changed, k)
	}
	return s.afterWrite(ctx, changed), nil
}

// RestoreDef un-retires one node (its parent must be active; descendants stay
// retired until restored one by one).
func (s *Service) RestoreDef(ctx context.Context, actor domain.Actor, key string) (domain.AttrDef, WriteResult, error) {
	snap, err := s.store.Load(ctx)
	if err != nil {
		return domain.AttrDef{}, WriteResult{}, err
	}
	old, ok := snap.Def(key)
	if !ok {
		return domain.AttrDef{}, WriteResult{}, errNotFound
	}
	if !old.Retired {
		return *old, s.afterWrite(ctx, nil), nil
	}
	upd := cloneDef(*old)
	upd.Retired, upd.RetiredAt = false, nil
	if upd, err = domain.ValidateDef(snap, upd); err != nil {
		return upd, WriteResult{}, invalid(err)
	}
	if err := s.replaceDef(ctx, actor, old, &upd, domain.ActionRestore, nil); err != nil {
		return upd, WriteResult{}, err
	}
	return upd, s.afterWrite(ctx, []string{key}), nil
}

// --- industries ---

func (s *Service) replaceIndustry(ctx context.Context, actor domain.Actor, old, upd *domain.Industry, action domain.ChangeAction) error {
	upd.ID = old.ID
	upd.Version = old.Version + 1
	upd.UpdatedBy = actor.Email
	upd.UpdatedAt = s.now().UTC()
	if err := s.store.ReplaceIndustry(ctx, upd, old.Version); err != nil {
		return err
	}
	s.record(ctx, actor, domain.EntityIndustry, upd.Key, action, old, upd, nil)
	return nil
}

// CreateIndustry adds an industry. Order 0 appends.
func (s *Service) CreateIndustry(ctx context.Context, actor domain.Actor, ind domain.Industry) (domain.Industry, WriteResult, error) {
	snap, err := s.store.Load(ctx)
	if err != nil {
		return ind, WriteResult{}, err
	}
	if _, exists := snap.Industry(ind.Key); exists {
		return ind, WriteResult{}, errKeyExists
	}
	if ind.Order <= 0 {
		ind.Order = 1
		for _, x := range snap.Industries {
			ind.Order = max(ind.Order, x.Order+1)
		}
	}
	if ind, err = domain.ValidateIndustry(snap, ind); err != nil {
		return ind, WriteResult{}, invalid(err)
	}
	ind.ID = primitive.NilObjectID
	ind.Retired, ind.RetiredAt = false, nil
	ind.Version = 1
	ind.UpdatedBy = actor.Email
	ind.UpdatedAt = s.now().UTC()
	if err := s.store.InsertIndustry(ctx, &ind); err != nil {
		return ind, WriteResult{}, err
	}
	s.record(ctx, actor, domain.EntityIndustry, ind.Key, domain.ActionCreate, nil, ind, nil)
	return ind, s.afterWrite(ctx, []string{ind.Key}), nil
}

// IndustryPatch is an industry update: nil fields are unchanged.
type IndustryPatch struct {
	Key             string              `json:"key"`
	ExpectedVersion int                 `json:"expectedVersion"`
	Name            *string             `json:"name,omitempty"`
	Order           *int                `json:"order,omitempty"`
	Required        *[]domain.Condition `json:"required,omitempty"`
	Preferred       *[]domain.Condition `json:"preferred,omitempty"`
}

// UpdateIndustry applies p with an expectedVersion CAS.
func (s *Service) UpdateIndustry(ctx context.Context, actor domain.Actor, p IndustryPatch) (domain.Industry, WriteResult, error) {
	snap, err := s.store.Load(ctx)
	if err != nil {
		return domain.Industry{}, WriteResult{}, err
	}
	old, ok := snap.Industry(p.Key)
	if !ok {
		return domain.Industry{}, WriteResult{}, errNotFound
	}
	if old.Version != p.ExpectedVersion {
		return domain.Industry{}, WriteResult{}, errVersionConflict
	}
	upd := *old
	upd.Required, upd.Preferred = slices.Clone(old.Required), slices.Clone(old.Preferred)
	if p.Name != nil {
		upd.Name = *p.Name
	}
	if p.Order != nil {
		upd.Order = *p.Order
	}
	if p.Required != nil {
		upd.Required = slices.Clone(*p.Required)
	}
	if p.Preferred != nil {
		upd.Preferred = slices.Clone(*p.Preferred)
	}
	if upd, err = domain.ValidateIndustry(snap, upd); err != nil {
		return upd, WriteResult{}, invalid(err)
	}
	if err := s.replaceIndustry(ctx, actor, old, &upd, domain.ActionUpdate); err != nil {
		return upd, WriteResult{}, err
	}
	return upd, s.afterWrite(ctx, []string{upd.Key}), nil
}

// SetIndustryRetired retires or restores an industry. Restoring re-validates
// its rules (they may name attributes retired since).
func (s *Service) SetIndustryRetired(ctx context.Context, actor domain.Actor, key string, retired bool) (domain.Industry, WriteResult, error) {
	snap, err := s.store.Load(ctx)
	if err != nil {
		return domain.Industry{}, WriteResult{}, err
	}
	old, ok := snap.Industry(key)
	if !ok {
		return domain.Industry{}, WriteResult{}, errNotFound
	}
	if old.Retired == retired {
		return *old, s.afterWrite(ctx, nil), nil
	}
	upd := *old
	action := domain.ActionRestore
	if retired {
		now := s.now().UTC()
		upd.Retired, upd.RetiredAt = true, &now
		action = domain.ActionRetire
	} else {
		upd.Retired, upd.RetiredAt = false, nil
		if upd, err = domain.ValidateIndustry(snap, upd); err != nil {
			return upd, WriteResult{}, invalid(err)
		}
	}
	if err := s.replaceIndustry(ctx, actor, old, &upd, action); err != nil {
		return upd, WriteResult{}, err
	}
	return upd, s.afterWrite(ctx, []string{key}), nil
}
