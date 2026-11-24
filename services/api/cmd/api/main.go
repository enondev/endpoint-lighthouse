package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/enondev/endpoint-lighthouse-api/internal/cloudflare"
	"github.com/enondev/endpoint-lighthouse-api/internal/config"
	"github.com/enondev/endpoint-lighthouse-api/internal/handlers"
	"github.com/enondev/endpoint-lighthouse-api/internal/redis"
	"github.com/enondev/endpoint-lighthouse-api/internal/sse"
)

type DriftWebhookPayload struct {
	IncidentID     int64  `json:"incident_id,omitempty"` // Matches drift_incidents.id (INTEGER)
	EndpointID     string `json:"endpoint_id"`           // Matches endpoints.id (TEXT)
	EndpointName   string `json:"endpoint_name,omitempty"`
	OldSchemaHash  string `json:"old_schema_hash"`
	NewSchemaHash  string `json:"new_schema_hash"`
	OldSnapshotKey string `json:"old_snapshot_key"` // Matches schema_baselines.kv_snapshot_key
	NewSnapshotKey string `json:"new_snapshot_key"`
	Severity       string `json:"severity"` // 'PENDING' | 'NON_BREAKING' | 'WARNING' | 'BREAKING'
	DiffSummary    string `json:"diff_summary"`
	DetectedAt     string `json:"detected_at"` // Matches drift_incidents.detected_at
}

var (
	snapDeployHTTPClient = &http.Client{Timeout: 45 * time.Second}
	snapDeployBackoffs   = []time.Duration{0, 10 * time.Second, 20 * time.Second, 35 * time.Second}
)

func main() {
	cfg := config.Load()

	rdb, err := redis.NewClient(cfg.RedisURL)
	if err != nil {
		log.Fatalf("failed to configure redis: %v", err)
	}

	d1 := cloudflare.NewD1Client(cfg.CFAccountID, cfg.CFD1DatabaseID, cfg.CFAPIToken)
	hub := sse.NewHub()
	apiHandler := handlers.NewAPIHandler(d1, hub)

	mux := http.NewServeMux()

	// Health & Keep-Alive
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","service":"api"}`))
	})

	// Public REST & SSE Endpoints
	mux.Handle("GET /api/v1/status", withCORS(cfg.AllowedOrigin, http.HandlerFunc(apiHandler.HandleStatus)))
	mux.Handle("GET /api/v1/endpoints", withCORS(cfg.AllowedOrigin, http.HandlerFunc(apiHandler.HandleEndpoints)))
	mux.Handle("GET /api/v1/incidents", withCORS(cfg.AllowedOrigin, http.HandlerFunc(apiHandler.HandleIncidents)))
	mux.Handle("GET /api/v1/stream", withCORS(cfg.AllowedOrigin, hub))

	// Internal Webhook: Drift Detected (from probe)
	mux.HandleFunc("POST /internal/drift", func(w http.ResponseWriter, r *http.Request) {
		if !validSecret(r.Header.Get("X-Internal-Secret"), cfg.InternalWebhookSecret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var payload DriftWebhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid json payload", http.StatusBadRequest)
			return
		}

		if payload.EndpointID == "" || payload.NewSchemaHash == "" {
			http.Error(w, "missing endpoint_id or new_schema_hash", http.StatusBadRequest)
			return
		}

		cooldownKey := fmt.Sprintf("cooldown:%s:%s", payload.EndpointID, payload.NewSchemaHash)
		acquired, err := rdb.AcquireCooldown(cooldownKey, cfg.CooldownTTL)
		if err != nil {
			log.Printf("[WARN] redis cooldown check failed (failing open): %v", err)
			acquired = true
		}

		w.Header().Set("Content-Type", "application/json")
		if !acquired {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"suppressed","reason":"cooldown_active"}`))
			return
		}

		rawJSON, _ := json.Marshal(payload)
		hub.Broadcast("drift_detected", string(rawJSON))

		if cfg.SnapDeployWorkerURL != "" {
			go triggerSnapDeploy(cfg.SnapDeployWorkerURL, cfg.InternalWebhookSecret, rawJSON)
		}

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"dispatched"}`))
	})

	// Internal Webhook: AI Analysis Complete (from ai-worker)
	mux.HandleFunc("POST /internal/analysis-completed", func(w http.ResponseWriter, r *http.Request) {
		if !validSecret(r.Header.Get("X-Internal-Secret"), cfg.InternalWebhookSecret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		apiHandler.HandleAnalysisComplete(w, r)
	})

	// Internal Admin Endpoint: On-Demand Reconciliation Sweeper for Unanalyzed OPEN Incidents
	mux.HandleFunc("POST /internal/reconcile", func(w http.ResponseWriter, r *http.Request) {
		if !validSecret(r.Header.Get("X-Internal-Secret"), cfg.InternalWebhookSecret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		dispatched, err := reconcilePending(r.Context(), apiHandler, rdb, cfg.SnapDeployWorkerURL, cfg.InternalWebhookSecret, 10)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprintf(w, `{"status":"reconciled","dispatched":%d}`, dispatched)
	})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Periodic Self-Healing Reconciliation Sweeper (quota-safe: queries D1 first, wakes SnapDeploy only if pending rows exist)
	if cfg.SnapDeployWorkerURL != "" {
		go func() {
			// Initial boot delay to let Render & Redis stabilize
			time.Sleep(15 * time.Second)

			runSweep := func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				dispatched, err := reconcilePending(ctx, apiHandler, rdb, cfg.SnapDeployWorkerURL, cfg.InternalWebhookSecret, 5)
				if err != nil {
					log.Printf("[WARN] periodic reconciliation sweep failed: %v", err)
					return
				}
				if dispatched > 0 {
					log.Printf("[INFO] periodic reconciliation dispatched %d orphaned incident(s)", dispatched)
				}
			}

			runSweep()
			ticker := time.NewTicker(fmt.Sprintf("%d", cfg.ReconcilePeriod) * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				runSweep()
			}
		}()
	}

	log.Printf("Endpoint Lighthouse api listening on :%s", cfg.Port)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}

func validSecret(provided, expected string) bool {
	if expected == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func reconcilePending(
	ctx context.Context,
	h *handlers.APIHandler,
	rdb *redis.Client,
	workerURL string,
	secret string,
	limit int,
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
		acquired, lockErr := rdb.AcquireCooldown(lockKey, 5*time.Minute)
		if lockErr != nil {
			log.Printf("[WARN] redis reconcile lock failed (failing open): %v", lockErr)
			acquired = true
		}
		if !acquired {
			continue
		}
		body, err := json.Marshal(inc)
		if err != nil {
			continue
		}
		go triggerSnapDeploy(workerURL, secret, body)
		dispatched++
	}
	return dispatched, nil
}

func triggerSnapDeploy(workerURL, secret string, body []byte) {
	targetURL := strings.TrimRight(workerURL, "/") + "/analyze"

	for attempt, wait := range snapDeployBackoffs {
		if wait > 0 {
			time.Sleep(wait)
		}
		req, err := http.NewRequest(http.MethodPost, targetURL, bytes.NewReader(body))
		if err != nil {
			log.Printf("[ERROR] snapdeploy request build failed: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if secret != "" {
			req.Header.Set("X-Internal-Secret", secret)
		}

		resp, err := snapDeployHTTPClient.Do(req)
		if err != nil {
			log.Printf("[WARN] snapdeploy attempt %d/%d failed: %v", attempt+1, len(snapDeployBackoffs), err)
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			log.Printf("[INFO] snapdeploy triggered, status=%d", resp.StatusCode)
			return
		}
		log.Printf("[WARN] snapdeploy attempt %d/%d status=%d body=%s", attempt+1, len(snapDeployBackoffs), resp.StatusCode, string(respBody))
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return
		}
	}
	log.Printf("[ERROR] snapdeploy exhausted all retries for %s", targetURL)
}

func withCORS(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
