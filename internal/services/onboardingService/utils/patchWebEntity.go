package utils

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/prompts"
	"github.com/atharva-ng/crunch/internal/services/onboardingService/constants"
	"github.com/atharva-ng/crunch/internal/services/onboardingService/dto"
	commonutils "github.com/atharva-ng/crunch/internal/utils"
	"github.com/microcosm-cc/bluemonday"
	"go.mongodb.org/mongo-driver/bson"
)

type fieldKind int

const (
	fieldString fieldKind = iota
	fieldInt
	fieldBool
	fieldStringArray
	fieldICPSignals
	fieldCompetitors
	fieldPublishing
	fieldThumbnailStyle
	fieldSEOStrategy
	fieldStyleArtifact
)

// patchableFields whitelists which BSON paths a client may mutate via the
// patch endpoint. Anything not listed here is rejected — this guards against
// arbitrary $set into untrusted paths.
var patchableFields = map[string]fieldKind{
	"context.business_name":            fieldString,
	"context.website":                  fieldString,
	"context.product_type":             fieldString,
	"context.primary_use_case":         fieldString,
	"context.business_model":           fieldString,
	"context.target_geography":         fieldString,
	"context.pricing_model":            fieldString,
	"context.key_differentiator":       fieldString,
	"context.brand_voice_signals":      fieldString,
	"context.icp_signals.company_size": fieldString,
	"context.user_domain_rating":       fieldInt,
	"internal_linking_enabled":         fieldBool,
	"publish_as_live":                  fieldBool,
	"thumbnail_style":                  fieldThumbnailStyle,
	"seo_strategy":                     fieldSEOStrategy,
	"context.key_features":             fieldStringArray,
	"context.integrations":             fieldStringArray,
	"context.inferred_fields":          fieldStringArray,
	"context.icp_signals.roles":        fieldStringArray,
	"context.icp_signals.industries":   fieldStringArray,
	"context.icp_signals.pain_points":  fieldStringArray,
	"context.icp_signals":              fieldICPSignals,
	"competitors":                      fieldCompetitors,
	"publishing":                       fieldPublishing,
	// Learned style artifacts (style replication). Hand-editable free text;
	// remove unsets the single artifact (the whole-profile Remove action goes
	// through the dedicated DELETE endpoint instead).
	"style_replication.tone_profile":             fieldStyleArtifact,
	"style_replication.structure_pattern":        fieldStyleArtifact,
	"style_replication.title_pattern":            fieldStyleArtifact,
	"style_replication.thumbnail_style_prompt":   fieldStyleArtifact,
	"style_replication.mid_article_style_prompt": fieldStyleArtifact,
	// Per-artifact apply toggles: which learned artifacts actually shape
	// generation. Plain bools (replace-only); absent = applied.
	"style_replication.apply.tone_profile":      fieldBool,
	"style_replication.apply.structure_pattern": fieldBool,
	"style_replication.apply.title_pattern":     fieldBool,
	"style_replication.apply.thumbnail_style":   fieldBool,
	"style_replication.apply.mid_article_style": fieldBool,
}

// styleArtifactMaxChars caps a hand-edited style artifact. The synthesis
// prompts target 100-400 words; this bound only guards against pasting a whole
// article into the textarea.
const styleArtifactMaxChars = 5000

var patchSanitizer = bluemonday.StrictPolicy()

// coreContextStrings are the scalar context fields the SIE prompts consume
// unconditionally. A patch may neither blank nor unset them — the profile
// screen can only replace them with a non-empty value.
var coreContextStrings = map[string]bool{
	"context.business_name":      true,
	"context.product_type":       true,
	"context.key_differentiator": true,
}

// coreContextArrays are the ICP lists the SIE prompts consume unconditionally.
// Array ops may never compose down to an empty list (key_features has the same
// floor, enforced with a typed error in PatchOnboardedUser).
var coreContextArrays = []string{
	"context.icp_signals.roles",
	"context.icp_signals.pain_points",
}

// ApplyPatchOps walks the requested ops and produces $set / $unset documents
// to apply in a single Mongo update. Array ops are evaluated against an
// in-memory working copy so that multiple ops on the same array compose
// correctly within one request.
func ApplyPatchOps(current *models.WebEntity, ops []dto.PatchOp) (bson.M, bson.M, error) {
	set := bson.M{}
	unset := bson.M{}

	stringArrays := map[string][]string{}
	stringArrayTouched := map[string]bool{}

	var competitors []models.Competitor
	competitorsTouched := false

	for i, op := range ops {
		kind, ok := patchableFields[op.Field]
		if !ok {
			return nil, nil, fmt.Errorf("op %d: field %q is not patchable", i, op.Field)
		}

		switch kind {
		case fieldString:
			if err := applyStringOp(set, unset, op); err != nil {
				return nil, nil, fmt.Errorf("op %d (%s): %w", i, op.Field, err)
			}

		case fieldInt:
			if err := applyIntOp(set, op); err != nil {
				return nil, nil, fmt.Errorf("op %d (%s): %w", i, op.Field, err)
			}

		case fieldBool:
			if err := applyBoolOp(set, op); err != nil {
				return nil, nil, fmt.Errorf("op %d (%s): %w", i, op.Field, err)
			}

		case fieldStringArray:
			if !stringArrayTouched[op.Field] {
				stringArrays[op.Field] = readStringArray(current, op.Field)
				stringArrayTouched[op.Field] = true
			}
			next, err := applyStringArrayOp(stringArrays[op.Field], op)
			if err != nil {
				return nil, nil, fmt.Errorf("op %d (%s): %w", i, op.Field, err)
			}
			stringArrays[op.Field] = next

		case fieldICPSignals:
			if err := applyICPSignalsOp(set, unset, op); err != nil {
				return nil, nil, fmt.Errorf("op %d (%s): %w", i, op.Field, err)
			}

		case fieldCompetitors:
			if !competitorsTouched {
				competitors = append([]models.Competitor(nil), current.Competitors...)
				competitorsTouched = true
			}
			next, err := applyCompetitorOp(competitors, op)
			if err != nil {
				return nil, nil, fmt.Errorf("op %d (%s): %w", i, op.Field, err)
			}
			competitors = next

		case fieldPublishing:
			if err := applyPublishingOp(set, unset, op, current); err != nil {
				return nil, nil, fmt.Errorf("op %d (%s): %w", i, op.Field, err)
			}

		case fieldThumbnailStyle:
			if err := applyThumbnailStyleOp(set, unset, op); err != nil {
				return nil, nil, fmt.Errorf("op %d (%s): %w", i, op.Field, err)
			}

		case fieldSEOStrategy:
			if err := applySEOStrategyOp(set, unset, op); err != nil {
				return nil, nil, fmt.Errorf("op %d (%s): %w", i, op.Field, err)
			}

		case fieldStyleArtifact:
			if err := applyStyleArtifactOp(set, unset, op); err != nil {
				return nil, nil, fmt.Errorf("op %d (%s): %w", i, op.Field, err)
			}
		}
	}

	for _, path := range coreContextArrays {
		if stringArrayTouched[path] && len(stringArrays[path]) == 0 {
			return nil, nil, fmt.Errorf("%s must keep at least one entry", path)
		}
	}

	for path, arr := range stringArrays {
		set[path] = arr
	}
	if competitorsTouched {
		set["competitors"] = competitors
	}

	return set, unset, nil
}

// EffectiveBusinessContext returns the business context as it will read after
// the staged $set document is applied — the entity's stored values overlaid
// with the staged core-field paths. The finalise gate judges completeness on
// this merged view so a request that fixes a hole and finalises in one call
// isn't falsely rejected. Unsets need no handling: ops that could unset a core
// path are rejected in ApplyPatchOps.
func EffectiveBusinessContext(entity *models.WebEntity, set bson.M) *models.BusinessContext {
	bc := models.BusinessContext{}
	if entity.BusinessContext != nil {
		bc = *entity.BusinessContext
	}
	if v, ok := set["context.business_name"].(string); ok {
		bc.BusinessName = &v
	}
	if v, ok := set["context.product_type"].(string); ok {
		bc.ProductType = &v
	}
	if v, ok := set["context.key_differentiator"].(string); ok {
		bc.KeyDifferentiator = &v
	}
	if v, ok := set["context.key_features"].([]string); ok {
		bc.KeyFeatures = v
	}
	if v, ok := set["context.icp_signals"].(models.ICPSignals); ok {
		bc.ICPSignals = &v
	}
	overlayICP := func(apply func(*models.ICPSignals)) {
		icp := models.ICPSignals{}
		if bc.ICPSignals != nil {
			icp = *bc.ICPSignals
		}
		apply(&icp)
		bc.ICPSignals = &icp
	}
	if v, ok := set["context.icp_signals.roles"].([]string); ok {
		overlayICP(func(icp *models.ICPSignals) { icp.Roles = v })
	}
	if v, ok := set["context.icp_signals.pain_points"].([]string); ok {
		overlayICP(func(icp *models.ICPSignals) { icp.PainPoints = v })
	}
	return &bc
}

func applyStringOp(set, unset bson.M, op dto.PatchOp) error {
	switch op.Op {
	case dto.PatchOpReplace:
		v, err := parseString(op.Value)
		if err != nil {
			return err
		}
		if coreContextStrings[op.Field] && strings.TrimSpace(v) == "" {
			return fmt.Errorf("field cannot be blank")
		}
		set[op.Field] = v
		return nil
	case dto.PatchOpRemove:
		if coreContextStrings[op.Field] {
			return fmt.Errorf("field cannot be removed")
		}
		unset[op.Field] = ""
		return nil
	default:
		return fmt.Errorf("op %q not supported on scalar string", op.Op)
	}
}

func applyIntOp(set bson.M, op dto.PatchOp) error {
	if op.Op != dto.PatchOpReplace {
		return fmt.Errorf("op %q not supported on scalar int", op.Op)
	}
	var v int
	if err := json.Unmarshal(op.Value, &v); err != nil {
		return fmt.Errorf("value must be int: %w", err)
	}
	set[op.Field] = v
	return nil
}

func applyBoolOp(set bson.M, op dto.PatchOp) error {
	if op.Op != dto.PatchOpReplace {
		return fmt.Errorf("op %q not supported on scalar bool", op.Op)
	}
	var v bool
	if err := json.Unmarshal(op.Value, &v); err != nil {
		return fmt.Errorf("value must be bool: %w", err)
	}
	set[op.Field] = v
	return nil
}

// applyThumbnailStyleOp handles the web-entity-wide thumbnail-style default.
// replace validates the ID against the catalog (only known styles persist,
// mirroring the IsValidPlatform guard in sanitizePublishing); remove unsets the
// override so the entity falls back to the system default.
func applyThumbnailStyleOp(set, unset bson.M, op dto.PatchOp) error {
	switch op.Op {
	case dto.PatchOpReplace:
		v, err := parseString(op.Value)
		if err != nil {
			return err
		}
		if !prompts.IsValidThumbnailStyle(v) {
			return fmt.Errorf("unknown thumbnail style %q", v)
		}
		set[op.Field] = v
		return nil
	case dto.PatchOpRemove:
		unset[op.Field] = ""
		return nil
	default:
		return fmt.Errorf("op %q not supported on thumbnail_style", op.Op)
	}
}

// applyStyleArtifactOp handles one learned style artifact (style replication
// hand edits). replace requires non-blank text under the length cap; remove
// unsets that single artifact so generation falls back to no style block.
func applyStyleArtifactOp(set, unset bson.M, op dto.PatchOp) error {
	switch op.Op {
	case dto.PatchOpReplace:
		v, err := parseString(op.Value)
		if err != nil {
			return err
		}
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("field cannot be blank — use remove to clear it")
		}
		if len(v) > styleArtifactMaxChars {
			return fmt.Errorf("value exceeds %d characters", styleArtifactMaxChars)
		}
		set[op.Field] = v
		return nil
	case dto.PatchOpRemove:
		unset[op.Field] = ""
		return nil
	default:
		return fmt.Errorf("op %q not supported on style artifact", op.Op)
	}
}

// applySEOStrategyOp handles the per-site SIE strategy choice. replace
// validates the id against the strategy catalog (mirroring
// applyThumbnailStyleOp); remove unsets the choice so the entity falls back to
// the domain-rating-derived default.
func applySEOStrategyOp(set, unset bson.M, op dto.PatchOp) error {
	switch op.Op {
	case dto.PatchOpReplace:
		v, err := parseString(op.Value)
		if err != nil {
			return err
		}
		if !models.IsValidSEOStrategy(v) {
			return fmt.Errorf("unknown seo strategy %q", v)
		}
		set[op.Field] = v
		return nil
	case dto.PatchOpRemove:
		unset[op.Field] = ""
		return nil
	default:
		return fmt.Errorf("op %q not supported on seo_strategy", op.Op)
	}
}

func applyStringArrayOp(current []string, op dto.PatchOp) ([]string, error) {
	switch op.Op {
	case dto.PatchOpAdd:
		v, err := parseString(op.Value)
		if err != nil {
			return nil, err
		}
		return append(current, v), nil

	case dto.PatchOpRemove:
		if op.Index != nil {
			if *op.Index < 0 || *op.Index >= len(current) {
				return nil, fmt.Errorf("index %d out of range [0,%d)", *op.Index, len(current))
			}
			return append(current[:*op.Index], current[*op.Index+1:]...), nil
		}
		v, err := parseString(op.Value)
		if err != nil {
			return nil, fmt.Errorf("remove without index requires a value: %w", err)
		}
		out := current[:0]
		for _, s := range current {
			if s != v {
				out = append(out, s)
			}
		}
		return out, nil

	case dto.PatchOpReplace:
		if op.Index != nil {
			v, err := parseString(op.Value)
			if err != nil {
				return nil, err
			}
			if *op.Index < 0 || *op.Index >= len(current) {
				return nil, fmt.Errorf("index %d out of range [0,%d)", *op.Index, len(current))
			}
			current[*op.Index] = v
			return current, nil
		}
		var v []string
		if err := json.Unmarshal(op.Value, &v); err != nil {
			return nil, fmt.Errorf("value must be []string: %w", err)
		}
		for i := range v {
			v[i] = patchSanitizer.Sanitize(v[i])
		}
		return v, nil
	}
	return nil, fmt.Errorf("op %q not supported on string array", op.Op)
}

func applyICPSignalsOp(set, unset bson.M, op dto.PatchOp) error {
	switch op.Op {
	case dto.PatchOpReplace:
		var v models.ICPSignals
		if err := json.Unmarshal(op.Value, &v); err != nil {
			return fmt.Errorf("value must be ICPSignals object: %w", err)
		}
		sanitizeICPSignals(&v)
		if len(v.Roles) == 0 || len(v.PainPoints) == 0 {
			return fmt.Errorf("icp_signals must keep at least one role and one pain point")
		}
		set[op.Field] = v
		return nil
	default:
		// remove is deliberately unsupported: the SIE prompts consume the ICP
		// lists unconditionally, so the object may never be unset.
		return fmt.Errorf("op %q not supported on icp_signals", op.Op)
	}
}

func applyCompetitorOp(current []models.Competitor, op dto.PatchOp) ([]models.Competitor, error) {
	switch op.Op {
	case dto.PatchOpAdd:
		var c models.Competitor
		if err := json.Unmarshal(op.Value, &c); err != nil {
			return nil, fmt.Errorf("value must be Competitor object: %w", err)
		}
		if err := sanitizeCompetitor(&c); err != nil {
			return nil, err
		}
		return append(current, c), nil

	case dto.PatchOpRemove:
		if op.Index != nil {
			if *op.Index < 0 || *op.Index >= len(current) {
				return nil, fmt.Errorf("index %d out of range [0,%d)", *op.Index, len(current))
			}
			return append(current[:*op.Index], current[*op.Index+1:]...), nil
		}
		// Remove by domain — caller sends {"domain": "x.com"} as value.
		var match struct {
			Domain string `json:"domain"`
		}
		if err := json.Unmarshal(op.Value, &match); err != nil || match.Domain == "" {
			return nil, fmt.Errorf("remove without index requires {\"domain\": \"...\"}")
		}
		out := current[:0]
		for _, c := range current {
			if c.Domain != match.Domain {
				out = append(out, c)
			}
		}
		return out, nil

	case dto.PatchOpReplace:
		if op.Index != nil {
			var c models.Competitor
			if err := json.Unmarshal(op.Value, &c); err != nil {
				return nil, fmt.Errorf("value must be Competitor object: %w", err)
			}
			if err := sanitizeCompetitor(&c); err != nil {
				return nil, err
			}
			if *op.Index < 0 || *op.Index >= len(current) {
				return nil, fmt.Errorf("index %d out of range [0,%d)", *op.Index, len(current))
			}
			current[*op.Index] = c
			return current, nil
		}
		var v []models.Competitor
		if err := json.Unmarshal(op.Value, &v); err != nil {
			return nil, fmt.Errorf("value must be []Competitor: %w", err)
		}
		for i := range v {
			if err := sanitizeCompetitor(&v[i]); err != nil {
				return nil, fmt.Errorf("competitor %d: %w", i, err)
			}
		}
		return v, nil
	}
	return nil, fmt.Errorf("op %q not supported on competitors", op.Op)
}

func parseString(raw json.RawMessage) (string, error) {
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("value must be string: %w", err)
	}
	return patchSanitizer.Sanitize(v), nil
}

// competitorDomainRe validates an already-normalized hostname: one or more DNS
// labels followed by an alphabetic TLD. Mirrors isValidDomain in the frontend
// (central-frontend/src/lib/domain.ts) so the API rejects exactly what the UI
// refuses to submit.
var competitorDomainRe = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)

// NormalizeCompetitorDomain reduces whatever the caller supplied — a pasted URL,
// a "www." host, a trailing path — to the bare lowercase hostname we store, and
// reports whether the result is domain-shaped. Competitor domains are the target
// of DataForSEO ranked-keyword lookups, so a non-domain string isn't cosmetic:
// it silently returns zero keywords for that competitor.
func NormalizeCompetitorDomain(raw string) (string, bool) {
	d := commonutils.ExtractDomain(raw)
	return d, competitorDomainRe.MatchString(d)
}

// sanitizeCompetitor strips HTML from the free-text fields and normalizes the
// domain to a bare hostname, rejecting anything that isn't domain-shaped. The
// HTML strip alone was never enough: a client bypassing the UI could PATCH
// "hello world" as a domain and it would persist, then contribute nothing but a
// diagnostic to every keyword run.
func sanitizeCompetitor(c *models.Competitor) error {
	c.CompanyName = patchSanitizer.Sanitize(c.CompanyName)
	c.Reason = patchSanitizer.Sanitize(c.Reason)

	raw := patchSanitizer.Sanitize(c.Domain)
	domain, ok := NormalizeCompetitorDomain(raw)
	if !ok {
		return fmt.Errorf("competitor domain %q is not a valid domain", raw)
	}
	c.Domain = domain
	return nil
}

// applyPublishingOp handles the composite "publishing" field. The client sends
// the whole config as a single replace; remove clears it. Granular sub-field
// patches aren't supported — keep the surface narrow.
//
// Two fields are preserved from the existing entity rather than taken from the
// incoming payload:
//   - apiKey: the GET response only exposes `hasApiKey: bool`, so the client
//     can't echo it back. Preserving avoids a benign mode edit silently
//     disconnecting the user's publishing integration.
//   - articlesPerWeek: not editable from settings (only set during onboarding).
//     Preserving here makes the rule enforced server-side too, so a tampered
//     payload can't change cadence.
func applyPublishingOp(set, unset bson.M, op dto.PatchOp, current *models.WebEntity) error {
	switch op.Op {
	case dto.PatchOpReplace:
		var v models.PublishingConfig
		if err := json.Unmarshal(op.Value, &v); err != nil {
			return fmt.Errorf("value must be PublishingConfig object: %w", err)
		}
		if current != nil && current.Publishing != nil {
			if v.ApiKey == "" {
				v.ApiKey = current.Publishing.ApiKey
			}
			// ProjectURL is returned in the clear (unlike apiKey) so a client
			// normally echoes it back; preserving on empty keeps a partial
			// payload from silently disconnecting the integration.
			if v.ProjectURL == "" {
				v.ProjectURL = current.Publishing.ProjectURL
			}
			v.ArticlesPerWeek = current.Publishing.ArticlesPerWeek
		}
		if err := sanitizePublishing(&v); err != nil {
			return err
		}
		set[op.Field] = v
		return nil
	case dto.PatchOpRemove:
		unset[op.Field] = ""
		return nil
	default:
		return fmt.Errorf("op %q not supported on publishing", op.Op)
	}
}

// authCollectionRe matches Payload collection slugs (e.g. "users",
// "admin-users"). Kept permissive on case: Payload slugs are conventionally
// lowercase but nothing enforces it server-side.
var authCollectionRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func sanitizePublishing(p *models.PublishingConfig) error {
	p.Platform = patchSanitizer.Sanitize(p.Platform)
	p.PublishMode = patchSanitizer.Sanitize(p.PublishMode)
	// CollectionID / ProjectURL are the publish target captured next to the
	// API key. Not secrets, so the client echoes them back on edit — sanitize
	// them like any other free-text field.
	p.CollectionID = patchSanitizer.Sanitize(p.CollectionID)
	p.ProjectURL = patchSanitizer.Sanitize(p.ProjectURL)
	p.AuthCollection = patchSanitizer.Sanitize(p.AuthCollection)

	if p.Platform != "" && !constants.IsValidPlatform(p.Platform) {
		return fmt.Errorf("unsupported platform %q", p.Platform)
	}
	// Framer publishes through the Server API's connect(projectUrl, apiKey),
	// Payload through its REST API at the server base URL — without a valid
	// project URL neither integration can work, so reject the config up front
	// rather than failing at publish time.
	if p.Platform == string(constants.PlatformFramer) || p.Platform == string(constants.PlatformPayload) {
		if p.ProjectURL == "" {
			return fmt.Errorf("projectUrl is required for platform %q", p.Platform)
		}
		u, err := url.Parse(p.ProjectURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("projectUrl must be a valid http(s) URL")
		}
		// Framer project links are routinely copied with editor state
		// (`?node=…`) appended — store the canonical URL without it.
		u.RawQuery = ""
		u.Fragment = ""
		p.ProjectURL = u.String()
	}
	// AuthCollection is a Payload collection slug; anything beyond
	// slug-shaped characters is a typo or an injection attempt.
	if p.AuthCollection != "" && !authCollectionRe.MatchString(p.AuthCollection) {
		return fmt.Errorf("authCollection must be a collection slug (letters, digits, - or _)")
	}
	if p.PublishMode != "" && !constants.IsValidPublishMode(p.PublishMode) {
		return fmt.Errorf("unsupported publish mode %q", p.PublishMode)
	}
	if !constants.IsValidCadence(p.ArticlesPerWeek) {
		return fmt.Errorf("unsupported articles_per_week %d", p.ArticlesPerWeek)
	}
	// Manual platforms can't auto-publish — coerce to review server-side too,
	// not just on the client, so a tampered payload still lands sane.
	if p.Platform == string(constants.PlatformManual) && p.PublishMode == string(constants.ModeAuto) {
		p.PublishMode = string(constants.ModeReview)
	}

	p.ApiKey = patchSanitizer.Sanitize(p.ApiKey)
	return nil
}

func sanitizeICPSignals(s *models.ICPSignals) {
	for i := range s.Roles {
		s.Roles[i] = patchSanitizer.Sanitize(s.Roles[i])
	}
	for i := range s.Industries {
		s.Industries[i] = patchSanitizer.Sanitize(s.Industries[i])
	}
	for i := range s.PainPoints {
		s.PainPoints[i] = patchSanitizer.Sanitize(s.PainPoints[i])
	}
	if s.CompanySize != nil {
		v := patchSanitizer.Sanitize(*s.CompanySize)
		s.CompanySize = &v
	}
}

// readStringArray pulls a string slice out of the current entity by BSON path.
// Returns a copy so callers can mutate freely.
func readStringArray(e *models.WebEntity, path string) []string {
	if e == nil || e.BusinessContext == nil {
		return nil
	}
	bc := e.BusinessContext
	var src []string
	switch path {
	case "context.key_features":
		src = bc.KeyFeatures
	case "context.integrations":
		src = bc.Integrations
	case "context.inferred_fields":
		src = bc.InferredFields
	case "context.icp_signals.roles":
		if bc.ICPSignals != nil {
			src = bc.ICPSignals.Roles
		}
	case "context.icp_signals.industries":
		if bc.ICPSignals != nil {
			src = bc.ICPSignals.Industries
		}
	case "context.icp_signals.pain_points":
		if bc.ICPSignals != nil {
			src = bc.ICPSignals.PainPoints
		}
	}
	return append([]string(nil), src...)
}
