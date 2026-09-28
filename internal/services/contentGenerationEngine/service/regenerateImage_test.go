package service

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/impl/llm"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	cgeStore "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/store"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// --- fakes ---
// Each fake embeds its interface so only the methods the regen path may touch
// are implemented; any other call nil-panics, which doubles as the proof that
// RegenerateImage performs no store writes and no pipeline dispatch.

type regenFakeStore struct {
	cgeStore.Store
	sa *models.ScheduledArticle
	mc *models.WebEntityMasterContext
	kw *models.Keyword

	gotPendingImagePos   string
	gotPendingImageEntry *models.CGEPendingImageRegen
}

func (f *regenFakeStore) GetScheduledArticle(context.Context, string) (bool, *models.ScheduledArticle, error) {
	return f.sa != nil, f.sa, nil
}
func (f *regenFakeStore) GetWebEntityMasterContextByScheduledArticleID(context.Context, string) (bool, *models.WebEntityMasterContext, error) {
	return f.mc != nil, f.mc, nil
}
func (f *regenFakeStore) GetKeyword(context.Context, string) (bool, *models.Keyword, error) {
	return f.kw != nil, f.kw, nil
}
func (f *regenFakeStore) SetPendingImageRegen(_ context.Context, _ string, position string, entry models.CGEPendingImageRegen) error {
	f.gotPendingImagePos = position
	f.gotPendingImageEntry = &entry
	return nil
}

type regenFakeImageGen struct {
	gotPrompt string
	data      []byte
}

func (f *regenFakeImageGen) GenerateImage(_ context.Context, req interfaces.ImageGenRequest) (*interfaces.ImageGenResponse, error) {
	f.gotPrompt = req.Prompt
	return &interfaces.ImageGenResponse{Data: f.data, MimeType: "image/png"}, nil
}

type regenFakeS3 struct {
	interfaces.S3
	uploadedKey    string
	uploadedBucket string
}

func (f *regenFakeS3) UploadFileUsingBytes(_ context.Context, key, bucket string, _ []byte) error {
	f.uploadedKey = key
	f.uploadedBucket = bucket
	return nil
}
func (f *regenFakeS3) GetS3UrlFromBucketAndFileName(bucket, key string) string {
	return "https://" + bucket + ".s3.amazonaws.com/" + key
}

// tinyPNG returns a valid 1x1 PNG so convertToPNG's decode succeeds.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func newRegenFixture(t *testing.T) (*contentGenerationEngineService, *regenFakeStore, *regenFakeImageGen, *regenFakeS3, string) {
	t.Helper()
	userID := primitive.NewObjectID()
	mcID := primitive.NewObjectID()
	st := &regenFakeStore{
		sa: &models.ScheduledArticle{UserID: userID, Title: "How to Test Things"},
		mc: &models.WebEntityMasterContext{
			ID:        mcID,
			UserID:    userID,
			KeywordID: primitive.NewObjectID(),
			ArticleContent: "Intro paragraph about testing.\n\n" +
				"![alt](https://bucket.s3.amazonaws.com/generated_images/old-mid-article.png)\n\n" +
				"## Deep Dive\n\nSection body here.\n",
			Images: []models.CGEImage{
				{Position: models.ImagePositionThumbnail, S3Key: "generated_images/old-thumb.png", Alt: "a"},
				{Position: models.ImagePositionMidArticle, S3Key: "generated_images/old-mid-article.png", Alt: "b"},
			},
		},
		kw: &models.Keyword{Keyword: "testing best practices"},
	}
	imgGen := &regenFakeImageGen{data: tinyPNG(t)}
	s3 := &regenFakeS3{}
	svc := &contentGenerationEngineService{
		store:    st,
		LLM:      &config.LLMProvider{Utils: llm.NewLlmUtils()},
		imageGen: imgGen,
		s3:       s3,
		s3Bucket: "test-bucket",
		values: config.ContentGenerationValues{
			ImageAspectRatio:          "16:9",
			MidArticleSectionMaxChars: 500,
			AltTextMaxChars:           120,
		},
	}
	return svc, st, imgGen, s3, userID.Hex()
}

// --- tests ---

func TestRegenerateImage_MidArticle(t *testing.T) {
	svc, st, imgGen, s3, userID := newRegenFixture(t)

	res, err := svc.RegenerateImage(context.Background(), userID, cge.RegenerateImageRequest{
		ScheduledArticleID: "sa1",
		Position:           models.ImagePositionMidArticle,
		Instructions:       "darker palette",
	})
	if err != nil {
		t.Fatalf("RegenerateImage: %v", err)
	}

	wantPrefix := models.RegeneratedImageKeyPrefix(userID, st.mc.ID.Hex()) + "-mid-article-"
	if !strings.HasPrefix(res.S3Key, wantPrefix) || !strings.HasSuffix(res.S3Key, ".png") {
		t.Fatalf("key = %q, want prefix %q + .png", res.S3Key, wantPrefix)
	}
	if s3.uploadedKey != res.S3Key || s3.uploadedBucket != "test-bucket" {
		t.Fatalf("upload = %q to %q, want returned key to test-bucket", s3.uploadedKey, s3.uploadedBucket)
	}
	if !strings.Contains(res.URL, res.S3Key) {
		t.Fatalf("url %q doesn't reference the key", res.URL)
	}
	if res.Alt == "" {
		t.Fatal("alt is empty")
	}
	// Section context extracted despite the placeholder being long gone, and
	// the art direction folded in.
	if !strings.Contains(imgGen.gotPrompt, "Deep Dive") {
		t.Fatalf("prompt missing section context: %.200q", imgGen.gotPrompt)
	}
	if !strings.Contains(imgGen.gotPrompt, "darker palette") {
		t.Fatalf("prompt missing art direction: %.200q", imgGen.gotPrompt)
	}
	// The regen is recorded as the slot's pending entry so it survives reloads.
	if st.gotPendingImagePos != models.ImagePositionMidArticle ||
		st.gotPendingImageEntry == nil || st.gotPendingImageEntry.S3Key != res.S3Key {
		t.Fatalf("pending entry not persisted: pos=%q entry=%+v", st.gotPendingImagePos, st.gotPendingImageEntry)
	}
}

func TestRegenerateImage_Thumbnail(t *testing.T) {
	svc, st, imgGen, _, userID := newRegenFixture(t)

	res, err := svc.RegenerateImage(context.Background(), userID, cge.RegenerateImageRequest{
		ScheduledArticleID: "sa1",
		Position:           models.ImagePositionThumbnail,
	})
	if err != nil {
		t.Fatalf("RegenerateImage: %v", err)
	}
	wantPrefix := models.RegeneratedImageKeyPrefix(userID, st.mc.ID.Hex()) + "-thumbnail-"
	if !strings.HasPrefix(res.S3Key, wantPrefix) {
		t.Fatalf("key = %q, want prefix %q", res.S3Key, wantPrefix)
	}
	// Thumbnail prompt context is the article intro.
	if !strings.Contains(imgGen.gotPrompt, "Intro paragraph about testing.") {
		t.Fatalf("prompt missing intro context: %.200q", imgGen.gotPrompt)
	}
}

func TestRegenerateImage_UsesEditedContentForContext(t *testing.T) {
	svc, st, imgGen, _, userID := newRegenFixture(t)
	st.mc.Edited = &models.CGEEdited{
		ArticleContent: "Edited intro the user rewrote.\n\n## Edited Section\n\nBody.\n",
	}

	if _, err := svc.RegenerateImage(context.Background(), userID, cge.RegenerateImageRequest{
		ScheduledArticleID: "sa1",
		Position:           models.ImagePositionThumbnail,
	}); err != nil {
		t.Fatalf("RegenerateImage: %v", err)
	}
	if !strings.Contains(imgGen.gotPrompt, "Edited intro the user rewrote.") {
		t.Fatalf("prompt built from pipeline content, want edited content: %.200q", imgGen.gotPrompt)
	}
}

func TestRegenerateImage_Guards(t *testing.T) {
	svc, _, _, _, userID := newRegenFixture(t)

	if _, err := svc.RegenerateImage(context.Background(), userID, cge.RegenerateImageRequest{
		ScheduledArticleID: "sa1", Position: "banner",
	}); !errors.Is(err, cge.ErrRegenInvalidInput) {
		t.Fatalf("bad position: err = %v, want ErrRegenInvalidInput", err)
	}

	if _, err := svc.RegenerateImage(context.Background(), primitive.NewObjectID().Hex(), cge.RegenerateImageRequest{
		ScheduledArticleID: "sa1", Position: models.ImagePositionThumbnail,
	}); !errors.Is(err, cge.ErrRegenArticleNotFound) {
		t.Fatalf("foreign user: err = %v, want ErrRegenArticleNotFound", err)
	}

	if _, err := svc.RegenerateImage(context.Background(), userID, cge.RegenerateImageRequest{
		ScheduledArticleID: "sa1",
		Position:           models.ImagePositionThumbnail,
		Instructions:       strings.Repeat("x", maxRegenInstructionsChars+1),
	}); !errors.Is(err, cge.ErrRegenInvalidInput) {
		t.Fatalf("oversized instructions: err = %v, want ErrRegenInvalidInput", err)
	}
}
