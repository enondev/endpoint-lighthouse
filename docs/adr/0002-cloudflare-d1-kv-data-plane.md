# ADR-0002: Cloudflare D1 + KV as Data Plane (Black-Box, No Server Mods)

## Status
Accepted

## Context
The system needs durable storage for endpoints, schema baselines, probe runs, and drift incidents, plus raw payload snapshots. The constraint is that no LLM server or Cloudflare infrastructure may be modified — everything must use standard REST APIs.

## Decision
Use Cloudflare's native products as the data plane, accessed strictly through their public REST APIs:

- **Cloudflare D1** (SQLite-compatible) — Stores `endpoints`, `schema_baselines`, `probe_runs`, and `drift_incidents` tables (see `migrations/schema.sql`). Accessed via `POST /d1/database/{database_id}/query`.
- **Cloudflare KV** — Stores immutable raw JSON payload snapshots keyed by `snap:{endpoint_id}:{hash[:16]}`. Accessed via KV namespace REST API.

Both are treated as black-box services. No custom schema migration tooling runs against D1 beyond the seeded `schema.sql`.

## Rationale
- **No server mods**: Satisfies the core constraint that the LLM/data infrastructure is a black box.
- **Serverless operational model**: No VMs or containers to manage for the data layer; D1 and KV are managed by Cloudflare.
- **Co-location**: Probe, API, and AI worker all read/write the same D1/KV, providing a shared source of truth without a message broker.

## Consequences
- Positive: Zero data-plane infrastructure to operate; standard HTTP client suffices; D1 gives SQL queryability for the API's read endpoints.
- Negative: D1 query latency adds to request tails; KV is eventually consistent for writes; no local fallback store for offline development (tests mock both).

## Related
ADR-0001 (architecture), ADR-0006 (schema inference)
