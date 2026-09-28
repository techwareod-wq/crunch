package interfaces

import "context"

// Mailer sends transactional email (the company invite-accept links —
// tenancy plan D3/D19). Implementations must be safe for concurrent use. A
// deployment with no mail transport wires the disabled impl, which returns
// ErrMailerDisabled — callers treat sending as best-effort and surface the
// link through the API response instead.
type Mailer interface {
	// Send delivers one plain-text email.
	Send(ctx context.Context, to, subject, body string) error
	// Enabled reports whether a real transport is configured — lets callers
	// phrase "we emailed them" vs "share this link" honestly.
	Enabled() bool
}
