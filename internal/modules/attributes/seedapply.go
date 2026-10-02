package attributes

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/modules/attributes/seed"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// SeedReport is the result of a seed run (dry or applied).
type SeedReport struct {
	DryRun             bool     `json:"dryRun"`
	CreatedDefs        []string `json:"createdDefs"`
	ExistingDefs       []string `json:"existingDefs"`
	CreatedIndustries  []string `json:"createdIndustries"`
	ExistingIndustries []string `json:"existingIndustries"`
	RulesVersion       int64    `json:"rulesVersion"`
}

// Seed inserts the seed tree and industries whose keys are missing (D-043).
// Existing keys are never touched, so re-running is a no-op and an admin's
// edits survive. Everything is validated against the full target tree
// before the first write; apply=false only reports.
func (s *Service) Seed(ctx context.Context, actor domain.Actor, apply bool) (*SeedReport, error) {
	return s.seedWith(ctx, actor, apply, seed.Defs(), seed.Industries())
}

func (s *Service) seedWith(ctx context.Context, actor domain.Actor, apply bool, defs []domain.AttrDef, inds []domain.Industry) (*SeedReport, error) {
	cur, err := s.store.Load(ctx)
	if err != nil {
		return nil, err
	}
	rep := &SeedReport{DryRun: !apply, CreatedDefs: []string{}, ExistingDefs: []string{}, CreatedIndustries: []string{}, ExistingIndustries: []string{}, RulesVersion: cur.Version}

	// Target tree = current + missing seed nodes; validate each new node
	// against it (seed nodes may reference later siblings, e.g. temp
	// tracking → GDP).
	all := append([]domain.AttrDef(nil), cur.Defs...)
	var newDefs []domain.AttrDef
	for _, d := range defs {
		if _, ok := cur.Def(d.Key); ok {
			rep.ExistingDefs = append(rep.ExistingDefs, d.Key)
			continue
		}
		newDefs = append(newDefs, d)
		all = append(all, d)
	}
	target := domain.NewSnapshot(cur.Version, all, nil)
	for i, d := range newDefs {
		v, err := domain.ValidateDef(target, d)
		if err != nil {
			return nil, invalid(fmt.Errorf("seed %s: %w", d.Key, err))
		}
		newDefs[i] = v
	}
	var newInds []domain.Industry
	for _, ind := range inds {
		if _, ok := cur.Industry(ind.Key); ok {
			rep.ExistingIndustries = append(rep.ExistingIndustries, ind.Key)
			continue
		}
		v, err := domain.ValidateIndustry(target, ind)
		if err != nil {
			return nil, invalid(fmt.Errorf("seed industry %s: %w", ind.Key, err))
		}
		newInds = append(newInds, v)
	}

	if !apply {
		for _, d := range newDefs {
			rep.CreatedDefs = append(rep.CreatedDefs, d.Key)
		}
		for _, ind := range newInds {
			rep.CreatedIndustries = append(rep.CreatedIndustries, ind.Key)
		}
		return rep, nil
	}

	var changed []string
	meta := map[string]any{"source": "seed"}
	for i := range newDefs {
		d := &newDefs[i]
		d.ID, d.Version, d.Retired = primitive.NilObjectID, 1, false
		s.stamp(d, actor)
		if err := s.store.InsertDef(ctx, d); err != nil && !errors.Is(err, errKeyExists) {
			s.afterWrite(ctx, changed)
			return rep, fmt.Errorf("seed insert %s: %w", d.Key, err)
		} else if err == nil {
			s.record(ctx, actor, domain.EntityAttributeDef, d.Key, domain.ActionCreate, nil, d, meta)
			rep.CreatedDefs = append(rep.CreatedDefs, d.Key)
			changed = append(changed, d.Key)
		}
	}
	for i := range newInds {
		ind := &newInds[i]
		ind.ID, ind.Version, ind.Retired = primitive.NilObjectID, 1, false
		ind.UpdatedBy, ind.UpdatedAt = actor.Email, s.now().UTC()
		if err := s.store.InsertIndustry(ctx, ind); err != nil && !errors.Is(err, errKeyExists) {
			s.afterWrite(ctx, changed)
			return rep, fmt.Errorf("seed insert industry %s: %w", ind.Key, err)
		} else if err == nil {
			s.record(ctx, actor, domain.EntityIndustry, ind.Key, domain.ActionCreate, nil, ind, meta)
			rep.CreatedIndustries = append(rep.CreatedIndustries, ind.Key)
			changed = append(changed, ind.Key)
		}
	}
	rep.RulesVersion = s.afterWrite(ctx, changed).RulesVersion
	return rep, nil
}
