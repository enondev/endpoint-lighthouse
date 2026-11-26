// Package fetch handles fetching OpenAPI/Swagger spec documents with
// ETag/If-None-Match conditional HTTP gating per AGENTS.md pipeline step 2.
package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Result contains the outcome of fetching a spec document.
type Result struct {
	// Body is the raw spec document bytes. Empty if NotModified is true.
	Body []byte
	// ETag is the ETag header value from the response, if present.
	ETag string
	// NotModified is true when the server returned HTTP 304 (Not Modified),
	// meaning the spec has not changed since the last fetch.
	NotModified bool
	// StatusCode is the HTTP status code returned by the server.
	StatusCode int
}

// Fetcher fetches spec documents conditionally.
type Fetcher struct {
	client *http.Client
}

// New creates a Fetcher with the given timeout.
func New(timeout time.Duration) *Fetcher {
	return &Fetcher{
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// Fetch retrieves the spec at the given URL. If etag is non-empty, it is sent
// as an If-None-Match header; a 304 response sets NotModified=true and leaves
// Body empty. Non-2xx responses (except 304) return an error.
func (f *Fetcher) Fetch(ctx context.Context, url, etag string) (*Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Accept", "application/json, application/yaml, application/vnd.oai.openapi, */*")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10MB cap
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	result := &Result{
		StatusCode:  resp.StatusCode,
		ETag:        resp.Header.Get("ETag"),
		NotModified: resp.StatusCode == http.StatusNotModified,
	}

	if resp.StatusCode == http.StatusNotModified {
		return result, nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch %s: unexpected HTTP %d", url, resp.StatusCode)
	}

	result.Body = body
	return result, nil
}
