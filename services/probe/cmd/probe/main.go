package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/enondev/endpoint-lighthouse-probe/internal/cloudflare"
	"github.com/enondev/endpoint-lighthouse-probe/internal/config"
	"github.com/enondev/endpoint-lighthouse-probe/internal/probe"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := config.Load()
	cf := cloudflare.NewClient(cfg.CFAccountID, cfg.CFD1DatabaseID, cfg.CFKVNamespaceID, cfg.CFAPIToken)
	runner := probe.NewRunner(cfg, cf)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go runner.Start(ctx)

	mux := http.NewServeMux()
	startTime := time.Now()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":           "healthy",
			"service":          "endpoint-lighthouse-probe",
			"uptime_sec":       int(time.Since(startTime).Seconds()),
			"active_endpoints": runner.Metrics.ActiveEndpoints.Load(),
			"heap_alloc_mb":    float64(m.HeapAlloc) / (1024 * 1024),
			"sys_mem_mb":       float64(m.Sys) / (1024 * 1024),
			"last_diff_ms":     float64(runner.Metrics.LastDiffMicros.Load()) / 1000.0,
		})
	})

	// On-demand re-probe of a specific endpoint, bypassing poll interval.
	mux.HandleFunc("POST /internal/reprobe/{endpoint_id}", func(w http.ResponseWriter, r *http.Request) {
		if !validSecret(r.Header.Get("X-Internal-Secret"), cfg.InternalWebhookSecret) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		endpointID := r.PathValue("endpoint_id")
		if endpointID == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"missing endpoint_id"}`))
			return
		}
		if runner.Reprobe(r.Context(), endpointID) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"reprobe_dispatched"}`))
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"endpoint not found"}`))
		}
	})

	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "endpoint_lighthouse_probes_total %d\n", runner.Metrics.TotalProbes.Load())
		fmt.Fprintf(w, "endpoint_lighthouse_not_modified_total %d\n", runner.Metrics.NotModifiedHits.Load())
		fmt.Fprintf(w, "endpoint_lighthouse_drift_detected_total %d\n", runner.Metrics.DriftDetected.Load())
		fmt.Fprintf(w, "endpoint_lighthouse_contract_errors_total %d\n", runner.Metrics.ContractErrors.Load())
		fmt.Fprintf(w, "endpoint_lighthouse_last_diff_microseconds %d\n", runner.Metrics.LastDiffMicros.Load())
		fmt.Fprintf(w, "endpoint_lighthouse_memory_sys_bytes %d\n", m.Sys)
		fmt.Fprintf(w, "endpoint_lighthouse_memory_heap_alloc_bytes %d\n", m.HeapAlloc)
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		slog.Info("http server listening", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server error", "err", err)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down probe")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func validSecret(provided, expected string) bool {
	if expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}