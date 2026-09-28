package service

import (
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
)

func strPtr(s string) *string { return &s }

func weWithLearnedStyle(thumbnailStyle *string, thumbLearned, midLearned string) *models.WebEntity {
	we := &models.WebEntity{ThumbnailStyle: thumbnailStyle}
	if thumbLearned != "" || midLearned != "" {
		sr := &models.StyleReplication{}
		if thumbLearned != "" {
			sr.ThumbnailStylePrompt = strPtr(thumbLearned)
		}
		if midLearned != "" {
			sr.MidArticleStylePrompt = strPtr(midLearned)
		}
		we.StyleReplication = sr
	}
	return we
}

func TestResolveImageStyle(t *testing.T) {
	blueprint := strPtr("blueprint")
	invalid := strPtr("does-not-exist")
	empty := strPtr("")

	cases := []struct {
		name          string
		we            *models.WebEntity
		override      *string
		wantThumbnail string
		wantThumb     string
		wantMid       string
	}{
		{
			name:          "override wins over web entity default",
			we:            weWithLearnedStyle(strPtr("editorial"), "", ""),
			override:      blueprint,
			wantThumbnail: "blueprint",
		},
		{
			name:          "nil override inherits web entity default",
			we:            weWithLearnedStyle(strPtr("blueprint"), "", ""),
			override:      nil,
			wantThumbnail: "blueprint",
		},
		{
			name:          "both nil resolve to system default",
			we:            weWithLearnedStyle(nil, "", ""),
			override:      nil,
			wantThumbnail: "editorial",
		},
		{
			name:          "invalid override is ignored, inherits web entity default",
			we:            weWithLearnedStyle(strPtr("blueprint"), "", ""),
			override:      invalid,
			wantThumbnail: "blueprint",
		},
		{
			name:          "empty override is ignored, falls back to system default",
			we:            weWithLearnedStyle(nil, "", ""),
			override:      empty,
			wantThumbnail: "editorial",
		},
		{
			name:          "invalid web entity default falls back to system default",
			we:            weWithLearnedStyle(strPtr("gone"), "", ""),
			override:      nil,
			wantThumbnail: "editorial",
		},
		{
			name:          "learned thumbnail beats entity default: empty id + learned prompt",
			we:            weWithLearnedStyle(strPtr("blueprint"), "- Pastel thumbs.", "- Flat mids."),
			override:      nil,
			wantThumbnail: "",
			wantThumb:     "- Pastel thumbs.",
			wantMid:       "- Flat mids.",
		},
		{
			name:          "explicit pick beats learned for the thumbnail, both learned prompts still stamp",
			we:            weWithLearnedStyle(nil, "- Pastel thumbs.", "- Flat mids."),
			override:      blueprint,
			wantThumbnail: "blueprint",
			wantThumb:     "- Pastel thumbs.",
			wantMid:       "- Flat mids.",
		},
		{
			name:          "mid-article learned alone: catalog thumbnail keeps working",
			we:            weWithLearnedStyle(strPtr("blueprint"), "", "- Flat mids."),
			override:      nil,
			wantThumbnail: "blueprint",
			wantMid:       "- Flat mids.",
		},
		{
			name:          "thumbnail learned alone: mid-article stays default",
			we:            weWithLearnedStyle(nil, "- Pastel thumbs.", ""),
			override:      nil,
			wantThumbnail: "",
			wantThumb:     "- Pastel thumbs.",
		},
		{
			name:          "invalid pick with learned thumbnail resolves to learned",
			we:            weWithLearnedStyle(strPtr("editorial"), "- Pastel thumbs.", ""),
			override:      invalid,
			wantThumbnail: "",
			wantThumb:     "- Pastel thumbs.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotThumbnail, gotThumb, gotMid := resolveImageStyle(tc.we, tc.override)
			if gotThumbnail != tc.wantThumbnail {
				t.Fatalf("resolveImageStyle() thumbnail id = %q, want %q", gotThumbnail, tc.wantThumbnail)
			}
			if gotThumb != tc.wantThumb {
				t.Fatalf("resolveImageStyle() thumbnail prompt = %q, want %q", gotThumb, tc.wantThumb)
			}
			if gotMid != tc.wantMid {
				t.Fatalf("resolveImageStyle() mid-article prompt = %q, want %q", gotMid, tc.wantMid)
			}
		})
	}
}
