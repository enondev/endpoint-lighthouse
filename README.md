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