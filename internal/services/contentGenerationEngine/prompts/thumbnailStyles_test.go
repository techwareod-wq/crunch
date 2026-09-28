package prompts

import "testing"

func TestIsValidThumbnailStyle(t *testing.T) {
	if !IsValidThumbnailStyle("editorial") {
		t.Fatalf("expected editorial to be a valid style")
	}
	if !IsValidThumbnailStyle("blueprint") {
		t.Fatalf("expected blueprint to be a valid style")
	}
	if IsValidThumbnailStyle("nope") {
		t.Fatalf("expected unknown id to be invalid")
	}
	if IsValidThumbnailStyle("") {
		t.Fatalf("expected empty id to be invalid")
	}
}

func TestThumbnailTemplateForStyle(t *testing.T) {
	// A known non-default style resolves to its own template.
	if got := ThumbnailTemplateForStyle("blueprint"); got != ThumbnailImagePromptBlueprint {
		t.Fatalf("blueprint did not resolve to its template")
	}
	// The default style resolves to the editorial template.
	if got := ThumbnailTemplateForStyle(DefaultThumbnailStyleID); got != ThumbnailImagePrompt {
		t.Fatalf("default style did not resolve to the editorial template")
	}
	// Unknown and empty IDs fall back to the default (editorial) template.
	if got := ThumbnailTemplateForStyle("nope"); got != ThumbnailImagePrompt {
		t.Fatalf("unknown id did not fall back to the default template")
	}
	if got := ThumbnailTemplateForStyle(""); got != ThumbnailImagePrompt {
		t.Fatalf("empty id did not fall back to the default template")
	}
}

func TestDefaultThumbnailStyleIsInCatalog(t *testing.T) {
	if !IsValidThumbnailStyle(DefaultThumbnailStyleID) {
		t.Fatalf("DefaultThumbnailStyleID %q is not present in the catalog", DefaultThumbnailStyleID)
	}
}
