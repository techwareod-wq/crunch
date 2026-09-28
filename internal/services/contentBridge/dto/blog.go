package dto

import "encoding/json"

// BlogSchema is the neutral, per-user description of a CMS collection's writable
// fields. Produced by GetBlogStructure (and internally by Post). The adapter
// maps app field keys onto these remote field IDs at publish time.
type BlogSchema struct {
	Platform     string        `json:"platform"`
	CollectionID string        `json:"collectionId"`
	Fields       []SchemaField `json:"fields"`

	// Source records how the schema was discovered (Payload: "graphql"
	// introspection, its only path — an unreadable schema fails the publish
	// rather than falling back to guessed field names). Empty for platforms
	// with a single discovery path (Framer).
	Source string `json:"source,omitempty"`
	// DraftsEnabled reports whether the collection supports a draft state
	// (Payload: versions+drafts on the collection config). When false, every
	// push is instantly live regardless of the post's draft flag — surfaced
	// to the review UI as a warning, never a block.
	DraftsEnabled bool `json:"draftsEnabled,omitempty"`
	// MediaCollection is the collection article images are uploaded into
	// (Payload: auto-detected from the upload field's relationTo, falling
	// back to "media").
	MediaCollection string `json:"mediaCollection,omitempty"`
}

// CollectionSummary is one CMS collection a platform reports. Returned by
// ListCollections so the onboarding UI can offer a picker instead of making
// the user hand-copy a collection id.
type CollectionSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SchemaField struct {
	RemoteID string `json:"remoteId"` // platform field id used on writes
	Name     string `json:"name"`     // human label / slug
	Type     string `json:"type"`     // normalized: string|richText|image|date|boolean|...
	Required bool   `json:"required"`

	// RelationTo is the target collection slug of a relationship/upload field
	// (Payload). Empty for scalar fields and platforms without relations.
	RelationTo string `json:"relationTo,omitempty"`
	// HasMany marks a relationship that stores an array of ids.
	HasMany bool `json:"hasMany,omitempty"`
	// Children carries the sub-fields of group/array fields (Payload: the SEO
	// plugin's meta group, FAQ arrays). Nil for flat fields.
	Children []SchemaField `json:"children,omitempty"`
}

// BlogPost is the neutral article representation handed to PostBlogStructure.
// All conversion (markdown->html, s3 key->absolute url) happens BEFORE this is
// built — adapters receive ready-to-map values.
type BlogPost struct {
	Title           string          `json:"title"`
	Slug            string          `json:"slug"`
	BodyHTML        string          `json:"bodyHtml"` // STUB: md -> html upstream
	MetaTitle       string          `json:"metaTitle"`
	MetaDescription string          `json:"metaDescription"`
	Excerpt         string          `json:"excerpt"`
	ThumbnailURL    string          `json:"thumbnailUrl"` // STUB: s3 key -> abs url upstream
	ThumbnailAlt    string          `json:"thumbnailAlt"` // alt text for the standalone thumbnail image
	ImageURLs       []string        `json:"imageUrls"`
	SchemaJSONLD    json.RawMessage `json:"schemaJsonLd,omitempty"`

	// PublishDate is the article's scheduled calendar date (RFC3339, 00:00 UTC of
	// the day) that maps onto the platform's date field. Empty when the caller has
	// no scheduled slot (the manual publish path) — the sidecar then falls back to
	// the current time, preserving the pre-existing behaviour.
	PublishDate string `json:"publishDate,omitempty"`

	// RemoteItemID, when non-empty, makes the publish an idempotent UPDATE of an
	// already-created CMS item rather than a fresh create. Populated from the
	// persisted CGEPublishState by the async handler.
	RemoteItemID string `json:"remoteItemId,omitempty"`
	// Draft controls the Framer CMS item's draft flag. true = push as a draft
	// (not live). Set from the resolved live value (Draft = !live); the sidecar
	// adapter also derives PublishSite from it (site publish runs only when
	// live). This is the single negation in the whole stack.
	Draft bool `json:"draft,omitempty"`

	// BodyMarkdown is the raw article markdown, alongside BodyHTML. Platforms
	// with structured rich text (Payload: Lexical) serialize from markdown in
	// the adapter; HTML-based platforms (Framer) keep using BodyHTML.
	BodyMarkdown string `json:"bodyMarkdown,omitempty"`
	// FocusKeyword is the triggering keyword's text, mapped onto an SEO focus
	// keyword field when the collection has one.
	FocusKeyword string `json:"focusKeyword,omitempty"`
	// FAQ carries the article's FAQ pairs (parsed from the generated FAQPage
	// JSON-LD), mapped onto an FAQ array field when the collection has one.
	FAQ []FAQItem `json:"faq,omitempty"`
}

// FAQItem is one question/answer pair for platforms with a structured FAQ
// field.
type FAQItem struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// PublishResult is what an adapter returns after a successful create/update.
type PublishResult struct {
	RemoteItemID string `json:"remoteItemId"`
	RemoteURL    string `json:"remoteUrl"`
	Updated      bool   `json:"updated"` // true if it was a PUT (update), false if POST (create)
	// SitePublished reports whether the platform also ran a full site publish
	// (Framer: item upserts only go live after a site publish, controlled by
	// the publishSite flag — default on).
	SitePublished bool `json:"sitePublished"`
}
