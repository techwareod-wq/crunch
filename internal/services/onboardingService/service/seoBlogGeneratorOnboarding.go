package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	llmDto "github.com/atharva-ng/crunch/internal/dto"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/locations"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/onboardingService"
	"github.com/atharva-ng/crunch/internal/services/onboardingService/constants"
	"github.com/atharva-ng/crunch/internal/services/onboardingService/dto"
	"github.com/atharva-ng/crunch/internal/services/onboardingService/prompts"
	onboardingstore "github.com/atharva-ng/crunch/internal/services/onboardingService/store"
	"github.com/atharva-ng/crunch/internal/services/onboardingService/utils"
	paymentService "github.com/atharva-ng/crunch/internal/services/paymentService"
	siteIntelligenceEngine "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine"
	"github.com/atharva-ng/crunch/internal/util/log"
	commonutils "github.com/atharva-ng/crunch/internal/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

var _ onboardingService.OnboardingService = (*seoBlogGeneratorOnboarding)(nil)

type seoBlogGeneratorOnboarding struct {
	store      onboardingstore.Store
	LLM        *config.LLMProvider
	dataForSEO interfaces.DataForSEO
	// dispatcher enqueues the async onboarding analysis chain
	// (ONBOARDING_PROCESS_WEBENTITY → ONBOARDING_GET_COMPETITOR_INFO).
	// OnboardUser returns as soon as the web entity exists; the heavy
	// website-fetch/LLM/SERP work runs on the queue and the frontend polls
	// GetOnboardingSteps for the WEBENTITY_CREATED → CONTEXT_CREATED flip.
	dispatcher interfaces.Dispatcher
	// payments gates the SIE trigger on a confirmed subscription, and sie
	// performs the trigger itself. Both are read inside GetOnboardingSteps,
	// where the finalised entity is already in hand. We depend only on the
	// root interface packages (no import cycle).
	payments paymentService.PaymentService
	sie      siteIntelligenceEngine.SiteIntelligenceService
	// locations is the shared boot-time DataForSEO country catalog (built in
	// providers wiring; the process fails to start without it, so this is
	// guaranteed non-nil after construction).
	locations *locations.Catalog
	// countriesList preserves the catalog order for the frontend dropdown.
	countriesList []dto.Country
	// values holds the onboarding tunables (SERP default language/depth) sourced
	// from values.onboarding.
	values config.OnboardingValues
	// dfsFetch holds the apis.dataForSEO rendered-fetch tunables (success
	// status code, flow deadline), mapped once at construction for
	// FetchRenderedWebsiteContent.
	dfsFetch commonutils.DataForSEOFetchValues
}

func NewSeoBlogGeneratorOnboardingService(
	s onboardingstore.Store,
	llm *config.LLMProvider,
	dfs interfaces.DataForSEO,
	dispatcher interfaces.Dispatcher,
	payments paymentService.PaymentService,
	sie siteIntelligenceEngine.SiteIntelligenceService,
	values config.OnboardingValues,
	dfsValues config.DataForSEOValues,
	locationCatalog *locations.Catalog,
) onboardingService.OnboardingService {
	// Locations are critical for onboarding validation and any subsequent
	// SIE/SERP calls — a nil catalog is a wiring bug, not a runtime state.
	if locationCatalog == nil {
		panic("onboarding: location catalog is nil")
	}
	countries := locationCatalog.Countries()
	list := make([]dto.Country, 0, len(countries))
	for _, c := range countries {
		list = append(list, dto.Country{Name: c.Name})
	}
	return &seoBlogGeneratorOnboarding{
		store:         s,
		LLM:           llm,
		dataForSEO:    dfs,
		dispatcher:    dispatcher,
		payments:      payments,
		sie:           sie,
		values:        values,
		locations:     locationCatalog,
		countriesList: list,
		dfsFetch: commonutils.DataForSEOFetchValues{
			TaskOKStatusCode: dfsValues.TaskOkStatusCode,
			FetchTimeout:     time.Duration(dfsValues.FetchTimeoutSeconds) * time.Second,
		},
	}
}

// OnboardUser is idempotent per user — if a webentity already exists for the
// user, the existing one is returned and no new record is created. The bool
// return reports whether a new entity was created (true) or an existing one
// was returned (false).
//
// The analysis itself (business context + competitor discovery) runs async:
// a successful create dispatches ONBOARDING_PROCESS_WEBENTITY and returns
// immediately; the frontend polls GetOnboardingSteps until the analysis
// completes (onboarding_analysis_done — competitor discovery may finish with
// an empty list).
// Re-submitting against an entity whose previous chain permanently failed
// (onboarding_error set, still no competitors) clears the marker and
// re-dispatches — that's the user-facing retry path.
func (s *seoBlogGeneratorOnboarding) OnboardUser(ctx context.Context, req dto.OnboardRequest, userId string) (string, bool, error) {
	// Validate the supplied country against the cached DataForSEO catalog
	// before anything else — if it isn't a recognized location we refuse to
	// kick off the analysis pipeline (the downstream SERP/keyword calls all
	// need a valid location_code).
	country, ok := s.locations.ByName(req.Country)
	if !ok {
		return "", false, apperrors.ErrInvalidCountry
	}

	existing, err := s.store.FindWebEntitiesByUserID(ctx, userId)
	if err != nil {
		return "", false, fmt.Errorf("failed to look up existing web entities: %w", err)
	}
	if len(existing) > 0 {
		e := existing[0]
		if e.OnboardingError != nil && !e.Finalised && len(e.Competitors) == 0 {
			if err := s.redispatchAnalysis(ctx, userId, e.ID.Hex()); err != nil {
				return "", false, err
			}
		}
		return e.ID.Hex(), false, nil
	}

	userOID, err := primitive.ObjectIDFromHex(userId)
	if err != nil {
		return "", false, fmt.Errorf("invalid user ID: %w", err)
	}

	// Tenancy plan §8/P3: the creation path stamps company_id at insert —
	// post-deploy signups never produce unstamped docs for companymigrate's
	// verify pass to trip on. Onboarding is the personal-company product
	// surface, so the personal company (minted lazily for legacy users) is
	// the owner.
	var companyID primitive.ObjectID
	if uFound, user, uErr := models.FindUserByID(ctx, userId); uErr == nil && uFound && user.Email != "" {
		if company, cErr := models.EnsurePersonalCompany(ctx, user.ID, user.Email, user.Name); cErr == nil {
			companyID = company.ID
		} else {
			log.Error("onboarding: personal company resolution failed — web entity created unstamped", "userId", userId, "err", cErr)
		}
	}

	internalLinkingDefault := true
	entity := &models.WebEntity{
		CompanyID:              companyID,
		UserID:                 userOID,
		WebsiteUrl:             req.WebsiteURL,
		CountryCode:            country.ISOCode,
		LocationCode:           country.LocationCode,
		InternalLinkingEnabled: &internalLinkingDefault,
	}
	if err := s.store.CreateWebEntity(ctx, entity); err != nil {
		if !mongo.IsDuplicateKeyError(err) {
			return "", false, fmt.Errorf("failed to create web entity: %w", err)
		}

		// Race lost: a concurrent OnboardUser for the same user passed the
		// find-then-create check and inserted first (the unique user_id index
		// rejected this insert). Re-read and return the winner's entity, treating
		// this as the existing-entity path.
		winners, rerr := s.store.FindWebEntitiesByUserID(ctx, userId)
		if rerr != nil {
			return "", false, fmt.Errorf("re-read after dup key: %w", rerr)
		}
		if len(winners) == 0 {
			return "", false, fmt.Errorf("web entity missing after dup key for user %s", userId)
		}
		// The winning request dispatches the analysis chain; don't double-dispatch.
		return winners[0].ID.Hex(), false, nil
	}

	if err := s.dispatchAnalysis(ctx, userId, entity.ID.Hex()); err != nil {
		// The entity exists but no analysis is in flight. Mark it failed so the
		// steps endpoint reports ONBOARDING_FAILED instead of an eternal
		// WEBENTITY_CREATED, and so a re-submit takes the retry path above.
		if markErr := models.SetWebEntityOnboardingError(ctx, entity.ID.Hex(), string(onboardingService.ProcessOnboardingWebEntity), err.Error()); markErr != nil {
			log.Error("onboarding: failed to mark web entity after dispatch failure", "webEntityId", entity.ID.Hex(), "err", markErr)
		}
		return "", false, fmt.Errorf("failed to dispatch onboarding analysis: %w", err)
	}
	return entity.ID.Hex(), true, nil
}

// dispatchAnalysis enqueues the first step of the async onboarding analysis
// chain for the given web entity.
func (s *seoBlogGeneratorOnboarding) dispatchAnalysis(ctx context.Context, userId, webEntityId string) error {
	return s.dispatcher.Dispatch(ctx, string(onboardingService.ProcessOnboardingWebEntity), userId, pipeline.StandardPayload{
		WebEntityID: webEntityId,
	})
}

// redispatchAnalysis retries a permanently failed analysis chain: clear the
// failure marker first, then dispatch. If the dispatch fails, re-mark the
// entity so the steps endpoint keeps reporting ONBOARDING_FAILED and the next
// re-submit retries again (clearing after dispatch instead would let a
// concurrent steps poll see ONBOARDING_FAILED while a live run is in flight
// and trigger a duplicate chain).
func (s *seoBlogGeneratorOnboarding) redispatchAnalysis(ctx context.Context, userId, webEntityId string) error {
	if err := s.store.PartialUpdateWebEntity(ctx, webEntityId, nil, bson.M{"onboarding_error": ""}); err != nil {
		return fmt.Errorf("failed to clear onboarding error: %w", err)
	}
	if err := s.dispatchAnalysis(ctx, userId, webEntityId); err != nil {
		if markErr := models.SetWebEntityOnboardingError(ctx, webEntityId, string(onboardingService.ProcessOnboardingWebEntity), err.Error()); markErr != nil {
			log.Error("onboarding: failed to re-mark web entity after retry dispatch failure", "webEntityId", webEntityId, "err", markErr)
		}
		return fmt.Errorf("failed to dispatch onboarding analysis retry: %w", err)
	}
	return nil
}

func (s *seoBlogGeneratorOnboarding) ProcessOnboardedUser(ctx context.Context, userId string, webEntityId string) error {
	exists, webEntity, err := s.store.GetWebEntityByID(ctx, webEntityId)
	if err != nil {
		return fmt.Errorf("failed to get web entity: %w", err)
	}
	if !exists {
		return fmt.Errorf("web entity not found")
	}
	if webEntity.UserID.Hex() != userId {
		return fmt.Errorf("incorrect user id")
	}

	website := webEntity.WebsiteUrl
	// Primary path is DataForSEO's browser-rendered crawl (defeats bot walls
	// and CSR-only shells); the direct HTTP fetch inside is the fallback.
	textContentStr, err := commonutils.FetchRenderedWebsiteContent(ctx, s.dataForSEO, s.dfsFetch, website, true, true)
	if err != nil {
		if errors.Is(err, commonutils.ErrScrapeBlocked) {
			// Both fetch paths hit a bot wall; it won't unblock between queue
			// retries. The user-facing retry path is re-submitting the form
			// (redispatchAnalysis).
			return fmt.Errorf("website blocked automated access or returned no content: %v: %w", err, pipeline.ErrPermanent)
		}
		return fmt.Errorf("failed to fetch website content: %w", err)
	}

	onboardingPromptDto := dto.OnboardingPromptRequest{
		WebsiteContent: textContentStr,
	}

	prompt, err := s.LLM.Utils.ConstructPrompt(prompts.OnboardUser, onboardingPromptDto)
	if err != nil {
		return fmt.Errorf("failed to construct prompt: %w", err)
	}

	promptReq := llmDto.PromptRequest{
		Messages: []llmDto.Message{
			{Role: "user", Content: prompt},
		},
		MaxTokens: s.LLM.DefaultMaxTokens,
	}

	response, err := s.LLM.Anthropic.Prompt(ctx, promptReq)
	if err != nil {
		return fmt.Errorf("failed to prompt LLM: %w", err)
	}
	cleanedResponse, err := s.LLM.Utils.CleanLLMResponse(response.Content)
	if err != nil {
		return fmt.Errorf("failed to clean LLM response: %w", err)
	}

	bc, err := utils.ParseBusinessContext(cleanedResponse)
	if err != nil {
		// The evidence goes to logs, not the returned error — onboarding_error
		// stays clean for the frontend. StopReason distinguishes a max_tokens
		// truncation from a prose preamble.
		log.Error("onboarding: LLM returned unparseable JSON",
			"step", string(onboardingService.ProcessOnboardingWebEntity),
			"webEntityId", webEntityId,
			"stopReason", response.StopReason,
			"responseSnippet", commonutils.Truncate(cleanedResponse, 300))
		return fmt.Errorf("failed to parse LLM response: %w", err)
	}

	if bc.ExtractionFailed {
		// The page fetched fine but doesn't describe an identifiable business
		// (parked domain, "coming soon" shell). Persist nothing and stop the
		// chain here rather than let sentinel prose flow downstream.
		return fmt.Errorf("could not identify a business from the website content: %w", pipeline.ErrPermanent)
	}

	if missing := bc.MissingCoreFields(); len(missing) > 0 {
		// The extraction prompt instructs the model to infer absent fields (and
		// record them in inferred_fields), so a hole here is a malformed
		// generation, not a property of the site. Fail the message so the queue
		// retries with a fresh LLM run rather than persist a context the SIE
		// pipeline can't prompt from.
		log.Error("onboarding: extracted business context is missing core fields",
			"step", string(onboardingService.ProcessOnboardingWebEntity),
			"webEntityId", webEntityId,
			"missing", strings.Join(missing, ","))
		return fmt.Errorf("extracted business context is missing core fields: %s", strings.Join(missing, ", "))
	}

	webEntityUpdate := models.WebEntity{
		BusinessContext: bc,
	}
	err = s.store.UpdateWebEntity(ctx, webEntityId, webEntityUpdate)
	if err != nil {
		return fmt.Errorf("failed to update web entity with business context: %w", err)
	}

	// Chain the second analysis step. A dispatch failure fails this message so
	// the queue retries it; the re-run redoes the (idempotent) context write
	// and dispatches again.
	if err := s.dispatcher.Dispatch(ctx, string(onboardingService.ProcessOnboardingCompetitorInfo), userId, pipeline.StandardPayload{
		WebEntityID: webEntityId,
	}); err != nil {
		return fmt.Errorf("failed to dispatch competitor info step: %w", err)
	}

	return nil
}

// PatchOnboardedUser applies the requested ops and, when req.Finalise is true,
// also flips the webentity's finalised flag in the same Mongo update so the
// transition is atomic. Patches against a finalised entity are allowed — the
// /settings screen continues to edit the same fields post-finalisation. The
// only field that's permanently uneditable is the top-level WebsiteUrl, which
// isn't on the patchable whitelist. The bool return reports the post-update
// finalised state.
//
// Competitors stay editable post-finalisation too (the settings screen lets
// users add and remove them), but the resulting list must always hold between
// one and onboarding.maxCompetitors entries. These bounds are enforced
// server-side here so a tampered payload can't bypass the UI. Key features
// share the lower bound: a patch may never leave the list empty.
func (s *seoBlogGeneratorOnboarding) PatchOnboardedUser(ctx context.Context, req dto.PatchWebEntityRequest, userId string) (int, bool, error) {
	exists, entity, err := s.store.GetWebEntityByID(ctx, req.WebEntityID)
	if err != nil {
		return 0, false, fmt.Errorf("failed to get web entity: %w", err)
	}
	if !exists {
		return 0, false, apperrors.ErrWebEntityNotFound
	}
	if entity.UserID.Hex() != userId {
		return 0, false, apperrors.ErrWebEntityNotFound
	}

	competitorsTouched := false
	keyFeaturesTouched := false
	for _, op := range req.Ops {
		if op.Field == "competitors" {
			competitorsTouched = true
		}
		if op.Field == "context.key_features" {
			keyFeaturesTouched = true
		}
	}
	set, unset, err := utils.ApplyPatchOps(entity, req.Ops)
	if err != nil {
		// Every ApplyPatchOps failure is a rejected client payload (unknown
		// field, wrong value type, malformed competitor domain), so answer 400
		// with the reason rather than the generic 500 the handler falls back to.
		// The detail is built from the caller's own ops — nothing internal leaks.
		return 0, false, &apperrors.Error{Code: http.StatusBadRequest, Message: err.Error()}
	}

	// Validate the post-patch competitor count against the configured bounds.
	// ApplyPatchOps always stages the full slice under "competitors" when any
	// competitor op ran, so the staged value is the authoritative final list.
	if competitorsTouched {
		comps, _ := set["competitors"].([]models.Competitor)
		if len(comps) < 1 {
			return 0, false, apperrors.ErrAtLeastOneCompetitor
		}
		if s.values.MaxCompetitors > 0 && len(comps) > s.values.MaxCompetitors {
			return 0, false, apperrors.ErrTooManyCompetitors
		}
	}

	// Key features feed the generation prompts, so the list may never be
	// patched down to empty. Like competitors, ApplyPatchOps stages the full
	// post-op slice, so the staged value is the authoritative final list.
	if keyFeaturesTouched {
		feats, _ := set["context.key_features"].([]string)
		if len(feats) < 1 {
			return 0, false, apperrors.ErrAtLeastOneKeyFeature
		}
	}

	if req.Finalise {
		// Discovery may complete with zero competitors (the user is expected to
		// add their own on the profile screen) — finalisation requires at least
		// one. SIE's competitor-keyword stage assumes at least one.
		finalComps := entity.Competitors
		if competitorsTouched {
			finalComps, _ = set["competitors"].([]models.Competitor)
		}
		if len(finalComps) == 0 {
			return 0, false, apperrors.ErrAtLeastOneCompetitor
		}
		// Finalisation starts the SIE pipeline, whose prompts consume the core
		// business-context fields unconditionally — so an entity may not
		// finalise while any of them are missing. Judged on the post-patch
		// state so a request that fills the hole and finalises together passes.
		if missing := utils.EffectiveBusinessContext(entity, set).MissingCoreFields(); len(missing) > 0 {
			return 0, false, &apperrors.Error{
				Code:    http.StatusBadRequest,
				Message: fmt.Sprintf("cannot finalise: business context is missing %s", strings.Join(missing, ", ")),
			}
		}
		if set == nil {
			set = bson.M{}
		}
		set["finalised"] = true
	}

	if len(set) == 0 && len(unset) == 0 {
		return 0, entity.Finalised, nil
	}

	if err := s.store.PartialUpdateWebEntity(ctx, req.WebEntityID, set, unset); err != nil {
		return 0, false, fmt.Errorf("failed to patch web entity: %w", err)
	}

	return len(req.Ops), entity.Finalised || req.Finalise, nil
}

func (s *seoBlogGeneratorOnboarding) EditOnboardedUser(ctx context.Context, req models.WebEntity, userId string, webEntityId string) error {
	exists, user, err := s.store.GetWebEntityByID(ctx, webEntityId)
	if !exists {
		return fmt.Errorf("user web entity not found")
	}
	if err != nil {
		return err
	}

	if user.UserID.Hex() != userId {
		return fmt.Errorf("incorrect user id")
	}

	err = s.store.UpdateWebEntity(ctx, webEntityId, req)
	if err != nil {
		return fmt.Errorf("failed to update web entity: %w", err)
	}

	return nil
}

func (s *seoBlogGeneratorOnboarding) GetCompetitorInfo(ctx context.Context, userId string, webEntityId string) error {
	exists, webEntity, err := s.store.GetWebEntityByID(ctx, webEntityId)
	if err != nil {
		return fmt.Errorf("failed to get web entity: %w", err)
	}
	if !exists {
		return fmt.Errorf("web entity not found")
	}
	if webEntity.UserID.Hex() != userId {
		return fmt.Errorf("incorrect user id")
	}

	// The SERP keyword and the competitors prompt both need the extracted
	// business context. Missing fields mean the LLM extraction under-delivered;
	// retrying this step can't fix that, so fail permanently (the retry path is
	// the user re-submitting, which re-runs the extraction).
	bc := webEntity.BusinessContext
	if bc == nil || bc.ProductType == nil || bc.BusinessName == nil {
		return fmt.Errorf("business context incomplete for web entity %s: %w", webEntityId, pipeline.ErrPermanent)
	}

	// Run several SERP queries instead of a single "{product} tools" search: that
	// one term skews toward directories and mega-sites. Merging a few
	// intent-varied queries (and deduping domains downstream) yields a cleaner
	// pool of real peers for the competitor LLM prompt to filter.
	productType := *webEntity.BusinessContext.ProductType
	serpKeywords := []string{
		productType + " software",
		productType + " alternatives",
	}
	// Add a role-anchored query when we know the primary ICP role.
	if bc.ICPSignals != nil {
		for _, role := range bc.ICPSignals.Roles {
			if r := strings.TrimSpace(role); r != "" {
				serpKeywords = append(serpKeywords, fmt.Sprintf("best %s for %s", productType, r))
				break
			}
		}
	}

	serpTasks := make([]llmDto.SerpTask, 0, len(serpKeywords))
	for _, kw := range serpKeywords {
		serpTasks = append(serpTasks, llmDto.SerpTask{
			Keyword:      kw,
			LocationCode: webEntity.LocationCode,
			LanguageCode: s.values.SerpDefaultLanguageCode,
			Depth:        s.values.CompetitorSerpDepth,
		})
	}
	serpReq := llmDto.GetSerpResultsRequest{Tasks: serpTasks}

	serpResp, err := s.dataForSEO.GetSerpResults(ctx, serpReq)
	if err != nil {
		return fmt.Errorf("failed to get SERP results: %w", err)
	}

	searchResults := utils.FormatSerpResultsForLLM(serpResp)

	// Auto-discovery seeds the list with onboarding.discoverCompetitors entries,
	// not the full onboarding.maxCompetitors cap — the user tops it up by hand.
	discoverCount := s.values.DiscoverCompetitorsOrDefault()

	getCompetitorsPromptDto := dto.GetCompititorsPromptRequest{
		BusinessName:    *webEntity.BusinessContext.BusinessName,
		ProductType:     *webEntity.BusinessContext.ProductType,
		UserDomain:      webEntity.WebsiteUrl,
		SearchResults:   searchResults,
		CompetitorCount: discoverCount,
	}

	prompt, err := s.LLM.Utils.ConstructPrompt(prompts.GetWebEntityCompetitors, getCompetitorsPromptDto)
	if err != nil {
		return fmt.Errorf("failed to construct prompt: %w", err)
	}

	promptReq := llmDto.PromptRequest{
		Messages: []llmDto.Message{
			{Role: "user", Content: prompt},
		},
		Model:     llmDto.AnthropicSonnet46,
		MaxTokens: s.LLM.DefaultMaxTokens,
	}

	response, err := s.LLM.Anthropic.Prompt(ctx, promptReq)
	if err != nil {
		return fmt.Errorf("failed to prompt LLM: %w", err)
	}

	cleanedResponse, err := s.LLM.Utils.CleanLLMResponse(response.Content)
	if err != nil {
		return fmt.Errorf("failed to clean LLM response: %w", err)
	}

	competitors, err := utils.ParseCompetitorInfo(cleanedResponse)
	if err != nil {
		log.Error("onboarding: LLM returned unparseable JSON",
			"step", string(onboardingService.ProcessOnboardingCompetitorInfo),
			"webEntityId", webEntityId,
			"stopReason", response.StopReason,
			"responseSnippet", commonutils.Truncate(cleanedResponse, 300))
		return fmt.Errorf("failed to parse competitor info: %w", err)
	}

	// The model occasionally answers with a company name or a full URL where a
	// bare domain belongs. Normalize to the hostname and drop anything still not
	// domain-shaped: DataForSEO keys ranked-keyword lookups on the domain, so a
	// junk entry contributes zero keywords, and once seeded it would also fail
	// the settings patch (which rejects non-domains) on the user's next save.
	comps := (*competitors)[:0]
	for _, c := range *competitors {
		domain, ok := utils.NormalizeCompetitorDomain(c.Domain)
		if !ok {
			log.Warn("onboarding: dropping discovered competitor with non-domain value",
				"webEntityId", webEntityId, "domain", c.Domain)
			continue
		}
		c.Domain = domain
		comps = append(comps, c)
	}

	// The prompt asks for exactly discoverCount, but guard against an over-eager
	// model so the seeded list never exceeds the discovery count (itself clamped
	// to the maxCompetitors cap the patch flow and UI both assume).
	if discoverCount > 0 && len(comps) > discoverCount {
		comps = comps[:discoverCount]
	}

	// Best-effort domain-rating fetch so SIE reads a real udr instead of racing
	// its own GetUserDomainRating step against the keyword steps (see
	// docs/plans/domain-rating-in-onboarding.md). Non-blocking: on failure — or a
	// rank of 0, meaning DataForSEO has no authority data for the domain — we
	// leave UserDomainRating unset (the write below skips the field when it's 0)
	// and SIE's step fills it in. It never fails competitor persistence.
	rating := 0
	if r, err := commonutils.FetchDomainRating(ctx, s.dataForSEO, webEntity.WebsiteUrl); err != nil {
		log.Error("onboarding: domain rating fetch failed, leaving unset for SIE",
			"error", err, "webEntityId", webEntityId, "url", webEntity.WebsiteUrl)
	} else {
		rating = r
	}

	// Zero competitors is a valid outcome (niche product, empty or unusable
	// SERP): proceed rather than fail. The analysis-done marker — not the
	// competitor list — is what advances GetOnboardingSteps to CONTEXT_CREATED,
	// where the user adds competitors by hand; finalisation still enforces the
	// ≥1 floor. Skipping the competitors write on empty also keeps a queue
	// re-run from clobbering a user-added list with nothing.
	set := bson.M{"onboarding_analysis_done": true}
	if len(comps) > 0 {
		set["competitors"] = comps
	} else {
		log.Warn("onboarding: competitor discovery returned zero competitors, proceeding without",
			"webEntityId", webEntityId, "emptySerp", searchResults == "")
	}
	if rating != 0 {
		set["context.user_domain_rating"] = rating
	}
	if err := s.store.PartialUpdateWebEntity(ctx, webEntityId, set, nil); err != nil {
		return fmt.Errorf("failed to update web entity with Competitor info: %w", err)
	}

	return nil
}

// GetOnboardingSteps returns the user's current onboarding step plus only the
// data the frontend needs to render that step. Step selection rule (highest
// priority first):
//   - any finalised webentity whose SIE context is at SIEStatusSchedulingDone (9) → SCHEDULING_DONE
//   - any finalised webentity whose SIE context is at SIEStatusClusteringDone (8) → SITE_INTELLIGENCE_DONE
//   - any finalised webentity with a live (non-errored, below clustering) SIE
//     context, or a finalised+paid webentity for which we trigger SIE here → SITE_INTELLIGENCE_TRIGGERED
//   - any finalised webentity (unpaid, no context) → FINALISED
//   - else any webentity whose analysis chain completed (onboarding_analysis_done,
//     or a non-empty competitor list on pre-marker docs) → CONTEXT_CREATED
//     (first one returned)
//   - else any webentity whose async analysis chain permanently failed
//     (onboarding_error set) → ONBOARDING_FAILED
//   - else any webentity exists → WEBENTITY_CREATED (first one returned)
//   - else → USER_CREATED
//
// This endpoint is the primary SIE trigger: when a finalised entity has a valid
// subscription and no in-flight run, it calls Orchestrate (idempotent) from
// inside the step read, so the trigger fires exactly once across every app
// entry point (deep-link, second tab, closed-tab recovery) with no frontend
// latch. See deriveFinalisedStep.
//
// Per-step payload:
//   - USER_CREATED → country list (the / onboarding form needs it)
//   - CONTEXT_CREATED → webentity + country list (the /profile screen needs both)
//   - every other step → no payload; the frontend redirects on `step` alone
func (s *seoBlogGeneratorOnboarding) GetOnboardingSteps(ctx context.Context, userId string) (*dto.OnboardingStepsResponse, error) {
	entities, err := s.store.FindWebEntitiesByUserID(ctx, userId)
	if err != nil {
		return nil, fmt.Errorf("failed to look up web entities: %w", err)
	}

	if len(entities) == 0 {
		return &dto.OnboardingStepsResponse{
			Step:      dto.OnboardingStepUserCreated,
			Countries: s.countriesList,
		}, nil
	}

	chosen := entities[0]
	for _, e := range entities {
		if e.Finalised {
			chosen = e
			break
		}
	}

	step := dto.OnboardingStepWebEntityCreated
	var wec *models.WebEntityContext
	switch {
	case chosen.Finalised:
		derived, derivedWec, err := s.deriveFinalisedStep(ctx, userId, chosen.ID.Hex())
		if err != nil {
			return nil, err
		}
		step = derived
		wec = derivedWec

	// Analysis chain completed — competitors may legitimately be empty
	// (discovery found none); the profile screen requires the user to add at
	// least one before finalising. The len check keeps legacy entities
	// (pre-marker docs) advancing on their non-empty list.
	case chosen.OnboardingAnalysisDone || len(chosen.Competitors) > 0:
		step = dto.OnboardingStepContextCreated

	// The async analysis chain died (retries exhausted or permanent failure)
	// before producing competitors. The frontend sends the user back to the
	// onboarding form; re-submitting re-dispatches via OnboardUser.
	case chosen.OnboardingError != nil:
		step = dto.OnboardingStepFailed
	}

	resp := &dto.OnboardingStepsResponse{Step: step}

	switch step {
	// CONTEXT_CREATED carries the webentity + country list: the /profile
	// screen renders both.
	case dto.OnboardingStepContextCreated:
		out := &dto.WebEntity{}
		out.FromDbModel(chosen)
		out.MaxCompetitors = s.values.MaxCompetitors
		resp.WebEntity = out
		resp.Countries = s.countriesList

	// WEBENTITY_CREATED carries the analyzing-screen progress, derived from
	// the entity already in hand — the /onboarding/analyzing poll renders it.
	case dto.OnboardingStepWebEntityCreated:
		resp.Analysis = dto.AnalysisProgressFromWebEntity(chosen)
	}

	// Any step derived with a WEC in hand carries the SIE progress payload —
	// the /onboarding/strategy poll renders it. On the final poll
	// (SCHEDULING_DONE) it lets the screen paint everything done while the
	// dashboard prefetch runs.
	if wec != nil {
		resp.SiteIntelligence = dto.SiteIntelligenceProgressFromWEC(wec)
	}

	return resp, nil
}

// deriveFinalisedStep computes the onboarding step for a finalised webentity and,
// when appropriate, triggers SIE as a side effect of the read. The rule:
//
//	WEC >= SchedulingDone (10)        → SCHEDULING_DONE        (no trigger)
//	WEC >= ClusteringDone (9)         → SITE_INTELLIGENCE_DONE (no trigger)
//	WEC in-flight (0..8, != error)    → SITE_INTELLIGENCE_TRIGGERED          (no trigger, no payment read)
//	no WEC, or WEC errored, + paid    → Orchestrate (create/resume) → SITE_INTELLIGENCE_TRIGGERED
//	no WEC, unpaid                    → FINALISED
//
// The common polling case (run in flight) does no payment read and no
// Orchestrate call — it just reports SITE_INTELLIGENCE_TRIGGERED. Only the FINALISED→triggered
// transition and errored-run recovery issue a payment read + Orchestrate, and
// Orchestrate is idempotent (in-flight no-ops, errored resumes from the last
// completed stage).
//
// The WEC read for step selection is returned alongside the step (nil when none
// exists) so GetOnboardingSteps can expose SIE progress without a second fetch.
// A fresh trigger returns a nil WEC even though Orchestrate just created one —
// the next poll picks it up.
func (s *seoBlogGeneratorOnboarding) deriveFinalisedStep(ctx context.Context, userId, webEntityId string) (string, *models.WebEntityContext, error) {
	found, wec, err := s.store.GetWebEntityContextByWebEntityAndUserID(ctx, webEntityId, userId)
	if err != nil {
		return "", nil, fmt.Errorf("failed to look up web entity context: %w", err)
	}

	if found {
		switch {
		case wec.Status >= models.SIEStatusSchedulingDone:
			return dto.OnboardingStepsSchedulingDone, wec, nil
		case wec.Status >= models.SIEStatusClusteringDone:
			return dto.OnboardingStepsSiteIntelligenceDone, wec, nil
		}
	}

	// Below clustering. A WEC that exists and isn't errored is a live run; honour
	// "if state is sie triggered, don't do anything" — no payment read, no trigger.
	inFlight := found && wec.Status != models.SIEStatusError
	if inFlight {
		return dto.OnboardingStepSiteIntelligenceTriggered, wec, nil
	}

	// No WEC (never triggered) or an errored run to retry. Gate on a confirmed
	// subscription, then trigger. Orchestrate creates the WEC as its first action
	// (so a successful call means a WEC now exists) or resumes an errored run.
	wecNowExists := found
	userObjID, err := primitive.ObjectIDFromHex(userId)
	if err != nil {
		return "", nil, fmt.Errorf("invalid user id %q: %w", userId, err)
	}

	paid, perr := s.payments.HasValidSubscription(ctx, userObjID)
	if perr != nil {
		// Treat a payment-read failure as not-paid: report the DB reality (no
		// trigger). The next poll retries the read.
		log.Error("onboarding: payment check failed during SIE trigger", "userId", userId, "err", perr)
	} else if paid {
		if oerr := s.sie.Orchestrate(ctx, userId, webEntityId); oerr != nil {
			// Log and fall through; the returned step reflects whether a WEC
			// exists. If this was an errored-run resume (found already true), the
			// WEC still exists → SITE_INTELLIGENCE_TRIGGERED. If it was a fresh trigger that
			// failed before/at create, found stays false → FINALISED, and the
			// next entry retries. A dispatch failure marks the WEC errored, so a
			// later poll resumes it.
			log.Error("onboarding: SIE Orchestrate failed", "userId", userId, "webEntityId", webEntityId, "err", oerr)
		} else {
			wecNowExists = true
		}
	}

	if wecNowExists {
		// The errored-resume path still has the (stale-but-real) WEC in hand;
		// the fresh-trigger path doesn't — return what we have.
		if found {
			return dto.OnboardingStepSiteIntelligenceTriggered, wec, nil
		}
		return dto.OnboardingStepSiteIntelligenceTriggered, nil, nil
	}
	return dto.OnboardingStepFinalised, nil, nil
}

// GetCurrentWebEntity returns the full web entity for the current user — the
// finalised one when present, otherwise the first record. This powers the
// /settings screen, which needs the entity regardless of onboarding step
// (unlike GetOnboardingSteps, which only surfaces it on CONTEXT_CREATED).
func (s *seoBlogGeneratorOnboarding) GetCurrentWebEntity(ctx context.Context, userId string) (*dto.WebEntity, error) {
	entities, err := s.store.FindWebEntitiesByUserID(ctx, userId)
	if err != nil {
		return nil, fmt.Errorf("failed to look up web entities: %w", err)
	}
	if len(entities) == 0 {
		return nil, apperrors.ErrWebEntityNotFound
	}

	chosen := entities[0]
	for _, e := range entities {
		if e.Finalised {
			chosen = e
			break
		}
	}

	out := &dto.WebEntity{}
	out.FromDbModel(chosen)
	out.MaxCompetitors = s.values.MaxCompetitors
	return out, nil
}

// GetPublishingOptions returns the static catalog the publishing screen
// renders. The data is built from constants/publishing.go — adding a new
// platform / mode / cadence is a one-file change.
func (s *seoBlogGeneratorOnboarding) GetPublishingOptions() *dto.PublishingOptionsResponse {
	return &dto.PublishingOptionsResponse{
		Platforms: constants.PlatformOptions,
		Modes:     constants.PublishModeOptions,
		Cadences:  constants.CadenceOptions,
	}
}
