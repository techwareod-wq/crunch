package dto

import (
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
)

func TestPublishStateFromDbModel_MapsLive(t *testing.T) {
	cases := []struct {
		name string
		live bool
	}{
		{"live push", true},
		{"draft push", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := PublishStateFromDbModel(&models.CGEPublishState{
				Status: models.CGEPublishStatusPublished,
				Live:   c.live,
			})
			if out == nil {
				t.Fatal("expected a non-nil view")
			}
			if out.Live != c.live {
				t.Fatalf("Live = %v, want %v", out.Live, c.live)
			}
		})
	}
}

func TestScheduledArticleFromDbModel_PublishAsLiveIsRawNullable(t *testing.T) {
	live := true
	cases := []struct {
		name  string
		model *bool
	}{
		{"inherit stays nil", nil},
		{"explicit override preserved", &live},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var a ScheduledArticle
			a.FromDbModel(&models.ScheduledArticle{PublishAsLive: c.model}, nil)
			if c.model == nil {
				if a.PublishAsLive != nil {
					t.Fatalf("nil override must stay nil (inherit), got %v", *a.PublishAsLive)
				}
				return
			}
			if a.PublishAsLive == nil || *a.PublishAsLive != *c.model {
				t.Fatalf("override not preserved: got %v", a.PublishAsLive)
			}
		})
	}
}
