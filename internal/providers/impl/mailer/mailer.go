// Package mailer implements the interfaces.Mailer over SMTP (STARTTLS via
// net/smtp) — in production the SES SMTP endpoint, no SDK (D-103). Messages
// are plain text, HTML, or multipart/alternative when both are given.
package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"net/smtp"
	"strings"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

// ErrMailerDisabled: no SMTP transport is configured (SMTP_HOST unset).
var ErrMailerDisabled = fmt.Errorf("mailer disabled: SMTP_HOST not configured")

// maxRecipients bounds one send; every transactional mail here has 1–2.
const maxRecipients = 50

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

func (m *smtpMailer) Send(ctx context.Context, msg dto.Mail) error {
	if !m.Enabled() {
		return ErrMailerDisabled
	}
	if err := Validate(msg); err != nil {
		return err
	}
	raw, err := BuildMessage(m.cfg.From, msg, newBoundary())
	if err != nil {
		return err
	}

	addr := m.cfg.Host + ":" + m.cfg.Port
	var auth smtp.Auth
	if m.cfg.Username != "" {
		auth = smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
	}
	envelopeFrom := m.cfg.From
	if a, err := mail.ParseAddress(m.cfg.From); err == nil {
		envelopeFrom = a.Address
	}

	done := make(chan error, 1)
	go func() {
		done <- smtp.SendMail(addr, auth, envelopeFrom, msg.To, raw)
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("mailer send to %v: %w", msg.To, err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("mailer send to %v: %w", msg.To, ctx.Err())
	}
}

// Validate rejects a mail that can never be sent: no or too many recipients,
// a malformed address, header injection (CR/LF), or no body.
func Validate(msg dto.Mail) error {
	if len(msg.To) == 0 || len(msg.To) > maxRecipients {
		return fmt.Errorf("mailer: need 1..%d recipients, got %d", maxRecipients, len(msg.To))
	}
	for _, to := range append([]string{msg.ReplyTo}, msg.To...) {
		if to == "" {
			continue
		}
		if strings.ContainsAny(to, "\r\n") {
			return fmt.Errorf("mailer: invalid address")
		}
		if _, err := mail.ParseAddress(to); err != nil {
			return fmt.Errorf("mailer: invalid address %q: %w", to, err)
		}
	}
	if strings.ContainsAny(msg.Subject, "\r\n") {
		return fmt.Errorf("mailer: invalid subject")
	}
	if msg.Text == "" && msg.HTML == "" {
		return fmt.Errorf("mailer: empty body")
	}
	return nil
}

// BuildMessage renders the RFC 5322 message. The subject is RFC 2047
// encoded and bodies are quoted-printable, so non-ASCII text and long lines
// survive SMTP. boundary is injected for deterministic tests.
func BuildMessage(from string, msg dto.Mail, boundary string) ([]byte, error) {
	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", from)
	header("To", strings.Join(msg.To, ", "))
	if msg.ReplyTo != "" {
		header("Reply-To", msg.ReplyTo)
	}
	header("Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	header("MIME-Version", "1.0")

	switch {
	case msg.Text != "" && msg.HTML != "":
		header("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
		b.WriteString("\r\n")
		for _, part := range []struct{ ctype, body string }{
			{"text/plain", msg.Text},
			{"text/html", msg.HTML},
		} {
			b.WriteString("--" + boundary + "\r\n")
			if err := writePart(&b, part.ctype, part.body); err != nil {
				return nil, err
			}
			b.WriteString("\r\n")
		}
		b.WriteString("--" + boundary + "--\r\n")
	case msg.HTML != "":
		if err := writePart(&b, "text/html", msg.HTML); err != nil {
			return nil, err
		}
	default:
		if err := writePart(&b, "text/plain", msg.Text); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

// writePart writes the part headers, a blank line, then the
// quoted-printable body.
func writePart(b *bytes.Buffer, ctype, body string) error {
	b.WriteString("Content-Type: " + ctype + "; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	qp := quotedprintable.NewWriter(b)
	if _, err := qp.Write([]byte(body)); err != nil {
		return err
	}
	return qp.Close()
}

func newBoundary() string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return "crunch-" + hex.EncodeToString(buf[:])
}
