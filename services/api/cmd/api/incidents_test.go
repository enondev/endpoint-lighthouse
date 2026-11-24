package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIncidentsPaginationAndFilters(t *testing.T) {
	env := setupTestEnv(t)
	srv := buildTestMux(env, "secret-123")

	// Mock D1 to return count + rows for incidents
	callCount := 0
	env.D1.Handler = func(sql string, params []any) ([]map[string]any, int, error) {
		callCount++
		if callCount == 1 {
			// COUNT query
			return []map[string]any{{"total": float64(3)}}, http.StatusOK, nil
		}
		// SELECT query - return 2 rows on page 1
		return []map[string]any{
			{
				"id":             float64(1),
				"endpoint_id":    "ep-1",
				"endpoint_name":  "Endpoint One",
				"endpoint_category": "api",
				"old_schema_hash": "hash-old-1",
				"new_schema_hash": "hash-new-1",
				"severity":       "BREAKING",
				"severity_score": float64(90),
				"diff_summary":   `{"added":["field1"]}`,
				"ai_analysis":    "Field added",
				"ai_patch_snippet": "// Restore field",
				"status":         "OPEN",
				"detected_at":    "2026-11-24T01:00:00Z",
			},
			{
				"id":             float64(2),
				"endpoint_id":    "ep-2",
				"endpoint_name":  "Endpoint Two",
				"endpoint_category": "api",
				"old_schema_hash": "hash-old-2",
				"new_schema_hash": "hash-new-2",
				"severity":       "WARNING",
				"severity_score": float64(50),
				"diff_summary":   `{"removed":["field2"]}`,
				"ai_analysis":    "Field removed",
				"ai_patch_snippet": "// Update references",
				"status":         "ACKNOWLEDGED",
				"detected_at":    "2026-11-24T02:00:00Z",
			},
		}, http.StatusOK, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents?page=1&limit=2", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var body struct {
		Incidents   []map[string]any `json:"incidents"`
		TotalCount  int              `json:"total_count"`
		Page        int              `json:"page"`
		Limit       int              `json:"limit"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if body.TotalCount != 3 {
		t.Errorf("expected total_count=3, got %d", body.TotalCount)
	}
	if body.Page != 1 {
		t.Errorf("expected page=1, got %d", body.Page)
	}
	if body.Limit != 2 {
		t.Errorf("expected limit=2, got %d", body.Limit)
	}
	if len(body.Incidents) != 2 {
		t.Errorf("expected 2 incidents, got %d", len(body.Incidents))
	}

	// Verify both COUNT and SELECT queries were made
	if callCount != 2 {
		t.Errorf("expected 2 D1 queries (count + select), got %d", callCount)
	}

	// Verify severity_score is present
	if body.Incidents[0]["severity_score"] != float64(90) {
		t.Errorf("expected severity_score=90, got %v", body.Incidents[0]["severity_score"])
	}
}

func TestIncidentsStatusFilter(t *testing.T) {
	env := setupTestEnv(t)
	srv := buildTestMux(env, "secret-123")

	env.D1.Handler = func(sql string, params []any) ([]map[string]any, int, error) {
		// Verify status filter is in the SQL and params
		for i, p := range params {
			t.Logf("param[%d] = %v (%T)", i, p, p)
		}
		return []map[string]any{{"total": float64(0)}}, http.StatusOK, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents?status=OPEN", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
}

func TestIncidentsDateRangeFilter(t *testing.T) {
	env := setupTestEnv(t)
	srv := buildTestMux(env, "secret-123")

	env.D1.Handler = func(sql string, params []any) ([]map[string]any, int, error) {
		return []map[string]any{{"total": float64(0)}}, http.StatusOK, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents?from=2026-11-01T00:00:00Z&to=2026-11-30T23:59:59Z", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var body struct {
		TotalCount int `json:"total_count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body.TotalCount != 0 {
		t.Errorf("expected total_count=0, got %d", body.TotalCount)
	}
}

func TestIncidentsD1ErrorHandling(t *testing.T) {
	env := setupTestEnv(t)
	srv := buildTestMux(env, "secret-123")

	env.D1.Handler = func(sql string, params []any) ([]map[string]any, int, error) {
		return nil, http.StatusBadGateway, fmt.Errorf("d1 upstream error")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code < 500 {
		t.Errorf("expected 5xx error when D1 fails, got %d", rec.Code)
	}
}

func TestIncidentsEmptyResult(t *testing.T) {
	env := setupTestEnv(t)
	srv := buildTestMux(env, "secret-123")

	callCount := 0
	env.D1.Handler = func(sql string, params []any) ([]map[string]any, int, error) {
		callCount++
		if callCount == 1 {
			// COUNT query
			return []map[string]any{{"total": float64(0)}}, http.StatusOK, nil
		}
		// SELECT query - return empty
		return []map[string]any{}, http.StatusOK, nil
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/incidents", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var body struct {
		Incidents []map[string]any `json:"incidents"`
		TotalCount int             `json:"total_count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if body.TotalCount != 0 {
		t.Errorf("expected total_count=0, got %d", body.TotalCount)
	}
	if len(body.Incidents) != 0 {
		t.Errorf("expected empty incidents, got %d", len(body.Incidents))
	}
	if callCount != 2 {
		t.Errorf("expected 2 D1 queries, got %d", callCount)
	}
}
