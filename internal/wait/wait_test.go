package wait_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/wait"
)

// identity jitter makes the schedule deterministic.
func identity(d time.Duration) time.Duration { return d }

// fakeClock advances only when the fake sleeper runs.
type fakeClock struct {
	now    time.Time
	slept  []time.Duration
	ctxErr error
}

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if c.ctxErr != nil {
		return c.ctxErr
	}
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
	return ctx.Err()
}

func opts(c *fakeClock) wait.Options {
	return wait.Options{
		Initial: 2 * time.Second,
		Factor:  1.5,
		Cap:     30 * time.Second,
		Jitter:  identity,
		Sleep:   c.sleep,
		Now:     func() time.Time { return c.now },
	}
}

func TestPollImmediateDoneNeverSleeps(t *testing.T) {
	c := &fakeClock{now: time.Unix(0, 0)}
	calls := 0
	err := wait.Poll(context.Background(), opts(c), func(context.Context) (bool, error) {
		calls++
		return true, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.Empty(t, c.slept)
}

func TestPollBackoffScheduleGrowsAndCaps(t *testing.T) {
	c := &fakeClock{now: time.Unix(0, 0)}
	calls := 0
	err := wait.Poll(context.Background(), opts(c), func(context.Context) (bool, error) {
		calls++
		return calls == 12, nil
	})
	require.NoError(t, err)
	require.Len(t, c.slept, 11)
	// 2s * 1.5^n, capped at 30s.
	assert.Equal(t, 2*time.Second, c.slept[0])
	assert.Equal(t, 3*time.Second, c.slept[1])
	assert.Equal(t, 4500*time.Millisecond, c.slept[2])
	assert.Equal(t, 30*time.Second, c.slept[10], "delay must cap at Cap")
}

func TestPollTimeout(t *testing.T) {
	c := &fakeClock{now: time.Unix(0, 0)}
	o := opts(c)
	o.Timeout = 10 * time.Second
	calls := 0
	err := wait.Poll(context.Background(), o, func(context.Context) (bool, error) {
		calls++
		return false, nil
	})
	require.ErrorIs(t, err, wait.ErrTimeout)
	// 2s + 3s + 4.5s = 9.5s; the next sleep is clamped to the 0.5s remaining,
	// one final poll runs at the deadline, then ErrTimeout.
	assert.Equal(t, 500*time.Millisecond, c.slept[len(c.slept)-1])
	assert.Equal(t, 5, calls)
}

func TestPollFnErrorStops(t *testing.T) {
	c := &fakeClock{now: time.Unix(0, 0)}
	boom := errors.New("boom")
	calls := 0
	err := wait.Poll(context.Background(), opts(c), func(context.Context) (bool, error) {
		calls++
		if calls == 2 {
			return false, boom
		}
		return false, nil
	})
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 2, calls)
}

func TestPollContextCanceledDuringSleep(t *testing.T) {
	c := &fakeClock{now: time.Unix(0, 0), ctxErr: context.Canceled}
	err := wait.Poll(context.Background(), opts(c), func(context.Context) (bool, error) {
		return false, nil
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestPollCanceledContextNeverCallsFn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	c := &fakeClock{now: time.Unix(0, 0)}
	err := wait.Poll(ctx, opts(c), func(context.Context) (bool, error) {
		calls++
		return true, nil
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, calls)
}

func TestPollDefaultsAreApplied(t *testing.T) {
	// Only Sleep/Now/Jitter injected: defaults Initial=2s, Factor=1.5, Cap=30s.
	c := &fakeClock{now: time.Unix(0, 0)}
	calls := 0
	err := wait.Poll(context.Background(), wait.Options{
		Jitter: identity,
		Sleep:  c.sleep,
		Now:    func() time.Time { return c.now },
	}, func(context.Context) (bool, error) {
		calls++
		return calls == 3, nil
	})
	require.NoError(t, err)
	require.Equal(t, []time.Duration{2 * time.Second, 3 * time.Second}, c.slept)
}
