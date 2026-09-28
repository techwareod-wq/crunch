package demo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/cron"
	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/modules"
	"github.com/atharva-ng/crunch/internal/pipeline"
	asynchandler "github.com/atharva-ng/crunch/internal/services/asyncHandler"
)

type fakeLLM struct {
	got dto.PromptRequest
	err error
}

func (f *fakeLLM) Prompt(ctx context.Context, req dto.PromptRequest) (*dto.PromptResponse, error) {
	f.got = req
	if f.err != nil {
		return nil, f.err
	}
	return &dto.PromptResponse{Content: "a summary", Provider: "fake"}, nil
}

func appCtxWith(llm *fakeLLM) *config.AppContext {
	appCtx := &config.AppContext{}
	appCtx.InternalServices.LLM = &config.LLMProvider{}
	if llm != nil {
		appCtx.InternalServices.LLM.Anthropic = llm
	}
	return appCtx
}

// runSummarize drives DEMO_SUMMARIZE through the module's registry, exactly as
// the async handler would.
func runSummarize(t *testing.T, appCtx *config.AppContext, payload []byte) error {
	t.Helper()
	reg := modules.BuildRegistry([]modules.Module{New(appCtx)})
	h, ok := reg[ProcessSummarize]
	if !ok {
		t.Fatal("DEMO_SUMMARIZE not registered")
	}
	return h(context.Background(), asynchandler.MessageEnvelope{
		ProcessType: ProcessSummarize,
		UserID:      "u1",
		Payload:     payload,
	})
}

func TestSummarize_PromptsTheLLM(t *testing.T) {
	llm := &fakeLLM{}
	body, _ := json.Marshal(SummarizePayload{Text: "hello world"})
	if err := runSummarize(t, appCtxWith(llm), body); err != nil {
		t.Fatalf("summarize failed: %v", err)
	}
	if len(llm.got.Messages) != 2 || llm.got.Messages[1].Content != "hello world" {
		t.Errorf("unexpected prompt: %+v", llm.got)
	}
}

func TestSummarize_NoProviderIsPermanent(t *testing.T) {
	body, _ := json.Marshal(SummarizePayload{Text: "hello"})
	err := runSummarize(t, appCtxWith(nil), body)
	if !errors.Is(err, pipeline.ErrPermanent) {
		t.Errorf("want ErrPermanent with no LLM configured, got %v", err)
	}
}

func TestSummarize_LLMErrorIsRetryable(t *testing.T) {
	body, _ := json.Marshal(SummarizePayload{Text: "hello"})
	err := runSummarize(t, appCtxWith(&fakeLLM{err: errors.New("429")}), body)
	if err == nil || errors.Is(err, pipeline.ErrPermanent) {
		t.Errorf("want a retryable error, got %v", err)
	}
}

func TestSummarize_BadPayloadIsPermanent(t *testing.T) {
	err := runSummarize(t, appCtxWith(&fakeLLM{}), []byte(`{"text":`))
	if !errors.Is(err, pipeline.ErrPermanent) {
		t.Errorf("want ErrPermanent for an undecodable payload, got %v", err)
	}
}

func TestModule_WiresSecondaryQueueAndCron(t *testing.T) {
	mods := []modules.Module{New(&config.AppContext{})}
	if pts := modules.LLMProcessTypes(mods); len(pts) != 1 || pts[0] != ProcessSummarize {
		t.Errorf("LLM process types = %v, want [%s]", pts, ProcessSummarize)
	}
	jobs := modules.CronJobs(mods)
	if len(jobs) != 1 || jobs[0].Name != JobHeartbeat || jobs[0].Process != ProcessSummarize {
		t.Fatalf("unexpected cron jobs: %+v", jobs)
	}
	if err := jobs[0].Spec.Validate(5*time.Minute, "UTC"); err != nil {
		t.Errorf("heartbeat spec invalid: %v", err)
	}
}

func TestResolveHeartbeat_OneSystemUnit(t *testing.T) {
	occ := cron.Occurrence{Job: JobHeartbeat, At: time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)}
	units, err := resolveHeartbeat(context.Background(), occ)
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || units[0].UserID != systemUserID {
		t.Fatalf("unexpected units: %+v", units)
	}
	if p, ok := units[0].Payload.(SummarizePayload); !ok || p.Text != "crunch heartbeat for 2026-09-28" {
		t.Errorf("unexpected payload: %+v", units[0].Payload)
	}
}
