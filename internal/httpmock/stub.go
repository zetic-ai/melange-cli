package httpmock

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// REST matches a request by HTTP method and URL path (e.g. "/v1/me").
func REST(method, path string) Matcher {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return func(req *http.Request) bool {
		return strings.EqualFold(req.Method, method) && req.URL.Path == path
	}
}

// StatusStringResponse responds with the given status code and body.
func StatusStringResponse(status int, body string) Responder {
	return func(req *http.Request) (*http.Response, error) {
		return newResponse(req, status, http.Header{}, strings.NewReader(body)), nil
	}
}

// JSONResponse marshals v as the response body with Content-Type
// application/json.
func JSONResponse(status int, v any) Responder {
	return func(req *http.Request) (*http.Response, error) {
		data, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		header := http.Header{}
		header.Set("Content-Type", "application/json")
		return newResponse(req, status, header, bytes.NewReader(data)), nil
	}
}

// WithHeader decorates a responder with an extra response header.
func WithHeader(responder Responder, key, value string) Responder {
	return func(req *http.Request) (*http.Response, error) {
		resp, err := responder(req)
		if resp != nil {
			resp.Header.Set(key, value)
		}
		return resp, err
	}
}

// ErrorResponse simulates a transport-level failure (e.g. connection refused).
func ErrorResponse(err error) Responder {
	return func(req *http.Request) (*http.Response, error) {
		return nil, err
	}
}

func newResponse(req *http.Request, status int, header http.Header, body io.Reader) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     header,
		Body:       io.NopCloser(body),
		Request:    req,
	}
}
