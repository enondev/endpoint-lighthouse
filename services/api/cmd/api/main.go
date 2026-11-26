package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/enondev/endpoint-lighthouse-api/internal/cloudflare"
	"github.com/enondev/endpoint-lighthouse-api/internal/config"
	"github.com/enondev/endpoint-lighthouse-api/internal/handlers"
	"github.com/enondev/endpoint-lighthouse-api/internal/observability"
	"github.com/enondev/endpoint-lighthouse-api/internal/redis"
	"github.com/enondev/endpoint-lighthouse-api/internal/sse"
)

type DriftWebhookPayload struct {
	IncidentID     int64  `json:"incident_id,omitempty"`
	EndpointID     string `json:"endpoint_id"`
	EndpointName   string `json:"endpoint_name,omitempty"`
	OldSchemaHash  string `json:"old_schema_hash"`
	NewSchemaHash  string `json:"new_schema_hash"`
	OldSnapshotKey string `json:"old_snapshot_key"`
	NewSnapshotKey string `json:"new_snapshot_key"`
	Severity       string `json:"severity"`
	DiffSummary    string `json:"diff_summary"`
	DetectedAt     string `json:"detected_at"`
}

var (
	snapDeployHTTPClient = &http.Client{Timeout: 45 * time.Second}
	warmUpHTTPClient     = &http.Client{Timeout: 8 * time.Second}
	// Cold-start-resilient backoffs: platforms spin down idle containers around
	// 15 min, so a warmed-up dispatch may still need several retries while the
	// ai-worker container boots. Total wait before giving up is ~50s plus per-request time.
	snapDeployBackoffs = []time.Duration{0, 5 * time.Second, 10 * time.Second, 15 * time.Second, 20 * time.Second}
)

func main() {
	cfg := config.Load()

	logger := observability.InitLogger(cfg.Environment, cfg.LogLevel)

	if err := observability.InitTracing(cfg.OTELServiceName); err != nil {
		logger.Warn("failed to initialize OpenTelemetry tracing", slog.String("error", err.Error()))
	}
	defer func() {
		if err := observability.Shutdown(context.Background()); err != nil {
			logger.Warn("failed to shutdown OpenTelemetry tracer", slog.String("error", err.Error()))
		}
	}()

	rdb, err := redis.NewClient(cfg.RedisURL)
	if err != nil {
		logger.Error("failed to configure redis", slog.String("error", err.Error()))
		return
	}

	d1 := cloudflare.NewD1Client(cfg.CFAccountID, cfg.CFD1DatabaseID, cfg.CFAPIToken)
	hub := sse.NewHub(logger)
	apiHandler := handlers.NewAPIHandler(d1, hub, logger)

	mux := http.NewServeMux()

	// Metrics endpoint (instrumented separately so it doesn't count itself)
	mux.Handle("GET /metrics", observability.MetricsHandler())

	// Health & Keep-Alive (with dependency probes)
	mux.Handle("GET /health", withRequestID(logger, observability.Handler("/health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		status := "ok"
		details := map[string]string{"service": "api"}

		// Redis dependency probe
		if err := rdb.Ping(ctx); err != nil {
			status = "degraded"
			details["redis"] = "unreachable: " + err.Error()
		} else {
			details["redis"] = "ok"
		}

		// D1 dependency probe
		if _, err := d1.Query(ctx, "SELECT 1 AS ok"); err != nil {
			status = "degraded"
			details["d1"] = "unreachable: " + err.Error()
		} else {
			details["d1"] = "ok"
		}

		w.Header().Set("Content-Type", "application/json")
		if status != "ok" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "details": details})
	}))))

	// Public REST & SSE Endpoints
	mux.Handle("GET /api/v1/status", withCORS(cfg.AllowedOrigin, withRequestID(logger, observability.Handler("/api/v1/status", http.HandlerFunc(apiHandler.HandleStatus)))))
	mux.Handle("GET /api/v1/endpoints", withCORS(cfg.AllowedOrigin, withRequestID(logger, observability.Handler("/api/v1/endpoints", http.HandlerFunc(apiHandler.HandleEndpoints)))))
	mux.Handle("GET /api/v1/incidents", withCORS(cfg.AllowedOrigin, withRequestID(logger, observability.Handler("/api/v1/incidents", http.HandlerFunc(apiHandler.HandleIncidents)))))
	mux.Handle("GET /api/v1/stream", withCORS(cfg.AllowedOrigin, withRequestID(logger, hub)))

	// Internal Webhook: Drift Detected (from probe)
	mux.Handle("POST /internal/drift", withRequestID(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := logger.With(append(observability.WithTrace(ctx), slog.String("handler", "drift"))...)

		if !validSecret(r.Header.Get("X-Internal-Secret"), cfg.InternalWebhookSecret) {
			logger.Warn("drift webhook unauthorized")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var payload DriftWebhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			logger.Warn("drift webhook invalid json", slog.String("error", err.Error()))
			http.Error(w, "invalid json payload", http.StatusBadRequest)
			return
		}

		logger.Info("drift webhook received",
			slog.String("endpoint_id", payload.EndpointID),
			slog.String("new_schema_hash", payload.NewSchemaHash),
			slog.String("severity", payload.Severity),
		)

		if payload.EndpointID == "" || payload.NewSchemaHash == "" {
			logger.Warn("drift webhook missing required fields")
			http.Error(w, "missing endpoint_id or new_schema_hash", http.StatusBadRequest)
			return
		}

		cooldownKey := fmt.Sprintf("cooldown:%s:%s", payload.EndpointID, payload.NewSchemaHash)
		acquired, err := rdb.AcquireCooldown(ctx, cooldownKey, cfg.CooldownTTL)
		if err != nil {
			logger.Warn("redis cooldown check failed (failing open)", slog.String("error", err.Error()))
			acquired = true
			observability.RecordRedisCooldown("error")
		} else if acquired {
			observability.RecordRedisCooldown("acquired")
		} else {
			observability.RecordRedisCooldown("suppressed")
		}

		w.Header().Set("Content-Type", "application/json")
		if !acquired {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"suppressed","reason":"cooldown_active"}`))
			return
		}

		rawJSON, _ := json.Marshal(payload)
		hub.Broadcast("drift_detected", string(rawJSON))
		observability.RecordSSEBroadcast("drift_detected")

		if cfg.SnapDeployWorkerURL != "" {
			go triggerSnapDeploy(logger, ctx, cfg.SnapDeployWorkerURL, cfg.InternalWebhookSecret, rawJSON)
		}

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"dispatched"}`))
	})))

	// Internal Webhook: AI Analysis Complete (from ai-worker)
	mux.Handle("POST /internal/analysis-completed", withRequestID(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := logger.With(append(observability.WithTrace(ctx), slog.String("handler", "analysis-completed"))...)

		if !validSecret(r.Header.Get("X-Internal-Secret"), cfg.InternalWebhookSecret) {
			logger.Warn("analysis webhook unauthorized")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		apiHandler.HandleAnalysisComplete(w, r)
		observability.RecordSSEBroadcast("analysis_ready")
	})))

	// Internal Admin Endpoint: On-Demand Reconciliation Sweeper
	mux.Handle("POST /internal/reconcile", withRequestID(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := logger.With(append(observability.WithTrace(ctx), slog.String("handler", "reconcile"))...)

		if !validSecret(r.Header.Get("X-Internal-Secret"), cfg.InternalWebhookSecret) {
			logger.Warn("reconcile unauthorized")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		dispatched, err := reconcilePending(ctx, apiHandler, rdb, cfg.SnapDeployWorkerURL, cfg.InternalWebhookSecret, 10, logger)
		if err != nil {
			logger.Error("reconcile failed", slog.String("error", err.Error()))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		logger.Info("reconcile complete", slog.Int("dispatched", dispatched))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprintf(w, `{"status":"reconciled","dispatched":%d}`, dispatched)
	})))

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Periodic Self-Healing Reconciliation Sweeper.
	// Runs on a sub-15-minute ticker to (a) keep the ai-worker container warm
	// against platform idle spin-down (~15 min) and (b) deterministically drain
	// any open/backlogged incidents awaiting AI analysis.
	if cfg.SnapDeployWorkerURL != "" {
		go func() {
			time.Sleep(15 * time.Second)
			runSweep := func() {
				// Keep the worker warm even when idle (prevents ~15 min spin-down).
				warmUpWorker(logger, cfg.SnapDeployWorkerURL)

				// Drain the backlog of pending incidents, bounded per cycle so a single
				// sweep can't block the ticker. Cold-start retries happen inside
				// triggerSnapDeploy, so a 2-minute cycle budget covers boot + dispatch.
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				totalDispatched := 0
				for i := 0; i < 5; i++ {
					dispatched, err := reconcilePending(ctx, apiHandler, rdb, cfg.SnapDeployWorkerURL, cfg.InternalWebhookSecret, 10, logger)
					if err != nil {
						logger.Warn("reconciliation sweep batch failed", slog.Int("batch", i), slog.String("error", err.Error()))
						break
					}
					totalDispatched += dispatched
					if dispatched == 0 {
						break
					}
				}
				if totalDispatched > 0 {
					logger.Info("reconciliation sweep drained backlog", slog.Int("dispatched", totalDispatched))
				}
			}
			runSweep()
			ticker := time.NewTicker(cfg.ReconcilePeriod)
			defer ticker.Stop()
			for range ticker.C {
				runSweep()
			}
		}()
	}

	// Metrics server on dedicated port
	go func() {
		muxMetrics := http.NewServeMux()
		muxMetrics.Handle("/metrics", observability.MetricsHandler())
		metricsSrv := &http.Server{
			Addr:              ":" + cfg.MetricsPort,
			Handler:           muxMetrics,
			ReadHeaderTimeout: 5 * time.Second,
		}
		logger.Info("metrics server listening", slog.String("addr", ":"+cfg.MetricsPort))
		if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server stopped", slog.String("error", err.Error()))
		}
	}()

	logger.Info("Endpoint Lighthouse api listening", slog.String("addr", ":"+cfg.Port))
	if err := srv.ListenAndServe(); err != nil {
		logger.Error("server stopped", slog.String("error", err.Error()))
	}
}

func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", binary.BigEndian.Uint64(b))
}

func withRequestID(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = newRequestID()
		}
		w.Header().Set("X-Request-ID", requestID)

		ctx := context.WithValue(r.Context(), requestIDKey{}, requestID)
		logger := logger.With(
			append(observability.WithTrace(ctx), slog.String("request_id", requestID), slog.String("method", r.Method), slog.String("path", r.URL.Path))...,
		)
		logger.Info("request started")

		start := time.Now()
		next.ServeHTTP(w, r.WithContext(ctx))

		duration := time.Since(start)
		logger.Info("request completed", slog.Duration("duration", duration))
	})
}

type requestIDKey struct{}

func requestIDFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey{}).(string); ok {
		return v
	}
	return ""
}

func validSecret(provided, expected string) bool {
	if expected == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

// warmUpWorker pings the ai-worker /health endpoint to keep the container warm
// against platform idle spin-down (~15 min). Errors are logged but never fail
// the sweep cycle: the dispatch retries below handle cold starts.
func warmUpWorker(logger *slog.Logger, workerURL string) {
	if workerURL == "" {
		return
	}
	target := strings.TrimRight(workerURL, "/") + "/health"
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		logger.Warn("worker warm-up request build failed", slog.String("error", err.Error()))
		return
	}
	if _, err := warmUpHTTPClient.Do(req); err != nil {
		logger.Debug("worker warm-up ping failed (container may be cold)", slog.String("error", err.Error()))
		return
	}
	logger.Debug("worker warm-up ping ok")
}

func reconcilePending(
	ctx context.Context,
	h *handlers.APIHandler,
	rdb *redis.Client,
	workerURL string,
	secret string,
	limit int,
	logger *slog.Logger,
) (int, error) {
	if workerURL == "" {
		return 0, nil
	}
	pending, err := h.FetchPendingIncidents(ctx, limit)
	if err != nil {
		return 0, err
	}
	dispatched := 0
	for _, inc := range pending {
		lockKey := fmt.Sprintf("reconcile:incident:%d", inc.IncidentID)
		acquired, lockErr := rdb.AcquireCooldown(ctx, lockKey, 5*time.Minute)
		if lockErr != nil {
			logger.Warn("redis reconcile lock failed (failing open)", slog.String("error", lockErr.Error()))
			acquired = true
		}
		if !acquired {
			continue
		}
		body, err := json.Marshal(inc)
		if err != nil {
			continue
		}
		// Fire-and-forget dispatch. The Redis lock (5-min TTL) prevents concurrent sweeps from
		// double-dispatching the same incident simultaneously. If the worker takes longer than
		// the TTL, a later sweep may re-dispatch; that is safe because
		// CloudflareClient.update_d1_incident is idempotent (upsert on endpoint_id +
		// old_schema_hash + new_schema_hash). The incident stays OPEN until the AI worker's
		// /internal/analysis-completed callback arrives, so dispatched count reflects
		// dispatches issued, not completions confirmed.
		go triggerSnapDeploy(logger, ctx, workerURL, secret, body)
		dispatched++
	}
	return dispatched, nil
}

func triggerSnapDeploy(logger *slog.Logger, ctx context.Context, workerURL, secret string, body []byte) {
	targetURL := strings.TrimRight(workerURL, "/") + "/analyze"

	for attempt, wait := range snapDeployBackoffs {
		if wait > 0 {
			time.Sleep(wait)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
		if err != nil {
			logger.Error("snapdeploy request build failed", slog.String("error", err.Error()))
			observability.RecordSnapDeployAttempt("error")
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if secret != "" {
			req.Header.Set("X-Internal-Secret", secret)
		}

		resp, err := snapDeployHTTPClient.Do(req)
		if err != nil {
			logger.Warn("snapdeploy attempt failed", slog.Int("attempt", attempt+1), slog.String("error", err.Error()))
			observability.RecordSnapDeployAttempt("error")
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			logger.Info("snapdeploy triggered", slog.Int("status", resp.StatusCode), slog.Int("attempt", attempt+1))
			observability.RecordSnapDeployAttempt("success")
			return
		}
		logger.Warn("snapdeploy attempt non-success", slog.Int("status", resp.StatusCode), slog.String("body", string(respBody)), slog.Int("attempt", attempt+1))
		observability.RecordSnapDeployAttempt("error")
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return
		}
	}
	logger.Error("snapdeploy exhausted all retries", slog.String("url", targetURL))
	observability.RecordSnapDeployAttempt("exhausted")
}

func withCORS(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
