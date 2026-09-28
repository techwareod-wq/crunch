package prompts

// ThumbnailStyleDef describes one selectable thumbnail visual style. The catalog
// is the single source of truth for both the generation pipeline (Template) and
// the pickers the frontend renders (Name / Description / Swatch). Adding a style
// is a backend-only change: register the embedded template and append an entry.
type ThumbnailStyleDef struct {
	ID          string   // stored value, e.g. "editorial"
	Name        string   // display name
	Description string   // one-liner for pickers
	Swatch      []string // hex colors for the settings card
	Template    string   // embedded thumbnail prompt template
}

// DefaultThumbnailStyleID is the system default resolved when nothing overrides
// it (nil web-entity default, nil per-article override) and the fallback for any
// unknown / since-removed ID at read time. It is today's behaviour.
const DefaultThumbnailStyleID = "editorial"

// ThumbnailStyleCatalog is the curated set of thumbnail styles. Order is the
// display order in the settings picker.
var ThumbnailStyleCatalog = []ThumbnailStyleDef{
	{
		ID:          "editorial",
		Name:        "Editorial",
		Description: "Warm cream background with orange and black abstract accents.",
		Swatch:      []string{"#F7F3EA", "#E8631A", "#111111"},
		Template:    ThumbnailImagePrompt,
	},
	{
		ID:          "blueprint",
		Name:        "Blueprint",
		Description: "Thin white linework on deep navy — technical drafting aesthetic.",
		Swatch:      []string{"#0E1A4A", "#2A4BC0", "#FFFFFF"},
		Template:    ThumbnailImagePromptBlueprint,
	},
}

// IsValidThumbnailStyle reports whether id is a known catalog style. Used at
// write time to reject unknown IDs before they are persisted.
func IsValidThumbnailStyle(id string) bool {
	for _, s := range ThumbnailStyleCatalog {
		if s.ID == id {
			return true
		}
	}
	return false
}

// ThumbnailTemplateForStyle returns the prompt template for a style ID, falling
// back to the default style's template for an empty or unknown ID. This is the
// read-time guard: styles removed from the catalog, or master contexts created
// before the field existed (empty ID), resolve to the default template.
func ThumbnailTemplateForStyle(id string) string {
	for _, s := range ThumbnailStyleCatalog {
		if s.ID == id {
			return s.Template
		}
	}
	return defaultThumbnailTemplate()
}

// defaultThumbnailTemplate returns the template for DefaultThumbnailStyleID.
func defaultThumbnailTemplate() string {
	for _, s := range ThumbnailStyleCatalog {
		if s.ID == DefaultThumbnailStyleID {
			return s.Template
		}
	}
	// DefaultThumbnailStyleID is always present in the catalog; this is a
	// defensive fallback so a mis-edit can never return an empty prompt.
	return ThumbnailImagePrompt
}
