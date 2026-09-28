package constants

type PublishPlatform string

const (
	PlatformFramer  PublishPlatform = "framer"
	PlatformPayload PublishPlatform = "payload"
	PlatformManual  PublishPlatform = "manual"
)

type PublishMode string

const (
	ModeReview PublishMode = "review"
	ModeAuto   PublishMode = "auto"
)

type Cadence int

const (
	Cadence3  Cadence = 3
	Cadence5  Cadence = 5
	Cadence10 Cadence = 10
	Cadence15 Cadence = 15
)

// AllPublishModes / AllCadences enumerate every valid value. Order is the
// order shown in the UI.
var AllPublishModes = []PublishMode{ModeReview, ModeAuto}

var AllCadences = []Cadence{Cadence3, Cadence5, Cadence10, Cadence15}

// ApiKeyField describes the credential input shown for a platform. Omitted
// when the platform requires no key (e.g. manual copy & paste).
//
// Deprecated: kept populated during rollout for older frontends; new clients
// render CredentialFields, which can describe more than one input.
type ApiKeyField struct {
	Label       string `json:"label"`
	Type        string `json:"type,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	HelpHref    string `json:"helpHref,omitempty"`
}

// CredentialField describes one credential input the platform needs. IDs map
// 1:1 onto PublishingConfig json fields (apiKey, projectUrl, collectionId) so
// the frontend can assemble the PATCH payload generically.
type CredentialField struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Type        string `json:"type,omitempty"` // "password" | "url" | "text"
	Placeholder string `json:"placeholder,omitempty"`
	HelpHref    string `json:"helpHref,omitempty"`
	Required    bool   `json:"required"`
}

type PlatformOption struct {
	ID                  PublishPlatform   `json:"id"`
	Title               string            `json:"title"`
	Hint                string            `json:"hint"`
	SupportsAutoPublish bool              `json:"supportsAutoPublish"`
	ApiKeyField         *ApiKeyField      `json:"apiKeyField,omitempty"`
	CredentialFields    []CredentialField `json:"credentialFields,omitempty"`
	EmptyHelp           string            `json:"emptyHelp,omitempty"`
}

// PublishModeOption pairs a mode id with its UI labels. RequiresAutoSupport
// marks modes that are only selectable when the chosen platform's
// SupportsAutoPublish is true — keeps the gating rule on the backend so the
// frontend doesn't have to hardcode which mode id means "auto".
type PublishModeOption struct {
	ID                  PublishMode `json:"id"`
	Title               string      `json:"title"`
	Description         string      `json:"description"`
	RequiresAutoSupport bool        `json:"requiresAutoSupport"`
}

type CadenceOption struct {
	Value Cadence `json:"value"`
	Label string  `json:"label"`
}

// PlatformOptions is the single source of truth for publishing destinations.
// To add a new destination: append a constant above and an entry here.
var PlatformOptions = []PlatformOption{
	{
		ID:                  PlatformFramer,
		Title:               "Framer",
		Hint:                "Auto-publish via API",
		SupportsAutoPublish: true,
		// ApiKeyField kept during rollout; CredentialFields is authoritative.
		ApiKeyField: &ApiKeyField{
			Label:       "API token",
			Type:        "password",
			Placeholder: "••••••••••••••••",
			HelpHref:    "#",
		},
		CredentialFields: []CredentialField{
			{
				ID:          "apiKey",
				Label:       "API token",
				Type:        "password",
				Placeholder: "••••••••••••••••",
				HelpHref:    "#",
				Required:    true,
			},
			{
				ID:          "projectUrl",
				Label:       "Project URL",
				Type:        "url",
				Placeholder: "https://framer.com/projects/…",
				Required:    true,
			},
			{
				ID:       "collectionId",
				Label:    "CMS collection ID",
				Type:     "text",
				Required: true,
			},
		},
	},
	{
		ID:                  PlatformPayload,
		Title:               "Payload CMS",
		Hint:                "Auto-publish via API",
		SupportsAutoPublish: true,
		CredentialFields: []CredentialField{
			{
				ID:          "apiKey",
				Label:       "API key",
				Type:        "password",
				Placeholder: "••••••••••••••••",
				HelpHref:    "#",
				Required:    true,
			},
			{
				ID:          "projectUrl",
				Label:       "Payload server URL",
				Type:        "url",
				Placeholder: "https://cms.example.com",
				Required:    true,
			},
			{
				ID:       "collectionId",
				Label:    "Blog collection",
				Type:     "text",
				Required: true,
			},
			{
				ID:          "authCollection",
				Label:       "API-key user collection",
				Type:        "text",
				Placeholder: "users",
				Required:    false,
			},
		},
	},
	{
		ID:                  PlatformManual,
		Title:               "Copy & paste",
		Hint:                "Manual workflow",
		SupportsAutoPublish: false,
		EmptyHelp:           "No setup needed. Articles will be ready to copy from your dashboard when each one is approved.",
	},
}

var PublishModeOptions = []PublishModeOption{
	{ID: ModeReview, Title: "Review before publishing", Description: "I approve each article before it goes live."},
	{ID: ModeAuto, Title: "Auto-publish", Description: "Publish automatically on the scheduled date.", RequiresAutoSupport: true},
}

var CadenceOptions = []CadenceOption{
	{Value: Cadence3, Label: "3 articles per week"},
	{Value: Cadence5, Label: "5 articles per week"},
	{Value: Cadence10, Label: "10 articles per week"},
	{Value: Cadence15, Label: "15 articles per week"},
}

func IsValidPlatform(s string) bool {
	for _, p := range PlatformOptions {
		if string(p.ID) == s {
			return true
		}
	}
	return false
}

func IsValidPublishMode(s string) bool {
	for _, m := range AllPublishModes {
		if string(m) == s {
			return true
		}
	}
	return false
}

// IsValidCadence reports whether n matches one of the canonical cadence values.
// Zero is treated as "unset" and returns true so partial patches don't fail.
func IsValidCadence(n int) bool {
	if n == 0 {
		return true
	}
	for _, c := range AllCadences {
		if int(c) == n {
			return true
		}
	}
	return false
}
