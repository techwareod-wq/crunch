package tokentracker

import (
	"sync"
	"time"

	"github.com/atharva-ng/crunch/internal/util/log"
)

// Tracker tracks LLM token usage over a fixed time window and gates
// queue consumption when usage exceeds the configured limit.
type Tracker struct {
	mu            sync.Mutex
	tokens        int
	limit         int
	resetInterval time.Duration
	stopCh        chan struct{}
}

// New creates a Tracker that resets every resetInterval and blocks consumption
// when cumulative tokens reach limit.
func New(limit int, resetInterval time.Duration) *Tracker {
	return &Tracker{
		limit:         limit,
		resetInterval: resetInterval,
		stopCh:        make(chan struct{}),
	}
}

// Add records tokens consumed by an LLM call.
func (t *Tracker) Add(tokens int) {
	t.mu.Lock()
	t.tokens += tokens
	total := t.tokens
	t.mu.Unlock()
	log.Info("token tracker: added tokens", "added", tokens, "total", total)
}

// CanConsume returns true if token usage is below the limit.
func (t *Tracker) CanConsume() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tokens < t.limit
}

// Start begins the fixed-interval reset ticker. Call Stop to clean up.
func (t *Tracker) Start() {
	ticker := time.NewTicker(t.resetInterval)
	go func() {
		for {
			select {
			case <-ticker.C:
				t.mu.Lock()
				prev := t.tokens
				t.tokens = 0
				t.mu.Unlock()
				if prev > 0 {
					log.Info("token tracker: reset", "previousTokens", prev)
				}
			case <-t.stopCh:
				ticker.Stop()
				return
			}
		}
	}()
}

// Stop shuts down the reset ticker.
func (t *Tracker) Stop() {
	close(t.stopCh)
}
