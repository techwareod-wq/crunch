package dto

import (
	"strings"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
)

type ICPSignals struct {
	Roles       []string `json:"roles,omitempty"`
	Industries  []string `json:"industries,omitempty"`
	CompanySize *string  `json:"companySize,omitempty"`
	PainPoints  []string `json:"painPoints,omitempty"`
}

type BusinessContext struct {
	BusinessName      *string     `json:"businessName,omitempty"`
	Website           *string     `json:"website,omitempty"`
	ProductType       *string     `json:"productType,omitempty"`
	PrimaryUseCase    *string     `json:"primaryUseCase,omitempty"`
	KeyFeatures       []string    `json:"keyFeatures,omitempty"`
	Integrations      []string    `json:"integrations,omitempty"`
	BusinessModel     *string     `json:"businessModel,omitempty"`
	TargetGeography   *string     `json:"targetGeography,omitempty"`
	PricingModel      *string     `json:"pricingModel,omitempty"`
	KeyDifferentiator *string     `json:"keyDifferentiator,omitempty"`
	ICPSignals        *ICPSignals `json:"icpSignals,omitempty"`
	BrandVoiceSignals *string     `json:"brandVoiceSignals,omitempty"`
	InferredFields    []string    `json:"inferredFields,omitempty"`
	UserDomainRating  int         `json:"userDomainRating,omitempty"`
}

type Competitor struct {
	Domain      string `json:"domain,omitempty"`
	CompanyName string `json:"companyName,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type PublishingConfig struct {
	Platform  string `json:"platform,omitempty"`
	HasApiKey bool   `json:"hasApiKey"`
	// ProjectURL and CollectionID are not secrets — returned in the clear so
	// the settings UI can show and edit the current values (the apiKey stays
	// write-only behind HasApiKey).
	ProjectURL      string `json:"projectUrl,omitempty"`
	CollectionID    string `json:"collectionId,omitempty"`
	ArticlesPerWeek int    `json:"articlesPerWeek,omitempty"`
	PublishMode     string `json:"publishMode,omitempty"`
}

type WebEntity struct {
	ID              string            `json:"id"`
	WebsiteUrl      string            `json:"websiteUrl"`
	CountryCode     string            `json:"countryCode"`
	LocationCode    int               `json:"locationCode,omitempty"`
	BusinessContext *BusinessContext  `json:"businessContext,omitempty"`
	Competitors     []Competitor      `json:"competitors,omitempty"`
	Publishing      *PublishingConfig `json:"publishing,omitempty"`
	Finalised       bool              `json:"finalised"`
	// InternalLinkingEnabled is the web-entity-wide default for the
	// internal-link-insertion generation step. Defaults to true for entities
	// with no stored value. Editable from the settings/profile tab.
	InternalLinkingEnabled bool `json:"internalLinkingEnabled"`
	// PublishAsLive is the web-entity-wide default for whether a published
	// article lands on the platform live (true) or as a draft (false).
	// Defaults to false (draft) for entities with no stored value. Editable
	// from the settings/profile tab.
	PublishAsLive bool `json:"publishAsLive"`
	// ThumbnailStyle is the web-entity-wide default thumbnail style ID, resolved
	// (nil/invalid → system default) so the settings picker can highlight it
	// directly and the sidebar can label the "Workspace default (currently X)"
	// option. Editable from the settings/profile tab.
	ThumbnailStyle string `json:"thumbnailStyle"`
	// SEOStrategy is the site's SIE strategy id, resolved (nil/invalid → the
	// domain-rating-derived default) so onboarding and settings can highlight
	// the active card directly. Editable during onboarding and from the
	// settings/profile tab.
	SEOStrategy string `json:"seoStrategy"`
	// SEOStrategyDefault is the strategy the domain rating recommends,
	// regardless of the user's explicit choice — the UI tags this card as
	// "Recommended". Kept server-side so the DR cutoff lives in one place.
	SEOStrategyDefault string `json:"seoStrategyDefault"`
	// MaxCompetitors mirrors onboarding.maxCompetitors so the onboarding UI can
	// cap the competitor editor without hardcoding the limit. Set by the service
	// (not FromDbModel, which has no config access).
	MaxCompetitors int `json:"maxCompetitors,omitempty"`
	// StyleReplication is the learned style profile (style replication). Nil
	// until a run has completed; the settings page renders the three artifacts
	// as editable textareas and the thumbnail picker keys its "Your style
	// (learned)" card on ImageStylePrompt.
	StyleReplication *StyleReplication `json:"styleReplication,omitempty"`
	CreatedAt        time.Time         `json:"createdAt"`
	UpdatedAt        time.Time         `json:"updatedAt"`
}

// StyleReplication is the wire view of the learned style profile. Artifact
// text is the RAW stored value (a user-disabled artifact still shows in the
// settings editor); Apply carries the resolved per-artifact toggles.
type StyleReplication struct {
	ToneProfile           string          `json:"toneProfile,omitempty"`
	StructurePattern      string          `json:"structurePattern,omitempty"`
	TitlePattern          string          `json:"titlePattern,omitempty"`
	ThumbnailStylePrompt  string          `json:"thumbnailStylePrompt,omitempty"`
	MidArticleStylePrompt string          `json:"midArticleStylePrompt,omitempty"`
	Apply                 StyleApplyFlags `json:"apply"`
	SourceURLs            []string        `json:"sourceUrls,omitempty"`
	LearnedAt             *time.Time      `json:"learnedAt,omitempty"`
}

// StyleApplyFlags are the resolved (absent → true) per-artifact toggles, sent
// without omitempty so the editor renders definite switch states.
type StyleApplyFlags struct {
	ToneProfile      bool `json:"toneProfile"`
	StructurePattern bool `json:"structurePattern"`
	TitlePattern     bool `json:"titlePattern"`
	ThumbnailStyle   bool `json:"thumbnailStyle"`
	MidArticleStyle  bool `json:"midArticleStyle"`
}

func (e *WebEntity) FromDbModel(m *models.WebEntity) {
	e.ID = m.ID.Hex()
	e.WebsiteUrl = m.WebsiteUrl
	e.CountryCode = m.CountryCode
	e.LocationCode = m.LocationCode
	e.Finalised = m.Finalised
	e.InternalLinkingEnabled = m.InternalLinkingEnabledOrDefault()
	e.PublishAsLive = m.PublishAsLiveOrDefault()
	e.ThumbnailStyle = m.ThumbnailStyleOrDefault()
	e.SEOStrategy = m.SEOStrategyOrDefault()
	dr := 0
	if m.BusinessContext != nil {
		dr = m.BusinessContext.UserDomainRating
	}
	e.SEOStrategyDefault = models.DefaultSEOStrategyForDR(dr)
	e.CreatedAt = m.CreatedAt
	e.UpdatedAt = m.UpdatedAt

	if m.BusinessContext != nil {
		bc := &BusinessContext{
			BusinessName:      m.BusinessContext.BusinessName,
			Website:           m.BusinessContext.Website,
			ProductType:       m.BusinessContext.ProductType,
			PrimaryUseCase:    m.BusinessContext.PrimaryUseCase,
			KeyFeatures:       m.BusinessContext.KeyFeatures,
			Integrations:      m.BusinessContext.Integrations,
			BusinessModel:     m.BusinessContext.BusinessModel,
			TargetGeography:   m.BusinessContext.TargetGeography,
			PricingModel:      m.BusinessContext.PricingModel,
			KeyDifferentiator: m.BusinessContext.KeyDifferentiator,
			BrandVoiceSignals: m.BusinessContext.BrandVoiceSignals,
			InferredFields:    m.BusinessContext.InferredFields,
			UserDomainRating:  m.BusinessContext.UserDomainRating,
		}
		if m.BusinessContext.ICPSignals != nil {
			bc.ICPSignals = &ICPSignals{
				Roles:       m.BusinessContext.ICPSignals.Roles,
				Industries:  m.BusinessContext.ICPSignals.Industries,
				CompanySize: m.BusinessContext.ICPSignals.CompanySize,
				PainPoints:  m.BusinessContext.ICPSignals.PainPoints,
			}
		}
		e.BusinessContext = bc
	}

	if len(m.Competitors) > 0 {
		comps := make([]Competitor, len(m.Competitors))
		for i, c := range m.Competitors {
			comps[i] = Competitor{
				Domain:      c.Domain,
				CompanyName: c.CompanyName,
				Reason:      c.Reason,
			}
		}
		e.Competitors = comps
	}

	if sr := m.StyleReplication; sr != nil {
		// Raw derefs, NOT the applied getters — a disabled artifact must keep
		// its text visible in the settings editor.
		raw := func(p *string) string {
			if p == nil {
				return ""
			}
			return strings.TrimSpace(*p)
		}
		e.StyleReplication = &StyleReplication{
			ToneProfile:           raw(sr.ToneProfile),
			StructurePattern:      raw(sr.StructurePattern),
			TitlePattern:          raw(sr.TitlePattern),
			ThumbnailStylePrompt:  raw(sr.ThumbnailStylePrompt),
			MidArticleStylePrompt: raw(sr.MidArticleStylePrompt),
			Apply: StyleApplyFlags{
				ToneProfile:      sr.ToneApplied(),
				StructurePattern: sr.StructureApplied(),
				TitlePattern:     sr.TitleApplied(),
				ThumbnailStyle:   sr.ThumbnailStyleApplied(),
				MidArticleStyle:  sr.MidArticleStyleApplied(),
			},
			SourceURLs: sr.SourceURLs,
			LearnedAt:  sr.LearnedAt,
		}
	}

	if m.Publishing != nil {
		e.Publishing = &PublishingConfig{
			Platform:        m.Publishing.Platform,
			HasApiKey:       m.Publishing.ApiKey != "",
			ProjectURL:      m.Publishing.ProjectURL,
			CollectionID:    m.Publishing.CollectionID,
			ArticlesPerWeek: m.Publishing.ArticlesPerWeek,
			PublishMode:     m.Publishing.PublishMode,
		}
	}
}

const (
	OnboardingStepUserCreated               = "USER_CREATED"
	OnboardingStepWebEntityCreated          = "WEBENTITY_CREATED"
	OnboardingStepContextCreated            = "CONTEXT_CREATED"
	OnboardingStepFinalised                 = "FINALISED"
	OnboardingStepSiteIntelligenceTriggered = "SITE_INTELLIGENCE_TRIGGERED"
	OnboardingStepsSiteIntelligenceDone     = "SITE_INTELLIGENCE_DONE"
	OnboardingStepsSchedulingDone           = "SCHEDULING_DONE"
	// OnboardingStepFailed reports a permanently failed async analysis chain
	// (onboarding_error set, no competitors yet). The frontend routes the user
	// back to the onboarding form; re-submitting clears the marker and
	// re-dispatches the chain.
	OnboardingStepFailed = "ONBOARDING_FAILED"
)

type Country struct {
	Name string `json:"name"`
}

type OnboardingStepsResponse struct {
	Step      string     `json:"step"`
	WebEntity *WebEntity `json:"webEntity"`
	Countries []Country  `json:"countries,omitempty"`
	// Analysis reports live progress of the async onboarding analysis chain.
	// Present only while Step == WEBENTITY_CREATED (the /onboarding/analyzing
	// screen); built from the WebEntity already loaded for step selection, so
	// it costs no extra reads on the polling path.
	Analysis *AnalysisProgress `json:"analysis,omitempty"`
	// SiteIntelligence reports live progress of the SIE pipeline. Present when
	// the step derivation had a WebEntityContext in hand (SITE_INTELLIGENCE_TRIGGERED
	// and later); the /onboarding/strategy screen renders it directly.
	SiteIntelligence *SiteIntelligenceProgress `json:"siteIntelligence,omitempty"`
}

// AnalysisProgress is the per-poll progress payload for the analyzing screen.
// The onboarding analysis chain has exactly two persisted transitions (business
// context landed, competitors landed); the second flips the step itself, so the
// only in-progress signal to expose is the context write plus the display
// fields it carries.
type AnalysisProgress struct {
	WebsiteRead  bool   `json:"websiteRead"`
	BusinessName string `json:"businessName,omitempty"`
	ProductType  string `json:"productType,omitempty"`
	BrandVoice   string `json:"brandVoice,omitempty"`
}

// AnalysisProgressFromWebEntity derives the analyzing-screen progress from a
// WebEntity already in memory. WebsiteRead flips when the extraction LLM's
// single context write lands (see ProcessOnboardedUser).
func AnalysisProgressFromWebEntity(m *models.WebEntity) *AnalysisProgress {
	p := &AnalysisProgress{}
	bc := m.BusinessContext
	if bc == nil {
		return p
	}
	p.WebsiteRead = true
	if bc.BusinessName != nil {
		p.BusinessName = *bc.BusinessName
	}
	if bc.ProductType != nil {
		p.ProductType = *bc.ProductType
	}
	if bc.BrandVoiceSignals != nil {
		p.BrandVoice = *bc.BrandVoiceSignals
	}
	return p
}

// SIEDataFetchProgress mirrors models.SIEDataFetchStepStatus. The fetches fan
// out in parallel, so the frontend renders them as independently completing
// rows rather than a sequence. (The domain rating is fetched during onboarding
// now, not as an SIE data-fetch step, so it is no longer reported here.)
type SIEDataFetchProgress struct {
	UserKeywordsDone        bool `json:"userKeywordsDone"`
	CompetitorKeywordsDone  bool `json:"competitorKeywordsDone"`
	PreExpandedKeywordsDone bool `json:"preExpandedKeywordsDone"`
	ExpandedKeywordsDone    bool `json:"expandedKeywordsDone"`
}

// SiteIntelligenceProgress is the per-poll progress payload for the strategy
// screen — counters and stage markers only, never the raw keyword pools.
type SiteIntelligenceProgress struct {
	// Status is wec.EffectiveStatus(): the SIE status ladder (0..9) with the
	// out-of-band error sentinel resolved to the furthest real stage, so the
	// frontend ladder never rewinds while a run retries.
	Status               int                  `json:"status"`
	DataFetch            SIEDataFetchProgress `json:"dataFetch"`
	CompetitorsProcessed int                  `json:"competitorsProcessed"`
	CompetitorsTotal     int                  `json:"competitorsTotal"`
	KeywordsCollected    int                  `json:"keywordsCollected"`
	FunnelProcessed      int                  `json:"funnelProcessed"`
	FunnelTotal          int                  `json:"funnelTotal"`
	ClusterCount         int                  `json:"clusterCount"`
	ArticlesScheduled    int                  `json:"articlesScheduled"`
	// ErroredRetrying reports the raw error sentinel: the run hit a hard error
	// and will be resumed by a subsequent poll's Orchestrate call.
	ErroredRetrying bool `json:"erroredRetrying"`
}

// SiteIntelligenceProgressFromWEC derives the strategy-screen progress from a
// WebEntityContext already in memory. Keyword pool lengths are summed here so
// the raw arrays never leave the server.
func SiteIntelligenceProgressFromWEC(wec *models.WebEntityContext) *SiteIntelligenceProgress {
	p := &SiteIntelligenceProgress{
		Status:               wec.EffectiveStatus(),
		CompetitorsProcessed: wec.CompetitorKeywordsProcessed,
		CompetitorsTotal:     wec.TotalCompetitors,
		KeywordsCollected:    len(wec.Keywords.UserKeyWords) + len(wec.Keywords.CompetitorKeyWords) + len(wec.Keywords.ExpandedKeyWords),
		FunnelProcessed:      wec.ProcessMetadata.FunnelClassificationMetadata.FunnelClassificationProcessed,
		FunnelTotal:          wec.ProcessMetadata.FunnelClassificationMetadata.FunnelClassificationTotal,
		ClusterCount:         len(wec.Clusters),
		ArticlesScheduled:    wec.ProcessMetadata.SchedulingMetadata.Total,
		ErroredRetrying:      wec.Status == models.SIEStatusError,
	}
	if s := wec.ProcessMetadata.DataFetchStepStatus; s != nil {
		p.DataFetch = SIEDataFetchProgress{
			UserKeywordsDone:        s.UserKeywordsDone,
			CompetitorKeywordsDone:  s.CompetitorKeywordsDone,
			PreExpandedKeywordsDone: s.PreExpandedKeywordsDone,
			ExpandedKeywordsDone:    s.ExpandedKeywordsDone,
		}
	}
	return p
}
