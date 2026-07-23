// Package wait provides a generic context-aware polling loop with
// exponential backoff, full jitter, and an overall timeout. It is the
// engine behind `--wait` flags.
package wait

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

// ErrTimeout is returned when Options.Timeout elapses before fn reports done.
var ErrTimeout = errors.New("timed out waiting")

// Options tunes a Poll loop. Zero values select the defaults; the function
// fields exist so tests can inject a deterministic jitter, sleeper, and clock.
type Options struct {
	Initial time.Duration // first backoff delay (default 2s)
	Factor  float64       // backoff multiplier (default 1.5)
	Cap     time.Duration // backoff ceiling (default 30s)
	Timeout time.Duration // overall budget; 0 = poll forever

	// Jitter maps the scheduled delay to the actual sleep. nil = full jitter:
	// a uniform draw from (0, d].
	Jitter func(d time.Duration) time.Duration
	// Sleep blocks for d or until ctx is done (returning ctx.Err()).
	// nil = timer-based sleep.
	Sleep func(ctx context.Context, d time.Duration) error
	// Now is the clock used for the Timeout budget. nil = time.Now.
	Now func() time.Time
}

// Poll calls fn immediately and then on a backoff schedule until fn reports
// done, fn returns an error, ctx is canceled, or the Timeout budget runs out
// (ErrTimeout). When less than one full delay remains in the budget the sleep
// is clamped so a final poll runs at the deadline.
func Poll(ctx context.Context, opts Options, fn func(ctx context.Context) (done bool, err error)) error {
	initial := opts.Initial
	if initial <= 0 {
		initial = 2 * time.Second
	}
	factor := opts.Factor
	if factor <= 1 {
		factor = 1.5
	}
	ceiling := opts.Cap
	if ceiling <= 0 {
		ceiling = 30 * time.Second
	}
	jitter := opts.Jitter
	if jitter == nil {
		jitter = fullJitter
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	pollCtx := ctx
	cancel := func() {}
	if opts.Timeout > 0 {
		pollCtx, cancel = context.WithTimeoutCause(ctx, opts.Timeout, ErrTimeout)
	}
	defer cancel()

	var deadline time.Time
	if opts.Timeout > 0 {
		deadline = now().Add(opts.Timeout)
	}

	delay := initial
	for {
		if err := pollContextError(ctx, pollCtx); err != nil {
			return err
		}
		done, err := fn(pollCtx)
		if ctxErr := pollContextError(ctx, pollCtx); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if !deadline.IsZero() && !now().Before(deadline) {
			return ErrTimeout
		}

		d := jitter(delay)
		if !deadline.IsZero() {
			if remaining := deadline.Sub(now()); d > remaining {
				d = remaining
			}
		}
		if err := sleep(pollCtx, d); err != nil {
			if ctxErr := pollContextError(ctx, pollCtx); ctxErr != nil {
				return ctxErr
			}
			return err
		}
		delay = time.Duration(float64(delay) * factor)
		if delay > ceiling {
			delay = ceiling
		}
	}
}

// pollContextError distinguishes Poll's own timeout from cancellation or a
// deadline inherited from the caller. Context APIs surface both timeout kinds
// as context.DeadlineExceeded, while callers of Poll need the former mapped to
// ErrTimeout and the latter preserved.
func pollContextError(parent, pollCtx context.Context) error {
	if pollCtx.Err() == nil {
		return nil
	}
	if errors.Is(context.Cause(pollCtx), ErrTimeout) {
		return ErrTimeout
	}
	if err := parent.Err(); err != nil {
		return err
	}
	return pollCtx.Err()
}

// fullJitter draws uniformly from (0, d].
func fullJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(d))) + 1
}

// sleepContext blocks for d or until ctx is done, returning ctx.Err() when
// interrupted so a canceled wait never runs out the delay.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
