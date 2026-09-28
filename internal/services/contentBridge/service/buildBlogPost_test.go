package service

import (
	"strings"
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
)

func fakeResolve(key string) string {
	if key == "" {
		return ""
	}
	return "https://cdn.example/" + key
}

func TestBuildBlogPostMapsAllFields(t *testing.T) {
	mc := &models.WebEntityMasterContext{
		ProposedTitle:  "How to Bake Bread",
		ArticleContent: "# Intro\n\nMix **flour** and water.",
		MetaAssets: &models.CGEMetaAssets{
			MetaTitle:       "Bake Bread (SEO)",
			MetaDescription: "A meta description",
			SocialExcerpt:   "A short excerpt",
		},
		Images: []models.CGEImage{
			{Position: "mid-article", S3Key: "mid.png"},
			{Position: "thumbnail", S3Key: "thumb.png", Alt: "loaf"},
		},
		Publish: &models.CGEPublishState{RemoteItemID: "item-7"},
	}

	scheduleDate := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	post := buildBlogPost(mc, "How to Bake Bread (edited)", scheduleDate, "bake bread", fakeResolve)

	if post.Title != "How to Bake Bread (edited)" {
		t.Fatalf("Title = %q, want the ScheduledArticle title", post.Title)
	}
	if post.PublishDate != "2026-07-18T00:00:00Z" {
		t.Fatalf("PublishDate = %q, want the scheduled calendar date in RFC3339", post.PublishDate)
	}
	if !strings.Contains(post.BodyHTML, "<h1>Intro</h1>") || !strings.Contains(post.BodyHTML, "<strong>flour</strong>") {
		t.Fatalf("BodyHTML not rendered from markdown: %q", post.BodyHTML)
	}
	if post.MetaTitle != "Bake Bread (SEO)" {
		t.Fatalf("MetaTitle = %q", post.MetaTitle)
	}
	if post.MetaDescription != "A meta description" {
		t.Fatalf("MetaDescription = %q", post.MetaDescription)
	}
	if post.Excerpt != "A short excerpt" {
		t.Fatalf("Excerpt = %q", post.Excerpt)
	}
	if post.ThumbnailURL != "https://cdn.example/thumb.png" {
		t.Fatalf("ThumbnailURL = %q, want the thumbnail-position image resolved", post.ThumbnailURL)
	}
	if post.ThumbnailAlt != "loaf" {
		t.Fatalf("ThumbnailAlt = %q, want the thumbnail-position image's alt text", post.ThumbnailAlt)
	}
	if post.RemoteItemID != "item-7" {
		t.Fatalf("RemoteItemID = %q", post.RemoteItemID)
	}
}

func TestFormatPublishDate(t *testing.T) {
	if got := formatPublishDate(time.Time{}); got != "" {
		t.Fatalf("formatPublishDate(zero) = %q, want empty so the sidecar falls back to now", got)
	}
	// A non-UTC input is normalised to UTC before formatting.
	loc := time.FixedZone("IST", 5*3600+1800) // +05:30
	in := time.Date(2026, 7, 18, 2, 0, 0, 0, loc)
	if got := formatPublishDate(in); got != "2026-07-17T20:30:00Z" {
		t.Fatalf("formatPublishDate(non-UTC) = %q, want the UTC RFC3339 rendering", got)
	}
}

func TestEffectiveContentPrecedence(t *testing.T) {
	tests := []struct {
		name string
		mc   *models.WebEntityMasterContext
		want string
	}{
		{
			name: "edited wins",
			mc: &models.WebEntityMasterContext{
				Edited:              &models.CGEEdited{ArticleContent: "edited body"},
				FinalArticleContent: "final body",
				ArticleContent:      "pipeline body",
			},
			want: "edited body",
		},
		{
			name: "edited-but-empty still wins (user cleared it)",
			mc: &models.WebEntityMasterContext{
				Edited:         &models.CGEEdited{ArticleContent: ""},
				ArticleContent: "pipeline body",
			},
			want: "",
		},
		{
			name: "final over pipeline when no edit",
			mc: &models.WebEntityMasterContext{
				FinalArticleContent: "final body",
				ArticleContent:      "pipeline body",
			},
			want: "final body",
		},
		{
			name: "pipeline fallback",
			mc:   &models.WebEntityMasterContext{ArticleContent: "pipeline body"},
			want: "pipeline body",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveContent(tt.mc); got != tt.want {
				t.Fatalf("effectiveContent = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEffectiveSlugPrecedence(t *testing.T) {
	tests := []struct {
		name string
		mc   *models.WebEntityMasterContext
		want string
	}{
		{
			name: "edited slug wins",
			mc: &models.WebEntityMasterContext{
				Edited:  &models.CGEEdited{URLSlug: "user-edited-slug"},
				Outline: &models.CGEOutline{URLSlug: "outline-slug"},
			},
			want: "user-edited-slug",
		},
		{
			name: "falls back to outline slug when edit is empty",
			mc: &models.WebEntityMasterContext{
				Edited:  &models.CGEEdited{URLSlug: ""},
				Outline: &models.CGEOutline{URLSlug: "outline-slug"},
			},
			want: "outline-slug",
		},
		{
			name: "outline slug when no edit block",
			mc: &models.WebEntityMasterContext{
				Outline: &models.CGEOutline{URLSlug: "outline-slug"},
			},
			want: "outline-slug",
		},
		{
			name: "outline slug parsed from RawJSON (legacy)",
			mc: &models.WebEntityMasterContext{
				Outline: &models.CGEOutline{RawJSON: `{"url_slug":"legacy-slug"}`},
			},
			want: "legacy-slug",
		},
		{
			name: "empty when nothing set",
			mc:   &models.WebEntityMasterContext{},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveSlug(tt.mc); got != tt.want {
				t.Fatalf("effectiveSlug = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestThumbnailNoThumbnail(t *testing.T) {
	mc := &models.WebEntityMasterContext{
		Images: []models.CGEImage{{Position: "mid-article", S3Key: "mid.png"}},
	}
	if url, alt := thumbnail(mc, fakeResolve); url != "" || alt != "" {
		t.Fatalf("thumbnail = (%q, %q), want empty when no thumbnail-position image", url, alt)
	}
}

func TestMarkdownToHTMLEmpty(t *testing.T) {
	if got := markdownToHTML(""); got != "" {
		t.Fatalf("markdownToHTML(\"\") = %q, want empty", got)
	}
}

func TestBuildBlogPostPayloadExtras(t *testing.T) {
	mc := &models.WebEntityMasterContext{
		ProposedTitle:  "T",
		ArticleContent: "# Intro\n\nBody.",
		SchemaMarkup: &models.CGESchemaMarkup{
			FAQSchema: []byte(`{
				"@type": "FAQPage",
				"mainEntity": [
					{"@type": "Question", "name": "How long?", "acceptedAnswer": {"@type": "Answer", "text": "About an hour."}},
					{"@type": "Question", "name": "", "acceptedAnswer": {"@type": "Answer", "text": "dropped: empty question"}}
				]
			}`),
		},
	}

	post := buildBlogPost(mc, "", time.Time{}, "bake bread", fakeResolve)

	if post.BodyMarkdown != "# Intro\n\nBody." {
		t.Fatalf("BodyMarkdown = %q, want the raw markdown alongside BodyHTML", post.BodyMarkdown)
	}
	if post.FocusKeyword != "bake bread" {
		t.Fatalf("FocusKeyword = %q", post.FocusKeyword)
	}
	if len(post.FAQ) != 1 || post.FAQ[0].Question != "How long?" || post.FAQ[0].Answer != "About an hour." {
		t.Fatalf("FAQ = %+v, want the one complete pair (incomplete entries dropped)", post.FAQ)
	}
}

func TestParseFAQDefensive(t *testing.T) {
	cases := []struct {
		name string
		sm   *models.CGESchemaMarkup
	}{
		{"nil schema markup", nil},
		{"empty faq schema", &models.CGESchemaMarkup{}},
		{"malformed json", &models.CGESchemaMarkup{FAQSchema: []byte(`{not json`)}},
		{"wrong shape", &models.CGESchemaMarkup{FAQSchema: []byte(`{"mainEntity": "nope"}`)}},
		{"no complete pairs", &models.CGESchemaMarkup{FAQSchema: []byte(`{"mainEntity": [{"name": "q only"}]}`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseFAQ(tc.sm); got != nil {
				t.Fatalf("parseFAQ = %+v, want nil (FAQ is optional, never an error)", got)
			}
		})
	}
}
