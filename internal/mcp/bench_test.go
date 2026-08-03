package mcp

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// BenchmarkNew measures the cost of constructing one fully registered MCP
// server — the per-request cost of the stateless HTTP transport. It is
// reference evidence, not a CI gate (`go test` never runs benchmarks): the
// CI-gated guard is the pointer-identity suite in schema_cache_test.go.
//
//	no-cache:     what stdio pays once, and what HTTP paid per request
//	              before Options.SchemaCache existed.
//	shared-cache: the HTTP steady state — one process-wide cache, schemas
//	              resolved on the first request only.
func BenchmarkNew(b *testing.B) {
	deps := Deps{Version: "bench"}
	b.Run("no-cache", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			New(deps, Options{})
		}
	})
	b.Run("shared-cache", func(b *testing.B) {
		cache := mcp.NewSchemaCache()
		New(deps, Options{SchemaCache: cache}) // warm, as the first request would
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			New(deps, Options{SchemaCache: cache})
		}
	})
}
