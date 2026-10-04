package service

import (
	"fmt"
	"slices"
	"strings"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/searchService/dto"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// defaultRow names chips whose node / field has no filterRow.
const defaultRow = "Features"

// Catalog lists what the filters can be built from (spec 04): chip rows
// (nodes, bool fields and pick/multi options, grouped by filterRow),
// numeric range filters in canonical units, the same by attribute (groups),
// industries, radius steps. Public: public + filterable only; admin: every
// node and projectable field. The root's fields are left out: area and
// price have their own filters.
func (s *svc) Catalog(country string, admin bool) (dto.PublicCatalog, string) {
	country = strings.ToUpper(strings.TrimSpace(country))
	if country == "" {
		country = "IN"
	}
	snap := s.rules.Snapshot()
	cfg := s.cfg()
	out := dto.PublicCatalog{
		RulesVersion: snap.Version, Country: country, Currency: cfg.CurrencyFor(country),
		DefaultRadiusKm: cfg.RadiusFor(country), RadiusSteps: radiusSteps(cfg),
		ChipRows: []dto.ChipRow{}, Ranges: []dto.RangeFilter{}, Groups: []dto.FilterGroup{}, Industries: []dto.IndustryItem{},
	}

	type placed struct {
		chip dto.Chip
		pos  int
		seq  int
	}
	rows := map[string][]placed{}
	var rowOrder []string
	seq := 0
	add := func(row string, pos int, c dto.Chip) {
		if row == "" {
			row = defaultRow
		}
		if _, ok := rows[row]; !ok {
			rowOrder = append(rowOrder, row)
		}
		seq++
		rows[row] = append(rows[row], placed{c, pos, seq})
	}

	for i := range snap.Nodes {
		n := &snap.Nodes[i]
		if n.Key == domain.RootKey || (!admin && !n.Public) {
			continue
		}
		g := dto.FilterGroup{Key: n.Key, Name: n.Name, Selectable: admin || n.Filterable, Public: n.Public, Ancestors: []string{}, Fields: []dto.FilterField{}}
		for p, ok := snap.Node(n.ParentKey); ok && p.Key != domain.RootKey; p, ok = snap.Node(p.ParentKey) {
			if g.ParentName == "" {
				g.ParentName = p.Name
			}
			g.Ancestors = append(g.Ancestors, p.Key)
		}
		if g.Selectable {
			add(n.FilterRow, n.FilterPos, dto.Chip{Key: n.Key, Label: n.Name})
		}
		for j := range n.Fields {
			f := &n.Fields[j]
			if !domain.Projectable(f.Type) || (!admin && (!f.Public || !f.Filterable)) {
				continue
			}
			path := n.Key + "." + f.Key
			row := f.FilterRow
			if row == "" {
				row = n.FilterRow
			}
			ff := dto.FilterField{Key: path, Name: f.Name, Type: string(f.Type), Public: n.Public && f.Public}
			switch f.Type {
			case domain.TypeBool:
				add(row, f.FilterPos, dto.Chip{Key: path, Label: f.Name})
			case domain.TypePick, domain.TypeMulti:
				opts := slices.Clone(f.Options)
				slices.SortStableFunc(opts, func(a, b models.FieldOption) int { return a.Order - b.Order })
				for _, o := range opts {
					c := dto.Chip{Key: path + ":" + o.Key, Label: o.Label}
					add(row, f.FilterPos, c)
					ff.Options = append(ff.Options, c)
				}
			case domain.TypeNumber, domain.TypeArea, domain.TypeRatio, domain.TypeRange:
				rf := dto.RangeFilter{Key: path, Label: n.Name + " · " + f.Name, Type: string(f.Type), Row: row}
				if f.Unit != nil {
					rf.Unit = domain.CanonicalUnit(f.Unit.Family)
				} else if f.Type == domain.TypeArea {
					rf.Unit = domain.UnitSqm
				}
				out.Ranges = append(out.Ranges, rf)
				ff.Unit = rf.Unit
			}
			g.Fields = append(g.Fields, ff)
		}
		if g.Selectable || len(g.Fields) > 0 {
			out.Groups = append(out.Groups, g)
		}
	}
	for _, r := range rowOrder {
		ps := rows[r]
		slices.SortStableFunc(ps, func(a, b placed) int {
			if a.pos != b.pos {
				return a.pos - b.pos
			}
			return a.seq - b.seq
		})
		row := dto.ChipRow{Row: r, Chips: make([]dto.Chip, 0, len(ps))}
		for _, p := range ps {
			row.Chips = append(row.Chips, p.chip)
		}
		out.ChipRows = append(out.ChipRows, row)
	}

	inds := slices.Clone(snap.Industries)
	slices.SortStableFunc(inds, func(a, b models.Industry) int { return a.Order - b.Order })
	for _, ind := range inds {
		out.Industries = append(out.Industries, dto.IndustryItem{Key: ind.Key, Name: ind.Name})
	}
	scope := "public"
	if admin {
		scope = "admin"
	}
	return out, fmt.Sprintf(`"catalog-%s-%s-%d"`, scope, country, snap.Version)
}
