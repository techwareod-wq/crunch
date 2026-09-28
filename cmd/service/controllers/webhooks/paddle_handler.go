package webhooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
	paymentdto "github.com/atharva-ng/crunch/internal/services/paymentService/dto"
	"github.com/atharva-ng/crunch/internal/util/log"
)

func HandlePaddleWebhook(w http.ResponseWriter, r *http.Request) {
	appCtx := config.GetAppContext(r)

	// Cap the request body before verification so a hostile payload can't
	// balloon memory (the SDK verifier has its own 2 MB cap; this matches it at
	// the handler boundary).
	r.Body = http.MaxBytesReader(w, r.Body, appCtx.Config.Values.Webhooks.MaxPaddleBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error("paddle webhook: failed to read body", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Fail closed: this endpoint mutates subscriptions, so an unsigned
	// request must never be trusted.
	if appCtx.Config.Paddle.WebhookSecret == "" {
		log.Error("paddle webhook: PADDLE_WEBHOOK_SECRET is not configured")
		http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
		return
	}

	if err := appCtx.PaddleProvider.VerifyWebhook(r, body); err != nil {
		// A sandbox/live secret mixup produces uniform 401s — the wrapped
		// error includes which environment the verifier was configured for.
		log.Error("paddle webhook: signature verification failed", "error", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var envelope paymentdto.WebhookEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		// Signed but malformed: Paddle retries any non-2xx, and a permanently
		// malformed event would retry-loop — log loudly and ack instead.
		log.Error("paddle webhook: failed to parse signed envelope — acking to avoid retry loop", "error", err)
		middleware.SendJSONResponse(w, r, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}

	// Detach from the client connection so an aborted delivery can't cancel
	// Mongo writes halfway through processing.
	processingTimeout := time.Duration(appCtx.Config.Values.Webhooks.PaddleProcessingTimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), processingTimeout)
	defer cancel()

	if err := appCtx.InternalServices.PaymentService.HandleWebhookEvent(ctx, envelope, body); err != nil {
		// 500 → Paddle redelivers with backoff; dedupe + ordering guard make
		// the retry safe.
		log.Error("paddle webhook: processing failed", "error", err, "event_id", envelope.EventID, "event_type", envelope.EventType)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]string{"status": "ok"})
}
