# Endpoint Lighthouse

## Problem

When companies integrate with external APIs (like GitHub, Stripe, Twilio, or public government/financial feeds), upstream providers frequently make unannounced changes—renaming a JSON key, changing a field from an integer to a string, or altering HTTP headers. Endpoint Lighthouse continuously monitors public APIs and OpenAPI/Swagger specs, detects structural drift the moment it happens, and uses AI to generate a plain-English breaking-change report and client migration patch.

## System Architecture

**frontend**

Web-based UI connected to `services/api` available at <https://enondev.github.io/endpoint-lighthouse>

**services/api**

Event-driven orchestrator that processes incoming webhooks from `services/probe` (`POST /internal/drift`) and `services/ai-worker` (`POST /internal/analysis-completed`), that enforces `SETNX` cooldown locks in Redis to prevent duplicates and redundant LLM runs. Dispatches jobs to `services/ai-worker`, with included self-healing reconciliation for orphaned `OPEN` incidents. Serves read-only REST endpoints (`/api/v1/status`, `/api/v1/endpoints`, `/api/v1/incidents`) from D1, and pushes live events to connected browsers over (`/api/v1/stream`) Server-Sent Events (SSE).

**services/ai-worker**

Compute plane that runs a stateless Docker container running a FastAPI backend for processing AI analysis sent from `services/api`.

**services/probe**

Data plane that polls externals endpoints, hashes response schema, writes snapshot to Cloudflare KV, records drift incidents in Cloudflare D1, and emits a webhook (`POST /internal/drift`) to `services/api`.

**services/engine**

Contract-drift plane (AGENTS.md engine). Runs a Go service implementing `fetch → gate → normalize → project → hash → diff → emit` against OpenAPI/Swagger spec sources. Detects structural contract drift (field removed, type narrowed, required added, enum removed, path removed) with severity classification (breaking/additive/cosmetic). Writes findings to Cloudflare D1 (`diffs`, `sources`, `operations`, `probes`, `observations`) and broadcasts via SSE. Runs alongside `services/api`/`services/probe`/`services/ai-worker` — extends, does not replace.

**Adapter pattern**

One engine, many adapters (AGENTS.md). `services/engine/internal/adapter/contractdrift` is the active adapter. `iprange` and `releasetracking` adapters are documented as out of scope per AGENTS.md scope rules (IP range feeds, release feeds, status pages, package registry metadata, static files are not contract sources).

## Testing & Verification

All test suites run completely offline against local mocks.

**AI Worker Unit & Integration Tests (`29/29` passing):**
```bash
cd services/ai-worker
pytest -v
```

**Cross-Platform End-to-End Pipeline Simulator:**
```bash
python scripts/simulate_drift.py
```

**Offline Playwright Browser E2E Suite:**
```bash
pytest tests/e2e -v
```

## Schema & Commands

D1 schema (tables + seed data) lives in `migrations/schema.sql` and `migrations/seeds.sql`. Apply locally or remotely via wrangler:

```bash
# Apply schema to local D1
npx wrangler d1 execute endpoint-lighthouse --local --file=./migrations/schema.sql
npx wrangler d1 execute endpoint-lighthouse --local --file=./migrations/seeds.sql

# Inspect (always run SELECT COUNT(*) first before destructive --remote)
npx wrangler d1 execute endpoint-lighthouse --remote --command="SELECT COUNT(*) FROM sources"
npx wrangler d1 execute endpoint-lighthouse --remote --command="SELECT COUNT(*) FROM operations"
npx wrangler d1 execute endpoint-lighthouse --remote --command="SELECT COUNT(*) FROM diffs"

# Apply schema to remote D1 (only after confirming expected row counts above)
npx wrangler d1 execute endpoint-lighthouse --remote --file=./migrations/schema.sql
npx wrangler d1 execute endpoint-lighthouse --remote --file=./migrations/seeds.sql
```

Never run destructive statements against `--remote` without first running the equivalent `SELECT COUNT(*)` and confirming the expected row count.

## Write Limits

The engine is designed to stay within Cloudflare D1 daily write limits:
- Observations are sampled (written only on state change or probe error, not every probe)
- Diffs are batched per sweep cycle (default max 100 diffs per sweep)
- Contract hash updates are batched per sweep cycle (default max 1000 per sweep)
- Per-sweep write budgets are configurable via `WriteBudget`
- The API `/health` endpoint uses a 10-second short-TTL cache to avoid hitting Redis/D1 on every platform check
- The reconciliation sweeper uses jittered initial delay and ticker to prevent thundering herd across instances