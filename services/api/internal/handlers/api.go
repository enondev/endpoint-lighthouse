package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/enondev/endpoint-lighthouse-api/internal/cloudflare"
	"github.com/enondev/endpoint-lighthouse-api/internal/observability"
	"github.com/enondev/endpoint-lighthouse-api/internal/sse"
)

type APIHandler struct {
	d1     *cloudflare.D1Client
	hub    *sse.Hub
	logger *slog.Logger
}

type PendingIncident struct {
	IncidentID    int64  `json:"incident_id"`
	EndpointID    string `json:"endpoint_id"`
	EndpointName  string `json:"endpoint_name"`
	EndpointURL   string `json:"endpoint_url"`
	OldSchemaHash string `json:"old_schema_hash"`
	NewSchemaHash string `json:"new_schema_hash"`
	BaselineKVKey string `json:"baseline_kv_key,omitempty"`
	DriftedKVKey  string `json:"drifted_kv_key,omitempty"`
}

func NewAPIHandler(d1 *cloudflare.D1Client, hub *sse.Hub, logger *slog.Logger) *APIHandler {
	return &APIHandler{d1: d1, hub: hub, logger: logger}
}

func (h *APIHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With(append(observability.WithTrace(ctx), slog.String("handler", "status"))...)

	start := time.Now()
	sql := `
		SELECT
			(SELECT COUNT(*) FROM endpoints WHERE is_active = 1) AS active_endpoints,
			(SELECT COUNT(*) FROM drift_incidents WHERE status = 'OPEN') AS open_incidents,
			(SELECT COUNT(*) FROM drift_incidents WHERE status = 'ACKNOWLEDGED') AS acknowledged_incidents,
			(SELECT MAX(probed_at) FROM probe_runs) AS last_probed_at
	`
	rows, err := h.d1.Query(ctx, sql)
	duration := time.Since(start)
	if err != nil {
		logger.Error("status endpoint d1 query failed", slog.String("error", err.Error()), slog.Duration("duration", duration))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	summary := map[string]any{
		"status":                 "operational",
		"active_endpoints":       0,
		"open_incidents":         0,
		"acknowledged_incidents": 0,
		"last_probed_at":         nil,
		"timestamp":              time.Now().UTC().Format(time.RFC3339),
	}
	if len(rows) > 0 {
		for k, v := range rows[0] {
			summary[k] = v
		}
		if open, ok := rows[0]["open_incidents"].(float64); ok && open > 0 {
			summary["status"] = "drift_detected"
		}
	}

	logger.Info("status endpoint served", slog.Int("open_incidents", toInt(summary["open_incidents"])), slog.Duration("duration", duration))
	writeJSON(w, http.StatusOK, summary)
}

func (h *APIHandler) HandleEndpoints(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With(append(observability.WithTrace(ctx), slog.String("handler", "endpoints"))...)

	start := time.Now()
	sql := `
		SELECT
			e.id,
			e.name,
			e.category,
			e.url,
			e.method,
			e.poll_interval_sec,
			e.is_active,
			e.last_etag,
			e.last_modified,
			e.created_at,
			sb.schema_hash AS current_schema_hash,
			sb.kv_snapshot_key,
			sb.field_count,
			(SELECT status_code FROM probe_runs pr WHERE pr.endpoint_id = e.id ORDER BY pr.probed_at DESC LIMIT 1) AS last_status_code,
			(SELECT latency_ms FROM probe_runs pr WHERE pr.endpoint_id = e.id ORDER BY pr.probed_at DESC LIMIT 1) AS last_latency_ms,
			(SELECT drift_detected FROM probe_runs pr WHERE pr.endpoint_id = e.id ORDER BY pr.probed_at DESC LIMIT 1) AS last_drift_detected,
			(SELECT error_message FROM probe_runs pr WHERE pr.endpoint_id = e.id ORDER BY pr.probed_at DESC LIMIT 1) AS last_error_message,
			(SELECT probed_at FROM probe_runs pr WHERE pr.endpoint_id = e.id ORDER BY pr.probed_at DESC LIMIT 1) AS last_probed_at
		FROM endpoints e
		LEFT JOIN schema_baselines sb ON sb.endpoint_id = e.id AND sb.is_current = 1
		ORDER BY e.category ASC, e.name ASC
	`
	rows, err := h.d1.Query(ctx, sql)
	duration := time.Since(start)
	if err != nil {
		logger.Error("endpoints endpoint d1 query failed", slog.String("error", err.Error()), slog.Duration("duration", duration))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	logger.Info("endpoints endpoint served", slog.Int("count", len(rows)), slog.Duration("duration", duration))
	writeJSON(w, http.StatusOK, map[string]any{"endpoints": rows})
}

func (h *APIHandler) HandleIncidents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With(append(observability.WithTrace(ctx), slog.String("handler", "incidents"))...)

	start := time.Now()
	limit := 25
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	page := 1
	if rawPage := r.URL.Query().Get("page"); rawPage != "" {
		if parsed, err := strconv.Atoi(rawPage); err == nil && parsed > 0 {
			page = parsed
		}
	}
	offset := (page - 1) * limit

	statusFilter := r.URL.Query().Get("status")
	fromFilter := r.URL.Query().Get("from")
	toFilter := r.URL.Query().Get("to")

	whereSql := "WHERE 1=1"
	args := []any{}

	if statusFilter == "OPEN" || statusFilter == "ACKNOWLEDGED" || statusFilter == "RESOLVED" {
		whereSql += " AND di.status = ?"
		args = append(args, statusFilter)
	}

	if fromFilter != "" {
		whereSql += " AND di.detected_at >= ?"
		args = append(args, fromFilter)
	}
	if toFilter != "" {
		whereSql += " AND di.detected_at <= ?"
		args = append(args, toFilter)
	}

	countSql := `SELECT COUNT(*) as total FROM drift_incidents di LEFT JOIN endpoints e ON e.id = di.endpoint_id ` + whereSql
	countRows, err := h.d1.Query(ctx, countSql, args...)
	if err != nil {
		logger.Error("incidents count d1 query failed", slog.String("error", err.Error()), slog.Duration("duration", time.Since(start)))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	totalCount := 0
	if len(countRows) > 0 {
		if c, ok := countRows[0]["total"].(float64); ok {
			totalCount = int(c)
		}
	}

	sql := `
		SELECT
			di.id,
			di.endpoint_id,
			e.name AS endpoint_name,
			e.category AS endpoint_category,
			di.old_schema_hash,
			di.new_schema_hash,
			di.severity,
			di.severity_score,
			di.diff_summary,
			di.ai_analysis,
			di.ai_patch_snippet,
			di.status,
			di.detected_at
		FROM drift_incidents di
		LEFT JOIN endpoints e ON e.id = di.endpoint_id
		` + whereSql + `
		ORDER BY di.detected_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := h.d1.Query(ctx, sql, args...)
	duration := time.Since(start)
	if err != nil {
		logger.Error("incidents select d1 query failed", slog.String("error", err.Error()), slog.Duration("duration", duration))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	logger.Info("incidents endpoint served", slog.Int("returned", len(rows)), slog.Int("total_count", totalCount), slog.Int("page", page), slog.Duration("duration", duration))
	writeJSON(w, http.StatusOK, map[string]any{"incidents": rows, "total_count": totalCount, "page": page, "limit": limit})
}

// FetchPendingIncidents queries D1 for incidents awaiting AI analysis or stuck with fallback analysis.
func (h *APIHandler) FetchPendingIncidents(ctx context.Context, limit int) ([]PendingIncident, error) {
	logger := h.logger.With(append(observability.WithTrace(ctx), slog.String("handler", "fetch_pending_incidents"))...)

	if limit <= 0 || limit > 25 {
		limit = 5
	}
	sql := `
		SELECT
			di.id AS incident_id,
			di.endpoint_id AS endpoint_id,
			COALESCE(e.name, di.endpoint_id) AS endpoint_name,
			COALESCE(e.url, '') AS endpoint_url,
			di.old_schema_hash AS old_schema_hash,
			di.new_schema_hash AS new_schema_hash,
			sb_old.kv_snapshot_key AS baseline_kv_key,
			sb_new.kv_snapshot_key AS drifted_kv_key
		FROM drift_incidents di
		LEFT JOIN endpoints e ON e.id = di.endpoint_id
		LEFT JOIN schema_baselines sb_old
			ON sb_old.endpoint_id = di.endpoint_id AND sb_old.schema_hash = di.old_schema_hash
		LEFT JOIN schema_baselines sb_new
			ON sb_new.endpoint_id = di.endpoint_id AND sb_new.schema_hash = di.new_schema_hash
		WHERE (
			(di.status = 'OPEN' AND (di.ai_analysis IS NULL OR TRIM(di.ai_analysis) = ''))
			OR di.ai_analysis LIKE '%Automated drift analysis completed.%'
		)
		  AND sb_old.kv_snapshot_key IS NOT NULL
		  AND sb_new.kv_snapshot_key IS NOT NULL
		ORDER BY di.detected_at DESC
		LIMIT ?;
	`
	start := time.Now()
	rows, err := h.d1.Query(ctx, sql, limit)
	duration := time.Since(start)
	if err != nil {
		logger.Error("fetch pending incidents d1 query failed", slog.String("error", err.Error()), slog.Duration("duration", duration))
		return nil, fmt.Errorf("query pending incidents: %w", err)
	}

	logger.Info("fetched pending incidents", slog.Int("count", len(rows)), slog.Duration("duration", duration))

	var out []PendingIncident
	raw, err := json.Marshal(rows)
	if err != nil {
		return nil, fmt.Errorf("marshal pending rows: %w", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("unmarshal pending incidents: %w", err)
	}
	return out, nil
}

// FetchAllOpenIncidents retrieves every OPEN drift incident regardless of
// ai_analysis state, used for one-time migration to the new analysis prompt.
// Both KV snapshot keys must be present so the worker can diff baseline vs drifted.
func (h *APIHandler) FetchAllOpenIncidents(ctx context.Context, limit int) ([]PendingIncident, error) {
	logger := h.logger.With(append(observability.WithTrace(ctx), slog.String("handler", "fetch_all_open_incidents"))...)

	if limit <= 0 || limit > 100 {
		limit = 50
	}
	sql := `
		SELECT
			di.id AS incident_id,
			di.endpoint_id AS endpoint_id,
			COALESCE(e.name, di.endpoint_id) AS endpoint_name,
			COALESCE(e.url, '') AS endpoint_url,
			di.old_schema_hash AS old_schema_hash,
			di.new_schema_hash AS new_schema_hash,
			sb_old.kv_snapshot_key AS baseline_kv_key,
			sb_new.kv_snapshot_key AS drifted_kv_key
		FROM drift_incidents di
		LEFT JOIN endpoints e ON e.id = di.endpoint_id
		LEFT JOIN schema_baselines sb_old
			ON sb_old.endpoint_id = di.endpoint_id AND sb_old.schema_hash = di.old_schema_hash
		LEFT JOIN schema_baselines sb_new
			ON sb_new.endpoint_id = di.endpoint_id AND sb_new.schema_hash = di.new_schema_hash
		WHERE di.status = 'OPEN'
		  AND sb_old.kv_snapshot_key IS NOT NULL
		  AND sb_new.kv_snapshot_key IS NOT NULL
		ORDER BY di.detected_at DESC
		LIMIT ?;
	`
	start := time.Now()
	rows, err := h.d1.Query(ctx, sql, limit)
	duration := time.Since(start)
	if err != nil {
		logger.Error("fetch all open incidents d1 query failed", slog.String("error", err.Error()), slog.Duration("duration", duration))
		return nil, fmt.Errorf("query all open incidents: %w", err)
	}

	logger.Info("fetched all open incidents", slog.Int("count", len(rows)), slog.Duration("duration", duration))

	var out []PendingIncident
	raw, err := json.Marshal(rows)
	if err != nil {
		return nil, fmt.Errorf("marshal all open rows: %w", err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("unmarshal all open incidents: %w", err)
	}
	return out, nil
}

// HandleAnalysisComplete receives a webhook from SnapDeploy (ai-worker-py)
// after it updates severity, diff_summary, ai_analysis, and ai_patch_snippet in drift_incidents.
func (h *APIHandler) HandleAnalysisComplete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With(append(observability.WithTrace(ctx), slog.String("handler", "analysis-completed"))...)

	var payload map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&payload); err != nil {
		logger.Warn("analysis-completed invalid json", slog.String("error", err.Error()))
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	raw, _ := json.Marshal(payload)
	h.hub.Broadcast("analysis_ready", string(raw))
	observability.RecordSSEBroadcast("analysis_ready")

	logger.Info("analysis-completed broadcasted",
		slog.Any("incident_id", payload["incident_id"]),
		slog.String("endpoint_id", toString(payload["endpoint_id"])),
		slog.String("severity", toString(payload["severity"])),
	)

	writeJSON(w, http.StatusOK, map[string]string{"status": "broadcasted"})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
