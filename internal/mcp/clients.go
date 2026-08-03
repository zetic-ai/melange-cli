package mcp

import (
	"context"
	"sync"

	"github.com/zetic-ai/melange-cli/internal/api/gen"
)

// ClientProvider resolves the generated API client tool handlers call.
// Resolution may fail (e.g. no token configured); handlers surface that
// failure per tool call rather than tearing down the server.
type ClientProvider interface {
	Client(ctx context.Context) (*gen.ClientWithResponses, error)
}

// StaticProvider resolves a client lazily, exactly once, and serves the
// cached client — or the cached resolve error — on every subsequent call.
// A resolve failure is deliberately not fatal: the MCP server keeps running
// and every tool call reports the same actionable error.
type StaticProvider struct {
	resolve func() (*gen.ClientWithResponses, error)

	once   sync.Once
	client *gen.ClientWithResponses
	err    error
}

// NewStaticProvider returns a StaticProvider that resolves the client via
// resolve on first use.
func NewStaticProvider(resolve func() (*gen.ClientWithResponses, error)) *StaticProvider {
	return &StaticProvider{resolve: resolve}
}

// Client implements ClientProvider.
func (p *StaticProvider) Client(_ context.Context) (*gen.ClientWithResponses, error) {
	p.once.Do(func() {
		p.client, p.err = p.resolve()
	})
	return p.client, p.err
}
