package interfaces

import "context"

// Mailer sends transactional email. Implementations must be safe for
// concurrent use. A deployment with no mail transport (empty SMTP_HOST) still
// gets a Mailer whose Enabled() is false — callers treat sending as
// best-effort.
type Mailer interface {
	// Send delivers one plain-text email.
	Send(ctx context.Context, to, subject, body string) error
	// Enabled reports whether a real transport is configured — lets callers
	// phrase "we emailed them" vs "share this link" honestly.
	Enabled() bool
}
