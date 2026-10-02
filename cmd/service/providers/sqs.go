package providers

import (
	"fmt"

	"github.com/atharva-ng/crunch/internal/config"
	sqsprovider "github.com/atharva-ng/crunch/internal/providers/impl/sqs"
)

// InjectDefaultSQSProvider wires the SQS work queue. There is no fallback: if
// the queue can't be initialised, startup must fail loudly rather than
// silently swallow every dispatched job.
func InjectDefaultSQSProvider(appCtx *config.AppContext) error {
	q, err := sqsprovider.NewSQS(appCtx.Config.AWS, appCtx.Config.SQS)
	if err != nil {
		return fmt.Errorf("init SQS provider: %w", err)
	}
	appCtx.QueueProvider = q
	return nil
}
