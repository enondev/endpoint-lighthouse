package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/enondev/endpoint-lighthouse-api/internal/cloudflare"
	"github.com/enondev/endpoint-lighthouse-api/internal/observability"
	"github.com/enondev/endpoint-lighthouse-api/internal/sse"
)

type APIHandler struct {
	d1     *cloudflare.D1Client
	hub    *sse.Hub
	logger *slog.Logger

	InternalWebhookSecret string
	httpClient            *http.Client
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

	if statusFilter == "OPEN" || statusFilter == "ACKNOWLEDGED" || statusFilter == "RESOLVED" || statusFilter == "INCONCLUSIVE" {
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

	// Add is_analyzed flag: an incident is analyzed if ai_analysis is populated,
	// regardless of status (handles legacy incidents analyzed before status tracking)
	analyzedRows := make([]map[string]any, len(rows))
	for i, row := range rows {
		rowCopy := make(map[string]any, len(row))
		for k, v := range row {
			rowCopy[k] = v
		}
		if ai, ok := row["ai_analysis"].(string); ok && ai != "" {
			rowCopy["is_analyzed"] = true
		} else {
			rowCopy["is_analyzed"] = false
		}
		analyzedRows[i] = rowCopy
	}

	logger.Info("incidents endpoint served", slog.Int("returned", len(rows)), slog.Int("total_count", totalCount), slog.Int("page", page), slog.Duration("duration", duration))
	writeJSON(w, http.StatusOK, map[string]any{"incidents": analyzedRows, "total_count": totalCount, "page": page, "limit": limit})
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
// Updates the incident status to reflect that analysis is complete and broadcasts SSE.
func (h *APIHandler) HandleAnalysisComplete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With(append(observability.WithTrace(ctx), slog.String("handler", "analysis-completed"))...)

	var payload map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&payload); err != nil {
		logger.Warn("analysis-completed invalid json", slog.String("error", err.Error()))
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	incidentID, ok := payload["incident_id"].(float64)
	if !ok || incidentID == 0 {
		logger.Warn("analysis-completed missing incident_id")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing incident_id"})
		return
	}

	// Update incident status to reflect analysis completion.
	// The ai-worker already updated severity/diff_summary/ai_analysis/ai_patch_snippet
	// via update_d1_incident; here we transition status off OPEN so the dashboard
	// can distinguish analyzed incidents from pending ones.
	status := "ANALYZED"
	_, err := h.d1.Query(ctx, `
		UPDATE drift_incidents
		SET status = ?
		WHERE id = ?
	`, status, int(incidentID))
	if err != nil {
		logger.Error("analysis-completed failed to update incident status",
			slog.Int("incident_id", int(incidentID)), slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update incident"})
		return
	}

	raw, _ := json.Marshal(payload)
	h.hub.Broadcast("analysis_ready", string(raw))
	observability.RecordSSEBroadcast("analysis_ready")

	logger.Info("analysis-completed broadcasted and status updated",
		slog.Int("incident_id", int(incidentID)),
		slog.String("endpoint_id", toString(payload["endpoint_id"])),
		slog.String("severity", toString(payload["severity"])),
		slog.String("status", status),
	)

	writeJSON(w, http.StatusOK, map[string]string{"status": "broadcasted", "incident_status": status})
}

// HandleIncidentByID returns a single drift incident by its ID for dashboard detail views.
func (h *APIHandler) HandleIncidentByID(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With(append(observability.WithTrace(ctx), slog.String("handler", "incident_by_id"))...)

	start := time.Now()
	idStr := r.PathValue("id")
	if idStr == "" {
		logger.Warn("incident by id missing id parameter")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
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
		WHERE di.id = ?
		LIMIT 1
	`
	rows, err := h.d1.Query(ctx, sql, idStr)
	duration := time.Since(start)
	if err != nil {
		logger.Error("incident by id d1 query failed", slog.String("error", err.Error()), slog.Duration("duration", duration))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	if len(rows) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "incident not found"})
		return
	}

	logger.Info("incident by id served", slog.String("id", idStr), slog.Duration("duration", duration))
	writeJSON(w, http.StatusOK, map[string]any{"incident": rows[0]})
}

// HandleIncidentStatusUpdate transitions a drift incident's status.
// Valid transitions: OPEN->ACKNOWLEDGED|RESOLVED, ACKNOWLEDGED->RESOLVED|OPEN, RESOLVED->OPEN.
func (h *APIHandler) HandleIncidentStatusUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With(append(observability.WithTrace(ctx), slog.String("handler", "incident_status_update"))...)

	start := time.Now()
	idStr := r.PathValue("id")
	if idStr == "" {
		logger.Warn("incident status update missing id parameter")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}

	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		logger.Warn("incident status update invalid json", slog.String("error", err.Error()))
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	newStatus := strings.ToUpper(req.Status)
	validStatuses := map[string]bool{"OPEN": true, "ACKNOWLEDGED": true, "RESOLVED": true}
	if !validStatuses[newStatus] {
		logger.Warn("incident status update invalid status", slog.String("status", newStatus))
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid status"})
		return
	}

	// First, fetch the current incident to validate transition
	sql := `SELECT status FROM drift_incidents WHERE id = ? LIMIT 1`
	rows, err := h.d1.Query(ctx, sql, idStr)
	if err != nil {
		logger.Error("incident status update d1 query failed", slog.String("error", err.Error()), slog.Duration("duration", time.Since(start)))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if len(rows) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "incident not found"})
		return
	}

	currentStatus, _ := rows[0]["status"].(string)

	// Validate transition
	validTransition := false
	switch currentStatus {
	case "OPEN":
		validTransition = newStatus == "ACKNOWLEDGED" || newStatus == "RESOLVED"
	case "ACKNOWLEDGED":
		validTransition = newStatus == "RESOLVED" || newStatus == "OPEN"
	case "RESOLVED":
		validTransition = newStatus == "OPEN"
	}

	if !validTransition {
		logger.Warn("incident status update invalid transition",
			slog.String("current_status", currentStatus),
			slog.String("new_status", newStatus),
		)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid status transition"})
		return
	}

	// Update the status
	_, err = h.d1.Query(ctx, `UPDATE drift_incidents SET status = ? WHERE id = ?`, newStatus, idStr)
	if err != nil {
		logger.Error("incident status update d1 update failed",
			slog.String("error", err.Error()),
			slog.Duration("duration", time.Since(start)),
		)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	logger.Info("incident status updated",
		slog.String("id", idStr),
		slog.String("current_status", currentStatus),
		slog.String("new_status", newStatus),
		slog.Duration("duration", time.Since(start)),
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":       "updated",
		"incident_id":  idStr,
		"old_status":   currentStatus,
		"new_status":   newStatus,
	})
}

// HandleWebhookEndpointsList lists all registered webhook endpoints for the account.

// HandleAccountCreate creates a new account (bootstrap only, no auth required).
func (h *APIHandler) HandleAccountCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With(append(observability.WithTrace(ctx), slog.String("handler", "account_create"))...)

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		logger.Warn("account create invalid json", slog.String("error", err.Error()))
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.Name == "" {
		logger.Warn("account create missing name")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}

	accountID := newRequestID()
	apiKey := newAPIKey()

	_, err := h.d1.Query(ctx, `
		INSERT INTO accounts (id, name, api_key, plan_tier, rate_limit_tier,
		                      webhook_endpoint_limit, endpoint_limit, incident_limit_monthly,
		                      created_at, updated_at)
		VALUES (?, ?, ?, 'free', 'free', 3, 5, 100, datetime('now'), datetime('now'))
	`, accountID, req.Name, apiKey)
	if err != nil {
		logger.Error("account create d1 insert failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	logger.Info("account created", slog.String("id", accountID), slog.String("name", req.Name))
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":            accountID,
		"name":          req.Name,
		"api_key":       apiKey,
		"plan_tier":     "free",
		"rate_limit_tier": "free",
	})
}

// HandleGetAccount returns the current account (gated by API key).
func (h *APIHandler) HandleGetAccount(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With(append(observability.WithTrace(ctx), slog.String("handler", "account_get"))...)

	accountID, ok := accountIDFromCtx(ctx)
	if !ok {
		logger.Warn("account get missing account ID in context")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	rows, err := h.d1.Query(ctx, `
		SELECT id, name, api_key, plan_tier, rate_limit_tier,
		       webhook_endpoint_limit, endpoint_limit, incident_limit_monthly,
		       created_at, updated_at
		FROM accounts WHERE id = ?
		LIMIT 1
	`, accountID)
	if err != nil {
		logger.Error("account get d1 query failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if len(rows) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "account not found"})
		return
	}

	row := rows[0]
	writeJSON(w, http.StatusOK, map[string]any{
		"id":             toString(row["id"]),
		"name":           toString(row["name"]),
		"api_key":        toString(row["api_key"]),
		"plan_tier":      toString(row["plan_tier"]),
		"rate_limit_tier": toString(row["rate_limit_tier"]),
		"webhook_endpoint_limit": toInt(row["webhook_endpoint_limit"]),
		"endpoint_limit":   toInt(row["endpoint_limit"]),
		"incident_limit_monthly": toInt(row["incident_limit_monthly"]),
		"created_at":     toString(row["created_at"]),
		"updated_at":     toString(row["updated_at"]),
	})
}

// HandleWebhookEndpointsList lists all registered webhook endpoints for the account.
func (h *APIHandler) HandleWebhookEndpointsList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With(append(observability.WithTrace(ctx), slog.String("handler", "webhook_endpoints_list"))...)

	accountID, ok := accountIDFromCtx(ctx)
	if !ok {
		logger.Warn("webhook endpoints list missing account ID in context")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	start := time.Now()
	sql := `
		SELECT id, url, secret, is_active, last_fired, created_at
		FROM webhook_endpoints
		WHERE account_id = ?
		ORDER BY created_at DESC
	`
	rows, err := h.d1.Query(ctx, sql, accountID)
	duration := time.Since(start)
	if err != nil {
		logger.Error("webhook endpoints list d1 query failed", slog.String("error", err.Error()), slog.Duration("duration", duration))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	logger.Info("webhook endpoints listed", slog.Int("count", len(rows)), slog.String("account_id", accountID), slog.Duration("duration", duration))
	writeJSON(w, http.StatusOK, map[string]any{"webhook_endpoints": rows})
}

// HandleWebhookEndpointCreate registers a new webhook endpoint for drift notifications.
func (h *APIHandler) HandleWebhookEndpointCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With(append(observability.WithTrace(ctx), slog.String("handler", "webhook_endpoint_create"))...)

	accountID, ok := accountIDFromCtx(ctx)
	if !ok {
		logger.Warn("webhook endpoint create missing account ID in context")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req struct {
		URL     string `json:"url"`
		Secret  string `json:"secret,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		logger.Warn("webhook endpoint create invalid json", slog.String("error", err.Error()))
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	if req.URL == "" {
		logger.Warn("webhook endpoint create missing url")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url is required"})
		return
	}

	_, err := h.d1.Query(ctx, `
		INSERT INTO webhook_endpoints (account_id, url, secret, is_active, created_at)
		VALUES (?, ?, ?, 1, datetime('now'))
	`, accountID, req.URL, req.Secret)
	if err != nil {
		logger.Error("webhook endpoint create d1 insert failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	logger.Info("webhook endpoint created", slog.String("url", req.URL), slog.String("account_id", accountID))
	writeJSON(w, http.StatusCreated, map[string]string{"status": "created"})
}

// HandleWebhookEndpointDelete removes a webhook endpoint by ID.
func (h *APIHandler) HandleWebhookEndpointDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := h.logger.With(append(observability.WithTrace(ctx), slog.String("handler", "webhook_endpoint_delete"))...)

	idStr := r.PathValue("id")
	if idStr == "" {
		logger.Warn("webhook endpoint delete missing id parameter")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}

	accountID, ok := accountIDFromCtx(ctx)
	if !ok {
		logger.Warn("webhook endpoint delete missing account ID in context")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	_, err := h.d1.Query(ctx, `DELETE FROM webhook_endpoints WHERE id = ? AND account_id = ?`, idStr, accountID)
	if err != nil {
		logger.Error("webhook endpoint delete d1 delete failed", slog.String("error", err.Error()))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	logger.Info("webhook endpoint deleted", slog.String("id", idStr), slog.String("account_id", accountID))
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// dispatchWebhook POSTs an incident payload to all registered webhook endpoints.
// Errors are logged but do not block the caller; individual webhook failures
// do not prevent other webhooks from being dispatched.
func (h *APIHandler) dispatchWebhook(incident map[string]any) {
	ctx := context.Background()
	logger := h.logger.With(slog.String("handler", "dispatch_webhook"))

	accountID, ok := incident["account_id"].(string)
	if !ok || accountID == "" {
		logger.Warn("webhook dispatch missing account_id in incident")
		return
	}

	rows, err := h.d1.Query(ctx, `SELECT id, url, secret FROM webhook_endpoints WHERE is_active = 1 AND account_id = ?`, accountID)
	if err != nil {
		logger.Error("webhook dispatch query failed", slog.String("error", err.Error()))
		return
	}
	if len(rows) == 0 {
		logger.Debug("no webhook endpoints registered")
		return
	}

	body, err := json.Marshal(incident)
	if err != nil {
		logger.Error("webhook dispatch marshal failed", slog.String("error", err.Error()))
		return
	}

	for _, row := range rows {
		url, _ := row["url"].(string)
		secret, _ := row["secret"].(string)

		go func(url, secret string) {
			req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
			if err != nil {
				logger.Error("webhook dispatch request build failed", slog.String("url", url), slog.String("error", err.Error()))
				return
			}
			req.Header.Set("Content-Type", "application/json")
			if secret != "" {
				req.Header.Set("X-Webhook-Secret", secret)
			}
			if h.InternalWebhookSecret != "" {
				req.Header.Set("X-Internal-Secret", h.InternalWebhookSecret)
			}

			resp, err := h.httpClient.Do(req)
			if err != nil {
				logger.Error("webhook dispatch failed", slog.String("url", url), slog.String("error", err.Error()))
				return
			}
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				logger.Debug("webhook dispatched", slog.String("url", url), slog.Int("status", resp.StatusCode))
			} else {
				logger.Warn("webhook dispatch failed", slog.String("url", url), slog.Int("status", resp.StatusCode))
			}
		}(url, secret)
	}
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

func accountIDFromCtx(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(accountIDKey{}).(string)
	return id, ok
}

func newAPIKey() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		// Fallback to time-based ID if crypto fails
		return fmt.Sprintf("ep_live_%x", time.Now().UnixNano())
	}
	return "ep_live_" + fmt.Sprintf("%x", b)
}

// accountIDKey is the context key for account ID.
type accountIDKey struct{}

// newRequestID generates a unique request ID.
func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}

