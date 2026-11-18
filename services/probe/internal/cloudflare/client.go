package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const cfAPIBase = "https://api." + "cloudflare.com/"

type Endpoint struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Category        string `json:"category"`
	URL             string `json:"url"`
	Method          string `json:"method"`
	PollIntervalSec int    `json:"poll_interval_sec"`
	LastETag        string `json:"last_etag"`
	LastModified    string `json:"last_modified"`
	BaselineHash    string `json:"baseline_hash"`
	InferredSchema  string `json:"inferred_schema"`
	KVSnapshotKey   string `json:"kv_snapshot_key"`
}

type ProbeRunRecord struct {
	EndpointID        string
	StatusCode        int
	LatencyMs         float64
	ResponseSizeBytes int
	ContentType       string
	SchemaHash        string
	DriftDetected     int // 0 or 1
	ErrorMessage      string
}

type DriftIncidentRecord struct {
	EndpointID    string
	OldSchemaHash string
	NewSchemaHash string
	Severity      string // 'PENDING', 'NON_BREAKING', 'WARNING', 'BREAKING'
	DiffSummary   string
}

type Client struct {
	accountID  string
	d1DBID     string
	kvNSID     string
	apiToken   string
	httpClient *http.Client
}

func NewClient(accountID, d1DBID, kvNSID, apiToken string) *Client {
	return &Client{
		accountID: strings.TrimSpace(accountID),
		d1DBID:    strings.TrimSpace(d1DBID),
		kvNSID:    strings.TrimSpace(kvNSID),
		apiToken:  strings.TrimSpace(apiToken),
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				DisableKeepAlives: true,
				WriteBufferSize:   4096,
				ReadBufferSize:    4096,
			},
		},
	}
}

func (c *Client) Enabled() bool {
	return c.accountID != "" && c.d1DBID != "" && c.apiToken != ""
}

func (c *Client) kvURL(key string) string {
	return fmt.Sprintf("%s/client/v4/accounts/%s/storage/kv/namespaces/%s/values/%s",
    cfAPIBase,
	  strings.TrimSpace(c.accountID),
	  strings.TrimSpace(c.kvNSID),
	  key)
}

func (c *Client) d1URL() string {
	return fmt.Sprintf("%s/client/v4/accounts/%s/d1/database/%s/query",
	  cfAPIBase,
	  strings.TrimSpace(c.accountID),
	  strings.TrimSpace(c.d1DBID))
}

// FetchActiveEndpoints queries D1 using the exact columns in 001_init.sql.
func (c *Client) FetchActiveEndpoints(ctx context.Context) ([]Endpoint, error) {
	query := `
		SELECT
			e.id,
			e.name,
			e.category,
			e.url,
			COALESCE(e.method, 'GET') AS method,
			COALESCE(e.poll_interval_sec, 600) AS poll_interval_sec,
			COALESCE(e.last_etag, '') AS last_etag,
			COALESCE(e.last_modified, '') AS last_modified,
			COALESCE(sb.schema_hash, '') AS baseline_hash,
			COALESCE(sb.inferred_schema, '') AS inferred_schema,
			COALESCE(sb.kv_snapshot_key, '') AS kv_snapshot_key
		FROM endpoints e
		LEFT JOIN schema_baselines sb
			ON e.id = sb.endpoint_id AND sb.is_current = 1
		WHERE e.is_active = 1;
	`
	rows, err := c.execD1(ctx, query, nil)
	if err != nil {
		return nil, err
	}

	var endpoints []Endpoint
	for _, r := range rows {
		ep := Endpoint{
			ID:             asString(r["id"]),
			Name:           asString(r["name"]),
			Category:       asString(r["category"]),
			URL:            asString(r["url"]),
			Method:         asString(r["method"]),
			LastETag:       asString(r["last_etag"]),
			LastModified:   asString(r["last_modified"]),
			BaselineHash:   asString(r["baseline_hash"]),
			InferredSchema: asString(r["inferred_schema"]),
			KVSnapshotKey:  asString(r["kv_snapshot_key"]),
		}
		if iv, ok := r["poll_interval_sec"].(float64); ok && iv > 0 {
			ep.PollIntervalSec = int(iv)
		} else {
			ep.PollIntervalSec = 600
		}
		endpoints = append(endpoints, ep)
	}
	return endpoints, nil
}

// GetSnapshotKV fetches a raw payload snapshot from KV using the exact key.
func (c *Client) GetSnapshotKV(ctx context.Context, kvKey string) ([]byte, error) {
	if kvKey == "" || c.kvNSID == "" {
		return nil, fmt.Errorf("missing kv key or namespace")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.kvURL(kvKey), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kv get status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
}

// PutSnapshotKV stores a raw JSON payload snapshot in KV.
func (c *Client) PutSnapshotKV(ctx context.Context, kvKey string, rawPayload []byte) error {
	if kvKey == "" || c.kvNSID == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.kvURL(kvKey), bytes.NewReader(rawPayload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("kv put failed with status %d", resp.StatusCode)
	}
	return nil
}

// RecordProbeRun inserts a row into probe_runs matching 001_init.sql.
func (c *Client) RecordProbeRun(ctx context.Context, rec ProbeRunRecord) error {
	query := `
		INSERT INTO probe_runs (
			endpoint_id,
			status_code,
			latency_ms,
			response_size_bytes,
			content_type,
			schema_hash,
			drift_detected,
			error_message,
			probed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, datetime('now'));
	`
	params := []any{
		rec.EndpointID,
		rec.StatusCode,
		rec.LatencyMs,
		rec.ResponseSizeBytes,
		nullableString(rec.ContentType),
		nullableString(rec.SchemaHash),
		rec.DriftDetected,
		nullableString(rec.ErrorMessage),
	}
	_, err := c.execD1(ctx, query, params)
	return err
}

// UpdateEndpointCacheHeaders persists ETag / Last-Modified back to endpoints.
func (c *Client) UpdateEndpointCacheHeaders(ctx context.Context, endpointID, etag, lastModified string) error {
	query := `
		UPDATE endpoints
		SET last_etag = COALESCE(?, last_etag),
		    last_modified = COALESCE(?, last_modified)
		WHERE id = ?;
	`
	_, err := c.execD1(ctx, query, []any{
		nullableString(etag),
		nullableString(lastModified),
		endpointID,
	})
	return err
}

// InsertDriftIncident creates an OPEN drift incident row in drift_incidents.
func (c *Client) InsertDriftIncident(ctx context.Context, inc DriftIncidentRecord) error {
	query := `
		INSERT INTO drift_incidents (
			endpoint_id,
			old_schema_hash,
			new_schema_hash,
			severity,
			diff_summary,
			status,
			detected_at
		) VALUES (?, ?, ?, ?, ?, 'OPEN', datetime('now'));
	`
	_, err := c.execD1(ctx, query, []any{
		inc.EndpointID,
		inc.OldSchemaHash,
		inc.NewSchemaHash,
		inc.Severity,
		inc.DiffSummary,
	})
	return err
}

// InsertInitialBaseline inserts a baseline row when bootstrapping an unseeded endpoint.
func (c *Client) InsertInitialBaseline(ctx context.Context, endpointID, schemaHash, inferredSchema, kvKey string, fieldCount int) error {
	query := `
		INSERT OR IGNORE INTO schema_baselines (
			endpoint_id,
			schema_hash,
			inferred_schema,
			kv_snapshot_key,
			field_count,
			is_current,
			created_at
		) VALUES (?, ?, ?, ?, ?, 1, datetime('now'));
	`
	_, err := c.execD1(ctx, query, []any{
		endpointID,
		schemaHash,
		inferredSchema,
		kvKey,
		fieldCount,
	})
	return err
}

func (c *Client) execD1(ctx context.Context, sql string, params []any) ([]map[string]any, error) {
	bodyPayload := map[string]any{"sql": sql}
	if len(params) > 0 {
		bodyPayload["params"] = params
	}
	rawBody, _ := json.Marshal(bodyPayload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.d1URL(), bytes.NewReader(rawBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var parsed struct {
		Success bool `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Result []struct {
			Results []map[string]any `json:"results"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	if !parsed.Success {
		msg := "unknown D1 error"
		if len(parsed.Errors) > 0 {
			msg = parsed.Errors[0].Message
		}
		return nil, fmt.Errorf("d1 query error: %s", msg)
	}
	if len(parsed.Result) == 0 {
		return nil, nil
	}
	return parsed.Result[0].Results, nil
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}