package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// HTTP request metrics
	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "endpoint_lighthouse_http_requests_total",
			Help: "Total number of HTTP requests handled by the API.",
		},
		[]string{"method", "path", "status"},
	)

	httpRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "endpoint_lighthouse_http_request_duration_seconds",
			Help:  "Duration of HTTP requests handled by the API.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 10),
		},
		[]string{"method", "path"},
	)

	// D1 query metrics
	d1QueriesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "endpoint_lighthouse_d1_queries_total",
			Help: "Total number of Cloudflare D1 queries issued by the API.",
		},
		[]string{"success"},
	)

	d1QueryDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "endpoint_lighthouse_d1_query_duration_seconds",
			Help:  "Duration of Cloudflare D1 queries.",
			Buckets: prometheus.ExponentialBuckets(0.005, 2, 10),
		},
		[]string{"success"},
	)

	// Redis cooldown metrics
	redisCooldownTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "endpoint_lighthouse_redis_cooldown_total",
			Help: "Total number of Redis cooldown (SETNX) attempts.",
		},
		[]string{"outcome"},
	)

	// SSE hub metrics
	sseClientsConnected = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "endpoint_lighthouse_sse_clients_connected",
			Help: "Number of SSE clients currently connected to the stream hub.",
		},
		[]string{},
	)

	sseBroadcastsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "endpoint_lighthouse_sse_broadcasts_total",
			Help: "Total number of SSE broadcast events.",
		},
		[]string{"event_type"},
	)

	// SnapDeploy worker metrics
	snapDeployAttemptsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "endpoint_lighthouse_snap_deploy_attempts_total",
			Help: "Total number of SnapDeploy worker dispatch attempts.",
		},
		[]string{"outcome"},
	)
)

func init() {
	prometheus.MustRegister(
		httpRequestsTotal,
		httpRequestDuration,
		d1QueriesTotal,
		d1QueryDuration,
		redisCooldownTotal,
		sseClientsConnected,
		sseBroadcastsTotal,
		snapDeployAttemptsTotal,
	)
}

// Handler wraps an http.Handler with Prometheus metrics instrumentation.
func Handler(name string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)
		duration := time.Since(start).Seconds()

		path := name
		if path == "" {
			path = r.URL.Path
		}

		httpRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(rw.statusCode)).Inc()
		httpRequestDuration.WithLabelValues(r.Method, path).Observe(duration)
	})
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// MetricsHandler returns an http.Handler that serves the Prometheus metrics endpoint.
func MetricsHandler() http.Handler {
	return promhttp.Handler()
}

// RecordD1Query records the outcome and duration of a D1 query.
func RecordD1Query(success bool, duration time.Duration) {
	d1QueriesTotal.WithLabelValues(strconv.FormatBool(success)).Inc()
	d1QueryDuration.WithLabelValues(strconv.FormatBool(success)).Observe(duration.Seconds())
}

// RecordRedisCooldown records the outcome of a Redis cooldown attempt.
func RecordRedisCooldown(outcome string) {
	redisCooldownTotal.WithLabelValues(outcome).Inc()
}

// RecordSSEBroadcast records an SSE broadcast event.
func RecordSSEBroadcast(eventType string) {
	sseBroadcastsTotal.WithLabelValues(eventType).Inc()
}

// SetSSEClientsConnected sets the number of connected SSE clients.
func SetSSEClientsConnected(n int) {
	sseClientsConnected.WithLabelValues().Set(float64(n))
}

// RecordSnapDeployAttempt records the outcome of a SnapDeploy dispatch attempt.
func RecordSnapDeployAttempt(outcome string) {
	snapDeployAttemptsTotal.WithLabelValues(outcome).Inc()
}
