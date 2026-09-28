package webhooks

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/services/userservice/dto"
	"github.com/atharva-ng/crunch/internal/util/log"
	svix "github.com/svix/svix-webhooks/go"
)

// clerkWebhookEvent represents the top-level structure of a Clerk webhook payload.
type clerkWebhookEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type clerkUserData struct {
	ID             string              `json:"id"`
	EmailAddresses []clerkEmailAddress `json:"email_addresses"`
	PrimaryEmailID string              `json:"primary_email_address_id"`
	FirstName      *string             `json:"first_name"`
	LastName       *string             `json:"last_name"`
}

type clerkEmailAddress struct {
	ID           string `json:"id"`
	EmailAddress string `json:"email_address"`
}

const (
	clerkEventUserCreated = "user.created"
	clerkEventUserUpdated = "user.updated"
	clerkEventUserDeleted = "user.deleted"
)

func HandleClerkWebhook(w http.ResponseWriter, r *http.Request) {
	appCtx := config.GetAppContext(r)

	// Cap the request body before verification so a hostile payload can't
	// balloon memory on this unauthenticated endpoint.
	r.Body = http.MaxBytesReader(w, r.Body, appCtx.Config.Values.Webhooks.MaxClerkBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error("clerk webhook: failed to read body", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Verify webhook signature using Svix. Fail closed if the secret is unset —
	// this endpoint mutates users, so an unsigned request must never be trusted.
	webhookSecret := appCtx.Config.Clerk.WebhookSecret
	if webhookSecret == "" {
		log.Error("clerk webhook: CLERK_WEBHOOK_SECRET is not configured")
		http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
		return
	}

	wh, err := svix.NewWebhook(webhookSecret)
	if err != nil {
		log.Error("clerk webhook: failed to create verifier", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	headers := http.Header{}
	headers.Set("svix-id", r.Header.Get("svix-id"))
	headers.Set("svix-timestamp", r.Header.Get("svix-timestamp"))
	headers.Set("svix-signature", r.Header.Get("svix-signature"))

	if err := wh.Verify(body, headers); err != nil {
		log.Error("clerk webhook: signature verification failed", "error", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var event clerkWebhookEvent
	if err := json.Unmarshal(body, &event); err != nil {
		log.Error("clerk webhook: failed to parse event", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	switch event.Type {
	case clerkEventUserCreated, clerkEventUserUpdated:
		var userData clerkUserData
		if err := json.Unmarshal(event.Data, &userData); err != nil {
			log.Error("clerk webhook: failed to parse user data", "error", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		email := extractPrimaryEmail(userData)
		name := buildFullName(userData.FirstName, userData.LastName)

		syncReq := dto.SyncUserRequest{
			ClerkID: userData.ID,
			Email:   email,
			Name:    name,
		}

		if err := appCtx.InternalServices.UserService.SyncUser(r.Context(), syncReq); err != nil {
			log.Error("clerk webhook: failed to sync user", "error", err, "clerk_id", userData.ID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		log.Info("clerk webhook: user synced", "clerk_id", userData.ID, "event", event.Type)

	case clerkEventUserDeleted:
		var userData struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(event.Data, &userData); err != nil {
			log.Error("clerk webhook: failed to parse delete event", "error", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if _, err := appCtx.InternalServices.UserService.DeleteUser(r.Context(), userData.ID); err != nil {
			log.Error("clerk webhook: failed to delete user", "error", err, "clerk_id", userData.ID)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		log.Info("clerk webhook: user deleted", "clerk_id", userData.ID)

	default:
		log.Info("clerk webhook: unhandled event type", "type", event.Type)
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]string{"status": "ok"})
}

func extractPrimaryEmail(data clerkUserData) string {
	for _, ea := range data.EmailAddresses {
		if ea.ID == data.PrimaryEmailID {
			return ea.EmailAddress
		}
	}
	if len(data.EmailAddresses) > 0 {
		return data.EmailAddresses[0].EmailAddress
	}
	return ""
}

func buildFullName(first, last *string) string {
	name := ""
	if first != nil {
		name = *first
	}
	if last != nil && *last != "" {
		if name != "" {
			name += " "
		}
		name += *last
	}
	return name
}
