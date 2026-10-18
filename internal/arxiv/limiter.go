package arxiv

import (
	"context"
	"time"
)

// All clients in the server share one gate, including retries. It is held
// through response-body consumption. Cooldown after completion is conservative
// and guarantees both a single connection and >=3 seconds between requests.
var sharedGate = make(chan struct{}, 1)
var nextRequest time.Time // accessed only while holding sharedGate

func acquire(ctx context.Context) error {
	select {
	case sharedGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func pause(ctx context.Context, duration time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
