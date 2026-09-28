package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/services/paymentService/dto"
	sie "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// Raw Paddle subscription statuses — Paddle's vocabulary, as it arrives on
// webhook payloads and GetSubscription responses.
//
// DISTINCT from the models.SubStatus* values we store, which
// mapPaddleSubscriptionStatus derives: raw "active" plus a scheduled cancel
// becomes models.SubStatusCanceling, so the two sets do not correspond
// one-to-one. Call sites that key on the RAW status (the trial lifecycle
// claims, the upgrade expand, the SIE backstop) depend on that difference —
// never substitute a models.SubStatus* constant for one of these.
const (
	paddleStatusActive   = "active"
	paddleStatusTrialing = "trialing"
	paddleStatusPastDue  = "past_due"
	paddleStatusCanceled = "canceled"
	paddleStatusPaused   = "paused"
)

// HandleWebhookEvent implements the verified-webhook pipeline:
//
//  1. Insert-first dedupe on the unique event_id index. On duplicate, skip
//     only if the existing row was processed; a row with a recorded error
//     and no processed_at is reprocessed (Paddle redelivery is the retry).
//  2. Dispatch on event_type. All subscription applies write full state
//     from the payload through an atomic last_event_at-guarded upsert, so
//     out-of-order and concurrently redelivered events can't regress state.
//  3. processed_at is stamped strictly AFTER the apply — a crash in between
//     leaves an unprocessed row that the Paddle retry picks up.
//
// A nil return acks 200. Events that can never be resolved (no user mapping:
// dashboard-created subs, simulator payloads) are marked processed with a
// note and acked, so Paddle doesn't retry-loop them for days.
func (s *service) HandleWebhookEvent(ctx context.Context, envelope dto.WebhookEnvelope, rawBody []byte) error {
	if envelope.EventID == "" || envelope.EventType == "" {
		log.Warn("paddle webhook: envelope missing event_id/event_type — acking without processing")
		return nil
	}

	duplicate, existing, err := s.store.InsertEvent(ctx, &models.PaddleEvent{
		EventID:    envelope.EventID,
		EventType:  envelope.EventType,
		OccurredAt: envelope.OccurredAt.UTC(),
		Payload:    string(rawBody),
	})
	if err != nil {
		return fmt.Errorf("insert paddle event: %w", err)
	}
	if duplicate && existing != nil && existing.ProcessedAt != nil {
		log.Info("paddle webhook: duplicate delivery skipped", "event_id", envelope.EventID, "event_type", envelope.EventType)
		return nil
	}
	if duplicate {
		log.Info("paddle webhook: reprocessing previously failed event", "event_id", envelope.EventID, "event_type", envelope.EventType)
	}

	note, err := s.dispatchEvent(ctx, envelope)
	if err != nil {
		if merr := s.store.MarkEventFailed(ctx, envelope.EventID, err.Error()); merr != nil {
			log.Error("paddle webhook: failed to record processing error", "error", merr, "event_id", envelope.EventID)
		}
		return fmt.Errorf("process paddle event %s (%s): %w", envelope.EventID, envelope.EventType, err)
	}

	if note != "" {
		log.Warn("paddle webhook: event acked without full processing", "event_id", envelope.EventID, "event_type", envelope.EventType, "note", note)
	}
	if err := s.store.MarkEventProcessed(ctx, envelope.EventID, note); err != nil {
		// The apply succeeded; failing the ack would cause a redelivery that
		// the ordering guard absorbs. Log and ack.
		log.Error("paddle webhook: failed to mark event processed", "error", err, "event_id", envelope.EventID)
	}
	return nil
}

// dispatchEvent returns (note, err): a non-empty note means the event was
// intentionally not applied (recorded on the event row, acked 200); an error
// means transient failure (500 → Paddle redelivers).
func (s *service) dispatchEvent(ctx context.Context, envelope dto.WebhookEnvelope) (string, error) {
	switch envelope.EventType {
	case dto.EventCustomerCreated, dto.EventCustomerUpdated:
		return s.applyCustomerEvent(ctx, envelope)

	case dto.EventSubscriptionCreated, dto.EventSubscriptionActivated, dto.EventSubscriptionTrialing,
		dto.EventSubscriptionUpdated, dto.EventSubscriptionCanceled, dto.EventSubscriptionPastDue,
		dto.EventSubscriptionPaused, dto.EventSubscriptionResumed:
		return s.applySubscriptionEvent(ctx, envelope)

	case dto.EventTransactionCompleted, dto.EventTransactionPaymentFailed:
		return s.applyTransactionEvent(ctx, envelope)

	default:
		// Stored in paddle_events for audit; nothing to apply.
		return "ignored event type", nil
	}
}

func (s *service) applyCustomerEvent(ctx context.Context, envelope dto.WebhookEnvelope) (string, error) {
	var data dto.WebhookCustomer
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		return fmt.Sprintf("malformed customer payload: %v", err), nil
	}
	if data.ID == "" {
		return "customer payload missing id", nil
	}

	found, _, err := s.store.FindCustomerByPaddleID(ctx, data.ID)
	if err != nil {
		return "", err
	}
	if found {
		// Pre-created mapping exists — just refresh the email.
		if data.Email != "" {
			if err := s.store.UpdateCustomerEmail(ctx, data.ID, data.Email); err != nil {
				return "", err
			}
		}
		return "", nil
	}

	// No mapping yet: only create one if the event carries our userId
	// (set by the checkout pre-create). Dashboard/simulator customers
	// without it would be orphans — record and ack.
	userID, ok := userIDFromCustomData(data.CustomData)
	if !ok {
		return "no paddle_customers mapping and no resolvable custom_data.userId", nil
	}
	if err := s.store.InsertCustomer(ctx, &models.PaddleCustomer{
		UserID:           userID,
		PaddleCustomerID: data.ID,
		Email:            data.Email,
	}); err != nil {
		// Duplicate inserts can race with the checkout pre-create; the
		// ordering doesn't matter — the mapping exists either way.
		found2, _, ferr := s.store.FindCustomerByPaddleID(ctx, data.ID)
		if ferr == nil && found2 {
			return "", nil
		}
		return "", err
	}
	return "", nil
}

func (s *service) applySubscriptionEvent(ctx context.Context, envelope dto.WebhookEnvelope) (string, error) {
	var data dto.WebhookSubscription
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		return fmt.Sprintf("malformed subscription payload: %v", err), nil
	}
	if data.ID == "" {
		return "subscription payload missing id", nil
	}

	// Resurrection guard: the admin deletion cascade hard-deletes subscription
	// docs, and ApplySubscriptionEvent below is an upsert — a late-delivered
	// event (queued before the cancel, delivered after the delete) would
	// re-create the doc. Tombstones are written before the delete, so this
	// check is race-free. Errors are transient → 500 → Paddle redelivers.
	if tombstoned, err := s.store.IsSubscriptionTombstoned(ctx, data.ID); err != nil {
		return "", err
	} else if tombstoned {
		return "subscription hard-deleted by admin — event skipped", nil
	}

	userID, note, err := s.resolveUserID(ctx, data.CustomerID, data.CustomData, data.ID)
	if err != nil || note != "" {
		return note, err
	}

	status := mapPaddleSubscriptionStatus(data)

	set := bson.M{
		"paddle_customer_id": data.CustomerID,
		"status":             status,
	}
	unset := bson.M{}

	// (a) Derive the app from the price — FAIL CLOSED on unmapped prices.
	// Authorization scope must never be chosen by fallback: an unmapped app2
	// price defaulting to Indexly would grant Indexly access that was never
	// bought. The event stays unprocessed → Paddle redelivers; once the plan
	// doc exists (admin write → cache reload) the redelivery applies cleanly.
	// Past the retry window → admin recompute repairs.
	if len(data.Items) == 0 {
		return "", fmt.Errorf("subscription %s payload has no items — cannot derive app", data.ID)
	}
	priceID := data.Items[0].Price.ID
	appID, ok := s.plans.AppForPrice(priceID)
	if !ok {
		// This line must page/alert: events are stalling until the plan doc exists.
		log.Error("webhook price has no plan doc — event quarantined",
			"price_id", priceID, "paddle_subscription_id", data.ID, "event_id", envelope.EventID)
		return "", fmt.Errorf("unmapped price %s", priceID)
	}
	set["app_id"] = appID

	if status == models.SubStatusTrialing {
		// Latch the trial fact; never unset.
		set["was_trialing"] = true
		// Trial legitimacy — FLAG, never block: a trialing sub for an
		// identity with prior subscription history is marked for admin
		// review. Errors are swallowed (flag-only input must not fail the
		// event); a missed flag is admin noise, not an access decision.
		if eligible, terr := s.store.TrialEligibleIdentity(ctx, userID, data.CustomerID, data.ID); terr != nil {
			log.Error("trial eligibility check failed — not flagged", "error", terr, "paddle_subscription_id", data.ID)
		} else if !eligible {
			set["trial_flagged"] = true
			log.Warn("trialing subscription for identity with prior history — flagged",
				"user_id", userID.Hex(), "paddle_subscription_id", data.ID)
		}
	}

	// (a2) Company linkage (team seat billing): a subscription whose
	// custom_data carries a VALID companyId is the company's per-seat sub —
	// verify the resolved buyer OWNS that company (D12) before linking; a
	// hand-crafted checkout must never bill someone else's company (refusal
	// fails the event loudly — Paddle redelivers, nothing is applied). Absent
	// or unparseable companyId is NOT an error: the event applies exactly as
	// today's individual flow (decision 7).
	companyID, isCompanySub := companyIDFromCustomData(data.CustomData)
	if isCompanySub {
		cFound, company, err := s.store.FindCompanyByID(ctx, companyID)
		if err != nil {
			return "", err
		}
		if !cFound || company.OwnerUserID != userID {
			log.Error("company-linked subscription REFUSED — buyer does not own the company",
				"paddle_subscription_id", data.ID, "company_id", companyID.Hex(),
				"buyer_user_id", userID.Hex(), "event_id", envelope.EventID)
			return "", fmt.Errorf("subscription %s links company %s but buyer %s does not own it",
				data.ID, companyID.Hex(), userID.Hex())
		}
		set["company_id"] = companyID
		qty := data.Items[0].Quantity
		if qty < 1 {
			qty = 1
		}
		set["quantity"] = qty
	}

	set["price_id"] = priceID
	set["product_id"] = data.Items[0].Price.ProductID
	// current_billing_period is null for paused/canceled payloads — keep the
	// stored valid_till in that case (paid-through rule still applies).
	if data.CurrentBillingPeriod != nil && !data.CurrentBillingPeriod.EndsAt.IsZero() {
		set["valid_till"] = data.CurrentBillingPeriod.EndsAt.UTC()
	}
	if data.StartedAt != nil {
		set["started_at"] = data.StartedAt.UTC()
	}
	// Full-state recompute: a cleared scheduled change must be $unset —
	// omitting it would silently keep the stale value.
	if data.ScheduledChange != nil && data.ScheduledChange.Action == "cancel" {
		set["scheduled_cancel_at"] = data.ScheduledChange.EffectiveAt.UTC()
	} else {
		unset["scheduled_cancel_at"] = ""
	}
	if data.CanceledAt != nil {
		set["canceled_at"] = data.CanceledAt.UTC()
	}

	update := bson.M{
		"$set": set,
		"$setOnInsert": bson.M{
			"user_id": userID,
		},
	}
	if len(unset) > 0 {
		update["$unset"] = unset
	}

	// Immediate cancellation (dashboard "cancel now", refund/chargeback)
	// fires subscription.canceled while valid_till is still in the future —
	// clamp it so access ends at the cancellation moment, not period end.
	if envelope.EventType == dto.EventSubscriptionCanceled {
		clampAt := envelope.OccurredAt.UTC()
		if data.CanceledAt != nil {
			clampAt = data.CanceledAt.UTC()
		}
		delete(set, "valid_till")
		update["$min"] = bson.M{"valid_till": clampAt}
	}

	applied, err := s.store.ApplySubscriptionEvent(ctx, data.ID, envelope.OccurredAt, update)
	if err != nil {
		return "", err
	}
	if !applied {
		log.Info("paddle webhook: stale subscription event skipped by ordering guard",
			"event_id", envelope.EventID, "event_type", envelope.EventType, "paddle_subscription_id", data.ID)
	}

	// Company subs divert here: seats sync + member fan-out, and NONE of the
	// individual machinery below (local-trial supersede, upgrade expand, trial
	// claims — the team plan has no trial, and a company purchase must not
	// touch the owner's personal trial state). The individual path continues
	// exactly as before this feature existed.
	if isCompanySub {
		return "", s.syncCompanyFromSubscription(ctx, data.ID, companyID, userID, appID)
	}

	// (b2) Supersede any card-less local trial the moment a real Paddle
	// subscription starts providing access (active/trialing). MUST run before
	// the recompute below so the newest-valid_till winner is the paid sub, not
	// an admin-extended trial whose valid_till outlasts the first billing
	// period. Idempotent: once the trial is canceled it no longer matches.
	if status == models.SubStatusActive || status == models.SubStatusTrialing {
		if err := s.store.EndLocalTrialsForUser(ctx, userID, appID, envelope.OccurredAt); err != nil {
			return "", err
		}
	}

	// (c) ALWAYS recompute — including applied==false: a redelivered event
	// heals a projection lost to a crash between the apply and this write.
	// userID is the RESOLVED user (paddle_customers/custom_data resolution
	// above), never $setOnInsert-derived. Error → event failed → Paddle
	// redelivers → the apply is absorbed by the guard and this retried.
	if err := s.store.RecomputeUserEntitlement(ctx, userID, appID); err != nil {
		return "", err
	}

	// (c2) Trial→paid upgrade expand: an activated Indexly sub (raw status —
	// convert-then-schedule-cancel still arrives as raw "active") for a user
	// whose WEC still runs in trial mode claims the expand via CAS and
	// dispatches SIE_UPGRADE_EXPAND. Redeliveries (and later activated events)
	// lose the CAS and no-op. Fires for live-trial upgrades AND
	// expired-trial-late-subscribers — sie_mode stays "trial" either way.
	if data.Status == paddleStatusActive && appID == models.AppIDIndexly {
		if err := s.triggerUpgradeExpand(ctx, userID); err != nil {
			return "", err
		}
	}

	// (c3) SIE cold-start backstop. The strategy-page poll is the PRIMARY
	// trigger (onboardingService deriveFinalisedStep); this covers the user who
	// pays and never returns to the tab, for whom nothing else would ever start
	// SIE. MUST run after (c) — the worker stamps sie_mode from the entitlement
	// projection — and after (c2), so an existing trial WEC takes the expand
	// path and this no-ops on it. Company-linked (team seat) subs are skipped:
	// they pay for a team company, not the buyer's personal one.
	if data.Status == paddleStatusActive && appID == models.AppIDIndexly && !isCompanySub {
		s.triggerSIEBackstop(ctx, userID)
	}

	// (d) Trial lifecycle claims — keyed on the RAW Paddle status (BEFORE
	// the canceling mapping). Conversion: raw "active" — a mid-trial cancel
	// keeps raw status "trialing" (only a scheduled_change), so it can never
	// claim; a genuine convert-then-schedule-cancel arrives as raw "active" +
	// scheduled_change and still claims. Expiry: raw "canceled"/"paused" for
	// a was_trialing sub that never converted. Both claims are set-once,
	// filter-guarded, idempotent across redeliveries.
	switch data.Status {
	case paddleStatusActive:
		if err := s.store.ClaimTrialConversion(ctx, data.ID); err != nil {
			return "", err
		}
	case paddleStatusCanceled, paddleStatusPaused:
		if err := s.store.ClaimTrialExpiry(ctx, data.ID); err != nil {
			return "", err
		}
	}

	// (e) Side-effect dispatch — TWO-MARKER pattern. Replaces claim-rollback:
	// an unclaim whose own write failed (plausible in the same Mongo blip as
	// the dispatch failure) would lose the conversion forever. A dispatch
	// failure just fails the event; redelivery finds fact-set/dispatch-unset
	// and retries. If MarkDispatched itself fails, redelivery dispatches once
	// more — delivery is AT-LEAST-ONCE and handlers are idempotent. No
	// rollback path, by design.
	found, sub, err := s.store.FindSubscriptionByPaddleID(ctx, data.ID)
	if err != nil {
		return "", err
	}
	if found {
		if sub.TrialConvertedAt != nil && sub.TrialConvertedDispatchedAt == nil {
			if err := s.dispatcher.Dispatch(ctx, string(entitlements.ProcessTrialConverted), userID.Hex(),
				entitlements.TrialConvertedPayload{
					AppID:                appID,
					PaddleSubscriptionID: data.ID,
					PriceID:              sub.PriceID,
					ConvertedAt:          sub.TrialConvertedAt.UTC(),
				}); err != nil {
				return "", err // event failed → redelivered → dispatch retried
			}
			if err := s.store.MarkTrialConversionDispatched(ctx, data.ID); err != nil {
				log.Error("mark trial conversion dispatched failed — next redelivery will dispatch a duplicate",
					"error", err, "paddle_subscription_id", data.ID)
			}
		}
		if sub.TrialExpiredAt != nil && sub.TrialExpiredDispatchedAt == nil {
			if err := s.dispatcher.Dispatch(ctx, string(entitlements.ProcessTrialExpired), userID.Hex(),
				entitlements.TrialExpiredPayload{
					AppID:                appID,
					PaddleSubscriptionID: data.ID,
					PriceID:              sub.PriceID,
					ExpiredAt:            sub.TrialExpiredAt.UTC(),
				}); err != nil {
				return "", err
			}
			if err := s.store.MarkTrialExpiryDispatched(ctx, data.ID); err != nil {
				log.Error("mark trial expiry dispatched failed — next redelivery will dispatch a duplicate",
					"error", err, "paddle_subscription_id", data.ID)
			}
		}
	}
	return "", nil
}

// syncCompanyFromSubscription is the company-sub tail of the apply: stamp the
// company's purchased seats from the CURRENT stored subscription state (read
// back rather than from the payload, so a redelivery heals a crash between
// apply and sync even when the apply itself was skipped as stale), then
// recompute the owner and every claimed member (bounded — claimed members ≤
// purchased seats). Any failure fails the event → Paddle redelivers → every
// write here is idempotent.
func (s *service) syncCompanyFromSubscription(ctx context.Context, paddleSubID string, companyID, ownerID primitive.ObjectID, appID string) error {
	found, sub, err := s.store.FindSubscriptionByPaddleID(ctx, paddleSubID)
	if err != nil {
		return err
	}
	if !found {
		// The apply upserted this doc moments ago; absence means a concurrent
		// admin hard-delete — the redelivery will hit the tombstone guard.
		return fmt.Errorf("company subscription %s vanished between apply and sync", paddleSubID)
	}

	// Seats = quantity while the sub is live (paid-through rule, not fully
	// canceled), else 0 — the invite gate closes but nobody is auto-archived
	// (decision 6): SeatsUsed above SeatsPurchased just blocks NEW invites.
	seats := 0
	if time.Now().UTC().Before(sub.ValidTill) && sub.Status != models.SubStatusCanceled {
		seats = sub.Quantity
		if seats < 1 {
			seats = 1
		}
	}
	if err := s.store.SetCompanyBilling(ctx, companyID, models.CompanyBilling{
		PaddleSubscriptionID: sub.PaddleSubscriptionID,
		SeatsPurchased:       seats,
	}); err != nil {
		return err
	}

	memberIDs, err := s.store.ListClaimedMembershipUserIDs(ctx, companyID)
	if err != nil {
		return err
	}
	recomputed := map[primitive.ObjectID]bool{}
	for _, uid := range append([]primitive.ObjectID{ownerID}, memberIDs...) {
		if recomputed[uid] {
			continue
		}
		recomputed[uid] = true
		if err := s.store.RecomputeUserEntitlement(ctx, uid, appID); err != nil {
			// Partial fan-out is fine: the redelivery re-runs the whole sync.
			return err
		}
	}
	return nil
}

// triggerUpgradeExpand scans the user's trial-mode WECs and, for each, claims
// the expand pipeline (upgrade_state "" → "expanding", findOneAndUpdate CAS)
// and dispatches SIE_UPGRADE_EXPAND. A dispatch failure rolls the claim back
// (guarded — only while no stage has run) and fails the event so the Paddle
// redelivery retries the whole trigger; without the rollback the redelivery
// would lose the CAS against a claim that has no message behind it.
func (s *service) triggerUpgradeExpand(ctx context.Context, userID primitive.ObjectID) error {
	wecs, err := s.store.FindTrialModeWECsForUser(ctx, userID)
	if err != nil {
		return err
	}
	for i := range wecs {
		wec := &wecs[i]
		claimed, err := s.store.ClaimWECUpgrade(ctx, wec.ID)
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		if err := s.dispatcher.Dispatch(ctx, string(sie.ProcessSIEUpgradeExpand), userID.Hex(),
			pipeline.StandardPayload{
				WebEntityID:        wec.WebEntityID.Hex(),
				WebEntityContextID: wec.ID.Hex(),
			}); err != nil {
			if unclaimErr := s.store.UnclaimWECUpgrade(ctx, wec.ID); unclaimErr != nil {
				log.Error("upgrade expand: unclaim after dispatch failure failed — WEC stuck expanding until admin retriggers",
					"error", unclaimErr, "webEntityContextId", wec.ID.Hex())
			}
			return err
		}
		log.Info("upgrade expand dispatched", "user_id", userID.Hex(), "webEntityContextId", wec.ID.Hex())
	}
	return nil
}

// sieBackstopPayload is the SITE_INTELLIGENCE_PROCESS message body. It mirrors
// asyncHandler.SiteIntelligencePayload, redeclared here so the payment service
// doesn't import the handler registry.
type sieBackstopPayload struct {
	WebEntityID string `json:"webEntityId"`
}

// triggerSIEBackstop starts the first SIE run for a buyer whose personal
// company has a finalised web entity that has never been processed. It resolves
// personal company → web entity (company_id, falling back to the legacy user_id
// key for docs companymigrate hasn't stamped) → no WEC, then dispatches
// SITE_INTELLIGENCE_PROCESS.
//
// The no-WEC precondition IS the idempotency marker — no dispatched_at field
// needed. Once the worker creates the WEC, every Paddle redelivery stops there.
// The one window it can't close (dispatch sent, WEC not yet created, redelivery
// arrives) is absorbed by Orchestrate's dup-key re-read, which no-ops on a
// healthy WEC.
//
// BEST EFFORT BY DESIGN: every failure logs and returns rather than failing the
// event. Unlike triggerUpgradeExpand — whose CAS claim would be stranded by a
// lost dispatch — nothing here is claimed, and the strategy-page poll still
// triggers the run the moment the user opens the app. Failing an
// entitlement-critical webhook for a backstop would be the worse trade.
func (s *service) triggerSIEBackstop(ctx context.Context, userID primitive.ObjectID) {
	found, company, err := s.store.FindPersonalCompanyByOwner(ctx, userID)
	if err != nil {
		log.Error("sie backstop: personal company lookup failed", "error", err, "user_id", userID.Hex())
		return
	}
	if !found {
		// Personal companies are minted at signup; absence is a data gap, not a
		// webhook problem.
		log.Warn("sie backstop: buyer has no personal company — skipped", "user_id", userID.Hex())
		return
	}

	found, entity, err := s.store.FindWebEntityForCompany(ctx, company.ID, userID)
	if err != nil {
		log.Error("sie backstop: web entity lookup failed", "error", err,
			"user_id", userID.Hex(), "company_id", company.ID.Hex())
		return
	}
	if !found || !entity.Finalised {
		// Paid before finishing onboarding — the strategy poll triggers the run
		// once they finalise.
		return
	}

	// Any WEC — live, done, or errored — means SIE already owns this entity
	// (the poll resumes errored runs, and the expand above claims trial ones).
	wecFound, _, err := s.store.FindWebEntityContextByWebEntityAndUser(ctx, entity.ID, userID)
	if err != nil {
		log.Error("sie backstop: web entity context lookup failed", "error", err,
			"user_id", userID.Hex(), "web_entity_id", entity.ID.Hex())
		return
	}
	if wecFound {
		return
	}

	if err := s.dispatcher.Dispatch(ctx, string(sie.ProcessSiteIntelligence), userID.Hex(),
		sieBackstopPayload{WebEntityID: entity.ID.Hex()}); err != nil {
		log.Error("sie backstop: dispatch failed — cold start falls back to the strategy poll",
			"error", err, "user_id", userID.Hex(), "web_entity_id", entity.ID.Hex())
		return
	}
	log.Info("sie backstop dispatched", "user_id", userID.Hex(),
		"company_id", company.ID.Hex(), "web_entity_id", entity.ID.Hex())
}

// mapPaddleSubscriptionStatus maps a Paddle subscription payload to our
// internal status. Unknown statuses are stored raw (readers treat anything
// non-valid by valid_till anyway) with a warning.
func mapPaddleSubscriptionStatus(data dto.WebhookSubscription) string {
	if data.Status == paddleStatusActive && data.ScheduledChange != nil && data.ScheduledChange.Action == "cancel" {
		return models.SubStatusCanceling
	}
	switch data.Status {
	case paddleStatusActive:
		return models.SubStatusActive
	case paddleStatusTrialing:
		// No longer collapsed to active: the trial/paid distinction drives
		// feature lists. Access-safe at any time — the gate is status-agnostic
		// (valid_till only).
		return models.SubStatusTrialing
	case paddleStatusPastDue:
		return models.SubStatusPastDue
	case paddleStatusCanceled:
		return models.SubStatusCanceled
	case paddleStatusPaused:
		return models.SubStatusPaused
	default:
		log.Warn("paddle webhook: unknown subscription status stored raw", "status", data.Status)
		return data.Status
	}
}

func (s *service) applyTransactionEvent(ctx context.Context, envelope dto.WebhookEnvelope) (string, error) {
	var data dto.WebhookTransaction
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		return fmt.Sprintf("malformed transaction payload: %v", err), nil
	}
	if data.ID == "" {
		return "transaction payload missing id", nil
	}

	status := models.TxnStatusCompleted
	if envelope.EventType == dto.EventTransactionPaymentFailed {
		status = models.TxnStatusFailed
	}

	// Resolve the owning user: subscription doc first, then the customer
	// mapping (always present for checkout-created customers thanks to
	// pre-create), then custom_data. Paddle does not guarantee the
	// subscription.created event arrives before its transactions.
	var (
		userID            primitive.ObjectID
		subscriptionMongo *primitive.ObjectID
	)
	if data.SubscriptionID != "" {
		found, sub, err := s.store.FindSubscriptionByPaddleID(ctx, data.SubscriptionID)
		if err != nil {
			return "", err
		}
		if found {
			userID = sub.UserID
			subscriptionMongo = &sub.ID
		}
	}
	if userID.IsZero() {
		resolved, note, err := s.resolveUserID(ctx, data.CustomerID, data.CustomData, data.ID)
		if err != nil || note != "" {
			return note, err
		}
		userID = resolved
	}

	set := bson.M{
		"paddle_subscription_id": data.SubscriptionID,
		"status":                 status,
		"amount_total":           data.Details.Totals.Total,
		"currency_code":          data.CurrencyCode,
	}
	if subscriptionMongo != nil {
		set["subscription_id"] = *subscriptionMongo
	}
	if data.BilledAt != nil {
		set["billed_at"] = data.BilledAt.UTC()
	}
	if data.InvoiceNumber != "" {
		set["invoice_number"] = data.InvoiceNumber
	}

	// Upsert, not insert-and-skip: Paddle retries a failed payment on the
	// SAME transaction ID, so a later transaction.completed must overwrite
	// the earlier failed row (last_event_at guard keeps the order honest).
	applied, err := s.store.ApplyTransactionEvent(ctx, data.ID, envelope.OccurredAt, bson.M{
		"$set":         set,
		"$setOnInsert": bson.M{"user_id": userID},
	})
	if err != nil {
		return "", err
	}
	if !applied {
		log.Info("paddle webhook: stale transaction event skipped by ordering guard",
			"event_id", envelope.EventID, "paddle_transaction_id", data.ID)
	}
	return "", nil
}

// resolveUserID maps a webhook payload to an app user: paddle_customers
// lookup by customer_id first (always present thanks to pre-create), then
// custom_data.userId. When neither resolves (dashboard-created entities,
// simulator payloads), it returns a note so the event is recorded and acked
// instead of retry-looping for days.
func (s *service) resolveUserID(ctx context.Context, customerID string, customData map[string]any, entityID string) (primitive.ObjectID, string, error) {
	if customerID != "" {
		found, customer, err := s.store.FindCustomerByPaddleID(ctx, customerID)
		if err != nil {
			return primitive.NilObjectID, "", err
		}
		if found {
			return customer.UserID, "", nil
		}
	}
	if userID, ok := userIDFromCustomData(customData); ok {
		return userID, "", nil
	}
	return primitive.NilObjectID,
		fmt.Sprintf("unresolvable user for entity %s (customer %s): no mapping, no custom_data.userId", entityID, customerID),
		nil
}

func userIDFromCustomData(customData map[string]any) (primitive.ObjectID, bool) {
	raw, ok := customData["userId"].(string)
	if !ok || raw == "" {
		return primitive.NilObjectID, false
	}
	id, err := primitive.ObjectIDFromHex(raw)
	if err != nil {
		return primitive.NilObjectID, false
	}
	return id, true
}

// companyIDFromCustomData parses custom_data.companyId — the marker the
// company checkout stamps on team subscriptions. Absent or unparseable means
// "individual subscription" (decision 7: garbage falls through, it never
// errors); only a VALID id enters the owner-verified linkage path.
func companyIDFromCustomData(customData map[string]any) (primitive.ObjectID, bool) {
	raw, ok := customData["companyId"].(string)
	if !ok || raw == "" {
		return primitive.NilObjectID, false
	}
	id, err := primitive.ObjectIDFromHex(raw)
	if err != nil {
		return primitive.NilObjectID, false
	}
	return id, true
}
