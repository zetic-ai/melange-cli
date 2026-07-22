package cmdutil_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/httpmock"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
)

func TestExitCodeAPIError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{
			name: "authentication_error → 4",
			err:  &api.Error{StatusCode: 401, Type: "authentication_error", Message: "bad token"},
			want: 4,
		},
		{
			name: "wrapped authentication_error → 4",
			err:  fmt.Errorf("outer: %w", &api.Error{StatusCode: 401, Type: "authentication_error"}),
			want: 4,
		},
		{
			name: "other api error → 1",
			err:  &api.Error{StatusCode: 500, Type: "api_error", Message: "boom"},
			want: 1,
		},
		{
			name: "invalid_request_error → 1",
			err:  &api.Error{StatusCode: 400, Type: "invalid_request_error"},
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, cmdutil.ExitCode(tt.err))
		})
	}
}

func TestNewAPIClientTimeoutEnv(t *testing.T) {
	t.Setenv("MELANGE_API_TIMEOUT", "45s")
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Version: "test"}

	client, err := cmdutil.NewAPIClient(f, "https://api.zetic.ai", "")
	require.NoError(t, err)
	assert.Equal(t, 45*time.Second, client.RequestTimeout())
}

func TestNewAPIClientRejectsUnboundedTimeout(t *testing.T) {
	t.Setenv("MELANGE_API_TIMEOUT", "0")
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Version: "test"}

	_, err := cmdutil.NewAPIClient(f, "https://api.zetic.ai", "")
	require.ErrorContains(t, err, "MELANGE_API_TIMEOUT")
}

func TestNewAPIClient(t *testing.T) {
	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, "{}"))

	ios, _, _, errOut := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:     ios,
		Version:       "1.2.3",
		HTTPTransport: reg,
	}

	client, err := cmdutil.NewAPIClient(f, "https://api.zetic.ai", "ztp_tok")
	require.NoError(t, err)

	resp, err := client.Do(context.Background(), "GET", "/v1/me", nil, nil)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	require.Len(t, reg.Requests, 1)
	got := reg.Requests[0]
	assert.Equal(t, "Bearer ztp_tok", got.Header.Get("Authorization"))
	assert.Contains(t, got.Header.Get("User-Agent"), "melange-cli/1.2.3")
	assert.Empty(t, errOut.String(), "no debug output without MELANGE_DEBUG")
}

func TestNewAPIClientDebugEnv(t *testing.T) {
	t.Setenv("MELANGE_DEBUG", "1")

	reg := &httpmock.Registry{}
	reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, "{}"))

	ios, _, _, errOut := iostreams.Test()
	f := &cmdutil.Factory{
		IOStreams:     ios,
		Version:       "1.2.3",
		HTTPTransport: reg,
	}

	client, err := cmdutil.NewAPIClient(f, "https://api.zetic.ai", "ztp_supersecret")
	require.NoError(t, err)

	resp, err := client.Do(context.Background(), "GET", "/v1/me", nil, nil)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	debug := errOut.String()
	assert.Contains(t, debug, "> GET https://api.zetic.ai/v1/me")
	assert.NotContains(t, debug, "ztp_supersecret", "token must never appear in debug output")
}

func TestNewAPIClientDebugEnvTruthy(t *testing.T) {
	tests := []struct {
		value string
		debug bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"Yes", true},
		{"on", true},
		{"", false},
		{"0", false},
		{"false", false},
		{"no", false},
		{"off", false},
		{"2", false},
	}
	for _, tt := range tests {
		t.Run("MELANGE_DEBUG="+tt.value, func(t *testing.T) {
			t.Setenv("MELANGE_DEBUG", tt.value)

			reg := &httpmock.Registry{}
			reg.Register(httpmock.REST("GET", "/v1/me"), httpmock.StatusStringResponse(200, "{}"))

			ios, _, _, errOut := iostreams.Test()
			f := &cmdutil.Factory{IOStreams: ios, Version: "1.2.3", HTTPTransport: reg}

			client, err := cmdutil.NewAPIClient(f, "https://api.zetic.ai", "ztp_tok")
			require.NoError(t, err)
			resp, err := client.Do(context.Background(), "GET", "/v1/me", nil, nil)
			require.NoError(t, err)
			defer resp.Body.Close() //nolint:errcheck

			if tt.debug {
				assert.Contains(t, errOut.String(), "> GET", "%q must enable debug logging", tt.value)
			} else {
				assert.Empty(t, errOut.String(), "%q must not enable debug logging", tt.value)
			}
		})
	}
}

func TestAuthErrorWrapsAPIError(t *testing.T) {
	// AuthError still maps to 4 even when wrapping something else.
	err := cmdutil.AuthError{Err: errors.New("no token")}
	assert.Equal(t, 4, cmdutil.ExitCode(err))
}
