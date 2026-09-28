// Package mailer implements the interfaces.Mailer over plain SMTP
// (STARTTLS via net/smtp). Deliberately minimal: short plain-text
// transactional mails, with delivery treated as best-effort by callers.
package mailer

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

// ErrMailerDisabled: no SMTP transport is configured (SMTP_HOST unset).
var ErrMailerDisabled = fmt.Errorf("mailer disabled: SMTP_HOST not configured")

type smtpMailer struct {
	cfg config.MailerConfig
}

// New builds a Mailer from the SMTP config. An empty Host yields the
// disabled impl — callers keep working, sends return ErrMailerDisabled.
func New(cfg config.MailerConfig) interfaces.Mailer {
	return &smtpMailer{cfg: cfg}
}

func (m *smtpMailer) Enabled() bool {
	return m.cfg.Host != "" && m.cfg.From != ""
}

func (m *smtpMailer) Send(ctx context.Context, to, subject, body string) error {
	if !m.Enabled() {
		return ErrMailerDisabled
	}
	to = strings.TrimSpace(to)
	if to == "" || strings.ContainsAny(to, "\r\n") || strings.ContainsAny(subject, "\r\n") {
		return fmt.Errorf("mailer: invalid recipient or subject")
	}

	msg := strings.Join([]string{
		"From: " + m.cfg.From,
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		body,
	}, "\r\n")

	addr := m.cfg.Host + ":" + m.cfg.Port
	var auth smtp.Auth
	if m.cfg.Username != "" {
		auth = smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
	}

	done := make(chan error, 1)
	go func() {
		done <- smtp.SendMail(addr, auth, m.cfg.From, []string{to}, []byte(msg))
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("mailer send to %s: %w", to, err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("mailer send to %s: %w", to, ctx.Err())
	}
}
