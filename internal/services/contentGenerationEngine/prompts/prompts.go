package prompts

import _ "embed"

//go:embed serpGapAnalysis.md
var SerpGapAnalysis string

//go:embed youtubeSummary.md
var YouTubeSummary string

// The outline and article templates are split in two so the static
// instructions can ride a prompt-cache breakpoint: the Instructions half is
// sent verbatim (never through the template engine — placeholders like
// {{IMAGE_THUMBNAIL}} are literal text there) and must stay byte-identical
// across calls to hit the cache; the Data half carries all per-article
// template fields and is rendered per call.

//go:embed outlineGenerationInstructions.md
var OutlineGenerationInstructions string

//go:embed outlineGenerationData.md
var OutlineGenerationData string

//go:embed articleGenerationInstructions.md
var ArticleGenerationInstructions string

//go:embed articleGenerationData.md
var ArticleGenerationData string

//go:embed internalLinkInsertion.md
var InternalLinkInsertion string

// Section regeneration follows the same split: the Instructions half is sent
// verbatim with a cache breakpoint ({{IMAGE_*}} tokens are literal text
// there), the Data half is rendered per call.

//go:embed sectionRegenerationInstructions.md
var SectionRegenerationInstructions string

//go:embed sectionRegenerationData.md
var SectionRegenerationData string

//go:embed thumbnailImagePrompt.md
var ThumbnailImagePrompt string

//go:embed thumbnailImagePromptBlueprint.md
var ThumbnailImagePromptBlueprint string

//go:embed midArticleImagePrompt.md
var MidArticleImagePrompt string

// Learned-style image templates (style replication): the Style-rules section
// is the entity's learned {{.ImageStyle}} block plus fixed safety lines the
// synthesis prompt cannot remove. Selected when the master context carries an
// ImageStylePrompt and (for thumbnails) no explicit catalog pick won.

//go:embed customThumbnailImagePrompt.md
var CustomThumbnailImagePrompt string

//go:embed customMidArticleImagePrompt.md
var CustomMidArticleImagePrompt string

//go:embed schemaGeneration.md
var SchemaGeneration string

//go:embed metaAssetsGeneration.md
var MetaAssetsGeneration string
