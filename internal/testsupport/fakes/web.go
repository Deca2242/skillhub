package fakes

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"skillhub/internal/shared/ports"
)

// WebError is how the fake Web fails, with the stable codes the curl
// adapter reports (CatalogReason): "http" with a status for a URL it does not
// serve, "too_large" for a body over the limit, or whatever Fail set.
type WebError struct {
	URL    string
	Reason string
	Status int
}

func (e *WebError) Error() string {
	return fmt.Sprintf("get %s: %s (HTTP %d)", e.URL, e.Reason, e.Status)
}

// CatalogReason is the stable code of the failure.
func (e *WebError) CatalogReason() string { return e.Reason }

// HTTPStatus is the HTTP status, or 0.
func (e *WebError) HTTPStatus() int { return e.Status }

// WebCall is one Get the fake Web received.
type WebCall struct {
	URL      string
	MaxBytes int64
}

// Web is an in-memory ports.HTTPSFetcher over a site the test serves. A URL
// it does not serve answers HTTP 404, as curl --fail reports it; a body
// larger than the limit is refused, as the curl adapter refuses it. It never
// touches the network.
type Web struct {
	mu     sync.Mutex
	bodies map[string][]byte
	errs   map[string]error
	calls  []WebCall
}

var _ ports.HTTPSFetcher = (*Web)(nil)

// NewWeb builds a Web that serves nothing.
func NewWeb() *Web { return &Web{bodies: map[string][]byte{}, errs: map[string]error{}} }

// Serve makes url answer body, and clears a failure set by Fail.
func (w *Web) Serve(url string, body []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.bodies[url] = slices.Clone(body)
	delete(w.errs, url)
}

// Fail makes url fail with err.
func (w *Web) Fail(url string, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.errs[url] = err
}

// Calls are the Gets received, in order.
func (w *Web) Calls() []WebCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.calls)
}

// URLs are the URLs of Calls, in order.
func (w *Web) URLs() []string {
	var out []string
	for _, c := range w.Calls() {
		out = append(out, c.URL)
	}
	return out
}

// Get implements ports.HTTPSFetcher.
func (w *Web) Get(ctx context.Context, url string, maxBytes int64) ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, WebCall{URL: url, MaxBytes: maxBytes})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(url, "https://") {
		return nil, &WebError{URL: url, Reason: "bad_url"}
	}
	if err, ok := w.errs[url]; ok {
		return nil, err
	}
	body, ok := w.bodies[url]
	if !ok {
		return nil, &WebError{URL: url, Reason: "http", Status: 404}
	}
	if int64(len(body)) > maxBytes {
		return nil, &WebError{URL: url, Reason: "too_large"}
	}
	return slices.Clone(body), nil
}
