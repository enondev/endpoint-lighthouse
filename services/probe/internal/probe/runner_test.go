package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/enondev/endpoint-lighthouse-probe/internal/cloudflare"
	"github.com/enondev/endpoint-lighthouse-probe/internal/config"
	"github.com/enondev/endpoint-lighthouse-probe/internal/schema"
)

func newTestRunner() *Runner {
	return &Runner{
		cfg:           config.Config{InternalWebhookSecret: "test-secret"},
		cf:            &cloudflare.Client{},
		httpClient:    &http.Client{},
		Metrics:       &Metrics{},
		etags:         make(map[string]string),
		lastModified:  make(map[string]string),
		baselineCache: make(map[string]*schema.Node),
		baselineHash:  make(map[string]string),
		endpoints:     make(map[string]cloudflare.Endpoint),
	}
}

func TestReprobeFound(t *testing.T) {
	r := newTestRunner()
	ep := cloudflare.Endpoint{
		ID:             "ep-1",
		Name:           "Test Endpoint",
		URL:            "https://example.com",
		Method:         "GET",
		PollIntervalSec: 60,
	}
	r.mu.Lock()
	r.endpoints["ep-1"] = ep
	r.mu.Unlock()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()
	ep.URL = srv.URL
	r.mu.Lock()
	r.endpoints["ep-1"] = ep
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if !r.Reprobe(ctx, "ep-1") {
		t.Fatal("expected Reprobe to return true for existing endpoint")
	}
}

func TestReprobeNotFound(t *testing.T) {
	r := newTestRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if r.Reprobe(ctx, "nonexistent") {
		t.Fatal("expected Reprobe to return false for non-existent endpoint")
	}
}

func TestClassifySeverity(t *testing.T) {
	if got := classifySeverity(nil); got != "PENDING" {
		t.Fatalf("expected PENDING for empty changes, got %s", got)
	}

	changes := []schema.DriftChange{{Path: "$.field", Kind: "removed_field", Breaking: true}}
	if got := classifySeverity(changes); got != "BREAKING" {
		t.Fatalf("expected BREAKING for breaking change, got %s", got)
	}

	changes = []schema.DriftChange{{Path: "$.field", Kind: "added_field", Breaking: false}}
	if got := classifySeverity(changes); got != "NON_BREAKING" {
		t.Fatalf("expected NON_BREAKING for additive change, got %s", got)
	}

	changes = []schema.DriftChange{
		{Path: "$.added", Kind: "added_field", Breaking: false},
		{Path: "$.removed", Kind: "removed_field", Breaking: true},
	}
	if got := classifySeverity(changes); got != "BREAKING" {
		t.Fatalf("expected BREAKING for mixed changes with breaking, got %s", got)
	}
}

func TestCountFields(t *testing.T) {
	if got := countFields(nil); got != 0 {
		t.Fatalf("expected 0 for nil node, got %d", got)
	}

	n := &schema.Node{Types: []string{"string"}}
	if got := countFields(n); got != 0 {
		t.Fatalf("expected 0 for node with no properties, got %d", got)
	}

	n = &schema.Node{
		Properties: map[string]*schema.Node{
			"name": {Types: []string{"string"}},
			"age":  {Types: []string{"integer"}},
		},
	}
	if got := countFields(n); got != 2 {
		t.Fatalf("expected 2 for node with 2 properties, got %d", got)
	}

	n = &schema.Node{
		Properties: map[string]*schema.Node{
			"address": {
				Types:      []string{"object"},
				Properties: map[string]*schema.Node{
					"street": {Types: []string{"string"}},
					"city":   {Types: []string{"string"}},
				},
			},
		},
	}
	if got := countFields(n); got != 3 {
		t.Fatalf("expected 3 for nested node (address + street + city), got %d", got)
	}
}
