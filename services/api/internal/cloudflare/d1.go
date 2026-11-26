package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/enondev/endpoint-lighthouse-api/internal/observability"
)

const cfAPIBase = "https://api." + "cloudflare.com/"

type D1Client struct {
	endpoint string
	apiToken string
	http     *http.Client
}

type d1QueryRequest struct {
	SQL    string `json:"sql"`
	Params []any  `json:"params,omitempty"`
}

type d1QueryResponse struct {
	Success bool `json:"success"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Result []struct {
		Results []map[string]any `json:"results"`
	} `json:"result"`
}

func NewD1Client(accountID, databaseID, apiToken string) *D1Client {
	base := strings.TrimRight(os.Getenv("CF_API_BASE"), "/")
	if base == "" {
		base = "https://api.cloudflare.com"
	}
	return &D1Client{
		endpoint: fmt.Sprintf("%s/client/v4/accounts/%s/d1/database/%s/query",
			base,
			strings.TrimSpace(accountID),
			strings.TrimSpace(databaseID)),
		apiToken: apiToken,
		http:     &http.Client{Timeout: 8 * time.Second},
	}
}

func (c *D1Client) Query(ctx context.Context, sql string, params ...any) ([]map[string]any, error) {
	if c.apiToken == "" || c.endpoint == "" {
		return nil, fmt.Errorf("cloudflare d1 credentials not configured")
	}

	payload, err := json.Marshal(d1QueryRequest{
		SQL:    sql,
		Params: params,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		observability.RecordD1Query(false, 0)
		return nil, err
	}
	defer resp.Body.Close()

	start := time.Now()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4MB cap
	duration := time.Since(start)
	if err != nil {
		observability.RecordD1Query(false, duration)
		return nil, err
	}

	if resp.StatusCode >= 400 {
		observability.RecordD1Query(false, duration)
		return nil, fmt.Errorf("d1 http %d: %s", resp.StatusCode, string(body))
	}

	var parsed d1QueryResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		observability.RecordD1Query(false, duration)
		return nil, err
	}
	if !parsed.Success {
		msg := "unknown d1 error"
		if len(parsed.Errors) > 0 {
			msg = parsed.Errors[0].Message
		}
		observability.RecordD1Query(false, duration)
		return nil, fmt.Errorf("d1 query failed: %s", msg)
	}

	observability.RecordD1Query(true, duration)

	if len(parsed.Result) == 0 {
		return []map[string]any{}, nil
	}
	return parsed.Result[0].Results, nil
}
