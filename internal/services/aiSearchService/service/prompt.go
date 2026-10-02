package service

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/services/searchService/dto"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// toolName is the forced extraction tool.
const toolName = "set_filters"

// prompt is the rules + vocabulary system block and the tool schema for
// one rulesVersion. The block is byte-identical across requests (cached).
type prompt struct {
	version int64
	system  string
	schema  json.RawMessage
}

// rules is the extraction instructions (system block 1, before the
// vocabulary). Edit prompts/set_filters_rules.md; it's compiled in.
//
//go:embed prompts/set_filters_rules.md
var rules string

// buildPrompt renders the vocabulary from the public filter catalog (04)
// plus node synonyms, and the tool schema with the same keys as enums.
func buildPrompt(snap *domain.Snapshot, cat dto.PublicCatalog) prompt {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(rules))
	b.WriteString("\n\nVocabulary\n\nChips (features; use the key):\n")
	var chips, ranges, industries []string
	for _, row := range cat.ChipRows {
		for _, c := range row.Chips {
			chips = append(chips, c.Key)
			fmt.Fprintf(&b, "- %s: %s", c.Key, chipLabel(snap, c))
			if syn := synonyms(snap, c.Key); syn != "" {
				fmt.Fprintf(&b, " (also: %s)", syn)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\nRanges (numbers; unit shown is the default):\n")
	for _, r := range cat.Ranges {
		ranges = append(ranges, r.Key)
		unit := r.Unit
		if unit == "" {
			unit = "no unit"
		}
		fmt.Fprintf(&b, "- %s: %s (%s, %s)\n", r.Key, r.Label, r.Type, unit)
	}
	b.WriteString("\nIndustries:\n")
	for _, ind := range cat.Industries {
		industries = append(industries, ind.Key)
		fmt.Fprintf(&b, "- %s: %s\n", ind.Key, ind.Name)
	}
	fmt.Fprintf(&b, "\nSort values: %s, %s, %s, %s, %s.\n",
		domain.SortRelevance, domain.SortDistance, domain.SortPriceAsc, domain.SortPriceDesc, domain.SortAreaDesc)

	schema, _ := json.Marshal(toolSchema(chips, ranges, industries))
	return prompt{version: cat.RulesVersion, system: b.String(), schema: schema}
}

// chipLabel names a chip with its node for context ("Cold storage › Type:
// Frozen").
func chipLabel(snap *domain.Snapshot, c dto.Chip) string {
	node, rest, isField := strings.Cut(c.Key, ".")
	if !isField {
		return c.Label
	}
	n, ok := snap.Node(node)
	if !ok {
		return c.Label
	}
	field, _, isOpt := strings.Cut(rest, ":")
	if isOpt {
		if f, ok := n.Field(field); ok {
			return n.Name + " › " + f.Name + ": " + c.Label
		}
	}
	return n.Name + " › " + c.Label
}

// synonyms lists a node chip's synonyms (AI vocabulary, spec 02).
func synonyms(snap *domain.Snapshot, key string) string {
	if strings.ContainsAny(key, ".:") {
		return ""
	}
	n, ok := snap.Node(key)
	if !ok {
		return ""
	}
	return strings.Join(n.Synonyms, ", ")
}

// enumOf is a string schema limited to vals (a plain string when empty: the
// API rejects an empty enum, and the validator drops unknown keys anyway).
func enumOf(vals []string) map[string]any {
	if len(vals) == 0 {
		return map[string]any{"type": "string"}
	}
	return map[string]any{"type": "string", "enum": vals}
}

func toolSchema(chips, ranges, industries []string) map[string]any {
	num := map[string]any{"type": "number"}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"location": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text": map[string]any{"type": "string"},
					"kind": enumOf([]string{"pincode", "place", "none"}),
				},
				"required": []string{"kind"},
			},
			"radiusKm": num,
			"area": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"value":  num,
					"unit":   enumOf([]string{domain.UnitSqft, domain.UnitSqm}),
					"intent": enumOf([]string{intentMin, intentMax, intentApprox}),
				},
				"required": []string{"value", "unit", "intent"},
			},
			"price": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"amount":   num,
					"currency": map[string]any{"type": "string"},
					"basis":    enumOf([]string{domain.BasisPerSqftMonth, domain.BasisPerSqmMonth, domain.BasisFlatMonth}),
					"intent":   enumOf([]string{intentMax, intentMin, intentApprox}),
				},
				"required": []string{"amount", "basis", "intent"},
			},
			"industries": map[string]any{"type": "array", "items": enumOf(industries)},
			"chips":      map[string]any{"type": "array", "items": enumOf(chips)},
			"ranges": map[string]any{"type": "array", "items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"key":  enumOf(ranges),
					"min":  num,
					"max":  num,
					"unit": map[string]any{"type": "string"},
				},
				"required": []string{"key"},
			}},
			"sort":       enumOf([]string{domain.SortRelevance, domain.SortDistance, domain.SortPriceAsc, domain.SortPriceDesc, domain.SortAreaDesc}),
			"unmapped":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"confidence": enumOf([]string{"high", "medium", "low"}),
		},
		"required": []string{"location", "industries", "chips", "unmapped", "confidence"},
	}
}
