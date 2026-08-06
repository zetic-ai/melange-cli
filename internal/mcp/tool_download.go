package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
)

// redactedURL replaces signed artifact URLs in the default output of
// request_model_download, mirroring `melange model download --json`: signed
// URLs are short-lived credentials and must never land in an agent transcript
// unless the caller explicitly asks for them.
const redactedURL = "<redacted>"

// registerDownload registers the billable download-authorization tool.
func registerDownload(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "request_model_download",
		Description: "Authorize downloading one converted target's artifacts. This is BILLABLE: " +
			"the target's size counts against the account's bandwidth quota, also for public models " +
			"owned by others — ask the user for explicit consent, then pass confirm: true. The " +
			"result lists the artifacts with every signed url replaced by \"<redacted>\"; " +
			"include_urls: true returns the real short-lived URLs into the transcript instead. " +
			"Every confirmed call is charged separately, so if a call fails ambiguously (timeout " +
			"or connection error) check with the user before retrying. " +
			"When the user is on the machine that should hold the files, tell them to run " +
			"'melange model download' instead: it verifies checksums and writes the files for them. " +
			"Take model_key from list_models and target_id from get_model with include_targets.",
		OutputSchema: outputSchema("request_model_download"),
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint:  false,
			DestructiveHint: falsePtr(),
			OpenWorldHint:   falsePtr(),
		},
	}, requestModelDownloadHandler(d))
}

// requestModelDownloadArgs are the arguments of request_model_download.
// Confirm is deliberately optional in the schema: an agent that omits it must
// get the consent instructions below, not a bare schema-validation failure.
type requestModelDownloadArgs struct {
	Repo        string `json:"repo" jsonschema:"Repository in ACCOUNT/NAME form (example: zetic/whisper-tiny)."`
	ModelKey    string `json:"model_key" jsonschema:"Opaque model key from list_models."`
	TargetID    string `json:"target_id" jsonschema:"Opaque target id from get_model with include_targets."`
	Confirm     bool   `json:"confirm,omitempty" jsonschema:"Set to true to confirm this billable download. Only send it after the user has explicitly consented to spending bandwidth quota."`
	IncludeURLs bool   `json:"include_urls,omitempty" jsonschema:"Return the real signed artifact URLs instead of \"<redacted>\". They are short-lived download credentials and will appear in the transcript."`
}

// requestModelDownloadHandler wraps POST
// .../models/{key}/targets/{id}/download-authorizations behind a confirmation
// gate that must pass before any request is made — the request itself is what
// gets charged.
func requestModelDownloadHandler(d Deps) mcp.ToolHandlerFor[requestModelDownloadArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in requestModelDownloadArgs) (*mcp.CallToolResult, any, error) {
		account, name, err := splitRepo(in.Repo)
		if err != nil {
			return toolError(err), nil, nil
		}
		if !in.Confirm {
			return toolError(fmt.Errorf(
				"request_model_download refused: confirm is not true, so nothing was authorized "+
					"and nothing was charged. Downloading target %s counts against the account's "+
					"bandwidth quota: obtain explicit consent from the user first, then call "+
					"request_model_download again with confirm: true once they agree",
				in.TargetID)), nil, nil
		}

		g, err := d.Clients.Client(ctx)
		if err != nil {
			return toolError(err), nil, nil
		}
		// A quota 429 here is not transient, so it must not be retried; the
		// fresh Idempotency-Key still lets the transport replay one logical
		// authorization after a 5xx without charging twice.
		resp, err := g.CreateDownloadAuthorizationWithResponse(api.WithNoRetryOn429(ctx),
			account, name, in.ModelKey, in.TargetID,
			&gen.CreateDownloadAuthorizationParams{IdempotencyKey: newIdempotencyKeyParam()})
		if err != nil {
			return toolError(err), nil, nil
		}
		if err := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); err != nil {
			return toolError(err), nil, nil
		}
		if in.IncludeURLs {
			return rawResult(resp.Body), nil, nil
		}
		redacted, err := redactAuthorization(resp.Body)
		if err != nil {
			// The generated client already decoded this body as an
			// authorization object, so a failure here is a programming fault,
			// not something the caller can act on — and never a reason to fall
			// back to the raw body, which carries the credentials.
			return nil, nil, fmt.Errorf("redacting download authorization: %w", err)
		}
		return rawResult(redacted), nil, nil
	}
}

// redactAuthorization replaces every signed artifact url in a raw
// authorization body with redactedURL. artifacts[].url is the only URL the
// response carries (gen.DownloadAuthorizationResponse: authorization_id,
// expires_at, artifacts[{name,size,checksum,url}]), which is exactly what
// `melange model download --json` redacts.
//
// The body is decoded and re-emitted, so key order becomes sorted — the same
// documented deviation from byte-exact output the CLI makes. Numbers are kept
// as literals (UseNumber) so an artifact size survives the round trip instead
// of being reformatted through float64, and HTML escaping stays off so both
// the placeholder and any `<`/`&` inside names survive verbatim.
func redactAuthorization(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var body map[string]any
	if err := dec.Decode(&body); err != nil {
		// The error deliberately does not quote the body it rejected.
		return nil, fmt.Errorf("decoding download authorization: %w", err)
	}
	if artifacts, ok := body["artifacts"].([]any); ok {
		for _, a := range artifacts {
			if artifact, ok := a.(map[string]any); ok {
				if _, ok := artifact["url"]; ok {
					artifact["url"] = redactedURL
				}
			}
		}
	}
	return marshalEnvelope(body)
}

// newIdempotencyKeyParam returns a fresh random UUIDv4 as the pointer type the
// generated params structs take. It is used by import_model and
// request_model_download: the key is generated once per logical tool call, so
// the API retry transport replays the same key on a 5xx retry instead of
// starting a second import or charging a second authorization.
//
// It duplicates internal/cmd/model's helper because that package pulls in
// cobra, which internal/mcp must not import.
func newIdempotencyKeyParam() *gen.IdempotencyKey {
	key := gen.IdempotencyKey(newIdempotencyKey())
	return &key
}

// newIdempotencyKey returns a random UUIDv4.
func newIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms; fall back to a
		// timestamp key rather than abandoning the call over it.
		return fmt.Sprintf("melange-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
