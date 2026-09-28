package models

import "testing"

func boolPtr(b bool) *bool  { return &b }
func sPtr(s string) *string { return &s }

// The applied getters are the single seam every generation consumer reads
// through — these tests pin the toggle semantics: absent Apply (or field) =
// applied, explicit false silences the artifact WITHOUT touching its text.
func TestStyleReplicationApplyGating(t *testing.T) {
	full := func(apply *StyleApplySelection) *StyleReplication {
		return &StyleReplication{
			ToneProfile:           sPtr("tone"),
			StructurePattern:      sPtr("structure"),
			TitlePattern:          sPtr("titles"),
			ThumbnailStylePrompt:  sPtr("thumbs"),
			MidArticleStylePrompt: sPtr("mids"),
			Apply:                 apply,
		}
	}

	// nil Apply → everything applied.
	sr := full(nil)
	if sr.Tone() != "tone" || sr.Structure() != "structure" || sr.TitleStyle() != "titles" ||
		sr.ThumbnailImageStyle() != "thumbs" || sr.MidArticleImageStyle() != "mids" {
		t.Fatalf("nil Apply must leave every artifact applied")
	}

	// Empty Apply struct → still everything applied (absent fields default on).
	sr = full(&StyleApplySelection{})
	if sr.Tone() != "tone" || sr.MidArticleImageStyle() != "mids" {
		t.Fatalf("empty Apply must leave every artifact applied")
	}

	// Each toggle silences exactly its own artifact.
	sr = full(&StyleApplySelection{ToneProfile: boolPtr(false)})
	if sr.Tone() != "" {
		t.Fatalf("disabled tone must read empty")
	}
	if sr.Structure() != "structure" || sr.TitleStyle() != "titles" {
		t.Fatalf("disabling tone must not touch other artifacts")
	}

	sr = full(&StyleApplySelection{ThumbnailStyle: boolPtr(false), MidArticleStyle: boolPtr(false)})
	if sr.ThumbnailImageStyle() != "" || sr.MidArticleImageStyle() != "" {
		t.Fatalf("disabled image styles must read empty")
	}
	if sr.Tone() != "tone" {
		t.Fatalf("disabling image styles must not touch tone")
	}

	// Disabled artifact keeps its raw text (the editor's contract).
	sr = full(&StyleApplySelection{ToneProfile: boolPtr(false)})
	if sr.ToneProfile == nil || *sr.ToneProfile != "tone" {
		t.Fatalf("disabling must never clear the stored text")
	}

	// Explicit true behaves like absent.
	sr = full(&StyleApplySelection{ToneProfile: boolPtr(true)})
	if sr.Tone() != "tone" {
		t.Fatalf("explicit true must apply")
	}
}

// The five-granular learn selection drives what a run scrapes and which
// artifacts synthesis may write — pin the derived helpers.
func TestStyleLearnSelectionHelpers(t *testing.T) {
	none := StyleLearnSelection{}
	if none.Any() || none.Articles() || none.Images() {
		t.Fatalf("empty selection must learn nothing")
	}

	titleOnly := StyleLearnSelection{TitlePattern: true}
	if !titleOnly.Articles() || titleOnly.Images() || !titleOnly.Any() {
		t.Fatalf("title-only must need article text and no images")
	}

	midOnly := StyleLearnSelection{MidArticleStyle: true}
	if midOnly.Articles() || !midOnly.Images() {
		t.Fatalf("mid-only must need images and no article text")
	}
	if midOnly.WantsPosition(ImagePositionThumbnail) || !midOnly.WantsPosition(ImagePositionMidArticle) {
		t.Fatalf("mid-only must want exactly the mid-article bucket")
	}
	if midOnly.WantsPosition("banner") {
		t.Fatalf("unknown positions are never wanted")
	}

	thumbOnly := StyleLearnSelection{ThumbnailStyle: true}
	if !thumbOnly.WantsPosition(ImagePositionThumbnail) || thumbOnly.WantsPosition(ImagePositionMidArticle) {
		t.Fatalf("thumb-only must want exactly the thumbnail bucket")
	}
}

// Title fallback: turning title styling off stops styled titles ENTIRELY —
// the tone fallback must not sneak style back into title calls.
func TestTitleToneFallbackRequiresBothToggles(t *testing.T) {
	sr := &StyleReplication{
		ToneProfile: sPtr("tone"),
		Apply:       &StyleApplySelection{TitlePattern: boolPtr(false)},
	}
	if sr.TitleToneFallback() != "" {
		t.Fatalf("title toggle off must silence the tone fallback too")
	}

	sr.Apply = &StyleApplySelection{ToneProfile: boolPtr(false)}
	if sr.TitleToneFallback() != "" {
		t.Fatalf("tone toggle off must silence the fallback")
	}

	sr.Apply = nil
	if sr.TitleToneFallback() != "tone" {
		t.Fatalf("both toggles on must surface the tone fallback")
	}

	// Nil-safe.
	var nilSR *StyleReplication
	if nilSR.TitleToneFallback() != "" {
		t.Fatalf("nil profile must fall back to empty")
	}
}
