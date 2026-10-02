package interfaces

import (
	"context"

	"github.com/atharva-ng/crunch/internal/dto"
)

// Mailer sends transactional email. Implementations must be safe for
// concurrent use. A deployment with no mail transport (empty SMTP_HOST) still
// gets a Mailer whose Enabled() is false — callers treat sending as
// best-effort. Feature code should not call Send directly: enqueue a
// mail.send job (internal/modules/mail) so a mail failure never fails the
// caller (D-103).
type Mailer interface {
	// Send delivers one email to every recipient in m.To.
	Send(ctx context.Context, m dto.Mail) error
	// Enabled reports whether a real transport is configured — lets callers
	// phrase "we emailed them" vs "share this link" honestly.
	Enabled() bool
}
