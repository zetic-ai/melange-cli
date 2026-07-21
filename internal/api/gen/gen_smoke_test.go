// Hand-written smoke test for the generated client (client.gen.go is the
// only generated file in this package; this file is safe from `make gen`).
//
// The references below are compile-time assertions that the vendored spec
// still produces the operations the CLI will be wired to in C2. If a
// backend spec regeneration renames or drops one, this file stops
// compiling — which is the point.
package gen

import (
	"context"
	"testing"
)

// Compile-time assertions that the expected operations exist with the
// expected shapes on both the raw Client and ClientWithResponses.
var (
	_ func(context.Context, ...RequestEditorFn) (*GetMeResult, error)                                                 = (*ClientWithResponses)(nil).GetMeWithResponse
	_ func(context.Context, *ListReposParams, ...RequestEditorFn) (*ListReposResult, error)                           = (*ClientWithResponses)(nil).ListReposWithResponse
	_ func(context.Context, CreateRepoJSONRequestBody, ...RequestEditorFn) (*CreateRepoResult, error)                 = (*ClientWithResponses)(nil).CreateRepoWithResponse
	_ func(context.Context, string, string, ...RequestEditorFn) (*GetRepoResult, error)                               = (*ClientWithResponses)(nil).GetRepoWithResponse
	_ func(context.Context, string, string, UpdateRepoJSONRequestBody, ...RequestEditorFn) (*UpdateRepoResult, error) = (*ClientWithResponses)(nil).UpdateRepoWithResponse
	_ func(context.Context, string, string, ...RequestEditorFn) (*DeleteRepoResult, error)                            = (*ClientWithResponses)(nil).DeleteRepoWithResponse
	_ ClientInterface                                                                                                 = (*Client)(nil)
	_ ClientWithResponsesInterface                                                                                    = (*ClientWithResponses)(nil)
)

func TestNewClientConstructs(t *testing.T) {
	client, err := NewClientWithResponses("https://api.zetic.ai")
	if err != nil {
		t.Fatalf("NewClientWithResponses: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
}
