package api

import (
	"context"
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
	rt = &authTransport{base: retry, host: baseURL.Host, token: opts.Token, userAgent: opts.UserAgent}

	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Transport: rt},
	}, nil
}

// Do sends a request to a path relative to the client's host (e.g. "/v1/me",
// optionally with a query string like "/v1/repos?limit=5") and returns the
// raw response. The caller owns the response body.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	p, query, _ := strings.Cut(path, "?")
	u := c.baseURL.JoinPath(p)
	u.RawQuery = query
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return c.http.Do(req)
}
