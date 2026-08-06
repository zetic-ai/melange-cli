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

// StaticProvider resolves a client lazily and caches only success: once a
// resolve succeeds, every later call serves the same client without touching
// the credential store again.
//
// A resolve failure is deliberately not fatal — the MCP server keeps running
// and the tool call reports the actionable error — and it is deliberately not
// cached. A client started while logged out would otherwise keep replaying the
// "run melange auth login" error for the rest of the process lifetime, even
// after the user logs in. Retrying costs one keyring/env read on a single
// stdio session, so there is no thundering herd to protect against.
type StaticProvider struct {
	resolve func() (*gen.ClientWithResponses, error)

	mu     sync.Mutex
	client *gen.ClientWithResponses
}

// NewStaticProvider returns a StaticProvider that resolves the client via
// resolve on first use, and again on every use until resolve succeeds.
func NewStaticProvider(resolve func() (*gen.ClientWithResponses, error)) *StaticProvider {
	return &StaticProvider{resolve: resolve}
}

// Client implements ClientProvider.
func (p *StaticProvider) Client(_ context.Context) (*gen.ClientWithResponses, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.client != nil {
		return p.client, nil
	}
	client, err := p.resolve()
	if err != nil {
		return nil, err
	}
	p.client = client
	return p.client, nil
}
