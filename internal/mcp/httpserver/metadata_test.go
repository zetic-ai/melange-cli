package httpserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newASStub is newMeStub plus the authorization server's own RFC 8414
// metadata document at /.well-known/oauth-authorization-server, so tests can
// walk the same discovery chain a real OAuth-capable MCP client follows. The
// issuer is the stub's own URL — random port and all — which makes every
// hardcoded-hostname mutation fail: only values genuinely derived from config
// can point back at it.
func newASStub(t *testing.T, users map[string]string) *httptest.Server {
	t.Helper()
	var stub *httptest.Server
	stub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"issuer":%[1]q,"authorization_endpoint":%[2]q,"token_endpoint":%[3]q,`+
				`"registration_endpoint":%[4]q,"scopes_supported":["read","write"],`+
				`"response_types_supported":["code"],"grant_types_supported":["authorization_code","refresh_token"],`+
				`"code_challenge_methods_supported":["S256"],"token_endpoint_auth_methods_supported":["none"]}`,
				stub.URL, stub.URL+"/oauth/authorize", stub.URL+"/v1/oauth/token", stub.URL+"/v1/oauth/register")
		case "/v1/me":
			token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			w.Header().Set("Content-Type", "application/json")
			if name, known := users[token]; known {
				_, _ = io.WriteString(w, meBody(name))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"unknown token"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(stub.Close)
	return stub
}

// resourceMetadataDoc is the RFC 9728 wire shape the tests decode. Kept as a
// test-local struct (not the SDK type) so a drift in the SDK's JSON tags
// would fail these assertions instead of silently traveling.
type resourceMetadataDoc struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
}

// getMetadata fetches path from ts without credentials and decodes the
// document, asserting the transport contract on the way.
func getMetadata(t *testing.T, tsURL, path string) resourceMetadataDoc {
	t.Helper()
	resp, err := http.Get(tsURL + path)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "discovery must not require a credential")
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")
	var doc resourceMetadataDoc
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	return doc
}

// TestProtectedResourceMetadataDocument pins the RFC 9728 document contract:
// served unauthenticated at the well-known path, correct content type,
// cacheable, and every value derived from config — the resource is this
// test's unique canonical URL and authorization_servers is the stub's
// random-port URL, so no hardcoded value can pass.
func TestProtectedResourceMetadataDocument(t *testing.T) {
	const resource = "https://mcp.metadata-doc.example"
	stub := newASStub(t, nil)
	_, ts := newTestServer(t, stub.URL, func(c *Config) { c.Resource = resource })

	resp, err := http.Get(ts.URL + "/.well-known/oauth-protected-resource")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "discovery must not require a credential")
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")
	assert.Contains(t, resp.Header.Get("Cache-Control"), "max-age=",
		"the document is fixed at startup; clients and shared caches may hold it")

	var doc resourceMetadataDoc
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	assert.Equal(t, resource, doc.Resource, "resource must be the configured canonical URL")
	assert.Equal(t, []string{stub.URL}, doc.AuthorizationServers,
		"authorization_servers must be the configured API host, in issuer form")
	assert.Equal(t, []string{"read", "write"}, doc.ScopesSupported,
		"scopes must mirror the authorization server's scopes_supported")
	assert.Equal(t, []string{"header"}, doc.BearerMethodsSupported,
		"tokens are accepted in the Authorization header only")

	for _, raw := range append([]string{doc.Resource}, doc.AuthorizationServers...) {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		assert.True(t, u.IsAbs() && u.Host != "", "discovery URLs must be absolute: %q", raw)
	}
}

// TestProtectedResourceMetadataOriginPosture pins that discovery is never
// gated the way the MCP endpoint is: a non-browser client (no Origin header)
// reads it, a browser from an origin OUTSIDE the empty allowlist reads it
// (where the protected chain would answer 403), and a CORS preflight gets its
// 204 rather than a 401. Moving the route behind originMiddleware or
// AuthMiddleware fails this test.
func TestProtectedResourceMetadataOriginPosture(t *testing.T) {
	stub := newASStub(t, nil)
	_, ts := newTestServer(t, stub.URL, func(c *Config) {
		c.Resource = "https://mcp.origin-posture.example"
		// AllowedOrigins deliberately left empty: the strictest MCP-endpoint
		// posture, under which discovery must still be open.
	})
	metaURL := ts.URL + "/.well-known/oauth-protected-resource"

	t.Run("no Origin (MCP SDKs, curl)", func(t *testing.T) {
		resp, err := http.Get(metaURL)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	t.Run("browser Origin outside the allowlist", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, metaURL, nil)
		require.NoError(t, err)
		req.Header.Set("Origin", "https://agent.example")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"metadata is public configuration; --allowed-origins gates the MCP endpoint, not discovery")
		assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	})

	t.Run("CORS preflight", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodOptions, metaURL, nil)
		require.NoError(t, err)
		req.Header.Set("Origin", "https://agent.example")
		req.Header.Set("Access-Control-Request-Method", http.MethodGet)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusNoContent, resp.StatusCode,
			"OPTIONS must reach the metadata handler, not 401 in the protected chain")
		assert.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	})
}

// TestNoResourceNoMetadataRoute pins the decision for a server without a
// resource identity: an RFC 9728 document exists to name a resource
// identifier, so with none configured the route is NOT registered — the
// well-known path falls through to the protected chain and 401s with the
// bare Bearer challenge (no resource_metadata pointer, because there is no
// document to point at).
func TestNoResourceNoMetadataRoute(t *testing.T) {
	_, ts := newTestServer(t, "https://api.invalid", nil)

	resp, err := http.Get(ts.URL + "/.well-known/oauth-protected-resource")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"no resource identity ⇒ no metadata route; the path is ordinary protected surface")
	assert.Equal(t, "Bearer", resp.Header.Get("WWW-Authenticate"),
		"and the challenge stays bare — advertising discovery that does not exist would strand clients")
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "authorization_servers")
}

// TestAuthMiddlewareChallengeTransition pins the PR2→PR4 handoff at the
// middleware seam: with a ResourceMetadataURL configured, the SDK itself
// emits the parameterized challenge and challengeWriter's bare fallback goes
// dormant — exactly one WWW-Authenticate value, parameterized, never the
// bare "Bearer" it would have written.
func TestAuthMiddlewareChallengeTransition(t *testing.T) {
	const metaURL = "https://rs.example/.well-known/oauth-protected-resource"
	chain := AuthMiddleware(PassthroughVerifier, metaURL,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(initializeBody))
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	values := rec.Header().Values("WWW-Authenticate")
	require.Len(t, values, 1, "exactly one challenge: no double-write from the fallback")
	assert.Equal(t, `Bearer resource_metadata="`+metaURL+`"`, values[0],
		"the SDK's parameterized challenge must win; a bare Bearer here means the fallback overrode it")
}

// TestChallengeAdvertisesResourceMetadataThroughStack proves the same
// transition over the full server for both 401 shapes — no credential (the
// SDK rejects before the verifier) and a rejected bearer (MeVerifier says
// invalid) — and that the advertised URL is derived from the configured
// resource.
func TestChallengeAdvertisesResourceMetadataThroughStack(t *testing.T) {
	const resource = "https://mcp.challenge.example"
	stub := newASStub(t, nil) // every bearer is unknown upstream
	_, ts := newTestServer(t, stub.URL, func(c *Config) { c.Resource = resource })

	want := `Bearer resource_metadata="` + resource + `/.well-known/oauth-protected-resource"`

	for name, token := range map[string]string{
		"no credential":   "",
		"rejected bearer": "zoa_rejected_2718281828",
	} {
		t.Run(name, func(t *testing.T) {
			resp := postMCP(t, ts.URL, token, "", strings.NewReader(initializeBody))
			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
			values := resp.Header.Values("WWW-Authenticate")
			require.Len(t, values, 1, "exactly one challenge — the parameterized header, fallback dormant")
			assert.Equal(t, want, values[0])
		})
	}
}

// TestDiscoveryChainCoherent walks the chain end to end exactly as an
// OAuth-capable client does: 401 challenge → resource_metadata URL → RFC
// 9728 document → authorization_servers[0] → the AS's RFC 8414 document.
// Every hop must be absolute and mutually consistent, or a real client
// stalls between two servers that each think the other is misconfigured.
func TestDiscoveryChainCoherent(t *testing.T) {
	const resource = "https://mcp.chain.example"
	stub := newASStub(t, nil)
	_, ts := newTestServer(t, stub.URL, func(c *Config) { c.Resource = resource })

	// Hop 1: the 401 names where the metadata lives.
	resp := postMCP(t, ts.URL, "", "", strings.NewReader(initializeBody))
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	challenge := resp.Header.Get("WWW-Authenticate")
	m := regexp.MustCompile(`resource_metadata="([^"]+)"`).FindStringSubmatch(challenge)
	require.NotNil(t, m, "challenge %q must carry resource_metadata", challenge)
	metaURL, err := url.Parse(m[1])
	require.NoError(t, err)
	require.True(t, metaURL.IsAbs() && metaURL.Host != "", "resource_metadata must be absolute: %q", m[1])
	assert.Equal(t, resource+"/.well-known/oauth-protected-resource", m[1],
		"the advertised URL must live on the canonical resource origin")

	// Hop 2: the document (fetched by path — this test server answers for
	// the canonical host).
	doc := getMetadata(t, ts.URL, metaURL.Path)
	assert.Equal(t, resource, doc.Resource,
		"RFC 9728 §3.3: the document's resource must equal the identifier the client asked about")
	require.Len(t, doc.AuthorizationServers, 1)

	// Hop 3: the authorization server's own metadata at the issuer.
	issuer := doc.AuthorizationServers[0]
	asResp, err := http.Get(issuer + "/.well-known/oauth-authorization-server")
	require.NoError(t, err)
	defer func() { _ = asResp.Body.Close() }()
	require.Equal(t, http.StatusOK, asResp.StatusCode,
		"authorization_servers[0] + the RFC 8414 well-known path must resolve")
	var asMeta struct {
		Issuer                string `json:"issuer"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}
	require.NoError(t, json.NewDecoder(asResp.Body).Decode(&asMeta))
	assert.Equal(t, issuer, asMeta.Issuer,
		"RFC 8414 §3.3: the AS document's issuer must equal the URL it was derived from")
	for _, raw := range []string{asMeta.AuthorizationEndpoint, asMeta.TokenEndpoint} {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		assert.True(t, u.IsAbs() && u.Host != "", "AS endpoints must be absolute: %q", raw)
	}
}

// TestProtectedResourceMetadataLocation pins the RFC 9728 §3 path grammar:
// the well-known segment is inserted between host and the resource's path
// component, and the advertised URL is that path on the resource's origin.
func TestProtectedResourceMetadataLocation(t *testing.T) {
	cases := []struct {
		resource, wantPath, wantURL string
	}{
		{"https://mcp.zetic.ai",
			"/.well-known/oauth-protected-resource",
			"https://mcp.zetic.ai/.well-known/oauth-protected-resource"},
		{"https://mcp.zetic.ai/mcp",
			"/.well-known/oauth-protected-resource/mcp",
			"https://mcp.zetic.ai/.well-known/oauth-protected-resource/mcp"},
		{"http://localhost:8321",
			"/.well-known/oauth-protected-resource",
			"http://localhost:8321/.well-known/oauth-protected-resource"},
	}
	for _, tc := range cases {
		t.Run(tc.resource, func(t *testing.T) {
			path, absolute, err := protectedResourceMetadataLocation(tc.resource)
			require.NoError(t, err)
			assert.Equal(t, tc.wantPath, path)
			assert.Equal(t, tc.wantURL, absolute)
		})
	}
}

// TestPathBearingResourceThroughStack pins the path-insertion rule over the
// full server: a resource with a path component serves its document at the
// suffixed well-known path, advertises exactly that URL in the challenge,
// and does NOT serve the unsuffixed path (which stays protected surface).
func TestPathBearingResourceThroughStack(t *testing.T) {
	const resource = "https://mcp.pathy.example/mcp"
	stub := newASStub(t, nil)
	_, ts := newTestServer(t, stub.URL, func(c *Config) { c.Resource = resource })

	doc := getMetadata(t, ts.URL, "/.well-known/oauth-protected-resource/mcp")
	assert.Equal(t, resource, doc.Resource)

	resp := postMCP(t, ts.URL, "", "", strings.NewReader(initializeBody))
	assert.Equal(t,
		`Bearer resource_metadata="https://mcp.pathy.example/.well-known/oauth-protected-resource/mcp"`,
		resp.Header.Get("WWW-Authenticate"))

	bare, err := http.Get(ts.URL + "/.well-known/oauth-protected-resource")
	require.NoError(t, err)
	defer func() { _ = bare.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, bare.StatusCode,
		"only the RFC 9728 path for THIS resource is public; near-miss paths stay protected")
}

// TestDoubleSlashResourceNormalizedThroughStack pins the fail-closed
// misconfiguration CanonicalResource's path.Clean exists for: an operator
// configuring `https://host//` used to get a canonical identity of
// `https://host/` — one no minted aud could match — whose derived well-known
// path ended in "/", a ServeMux SUBTREE pattern serving the metadata document
// at every subpath. Both symptoms are pinned here through the full server.
func TestDoubleSlashResourceNormalizedThroughStack(t *testing.T) {
	stub := newASStub(t, nil)
	srv, ts := newTestServer(t, stub.URL, func(c *Config) {
		c.Resource = "https://mcp.slashy.example//"
	})

	// The stored identity is the aud-matchable canonical form.
	assert.Equal(t, "https://mcp.slashy.example", srv.cfg.Resource,
		"a doubled trailing slash must normalize to the slash-free identity")

	// The document is served at the exact well-known path…
	doc := getMetadata(t, ts.URL, "/.well-known/oauth-protected-resource")
	assert.Equal(t, "https://mcp.slashy.example", doc.Resource)

	// …and ONLY there: a subpath is protected surface, not a subtree of
	// public metadata.
	sub, err := http.Get(ts.URL + "/.well-known/oauth-protected-resource/anything")
	require.NoError(t, err)
	defer func() { _ = sub.Body.Close() }()
	assert.Equal(t, http.StatusUnauthorized, sub.StatusCode,
		"the well-known route must be an exact match, never a subtree")
}
