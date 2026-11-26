package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchNotModified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"abc123"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc123"`)
		w.Write([]byte(`{"openapi":"3.0.0"}`))
	}))
	defer srv.Close()

	f := New(5 * time.Second)
	ctx := context.Background()

	// First fetch: returns body + etag
	r, err := f.Fetch(ctx, srv.URL, "")
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if r.NotModified {
		t.Fatal("expected body, got NotModified")
	}
	if r.ETag != `"abc123"` {
		t.Fatalf("expected etag %q, got %q", `"abc123"`, r.ETag)
	}

	// Second fetch with etag: 304, no body
	r2, err := f.Fetch(ctx, srv.URL, r.ETag)
	if err != nil {
		t.Fatalf("fetch with etag failed: %v", err)
	}
	if !r2.NotModified {
		t.Fatal("expected NotModified, got body")
	}
	if len(r2.Body) != 0 {
		t.Fatal("expected empty body on 304")
	}
}

func TestFetchUnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`error`))
	}))
	defer srv.Close()

	f := New(5 * time.Second)
	ctx := context.Background()

	_, err := f.Fetch(ctx, srv.URL, "")
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected 500 in error, got: %v", err)
	}
}

func TestFetchReadLimit(t *testing.T) {
	// Body larger than 10MB cap
	largeBody := strings.Repeat("x", 11<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(largeBody))
	}))
	defer srv.Close()

	f := New(5 * time.Second)
	ctx := context.Background()

	r, err := f.Fetch(ctx, srv.URL, "")
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if len(r.Body) != 10<<20 {
		t.Fatalf("expected body capped at 10MB, got %d bytes", len(r.Body))
	}
}
