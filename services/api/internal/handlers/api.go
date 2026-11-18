package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/enondev/endpoint-lighthouse-api/internal/cloudflare"
	"github.com/enondev/endpoint-lighthouse-api/internal/sse"
)

type APIHandler struct {
	d1  *cloudflare.D1Client
	hub *sse.Hub
}

func NewAPIHandler(d1 *cloudflare.D1Client, hub *sse.Hub) *APIHandler {
	return &APIHandler{d1: d1, hub: hub}
}

func (h *APIHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	sql := `
		SELECT
			(SELECT COUNT(*) FROM endpoints WHERE is_active = 1) AS active_endpoints,
			(SELECT COUNT(*) FROM drift_incidents WHERE status = 'OPEN') AS open_incidents,
			(SELECT COUNT(*) FROM drift_incidents WHERE status = 'ACKNOWLEDGED') AS acknowledged_incidents,
			(SELECT MAX(probed_at) FROM probe_runs) AS last_probed_at
	`
	rows, err := h.d1.Query(r.Context(), sql)
	if err != nil {
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

	writeJSON(w, http.StatusOK, summary)
}

func (h *APIHandler) HandleEndpoints(w http.ResponseWriter, r *http.Request) {
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
	rows, err := h.d1.Query(r.Context(), sql)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"endpoints": rows})
}

func (h *APIHandler) HandleIncidents(w http.ResponseWriter, r *http.Request) {
	limit := 25
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	statusFilter := r.URL.Query().Get("status") // Optional: 'OPEN', 'ACKNOWLEDGED', 'RESOLVED'

	var (
		rows []map[string]any
		err  error
	)

	if statusFilter == "OPEN" || statusFilter == "ACKNOWLEDGED" || statusFilter == "RESOLVED" {
		// Uses idx_incidents_status (status, detected_at DESC)
		sql := `
			SELECT
				di.id,
				di.endpoint_id,
				e.name AS endpoint_name,
				e.category AS endpoint_category,
				di.old_schema_hash,
				di.new_schema_hash,
				di.severity,
				di.diff_summary,
				di.ai_analysis,
				di.ai_patch_snippet,
				di.status,
				di.detected_at
			FROM drift_incidents di
			LEFT JOIN endpoints e ON e.id = di.endpoint_id
			WHERE di.status = ?
			ORDER BY di.detected_at DESC
			LIMIT ?
		`
		rows, err = h.d1.Query(r.Context(), sql, statusFilter, limit)
	} else {
		sql := `
			SELECT
				di.id,
				di.endpoint_id,
				e.name AS endpoint_name,
				e.category AS endpoint_category,
				di.old_schema_hash,
				di.new_schema_hash,
				di.severity,
				di.diff_summary,
				di.ai_analysis,
				di.ai_patch_snippet,
				di.status,
				di.detected_at
			FROM drift_incidents di
			LEFT JOIN endpoints e ON e.id = di.endpoint_id
			ORDER BY di.detected_at DESC
			LIMIT ?
		`
		rows, err = h.d1.Query(r.Context(), sql, limit)
	}

	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"incidents": rows})
}

// HandleAnalysisComplete receives a webhook from SnapDeploy (ai-worker-py)
// after it updates severity, diff_summary, ai_analysis, and ai_patch_snippet in drift_incidents.
func (h *APIHandler) HandleAnalysisComplete(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	raw, _ := json.Marshal(payload)
	h.hub.Broadcast("analysis_ready", string(raw))

	writeJSON(w, http.StatusOK, map[string]string{"status": "broadcasted"})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}