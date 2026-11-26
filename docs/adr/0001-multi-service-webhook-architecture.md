# ADR-0001: Multi-Service Webhook-Decoupled Architecture

## Status
Accepted

## Context
Endpoint Lighthouse must monitor public APIs continuously and detect schema drift that can take minutes to resolve. The browser cannot wait for synchronous completion. The system needs three distinct planes: a data plane (probe), an orchestrator (api), and a compute plane (ai-worker).

## Decision
Decompose into three services communicating via webhooks over standard HTTP:

1. **services/probe** (Go) — Data plane. Polls external endpoints on intervals, infers JSON schema, writes probe runs and snapshots to Cloudflare D1/KV, and emits `DRIFT_DETECTED` webhooks to both the API orchestrator and the AI worker.

2. **services/api** (Go) — Event-driven orchestrator. Receives drift webhooks from the probe (`POST /internal/drift`), enforces Redis SETNX cooldown locks to prevent duplicate LLM runs, dispatches jobs to the AI worker, receives `analysis-completed` webhooks from the AI worker, broadcasts live events to the frontend over SSE, and serves read-only REST endpoints from D1.

3. **services/ai-worker** (Python/FastAPI) — Compute plane. Stateless container that fetches KV snapshots, computes semantic diffs, runs Cloudflare Workers AI for plain-English analysis, persists results to D1, and calls back to the API to signal completion.

No shared database or message broker between services — all coordination happens via webhook callbacks and shared D1/KV state.

## Rationale
- **Decoupling**: Each plane can be scaled, deployed, and failed independently. The probe never blocks on AI analysis.
- **Fire-and-forget**: The probe emits a webhook and continues polling; the AI worker processes asynchronously and calls back when done.
- **Black-box data store**: Cloudflare D1 and KV are used as-is with no server modifications (standard REST API only).

## Consequences
- Positive: Independent deployability; no single point of coordination; background processing matches the minutes-long AI inference constraint.
- Negative: Eventual consistency — an incident may exist in D1 before AI analysis completes. Requires cooldown locks and a reconciliation sweeper to handle orphaned incidents.

## Related
ADR-0002 (Cloudflare D1+KV), ADR-0003 (Redis cooldown), ADR-0004 (SSE), ADR-0008 (reconciliation)
