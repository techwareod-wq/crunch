package store

import (
	"context"

	"github.com/atharva-ng/crunch/internal/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type Store interface {
	CreateWebEntityMasterContext(ctx context.Context, mc *models.WebEntityMasterContext) error
	GetWebEntityMasterContext(ctx context.Context, id string) (bool, *models.WebEntityMasterContext, error)
	GetWebEntityMasterContextByKeywordAndWEC(ctx context.Context, webEntityContextID string, keywordID primitive.ObjectID) (bool, *models.WebEntityMasterContext, error)
	GetKeyword(ctx context.Context, id string) (bool, *models.Keyword, error)
	GetKeywordsByIDs(ctx context.Context, ids []primitive.ObjectID) ([]models.Keyword, error)
	UpdateWebEntityMasterContext(ctx context.Context, id string, req models.WEMCUpdateReq) error
	ResetCGEForRetry(ctx context.Context, id string, mc *models.WebEntityMasterContext) error
	ClearCGEErrorData(ctx context.Context, id string) error
	AppendCGEErrorData(ctx context.Context, id string, errData models.CGEErrorData) error
	SetCGEStatusIfCurrent(ctx context.Context, id string, currentStatus, newStatus int) error
	AtomicClaimOutlineDispatch(ctx context.Context, id string) (bool, error)
	DecrementUrlScrapesRemaining(ctx context.Context, masterContextID string) (int, error)
	MarkUrlScrapeDone(ctx context.Context, masterContextID string, url string) error
	MarkUrlScrapeFailed(ctx context.Context, masterContextID string, url string) error
	MarkYouTubeTranscriptSkipped(ctx context.Context, masterContextID string, videoID string) error
	UpdateCGEArticleStructure(ctx context.Context, masterContextID, url string, structure models.CGEArticleStructure) error
	SetTopicResearchInsightAndMarkDone(ctx context.Context, masterContextID, variant string, insight *models.CGETopicInsight) error
	SaveYouTubeTranscriptAndMarkDone(ctx context.Context, masterContextID, videoID, transcript string) error
	InitCGEImages(ctx context.Context, masterContextID string) error
	SetCGEImageByPosition(ctx context.Context, masterContextID string, img models.CGEImage) error
	SetCGEImageGenPrompt(ctx context.Context, masterContextID, position string, p models.CGEImageGenPrompt) error
	AtomicClaimImageReplacementDispatch(ctx context.Context, id string) (bool, error)
	AtomicClaimFinalAssemblyDispatch(ctx context.Context, id string) (bool, error)

	// Read-only access to parent entities
	GetWebEntityByID(ctx context.Context, id string) (bool, *models.WebEntity, error)
	GetWebEntityContext(ctx context.Context, id string) (bool, *models.WebEntityContext, error)

	// Regenerate entry points resolve the target from the calendar slot the
	// client holds, so they need the slot (ownership) and its master context —
	// plus the pending-regen writers that make a regen survive reloads.
	GetScheduledArticle(ctx context.Context, id string) (bool, *models.ScheduledArticle, error)
	GetWebEntityMasterContextByScheduledArticleID(ctx context.Context, scheduledArticleID string) (bool, *models.WebEntityMasterContext, error)
	AppendPendingSectionRegen(ctx context.Context, masterContextID string, entry models.CGEPendingSectionRegen) error
	SetPendingImageRegen(ctx context.Context, masterContextID, position string, entry models.CGEPendingImageRegen) error

	// ScheduledArticle lifecycle — flipped by CGE entry/exit to drive the
	// calendar UI without joining to the master context.
	SetScheduledArticleStatus(ctx context.Context, id string, status models.ScheduledArticleStatus) error
	RecordArticleGenerationStart(ctx context.Context, scheduledArticleID string) error
}

type store struct{}

func NewStore() Store {
	return &store{}
}

func (s *store) CreateWebEntityMasterContext(ctx context.Context, mc *models.WebEntityMasterContext) error {
	return models.CreateWebEntityMasterContext(ctx, mc)
}

func (s *store) GetWebEntityMasterContext(ctx context.Context, id string) (bool, *models.WebEntityMasterContext, error) {
	return models.GetWebEntityMasterContext(ctx, id)
}

func (s *store) GetWebEntityMasterContextByKeywordAndWEC(ctx context.Context, webEntityContextID string, keywordID primitive.ObjectID) (bool, *models.WebEntityMasterContext, error) {
	return models.GetWebEntityMasterContextByKeywordAndWEC(ctx, webEntityContextID, keywordID)
}

func (s *store) GetKeyword(ctx context.Context, id string) (bool, *models.Keyword, error) {
	return models.GetKeyword(ctx, id)
}

func (s *store) GetKeywordsByIDs(ctx context.Context, ids []primitive.ObjectID) ([]models.Keyword, error) {
	return models.GetKeywordsByIDs(ctx, ids)
}

func (s *store) ResetCGEForRetry(ctx context.Context, id string, mc *models.WebEntityMasterContext) error {
	return models.ResetCGEForRetry(ctx, id, mc)
}

func (s *store) ClearCGEErrorData(ctx context.Context, id string) error {
	return models.ClearCGEErrorData(ctx, id)
}

func (s *store) UpdateWebEntityMasterContext(ctx context.Context, id string, req models.WEMCUpdateReq) error {
	return models.UpdateWebEntityMasterContext(ctx, id, req)
}

func (s *store) AppendCGEErrorData(ctx context.Context, id string, errData models.CGEErrorData) error {
	return models.AppendCGEErrorData(ctx, id, errData)
}

func (s *store) SetCGEStatusIfCurrent(ctx context.Context, id string, currentStatus, newStatus int) error {
	return models.SetCGEStatusIfCurrent(ctx, id, currentStatus, newStatus)
}

func (s *store) AtomicClaimOutlineDispatch(ctx context.Context, id string) (bool, error) {
	return models.AtomicClaimOutlineDispatch(ctx, id)
}

func (s *store) DecrementUrlScrapesRemaining(ctx context.Context, masterContextID string) (int, error) {
	return models.DecrementUrlScrapesRemaining(ctx, masterContextID)
}

func (s *store) MarkUrlScrapeDone(ctx context.Context, masterContextID string, url string) error {
	return models.MarkUrlScrapeDone(ctx, masterContextID, url)
}

func (s *store) MarkUrlScrapeFailed(ctx context.Context, masterContextID string, url string) error {
	return models.MarkUrlScrapeFailed(ctx, masterContextID, url)
}

func (s *store) MarkYouTubeTranscriptSkipped(ctx context.Context, masterContextID string, videoID string) error {
	return models.MarkYouTubeTranscriptSkipped(ctx, masterContextID, videoID)
}

func (s *store) UpdateCGEArticleStructure(ctx context.Context, masterContextID, url string, structure models.CGEArticleStructure) error {
	return models.UpdateCGEArticleStructure(ctx, masterContextID, url, structure)
}

func (s *store) SetTopicResearchInsightAndMarkDone(ctx context.Context, masterContextID, variant string, insight *models.CGETopicInsight) error {
	return models.SetTopicResearchInsightAndMarkDone(ctx, masterContextID, variant, insight)
}

func (s *store) SaveYouTubeTranscriptAndMarkDone(ctx context.Context, masterContextID, videoID, transcript string) error {
	return models.SaveYouTubeTranscriptAndMarkDone(ctx, masterContextID, videoID, transcript)
}

func (s *store) InitCGEImages(ctx context.Context, masterContextID string) error {
	return models.InitCGEImages(ctx, masterContextID)
}

func (s *store) SetCGEImageByPosition(ctx context.Context, masterContextID string, img models.CGEImage) error {
	return models.SetCGEImageByPosition(ctx, masterContextID, img)
}

func (s *store) SetCGEImageGenPrompt(ctx context.Context, masterContextID, position string, p models.CGEImageGenPrompt) error {
	return models.SetCGEImageGenPrompt(ctx, masterContextID, position, p)
}

func (s *store) AtomicClaimImageReplacementDispatch(ctx context.Context, id string) (bool, error) {
	return models.AtomicClaimImageReplacementDispatch(ctx, id)
}

func (s *store) AtomicClaimFinalAssemblyDispatch(ctx context.Context, id string) (bool, error) {
	return models.AtomicClaimFinalAssemblyDispatch(ctx, id)
}

func (s *store) GetWebEntityByID(ctx context.Context, id string) (bool, *models.WebEntity, error) {
	return models.FindWebEntityByID(ctx, id)
}

func (s *store) GetWebEntityContext(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
	return models.GetWebEntityContext(ctx, id)
}

func (s *store) GetScheduledArticle(ctx context.Context, id string) (bool, *models.ScheduledArticle, error) {
	return models.GetScheduledArticle(ctx, id)
}

func (s *store) GetWebEntityMasterContextByScheduledArticleID(ctx context.Context, scheduledArticleID string) (bool, *models.WebEntityMasterContext, error) {
	return models.GetWebEntityMasterContextByScheduledArticleID(ctx, scheduledArticleID)
}

func (s *store) AppendPendingSectionRegen(ctx context.Context, masterContextID string, entry models.CGEPendingSectionRegen) error {
	return models.AppendPendingSectionRegen(ctx, masterContextID, entry)
}

func (s *store) SetPendingImageRegen(ctx context.Context, masterContextID, position string, entry models.CGEPendingImageRegen) error {
	return models.SetPendingImageRegen(ctx, masterContextID, position, entry)
}

func (s *store) SetScheduledArticleStatus(ctx context.Context, id string, status models.ScheduledArticleStatus) error {
	return models.SetScheduledArticleStatus(ctx, id, status)
}

func (s *store) RecordArticleGenerationStart(ctx context.Context, scheduledArticleID string) error {
	return models.RecordArticleGenerationStart(ctx, scheduledArticleID)
}
