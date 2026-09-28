package service

import (
	"context"
	"errors"
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/scheduledArticleService"
)

// SaveDraft settles the pending-regen set; the shared fakeStore records the
// clear so regen-related saves can assert it (and non-regen saves don't panic
// on the embedded-interface call).
func (f *fakeStore) ClearPendingRegen(context.Context, string) error {
	f.clearedPendingRegen = true
	return nil
}

// A key from the server-side regeneration namespace commits through SaveDraft
// exactly like a user-uploaded replacement: swap, body URL rewrite, old object
// orphaned for deletion.
func TestSaveDraft_AcceptsRegeneratedImageKey(t *testing.T) {
	oldKey := "generated_images/old.png"
	oldURL := "https://" + testBucket + ".s3.amazonaws.com/" + oldKey
	s, st, s3 := newFixture(
		[]models.CGEImage{{Position: "thumbnail", S3Key: oldKey, Alt: "old alt"}},
		"intro\n<img src=\""+oldURL+"\" alt=\"old alt\" />\noutro",
	)

	newKey := models.RegeneratedImageKeyPrefix(userID(st), mcID(st)) + "-thumbnail-123.png"
	err := s.SaveDraft(context.Background(), userID(st), "sa1", scheduledArticleService.SaveDraftPayload{
		ArticleContent: "intro\n<img src=\"" + oldURL + "\" alt=\"old alt\" />\noutro",
		Images:         []scheduledArticleService.SaveDraftImage{{Position: "thumbnail", Alt: "regen alt", NewS3Key: newKey}},
	})
	if err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}

	img := st.gotWEMCUpdate.Images[0]
	if img.S3Key != newKey || img.Alt != "regen alt" {
		t.Fatalf("stored image not swapped to regen key: %+v", img)
	}
	if len(s3.deleted) != 1 || s3.deleted[0] != oldKey {
		t.Fatalf("replaced object not orphan-deleted: %v", s3.deleted)
	}
}

// A regen-shaped key for another user/article is still rejected.
func TestSaveDraft_RejectsForeignRegeneratedImageKey(t *testing.T) {
	s, st, _ := newFixture(
		[]models.CGEImage{{Position: "thumbnail", S3Key: "generated_images/old.png", Alt: "a"}},
		"body",
	)

	foreign := models.RegeneratedImageKeyPrefix("otheruser", "othermc") + "-thumbnail-123.png"
	err := s.SaveDraft(context.Background(), userID(st), "sa1", scheduledArticleService.SaveDraftPayload{
		ArticleContent: "body",
		Images:         []scheduledArticleService.SaveDraftImage{{Position: "thumbnail", Alt: "a", NewS3Key: foreign}},
	})
	if !errors.Is(err, scheduledArticleService.ErrInvalidImageKey) {
		t.Fatalf("err = %v, want ErrInvalidImageKey", err)
	}
}
