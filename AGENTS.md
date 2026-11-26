# AGENTS.md

# Endpoint Lighthouse

## What this project is

**Endpoint Lighthouse** is an event-driven API contract drift monitor. It polls public API endpoints, hashes response schemas, detects structural drift the moment it happens, and uses AI to generate plain-English breaking-change reports and client migration patches.

### Architecture (three services, event-driven)

```
services/probe        services/api              services/ai-worker
(data plane)          (orchestrator)            (compute plane)
polls endpoints        receives webhooks         FastAPI backend
hashes schema          SETNX cooldown locks      /analyze endpoint
writes snapshot to KV  D1 read endpoints         Workers AI LLM analysis
POST /internal/drift   SSE /api/v1/stream        POST /internal/analysis-completed
```

```
probe ──POST /internal/drift──► api ──POST /analyze──► ai-worker
                                                │
                                                └──POST /internal/analysis-completed──┘
```

- **services/probe** (Go): polls external endpoints on `poll_interval_sec`, hashes response schema, writes snapshot to Cloudflare KV, records drift incidents in Cloudflare D1, emits webhook `POST /internal/drift` to api. Also exposes `POST /internal/reprobe/{endpoint_id}` for on-demand re-probe.
- **services/api** (Go stdlib mux): event-driven orchestrator. Enforces `SETNX` cooldown locks in Redis to prevent duplicate/ redundant LLM runs. Dispatches jobs to ai-worker. Self-healing reconciliation sweeper for orphaned `OPEN` incidents (sub-15-minute ticker with warm-up ping). Serves read-only REST (`/api/v1/status`, `/api/v1/endpoints`, `/api/v1/incidents`, `/api/v1/incidents/{id}`, `PATCH /api/v1/incidents/{id}/status`) and SSE (`/api/v1/stream` with `Last-Event-ID` replay).
- **services/ai-worker** (Python FastAPI): stateless Docker container. Runs AI analysis via Cloudflare Workers AI, writes results back to D1 via `POST /internal/analysis-completed`, marks incidents `ANALYZED`.

### Data plane (Cloudflare D1 / KV)

- `drift_incidents` — incident records (`status: OPEN | ACKNOWLEDGED | RESOLVED | ANALYZED | INCONCLUSIVE`, `ai_analysis`, `ai_patch_snippet`, `severity`, `severity_score`, `diff_summary`)
- `endpoints` — monitored endpoints (`url`, `poll_interval_sec`, `is_active`, `last_etag`, `last_modified`)
- `schema_baselines` — per-endpoint schema snapshots (`schema_hash`, `kv_snapshot_key`, `is_current`)
- `probe_runs` — per-probe observation records
- Cloudflare KV — snapshot storage keyed by `snap:<endpoint_id>:<hash_prefix>`

### Database migrations

Schema changes are managed via versioned migration files in `migrations/`:

| File | Description |
|------|-------------|
| `001_initial_schema.sql` | Core tables: `endpoints`, `schema_baselines`, `probe_runs`, `drift_incidents` |
| `002_contract_drift_tables.sql` | Contract drift tables: `sources`, `operations`, `probes`, `observations`, `diffs` |
| `003_add_inconclusive_status.sql` | Documents `INCONCLUSIVE` status for `drift_incidents` (app-level enforcement) |
| `schema.sql` | Legacy combined schema (kept for reference) |
| `seeds.sql` | Seed data for endpoints and sources |
| `old.sql` / `seed_discovered.sql` | Legacy migration files |

Apply migrations in order (001 → 002 → 003). SQLite does not support `ALTER TABLE ... ADD CONSTRAINT`, so the `drift_incidents.status` column accepts any `TEXT` value; new status values (e.g., `INCONCLUSIVE`) are enforced at the application level.

### Testing (offline against local mocks)

- AI Worker: `cd services/ai-worker && pytest -v` (29 tests)
- Probe: `cd services/probe && go test ./...` (13 tests: runner + schema inference)
- API: `cd services/api && go test -v ./cmd/api/ ./internal/...` (17 tests)
- Cross-platform simulator: `python scripts/simulate_drift.py`

### Configuration (env vars)

| Var | Default | Description |
|-----|---------|-------------|
| `RENDER_API_URL` | — | api base URL for webhooks (probe→api) |
| `SNAPDEPLOY_WORKER_URL` | — | ai-worker URL for analysis dispatch |
| `INTERNAL_WEBHOOK_SECRET` | — | **Required.** HMAC secret for internal webhook auth (`X-Internal-Secret`). If unset, all webhook auth fails closed. |
| `AI_WORKER_SECRET` | — | Legacy secret field (no longer used for auth) |
| `REDIS_URL` | — | Redis URL for SETNX cooldown locks |
| `CF_API_TOKEN`, `CF_ACCOUNT_ID`, `CF_D1_DATABASE_ID`, `CF_KV_NAMESPACE_ID` | — | Cloudflare credentials |
| `ALLOWED_ORIGIN` | `*` | CORS origin for public REST endpoints |
| `RECONCILE_PERIOD` | `5m` | Periodic reconciliation sweeper interval (sub-15min to avoid idle spin-down) |
| `RECONCILE_BACKOFFS` | `0,5,10,15,20s` | Cold-start dispatch backoffs |

## How to run

### Prerequisites
- Go 1.26+ (services/api, services/probe, services/engine)
- Python 3.12+ (services/ai-worker)
- Cloudflare account (D1, KV, Workers AI)
- Redis (for SETNX cooldown locks)

### Local development

```bash
# api
cd services/api
go test -v ./cmd/api/ ./internal/...

# probe
cd services/probe
go test -v ./...

# ai-worker
cd services/ai-worker
python -m pytest tests/ -v

# Cross-platform drift simulator (offline, local mocks)
python scripts/simulate_drift.py
```

### Deploy (Render)

- `services/api` → Render web service (`go build`, port 10000)
- `services/probe` → Render worker service
- `services/ai-worker` → Render worker service (Dockerfile, port 8000)
- Set `INTERNAL_WEBHOOK_SECRET`, `CLOUDFLARE_WEB_ANALYTICS_TOKEN`, and all `CF_*` env vars in Render dashboard

## Architecture decision records (ADRs)

See `docs/adr/` for 13 ADRs covering:
- 0001: Multi-service webhook architecture
- 0002: Cloudflare D1/KV data plane
- 0003: Redis SETNX cooldown
- 0004: SSE real-time updates
- 0005: Conditional HTTP requests
- 0006: Schema inference AST diff
- 0007: Severity classification
- 0008: Reconciliation sweeper
- 0009: Observability stack
- 0010: Logging format
- 0011: Tracing fallback
- 0012: Request ID
- 0013: Health probes

## Security

- All internal webhooks (`/internal/drift`, `/internal/analysis-completed`, `/internal/reconcile`, `/internal/reconcile-all`, `/internal/reprobe/{id}`) require `X-Internal-Secret` header matching `INTERNAL_WEBHOOK_SECRET`. If unset, all requests are rejected (fail closed).
- Public REST endpoints (`/api/v1/*`) are rate-limited (60 req/min per IP) and CORS-gated (`ALLOWED_ORIGIN`).
- `/metrics` endpoints are unauthenticated — do not expose to public internet without a proxy.
- Secrets are never logged. Webhook response bodies are excluded from log lines.

## Incident lifecycle

`OPEN` → (ai-worker analysis completes) → `ANALYZED` → (user acknowledges) → `ACKNOWLEDGED` → (user resolves) → `RESOLVED` → (re-open if needed) → `OPEN`

- `POST /internal/reconcile-all` — one-time migration to re-analyze all OPEN incidents with current prompt (batched: 5 per batch, 30s inter-batch delay, 30-min Redis lock per incident)
- `PATCH /api/v1/incidents/{id}/status` — transition status with validation
- `GET /api/v1/incidents?status=OPEN|ACKNOWLEDGED|RESOLVED|ANALYZED|INCONCLUSIVE` — filter by status

## Request tracing

W3C TraceContext is propagated across service boundaries:
- probe → api: `traceparent`/`tracestate` headers on `POST /internal/drift`
- api → ai-worker: trace context on `POST /analyze`
- ai-worker → api: trace context on `POST /internal/analysis-completed`

OpenTelemetry is initialized in api and ai-worker. Probe also initializes OTel for outbound webhook injection.

## Test commands

```bash
# All services
cd services/api && go test -v ./cmd/api/ ./internal/...
cd services/probe && go test -v ./...
cd services/ai-worker && python -m pytest tests/ -v
```
