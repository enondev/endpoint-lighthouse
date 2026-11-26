package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enondev/endpoint-lighthouse-api/internal/observability"
)

// TestMetricsHandlerExposesPrometheusOutput verifies the /metrics endpoint
// returns a valid Prometheus text exposition with the expected metric families.
func TestMetricsHandlerExposesPrometheusOutput(t *testing.T) {
	// Prime the metrics: handle a request through the middleware to populate
	// http_requests_*, then call Record* helpers for the remaining families.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	wrapped := observability.Handler("/api/v1/priming", inner)
	wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/priming", nil))

	observability.RecordD1Query(true, 0)
	observability.RecordRedisCooldown("acquired")
	observability.RecordSSEBroadcast("drift_detected")
	observability.SetSSEClientsConnected(1)
	observability.RecordSnapDeployAttempt("success")

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()

	observability.MetricsHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	body := rec.Body.String()
	expected := []string{
		"endpoint_lighthouse_http_requests_total",
		"endpoint_lighthouse_http_request_duration_seconds",
		"endpoint_lighthouse_d1_queries_total",
		"endpoint_lighthouse_d1_query_duration_seconds",
		"endpoint_lighthouse_redis_cooldown_total",
		"endpoint_lighthouse_sse_clients_connected",
		"endpoint_lighthouse_sse_broadcasts_total",
		"endpoint_lighthouse_snap_deploy_attempts_total",
	}
	for _, metric := range expected {
		if !strings.Contains(body, metric) {
			t.Errorf("metrics output missing %q", metric)
		}
	}
}

// TestHandlerMiddlewareRecordsRequestMetrics verifies the observability.Handler
// wrapper increments request counters and observes duration.
func TestHandlerMiddlewareRecordsRequestMetrics(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	wrapped := observability.Handler("/api/v1/test", inner)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rec.Code)
	}

	// Metrics are registered globally; verify they can be scraped without panic.
	body := observabilityMetricsBody()
	if !strings.Contains(body, "endpoint_lighthouse_http_requests_total") {
		t.Error("expected http_requests_total metric after handled request")
	}
	if !strings.Contains(body, "/api/v1/test") {
		t.Error("expected path label /api/v1/test in metrics")
	}
}

// observabilityMetricsBody scrapes the current Prometheus registry output.
func observabilityMetricsBody() string {
	rec := httptest.NewRecorder()
	observability.MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

// TestWithTraceReturnsEmptyWhenNoSpan verifies WithTrace returns nil when
// the context carries no recording OpenTelemetry span.
func TestWithTraceReturnsEmptyWhenNoSpan(t *testing.T) {
	attrs := observability.WithTrace(context.Background())
	if len(attrs) != 0 {
		t.Errorf("expected no trace attributes without a span, got %v", attrs)
	}
}
