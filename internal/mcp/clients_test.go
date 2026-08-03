package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
)

func TestStaticProviderResolvesOnceAndCaches(t *testing.T) {
	calls := 0
	want := &gen.ClientWithResponses{}
	p := NewStaticProvider(func() (*gen.ClientWithResponses, error) {
		calls++
		return want, nil
	})

	ctx := context.Background()
	for range 3 {
		got, err := p.Client(ctx)
		require.NoError(t, err)
		assert.Same(t, want, got)
	}
	assert.Equal(t, 1, calls, "resolve runs exactly once")
}

func TestStaticProviderReturnsResolveFailureOnEveryCall(t *testing.T) {
	calls := 0
	resolveErr := errors.New("not logged in")
	p := NewStaticProvider(func() (*gen.ClientWithResponses, error) {
		calls++
		return nil, resolveErr
	})

	ctx := context.Background()
	for range 3 {
		got, err := p.Client(ctx)
		assert.Nil(t, got)
		assert.ErrorIs(t, err, resolveErr, "the failure is returned on every call, not fatal")
	}
	assert.Equal(t, 3, calls, "a failed resolve is retried, never cached")
}

// TestStaticProviderRetriesAfterFailureThenCachesSuccess pins the reason
// failures are not cached: a user who starts their MCP client logged out, sees
// the auth error, and then runs `melange auth login` must be served a working
// client on the next tool call — without restarting the server. Once resolve
// succeeds the result is cached, so recovery costs exactly one extra resolve.
func TestStaticProviderRetriesAfterFailureThenCachesSuccess(t *testing.T) {
	calls := 0
	resolveErr := errors.New("not logged in")
	want := &gen.ClientWithResponses{}
	p := NewStaticProvider(func() (*gen.ClientWithResponses, error) {
		calls++
		if calls == 1 {
			return nil, resolveErr
		}
		return want, nil
	})

	ctx := context.Background()
	got, err := p.Client(ctx)
	assert.Nil(t, got)
	require.ErrorIs(t, err, resolveErr)

	// The credentials appeared between calls: the next call retries and wins.
	got, err = p.Client(ctx)
	require.NoError(t, err)
	assert.Same(t, want, got)

	// And that success is cached — no third resolve.
	got, err = p.Client(ctx)
	require.NoError(t, err)
	assert.Same(t, want, got)
	assert.Equal(t, 2, calls, "resolve retried once after failure, then cached")
}

func TestStaticProviderConcurrentAccess(t *testing.T) {
	calls := 0
	p := NewStaticProvider(func() (*gen.ClientWithResponses, error) {
		calls++
		return &gen.ClientWithResponses{}, nil
	})

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Client(context.Background())
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, calls)
}
