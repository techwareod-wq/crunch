package providers

import (
	"fmt"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	sqsprovider "github.com/atharva-ng/crunch/internal/providers/impl/sqs"
	"github.com/atharva-ng/crunch/internal/tokentracker"
)

// InjectDefaultSQSProvider wires the primary and secondary (gated) SQS queues.
// There is no fallback: if either queue can't be initialised, startup must fail
// loudly rather than silently swallow every dispatched job.
func InjectDefaultSQSProvider(appCtx *config.AppContext, tracker *tokentracker.Tracker) error {
	q, err := sqsprovider.NewSQS(appCtx.Config.AWS, appCtx.Config.SQS)
	if err != nil {
		return fmt.Errorf("init primary SQS provider: %w", err)
	}
	appCtx.QueueProvider = q

	secondaryCfg := appCtx.Config.SQS
	secondaryCfg.QueueURL = appCtx.Config.SQS.SecondaryQueueURL

	sq, err := sqsprovider.NewGatedSQS(appCtx.Config.AWS, secondaryCfg, tracker.CanConsume, time.Duration(appCtx.Config.Values.SQS.GatedPauseSeconds)*time.Second)
	if err != nil {
		return fmt.Errorf("init secondary SQS provider: %w", err)
	}
	appCtx.SecondaryQueueProvider = sq
	return nil
}
