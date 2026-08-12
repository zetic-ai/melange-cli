package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/api"
)

func makeResponse(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestHandleResponseSuccessPassthrough(t *testing.T) {
	resp := makeResponse(200, `{"ok":true}`, nil)
	assert.NoError(t, api.HandleResponse(resp))
	// 2xx bodies must remain readable by the caller.
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, string(body))
}

func TestHandleResponseFullEnvelope(t *testing.T) {
	body := `{
		"type": "error",
		"error": {
			"type": "invalid_request_error",
			"message": "name is required",
			"fields": [{"field": "name", "message": "must not be empty"}]
		},
		"request_id": "req_123"
	}`
	err := api.HandleResponse(makeResponse(400, body, nil))
	require.Error(t, err)

	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.StatusCode)
	assert.Equal(t, "invalid_request_error", apiErr.Type)
	assert.Equal(t, "name is required", apiErr.Message)
	require.Len(t, apiErr.Fields, 1)
	assert.Equal(t, "name", apiErr.Fields[0].Field)
	assert.Equal(t, "must not be empty", apiErr.Fields[0].Message)
	assert.Equal(t, "req_123", apiErr.RequestID)
}

func TestHandleResponseBillingCode(t *testing.T) {
	body := `{
		"type": "error",
		"error": {
			"type": "billing_error",
			"code": "credit_balance_exhausted",
			"message": "credit_balance_exhausted"
		},
		"request_id": "req_402"
	}`
	err := api.HandleResponse(makeResponse(402, body, nil))
	require.Error(t, err)

	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 402, apiErr.StatusCode)
	assert.Equal(t, "billing_error", apiErr.Type)
	assert.Equal(t, "credit_balance_exhausted", apiErr.Code)
	assert.Equal(t,
		"melange API: credit_balance_exhausted (billing_error/credit_balance_exhausted, HTTP 402, request req_402)",
		apiErr.Error())
}

func TestHandleResponseRequestIDHeaderFallback(t *testing.T) {
	header := http.Header{}
	header.Set("X-Request-ID", "req_hdr")
	body := `{"type":"error","error":{"type":"not_found_error","message":"no such model"}}`
	err := api.HandleResponse(makeResponse(404, body, header))

	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "req_hdr", apiErr.RequestID)
}

func TestHandleResponseNonJSONBody(t *testing.T) {
	err := api.HandleResponse(makeResponse(502, "<html>bad gateway from proxy</html>", nil))

	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 502, apiErr.StatusCode)
	assert.Equal(t, "http_error", apiErr.Type)
	assert.Equal(t, "<html>bad gateway from proxy</html>", apiErr.Message)
}

func TestHandleResponseNonJSONBodyTruncatedTo200Bytes(t *testing.T) {
	long := strings.Repeat("x", 500)
	err := api.HandleResponse(makeResponse(500, long, nil))

	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Len(t, apiErr.Message, 200)
}

func TestHandleResponseEnvelopeShapeWithoutDiscriminatorFallsBack(t *testing.T) {
	// Envelope-shaped JSON without top-level type:"error" must not be trusted
	// as the Melange envelope (e.g. a proxy echoing similar JSON).
	body := `{"error":{"type":"proxy_error","message":"upstream exploded"}}`
	err := api.HandleResponse(makeResponse(502, body, nil))

	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "http_error", apiErr.Type)
	assert.Equal(t, body, apiErr.Message)
	assert.Empty(t, apiErr.Fields)
}

func TestHandleResponseTruncationPreservesRuneBoundary(t *testing.T) {
	// 199 ASCII bytes followed by a 2-byte rune straddling the 200-byte cap:
	// a byte-wise cut would leave a broken UTF-8 sequence.
	body := strings.Repeat("x", 199) + "é" + strings.Repeat("y", 50)
	err := api.HandleResponse(makeResponse(500, body, nil))

	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, strings.Repeat("x", 199), apiErr.Message)
	assert.True(t, utf8.ValidString(apiErr.Message))
}

func TestHandleResponseEmptyBodyUsesStatusText(t *testing.T) {
	err := api.HandleResponse(makeResponse(503, "", nil))

	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "Service Unavailable", apiErr.Message)
}

func TestHandleResponse401FallbackType(t *testing.T) {
	err := api.HandleResponse(makeResponse(401, "unauthorized", nil))

	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, "authentication_error", apiErr.Type)
}

func TestHandleResponseRetryAfter(t *testing.T) {
	header := http.Header{}
	header.Set("Retry-After", "7")
	err := api.HandleResponse(makeResponse(429, "", header))

	var apiErr *api.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 7*time.Second, apiErr.RetryAfter)
}

func TestErrorString(t *testing.T) {
	tests := []struct {
		name string
		err  *api.Error
		want string
	}{
		{
			name: "with request id",
			err: &api.Error{
				StatusCode: 401,
				Type:       "authentication_error",
				Message:    "invalid token",
				RequestID:  "req_1",
			},
			want: "melange API: invalid token (authentication_error, HTTP 401, request req_1)",
		},
		{
			name: "without request id",
			err: &api.Error{
				StatusCode: 500,
				Type:       "api_error",
				Message:    "boom",
			},
			want: "melange API: boom (api_error, HTTP 500)",
		},
		{
			name: "with refusal code",
			err: &api.Error{
				StatusCode: 402,
				Type:       "billing_error",
				Code:       "subscription_past_due",
				Message:    "subscription past due",
			},
			want: "melange API: subscription past due (billing_error/subscription_past_due, HTTP 402)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.err.Error())
		})
	}
}

func TestErrorRetryable(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{429, true},
		{502, true},
		{503, true},
		{504, true},
		{400, false},
		{401, false},
		{403, false},
		{404, false},
		{500, false},
	}
	for _, tt := range tests {
		err := &api.Error{StatusCode: tt.status}
		assert.Equal(t, tt.want, err.Retryable(), "status %d", tt.status)
	}
}
