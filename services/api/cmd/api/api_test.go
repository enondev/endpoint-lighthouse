package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildTestMux wires the exact HTTP routes from main.go against the test environment.
func buildTestMux(env *testEnv, internalSecret string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","service":"api"}`))
	})

	mux.HandleFunc("GET /api/v1/status", env.Handler.HandleStatus)
	mux.HandleFunc("GET /api/v1/endpoints", env.Handler.HandleEndpoints)
	mux.HandleFunc("GET /api/v1/incidents", env.Handler.HandleIncidents)

	mux.HandleFunc("POST /internal/drift", func(w http.ResponseWriter, r *http.Request) {
		if !validSecret(r.Header.Get("X-Internal-Secret"), internalSecret) {
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
		acquired, err := env.RDB.AcquireCooldown(r.Context(), cooldownKey, 15*time.Minute)
		if err != nil {
			acquired = true
		}

		w.Header().Set("Content-Type", "application/json")
		if !acquired {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"suppressed","reason":"cooldown_active"}`))
			return
		}

		rawJSON, _ := json.Marshal(payload)
		env.Hub.Broadcast("drift_detected", string(rawJSON))

		if env.SnapDeploy.Server.URL != "" {
			go triggerSnapDeploy(env.Logger, r.Context(), env.SnapDeploy.Server.URL, internalSecret, rawJSON)
		}

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"dispatched"}`))
	})

	mux.HandleFunc("POST /internal/analysis-completed", func(w http.ResponseWriter, r *http.Request) {
		if !validSecret(r.Header.Get("X-Internal-Secret"), internalSecret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		env.Handler.HandleAnalysisComplete(w, r)
	})

	mux.HandleFunc("POST /internal/reconcile", func(w http.ResponseWriter, r *http.Request) {
		if !validSecret(r.Header.Get("X-Internal-Secret"), internalSecret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		dispatched, err := reconcilePending(r.Context(), env.Handler, env.RDB, env.SnapDeploy.Server.URL, internalSecret, 10, env.Logger)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprintf(w, `{"status":"reconciled","dispatched":%d}`, dispatched)
	})

	mux.HandleFunc("POST /internal/reconcile-all", func(w http.ResponseWriter, r *http.Request) {
		if !validSecret(r.Header.Get("X-Internal-Secret"), internalSecret) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		dispatched, err := reconcileAll(r.Context(), env.Handler, env.RDB, env.SnapDeploy.Server.URL, internalSecret, 50, env.Logger)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprintf(w, `{"status":"reconciled-all","dispatched":%d}`, dispatched)
	})

	return mux
}

func TestHealthEndpoint(t *testing.T) {
	env := setupTestEnv(t)
	srv := buildTestMux(env, "secret-123")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	if got := rec.Body.String(); got != `{"status":"ok","service":"api"}` {
		t.Fatalf("unexpected body: %s", got)
	}
}

func TestInternalEndpointsAuthGuard(t *testing.T) {
	env := setupTestEnv(t)
	srv := buildTestMux(env, "super-secret")

	routes := []string{
		"/internal/drift",
		"/internal/analysis-completed",
		"/internal/reconcile",
		"/internal/reconcile-all",
	}

	for _, path := range routes {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(`{}`))
			req.Header.Set("X-Internal-Secret", "wrong-secret")
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("expected 401 Unauthorized for %s, got %d", path, rec.Code)
			}
		})
	}
}

func TestInternalDriftCooldownAndDispatch(t *testing.T) {
	env := setupTestEnv(t)
	secret := "test-secret"
	srv := buildTestMux(env, secret)

	payload := `{"incident_id":42,"endpoint_id":"ep-1","new_schema_hash":"sha-new","severity":"PENDING"}`

	req1 := httptest.NewRequest(http.MethodPost, "/internal/drift", bytes.NewBufferString(payload))
	req1.Header.Set("X-Internal-Secret", secret)
	rec1 := httptest.NewRecorder()
	srv.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d (%s)", rec1.Code, rec1.Body.String())
	}
	if !bytes.Contains(rec1.Body.Bytes(), []byte(`"dispatched"`)) {
		t.Fatalf("expected dispatched status, got %s", rec1.Body.String())
	}

	reqs := env.SnapDeploy.WaitForRequests(t, 1, time.Second)
	first := reqs[0]
	if first.Path != "/analyze" || first.Secret != secret {
		t.Fatalf("unexpected snapdeploy request: %+v", first)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/internal/drift", bytes.NewBufferString(payload))
	req2.Header.Set("X-Internal-Secret", secret)
	rec2 := httptest.NewRecorder()
	srv.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", rec2.Code)
	}
	if !bytes.Contains(rec2.Body.Bytes(), []byte(`"suppressed"`)) {
		t.Fatalf("expected suppressed status on duplicate drift, got %s", rec2.Body.String())
	}
}

func TestInternalDriftFailsOpenWhenRedisDown(t *testing.T) {
	env := setupTestEnv(t)
	secret := "test-secret"
	srv := buildTestMux(env, secret)

	env.Redis.FailNext = true

	payload := `{"incident_id":99,"endpoint_id":"ep-failopen","new_schema_hash":"sha-99"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/drift", bytes.NewBufferString(payload))
	req.Header.Set("X-Internal-Secret", secret)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on Redis fail-open, got %d", rec.Code)
	}
	env.SnapDeploy.WaitForRequests(t, 1, time.Second)
}

func TestTriggerSnapDeployRetriesTransientFailures(t *testing.T) {
	env := setupTestEnv(t)

	env.SnapDeploy.StatusQueue = []int{
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusAccepted,
	}

	triggerSnapDeploy(env.Logger, context.Background(), env.SnapDeploy.Server.URL, "sec-retry", []byte(`{"incident_id":7}`))

	reqs := env.SnapDeploy.WaitForRequests(t, 3, time.Second)
	if len(reqs) != 3 {
		t.Fatalf("expected 3 attempts (2 retries + 1 success), got %d", len(reqs))
	}
}

func TestTriggerSnapDeployStopsOnClientError(t *testing.T) {
	env := setupTestEnv(t)

	env.SnapDeploy.StatusQueue = []int{http.StatusBadRequest, http.StatusAccepted}

	triggerSnapDeploy(env.Logger, context.Background(), env.SnapDeploy.Server.URL, "sec-400", []byte(`{"incident_id":8}`))

	reqs := env.SnapDeploy.WaitForRequests(t, 1, time.Second)
	if len(reqs) != 1 {
		t.Fatalf("expected immediate abort on 400 Bad Request, got %d attempts", len(reqs))
	}
}

func TestReconcilePendingConcurrentLocking(t *testing.T) {
	env := setupTestEnv(t)
	secret := "reconcile-secret"
	srv := buildTestMux(env, secret)

	env.D1.Handler = func(sql string, params []any) ([]map[string]any, int, error) {
		return []map[string]any{
			{
				"incident_id":     float64(101),
				"endpoint_id":     "ep-alpha",
				"endpoint_name":   "Alpha API",
				"endpoint_url":    "https://example.com",
				"old_schema_hash": "h1",
				"new_schema_hash": "h2",
				"baseline_kv_key": "k1",
				"drifted_kv_key":  "k2",
			},
			{
				"incident_id":     float64(102),
				"endpoint_id":     "ep-beta",
				"endpoint_name":   "Beta API",
				"endpoint_url":    "https://example.com",
				"old_schema_hash": "h3",
				"new_schema_hash": "h4",
				"baseline_kv_key": "k3",
				"drifted_kv_key":  "k4",
			},
		}, http.StatusOK, nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/internal/reconcile", nil)
			req.Header.Set("X-Internal-Secret", secret)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusAccepted {
				t.Errorf("expected 202 Accepted, got %d", rec.Code)
			}
		}()
	}
	wg.Wait()

	reqs := env.SnapDeploy.WaitForRequests(t, 2, time.Second)
	if len(reqs) != 2 {
		t.Fatalf("expected exactly 2 dispatched incidents, got %d", len(reqs))
	}
}

func TestReconcileAllDispatches(t *testing.T) {
	env := setupTestEnv(t)
	secret := "reconcile-all-secret"
	srv := buildTestMux(env, secret)

	env.D1.Handler = func(sql string, params []any) ([]map[string]any, int, error) {
		return []map[string]any{
			{
				"incident_id":     float64(201),
				"endpoint_id":     "ep-gamma",
				"endpoint_name":   "Gamma API",
				"endpoint_url":    "https://gamma.example.com",
				"old_schema_hash": "g1",
				"new_schema_hash": "g2",
				"baseline_kv_key": "gk1",
				"drifted_kv_key":  "gk2",
			},
			{
				"incident_id":     float64(202),
				"endpoint_id":     "ep-delta",
				"endpoint_name":   "Delta API",
				"endpoint_url":    "https://delta.example.com",
				"old_schema_hash": "d1",
				"new_schema_hash": "d2",
				"baseline_kv_key": "dk1",
				"drifted_kv_key":  "dk2",
			},
		}, http.StatusOK, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/internal/reconcile-all", nil)
	req.Header.Set("X-Internal-Secret", secret)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"reconciled-all"`)) {
		t.Fatalf("expected reconciled-all status, got %s", rec.Body.String())
	}

	// With batchSize=5 and only 2 incidents, all should dispatch in one batch.
	reqs := env.SnapDeploy.WaitForRequests(t, 2, time.Second)
	if len(reqs) != 2 {
		t.Fatalf("expected exactly 2 dispatched incidents, got %d", len(reqs))
	}
	for _, r := range reqs {
		if r.Path != "/analyze" {
			t.Errorf("unexpected path %s, expected /analyze", r.Path)
		}
		if r.Secret != secret {
			t.Errorf("unexpected secret %s, expected %s", r.Secret, secret)
		}
	}
}

func TestReconcileAllEmpty(t *testing.T) {
	env := setupTestEnv(t)
	secret := "reconcile-all-empty-secret"
	srv := buildTestMux(env, secret)

	env.D1.Handler = func(sql string, params []any) ([]map[string]any, int, error) {
		return []map[string]any{}, http.StatusOK, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/internal/reconcile-all", nil)
	req.Header.Set("X-Internal-Secret", secret)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"dispatched":0`)) {
		t.Fatalf("expected dispatched:0 for empty incidents, got %s", rec.Body.String())
	}

	time.Sleep(300 * time.Millisecond)
	env.SnapDeploy.mu.Lock()
	got := len(env.SnapDeploy.Requests)
	env.SnapDeploy.mu.Unlock()
	if got != 0 {
		t.Fatalf("expected no dispatches for empty incidents, got %d", got)
	}
}

func TestAnalysisCompletedBroadcastsSSE(t *testing.T) {
	env := setupTestEnv(t)
	secret := "analysis-secret"
	srv := buildTestMux(env, secret)

	body := `{
		"incident_id": 101,
		"endpoint_id": "ep-alpha",
		"severity": "BREAKING",
		"ai_explanation": "Required field removed",
		"remediation_suggestion": "Restore field or bump API version"
	}`

	req := httptest.NewRequest(http.MethodPost, "/internal/analysis-completed", bytes.NewBufferString(body))
	req.Header.Set("X-Internal-Secret", secret)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from /internal/analysis-completed, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"broadcasted"`)) {
		t.Fatalf("expected broadcasted response, got %s", rec.Body.String())
	}

	// Malformed JSON should return 400 Bad Request
	badReq := httptest.NewRequest(http.MethodPost, "/internal/analysis-completed", bytes.NewBufferString(`{not-json`))
	badReq.Header.Set("X-Internal-Secret", secret)
	badRec := httptest.NewRecorder()
	srv.ServeHTTP(badRec, badReq)

	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request on malformed JSON, got %d", badRec.Code)
	}
}

func TestPublicEndpointsD1ErrorHandling(t *testing.T) {
	env := setupTestEnv(t)
	srv := buildTestMux(env, "secret")

	env.D1.Handler = func(sql string, params []any) ([]map[string]any, int, error) {
		// Return account info for auth queries
		if strings.Contains(sql, "accounts") && strings.Contains(sql, "api_key") {
			return []map[string]any{
				{
					"id":                  "acc-test-1",
					"name":                "Test Account",
					"api_key":             "test-api-key",
					"plan_tier":           "free",
					"rate_limit_tier":     "free",
					"webhook_endpoint_limit": 3,
					"endpoint_limit":      5,
					"incident_limit_monthly": 100,
					"created_at":          "2026-01-01T00:00:00Z",
					"updated_at":          "2026-01-01T00:00:00Z",
				},
			}, http.StatusOK, nil
		}
		// Return error for all other queries
		return nil, http.StatusBadGateway, errors.New("d1 upstream unavailable")
	}

	for _, path := range []string{"/api/v1/status", "/api/v1/endpoints", "/api/v1/incidents"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("X-API-Key", "test-api-key")
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code < 500 {
				t.Errorf("expected 5xx error when D1 fails on %s, got %d", path, rec.Code)
			}
		})
	}
}
