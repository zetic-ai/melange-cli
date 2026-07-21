package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Options configures a Client.
type Options struct {
	Host      string            // base URL, e.g. "https://api.zetic.ai"; scheme defaults to https
	Token     string            // Bearer token; empty = unauthenticated
	UserAgent string            // e.g. "melange-cli/1.0.0 (darwin; arm64)"
	Debug     io.Writer         // nil = debug logging off
	Transport http.RoundTripper // base transport; nil = http.DefaultTransport
}

// Client is an HTTP client for the Melange public API. The transport chain is
// (outermost first): auth -> retry -> debug -> base.
type Client struct {
	baseURL *url.URL
	http    *http.Client
}

// NewClient builds a Client from opts.
func NewClient(opts Options) (*Client, error) {
	host := opts.Host
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	baseURL, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("parsing API host: %w", err)
	}

	base := opts.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	rt := base
	if opts.Debug != nil {
		rt = &debugTransport{base: rt, out: opts.Debug}
	}
	retry := newRetryTransport(rt)
	rt = &authTransport{base: retry, token: opts.Token, userAgent: opts.UserAgent}

	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Transport: rt},
	}, nil
}

// Do sends a request to a path relative to the client's host (e.g. "/v1/me")
// and returns the raw response. The caller owns the response body.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	u := c.baseURL.JoinPath(path)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return c.http.Do(req)
}

// JSON sends a JSON request and decodes a JSON response. in and out may each
// be nil. Non-2xx responses are returned as *Error.
func (c *Client) JSON(ctx context.Context, method, path string, in any, out any) error {
	var body io.Reader
	headers := map[string]string{"Accept": "application/json"}
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		body = bytes.NewReader(data)
		headers["Content-Type"] = "application/json"
	}

	resp, err := c.Do(ctx, method, path, body, headers)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		_ = resp.Body.Close()
	}()

	if err := HandleResponse(resp); err != nil {
		return err
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding response body: %w", err)
		}
	}
	return nil
}
