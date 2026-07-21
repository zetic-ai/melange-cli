package httpmock_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
)

func TestRegistryMatchesRoute(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(
		httpmock.REST("GET", "/v1/me"),
		httpmock.StatusStringResponse(200, `{"ok":true}`),
	)

	client := &http.Client{Transport: reg}
	resp, err := client.Get("https://api.zetic.ai/v1/me")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, 200, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, string(body))
	require.Len(t, reg.Requests, 1)
	assert.Equal(t, "/v1/me", reg.Requests[0].URL.Path)
}

func TestRegistryUnmatchedRequestErrors(t *testing.T) {
	reg := &httpmock.Registry{}
	client := &http.Client{Transport: reg}

	_, err := client.Get("https://api.zetic.ai/v1/nothing") //nolint:bodyclose
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no registered stub")
}

func TestRegistrySequentialStubs(t *testing.T) {
	// Two stubs for the same route are consumed in registration order,
	// which lets tests simulate retry sequences (502 then 200).
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(502, "bad gateway"))
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, "ok"))

	client := &http.Client{Transport: reg}

	resp1, err := client.Get("https://api.zetic.ai/v1/me")
	require.NoError(t, err)
	resp1.Body.Close() //nolint:errcheck
	resp2, err := client.Get("https://api.zetic.ai/v1/me")
	require.NoError(t, err)
	resp2.Body.Close() //nolint:errcheck

	assert.Equal(t, 502, resp1.StatusCode)
	assert.Equal(t, 200, resp2.StatusCode)
	assert.Len(t, reg.Requests, 2)
}

func TestJSONResponseSetsContentType(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.JSONResponse(200, map[string]string{"a": "b"}))

	client := &http.Client{Transport: reg}
	resp, err := client.Get("https://api.zetic.ai/v1/me")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	body, _ := io.ReadAll(resp.Body)
	assert.JSONEq(t, `{"a":"b"}`, string(body))
}

func TestWithHeader(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(
		httpmock.REST("GET", "/v1/me"),
		httpmock.WithHeader(httpmock.StatusStringResponse(429, "slow down"), "Retry-After", "3"),
	)

	client := &http.Client{Transport: reg}
	resp, err := client.Get("https://api.zetic.ai/v1/me")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, "3", resp.Header.Get("Retry-After"))
}

func TestErrorResponse(t *testing.T) {
	reg := &httpmock.Registry{}
	boom := errors.New("connection refused")
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.ErrorResponse(boom))

	req, _ := http.NewRequest("GET", "https://api.zetic.ai/v1/me", nil)
	_, err := reg.RoundTrip(req) //nolint:bodyclose
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
}

func TestVerifyReportsUnmatchedStubs(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, "ok"))

	fake := &fakeT{}
	reg.Verify(fake)
	assert.True(t, fake.failed, "Verify should fail when stubs are unmatched")
	assert.True(t, strings.Contains(fake.msg, "1 unmatched stub"), "got: %s", fake.msg)
}

type fakeT struct {
	testing.TB
	failed bool
	msg    string
}

func (f *fakeT) Helper() {}
func (f *fakeT) Errorf(format string, args ...any) {
	f.failed = true
	f.msg = fmt.Sprintf(format, args...)
}
