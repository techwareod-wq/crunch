package prompts

import _ "embed"

// One-shot synthesis templates. None is prompt-cached: each runs once per
// wizard run with fully dynamic content, so there is no reusable prefix.
//
// The image synthesis template is split per position to mirror the generation
// templates' text contract: thumbnails are strictly text-free, mid-article
// images may learn typography/label/diagram habits from the references.

//go:embed toneStructureSynthesis.md
var ToneStructureSynthesis string

//go:embed thumbnailStyleSynthesis.md
var ThumbnailStyleSynthesis string

//go:embed midArticleStyleSynthesis.md
var MidArticleStyleSynthesis string
