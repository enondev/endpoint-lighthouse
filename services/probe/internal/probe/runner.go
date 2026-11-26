package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	mathrand "math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/enondev/endpoint-lighthouse-probe/internal/cloudflare"
	"github.com/enondev/endpoint-lighthouse-probe/internal/config"
	"github.com/enondev/endpoint-lighthouse-probe/internal/schema"
)

type Metrics struct {
	TotalProbes     atomic.Uint64
	NotModifiedHits atomic.Uint64
	DriftDetected   atomic.Uint64
	ContractErrors  atomic.Uint64
	LastDiffMicros  atomic.Int64
	ActiveEndpoints atomic.Int64
}

type Runner struct {
	cfg        config.Config
	cf         *cloudflare.Client
	httpClient *http.Client
	Metrics    *Metrics

	mu            sync.RWMutex
	etags         map[string]string
	lastModified  map[string]string
	baselineCache map[string]*schema.Node
	baselineHash  map[string]string
	endpoints     map[string]cloudflare.Endpoint // indexed by endpoint ID for on-demand re-probe
}

var bufPool = sync.Pool{
	New: func() any {
		return bytes.NewBuffer(make([]byte, 0, 32*1024))
	},
}

func readBodyPooled(r io.Reader, maxBytes int64) ([]byte, error) {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer func() {
		if buf.Cap() <= 256*1024 {
			buf.Reset()
			bufPool.Put(buf)
		}
	}()

	if _, err := io.Copy(buf, io.LimitReader(r, maxBytes)); err != nil {
		return nil, err
	}
	out := make([]byte, buf.Len())
	copy(out, buf.Bytes())
	return out, nil
}

func NewRunner(cfg config.Config, cf *cloudflare.Client) *Runner {
	transport := &http.Transport{
		DisableKeepAlives:     true,
		MaxIdleConns:          2,
		IdleConnTimeout:       10 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		WriteBufferSize:       4096,
		ReadBufferSize:        4096,
	}

	return &Runner{
		cfg:     cfg,
		cf:      cf,
		Metrics: &Metrics{},
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   cfg.RequestTimeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		etags:         make(map[string]string),
		lastModified:  make(map[string]string),
		baselineCache: make(map[string]*schema.Node),
		baselineHash:  make(map[string]string),
	}
}

func (r *Runner) Start(ctx context.Context) {
	endpoints, err := r.loadEndpoints(ctx)
	if err != nil {
		slog.Error("failed loading endpoints, using fallback smoke endpoint", "err", err)
		endpoints = []cloudflare.Endpoint{
			{
				ID:              "httpbin-get",
				Name:            "HTTPBin GET Smoke",
				Category:        "smoke",
				URL:             "https://httpbin.org",
				Method:          "GET",
				PollIntervalSec: 600,
			},
		}
	}

	r.Metrics.ActiveEndpoints.Store(int64(len(endpoints)))
	slog.Info("starting probe workers", "endpoint_count", len(endpoints))

	// Index endpoints by ID for on-demand re-probe
	r.mu.Lock()
	r.endpoints = make(map[string]cloudflare.Endpoint, len(endpoints))
	for _, ep := range endpoints {
		r.endpoints[ep.ID] = ep
	}
	r.mu.Unlock()

	// Hydrate in-memory cache directly from D1 (zero extra KV round-trips on startup)
	for _, ep := range endpoints {
		r.mu.Lock()
		if ep.LastETag != "" {
			r.etags[ep.ID] = ep.LastETag
		}
		if ep.LastModified != "" {
			r.lastModified[ep.ID] = ep.LastModified
		}
		if ep.BaselineHash != "" {
			r.baselineHash[ep.ID] = ep.BaselineHash
		}
		if ep.InferredSchema != "" {
			if ast, err := schema.ParseSchemaJSON(ep.InferredSchema); err == nil {
				r.baselineCache[ep.ID] = ast
			} else {
				slog.Warn("failed parsing stored baseline schema", "endpoint", ep.ID, "err", err)
			}
		}
		r.mu.Unlock()
	}

	var wg sync.WaitGroup
	for _, ep := range endpoints {
		wg.Add(1)
		go func(target cloudflare.Endpoint) {
			defer wg.Done()
			r.workerLoop(ctx, target)
		}(ep)
	}

	if r.cfg.RenderAPIURL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.keepRenderWarm(ctx)
		}()
	}

	wg.Wait()
}

func (r *Runner) loadEndpoints(ctx context.Context) ([]cloudflare.Endpoint, error) {
	if !r.cf.Enabled() {
		return nil, fmt.Errorf("cloudflare credentials not configured")
	}
	return r.cf.FetchActiveEndpoints(ctx)
}

func (r *Runner) workerLoop(ctx context.Context, ep cloudflare.Endpoint) {
	stagger := time.Duration(mathrand.Int64N(int64(r.cfg.MaxStartupStagger) + 1))
	select {
	case <-ctx.Done():
		return
	case <-time.After(stagger):
	}

	for {
		r.probeOnce(ctx, ep)

		base := float64(time.Duration(ep.PollIntervalSec) * time.Second)
		jitterFactor := 0.90 + mathrand.Float64()*0.20
		sleepDur := time.Duration(base * jitterFactor)

		select {
		case <-ctx.Done():
			return
		case <-time.After(sleepDur):
		}
	}
}

func (r *Runner) probeOnce(ctx context.Context, ep cloudflare.Endpoint) {
	r.Metrics.TotalProbes.Add(1)
	start := time.Now()

	req, err := http.NewRequestWithContext(ctx, ep.Method, ep.URL, nil)
	if err != nil {
		slog.Error("invalid request", "endpoint", ep.ID, "err", err)
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", r.cfg.UserAgent)

	r.mu.RLock()
	etag := r.etags[ep.ID]
	lastMod := r.lastModified[ep.ID]
	r.mu.RUnlock()

	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastMod != "" {
		req.Header.Set("If-Modified-Since", lastMod)
	}

	resp, err := r.httpClient.Do(req)
	latencyMs := float64(time.Since(start).Microseconds()) / 1000.0
	if err != nil {
		r.Metrics.ContractErrors.Add(1)
		r.persistRun(ctx, cloudflare.ProbeRunRecord{
			EndpointID:   ep.ID,
			StatusCode:   0,
			LatencyMs:    latencyMs,
			ErrorMessage: err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")

	// Strict redirect policy: catch 3xx contract changes
	if resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.StatusCode != http.StatusNotModified {
		r.Metrics.ContractErrors.Add(1)
		loc := resp.Header.Get("Location")
		r.persistRun(ctx, cloudflare.ProbeRunRecord{
			EndpointID:   ep.ID,
			StatusCode:   resp.StatusCode,
			LatencyMs:    latencyMs,
			ContentType:  contentType,
			ErrorMessage: fmt.Sprintf("unexpected %d redirect to %s", resp.StatusCode, loc),
		})
		return
	}

	// Bandwidth optimization: 304 Not Modified
	if resp.StatusCode == http.StatusNotModified {
		r.Metrics.NotModifiedHits.Add(1)
		r.persistRun(ctx, cloudflare.ProbeRunRecord{
			EndpointID:  ep.ID,
			StatusCode:  resp.StatusCode,
			LatencyMs:   latencyMs,
			ContentType: contentType,
		})
		slog.Info("PROBE_NOT_MODIFIED",
			"endpoint", ep.ID,
			"status", resp.StatusCode,
			"latency_ms", latencyMs,
		)
		return
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r.Metrics.ContractErrors.Add(1)
		r.persistRun(ctx, cloudflare.ProbeRunRecord{
			EndpointID:   ep.ID,
			StatusCode:   resp.StatusCode,
			LatencyMs:    latencyMs,
			ContentType:  contentType,
			ErrorMessage: fmt.Sprintf("unexpected HTTP %d", resp.StatusCode),
		})
		return
	}

	newETag := resp.Header.Get("ETag")
	newLastMod := resp.Header.Get("Last-Modified")
	if newETag != "" || newLastMod != "" {
		r.mu.Lock()
		if newETag != "" {
			r.etags[ep.ID] = newETag
		}
		if newLastMod != "" {
			r.lastModified[ep.ID] = newLastMod
		}
		r.mu.Unlock()
		if r.cf.Enabled() {
			_ = r.cf.UpdateEndpointCacheHeaders(ctx, ep.ID, newETag, newLastMod)
		}
	}

	body, err := readBodyPooled(resp.Body, r.cfg.MaxBodyBytes)
	if err != nil {
		r.Metrics.ContractErrors.Add(1)
		return
	}
	bodySize := len(body)

	diffStart := time.Now()
	canonical, observedHash, observedAST, err := schema.InferFromBytes(body)
	if err != nil {
		r.Metrics.ContractErrors.Add(1)
		r.persistRun(ctx, cloudflare.ProbeRunRecord{
			EndpointID:        ep.ID,
			StatusCode:        resp.StatusCode,
			LatencyMs:         latencyMs,
			ResponseSizeBytes: bodySize,
			ContentType:       contentType,
			ErrorMessage:      "non-JSON response: " + err.Error(),
		})
		return
	}

	r.mu.RLock()
	baseHash := r.baselineHash[ep.ID]
	baseAST := r.baselineCache[ep.ID]
	r.mu.RUnlock()

	// Bootstrap baseline if endpoint had no active baseline row
	if baseHash == "" {
		slog.Info("BASELINE_CREATED",
			"endpoint", ep.ID,
			"status", resp.StatusCode,
			"latency_ms", latencyMs,
			"hash", observedHash[:12],
		)

		kvKey := fmt.Sprintf("snap:%s:%s", ep.ID, observedHash[:16])
		r.mu.Lock()
		r.baselineHash[ep.ID] = observedHash
		r.baselineCache[ep.ID] = observedAST
		r.mu.Unlock()

		if r.cf.Enabled() {
			_ = r.cf.PutSnapshotKV(ctx, kvKey, body)
			_ = r.cf.InsertInitialBaseline(ctx, ep.ID, observedHash, string(canonical), kvKey, countFields(observedAST))
		}
		r.persistRun(ctx, cloudflare.ProbeRunRecord{
			EndpointID:        ep.ID,
			StatusCode:        resp.StatusCode,
			LatencyMs:         latencyMs,
			ResponseSizeBytes: bodySize,
			ContentType:       contentType,
			SchemaHash:        observedHash,
			DriftDetected:     0,
		})
		return
	}

	if observedHash == baseHash {
		diffUs := time.Since(diffStart).Microseconds()
		r.Metrics.LastDiffMicros.Store(diffUs)
		r.persistRun(ctx, cloudflare.ProbeRunRecord{
			EndpointID:        ep.ID,
			StatusCode:        resp.StatusCode,
			LatencyMs:         latencyMs,
			ResponseSizeBytes: bodySize,
			ContentType:       contentType,
			SchemaHash:        observedHash,
			DriftDetected:     0,
		})
		slog.Info("PROBE_OK",
			"endpoint", ep.ID,
			"status", resp.StatusCode,
			"latency_ms", latencyMs,
			"diff_ms", float64(diffUs)/1000.0,
		)
		return
	}

	// Hash mismatch -> compute structural AST diff (<15ms target)
	var changes []schema.DriftChange
	if baseAST != nil {
		changes = schema.DiffAST("$", baseAST, observedAST)
	}
	diffUs := time.Since(diffStart).Microseconds()
	r.Metrics.LastDiffMicros.Store(diffUs)
	r.Metrics.DriftDetected.Add(1)

	severity := classifySeverity(changes)
	diffJSON, _ := json.Marshal(changes)
	newKVKey := fmt.Sprintf("snap:%s:%s", ep.ID, observedHash[:16])

	slog.Warn("DRIFT_DETECTED",
		"endpoint", ep.ID,
		"severity", severity,
		"baseline_hash", baseHash[:12],
		"observed_hash", observedHash[:12],
		"changes", len(changes),
		"diff_ms", float64(diffUs)/1000.0,
	)

	if r.cf.Enabled() {
		_ = r.cf.PutSnapshotKV(ctx, newKVKey, body)
		_ = r.cf.InsertDriftIncident(ctx, cloudflare.DriftIncidentRecord{
			EndpointID:    ep.ID,
			OldSchemaHash: baseHash,
			NewSchemaHash: observedHash,
			Severity:      severity,
			DiffSummary:   string(diffJSON),
		})
	}

	r.persistRun(ctx, cloudflare.ProbeRunRecord{
		EndpointID:        ep.ID,
		StatusCode:        resp.StatusCode,
		LatencyMs:         latencyMs,
		ResponseSizeBytes: bodySize,
		ContentType:       contentType,
		SchemaHash:        observedHash,
		DriftDetected:     1,
	})

	r.notifyDrift(ctx, ep, baseHash, observedHash, severity, changes)
}

func classifySeverity(changes []schema.DriftChange) string {
	if len(changes) == 0 {
		return "PENDING"
	}
	for _, c := range changes {
		if c.Breaking {
			return "BREAKING"
		}
	}
	return "NON_BREAKING"
}

func countFields(n *schema.Node) int {
	if n == nil {
		return 0
	}
	total := len(n.Properties)
	for _, child := range n.Properties {
		total += countFields(child)
	}
	if n.Items != nil {
		total += countFields(n.Items)
	}
	return total
}

func (r *Runner) notifyDrift(ctx context.Context, ep cloudflare.Endpoint, baseHash, observedHash, severity string, changes []schema.DriftChange) {
	payload := map[string]any{
		"event":         "DRIFT_DETECTED",
		"endpoint_id":   ep.ID,
		"endpoint_name": ep.Name,
		"url":           ep.URL,
		"severity":      severity,
		"baseline_hash": baseHash,
		"observed_hash": observedHash,
		"changes":       changes,
		"detected_at":   time.Now().UTC().Format(time.RFC3339),
	}
	raw, _ := json.Marshal(payload)

	if r.cfg.RenderAPIURL != "" {
		r.postWebhook(ctx, strings.TrimRight(r.cfg.RenderAPIURL, "/")+"/internal/drift", raw)
	}
}

func (r *Runner) postWebhook(ctx context.Context, targetURL string, body []byte) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if r.cfg.InternalWebhookSecret != "" {
		req.Header.Set("X-Internal-Secret", r.cfg.InternalWebhookSecret)
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		slog.Warn("webhook dispatch failed", "url", targetURL, "err", err)
		return
	}
	resp.Body.Close()
}

// Reprobe triggers an immediate probe of the given endpoint, bypassing the
// poll interval. Returns true if the endpoint was found and re-probed.
func (r *Runner) Reprobe(ctx context.Context, endpointID string) bool {
	r.mu.RLock()
	ep, ok := r.endpoints[endpointID]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	r.probeOnce(ctx, ep)
	return true
}

func (r *Runner) keepRenderWarm(ctx context.Context) {
	ticker := time.NewTicker(r.cfg.RenderWarmInterval)
	defer ticker.Stop()

	healthURL := strings.TrimRight(r.cfg.RenderAPIURL, "/") + "/health"
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
			if err != nil {
				continue
			}
			resp, err := r.httpClient.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}
	}
}

func (r *Runner) persistRun(ctx context.Context, rec cloudflare.ProbeRunRecord) {
	if !r.cf.Enabled() {
		return
	}
	if err := r.cf.RecordProbeRun(ctx, rec); err != nil {
		slog.Warn("failed to record probe run in D1", "endpoint", rec.EndpointID, "err", err)
	}
}
