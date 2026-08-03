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
	assert.Equal(t, 1, calls, "a failed resolve is cached, not retried")
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
