// Package mail is the platform module that owns the mail.send async job
// (D-103). Feature code never calls Mailer.Send inline: it calls Enqueue, and
// the job sends with SQS retries, so a mail failure never fails the caller.
package mail

import (
	"context"
	"errors"
	"fmt"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/modules"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/impl/mailer"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	asynchandler "github.com/atharva-ng/crunch/internal/services/asyncHandler"
)

// ProcessSend is the mail.send job.
const ProcessSend pipeline.ProcessType = "mail.send"

// systemUserID owns mail jobs (they belong to no single user's quota).
const systemUserID = "system"

// Module registers the mail.send handler.
type Module struct {
	modules.Base
	appCtx *config.AppContext
}

var _ modules.Module = (*Module)(nil)

// New builds the mail module. appCtx is read at call time, never here.
func New(appCtx *config.AppContext) *Module {
	return &Module{appCtx: appCtx}
}

func (m *Module) Name() string { return "mail" }

func (m *Module) RegisterHandlers(reg asynchandler.Registry) {
	reg.Register(ProcessSend, asynchandler.Typed(func(ctx context.Context, _ string, p dto.Mail) error {
		return Send(ctx, m.appCtx.InternalServices.Mailer, p)
	}))
}

// Send is the mail.send handler body. A disabled mailer or an invalid mail
// can never succeed on retry, so both are permanent; transport errors retry.
func Send(ctx context.Context, mlr interfaces.Mailer, p dto.Mail) error {
	if mlr == nil || !mlr.Enabled() {
		return fmt.Errorf("mail.send: %w: %w", mailer.ErrMailerDisabled, pipeline.ErrPermanent)
	}
	if err := mailer.Validate(p); err != nil {
		return fmt.Errorf("mail.send: %w: %w", err, pipeline.ErrPermanent)
	}
	if err := mlr.Send(ctx, p); err != nil {
		if errors.Is(err, mailer.ErrMailerDisabled) {
			return fmt.Errorf("mail.send: %w: %w", err, pipeline.ErrPermanent)
		}
		return fmt.Errorf("mail.send: %w", err)
	}
	return nil
}

// Enqueue dispatches one mail.send job. Callers log a returned error and carry
// on — mail is best-effort (D-103).
func Enqueue(ctx context.Context, d interfaces.Dispatcher, m dto.Mail) error {
	if err := mailer.Validate(m); err != nil {
		return err
	}
	return d.Dispatch(ctx, string(ProcessSend), systemUserID, m)
}
