// Package httpmock provides a minimal mock http.RoundTripper for tests,
// modeled on gh's pkg/httpmock (simplified). A Registry holds route stubs
// that are consumed in registration order, which allows simulating retry
// sequences (e.g. 502 then 200) by registering the same route twice.
package httpmock

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// Matcher decides whether a stub applies to a request.
type Matcher func(req *http.Request) bool

// Responder produces the stubbed response for a matched request.
type Responder func(req *http.Request) (*http.Response, error)

// stub pairs a matcher with its responder; each stub is consumed once.
type stub struct {
	matched   bool
	match     Matcher
	responder Responder
}

// Registry is an http.RoundTripper that serves registered stubs and records
// every matched request in Requests.
type Registry struct {
	mu       sync.Mutex
	stubs    []*stub
	Requests []*http.Request
}

// Register adds a stub. Stubs are matched in registration order and each
// stub responds at most once.
func (r *Registry) Register(m Matcher, resp Responder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stubs = append(r.stubs, &stub{match: m, responder: resp})
}

// RoundTrip implements http.RoundTripper.
func (r *Registry) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	var matched *stub
	for _, s := range r.stubs {
		if !s.matched && s.match(req) {
			s.matched = true
			matched = s
			break
		}
	}
	if matched != nil {
		r.Requests = append(r.Requests, req)
	}
	r.mu.Unlock()

	if matched == nil {
		return nil, fmt.Errorf("httpmock: no registered stub for %s %s", req.Method, req.URL)
	}
	return matched.responder(req)
}

// Verify fails the test when registered stubs were never matched.
func (r *Registry) Verify(t testing.TB) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	unmatched := 0
	for _, s := range r.stubs {
		if !s.matched {
			unmatched++
		}
	}
	if unmatched > 0 {
		t.Errorf("httpmock: %d unmatched stub(s)", unmatched)
	}
}
