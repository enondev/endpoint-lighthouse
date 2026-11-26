# ADR-0003: Redis SETNX Cooldown for Deduplication

## Status
Accepted

## Context
The probe may detect drift for the same endpoint+schema combination repeatedly while the AI worker is still processing (minutes-long inference). Without deduplication, each drift event would trigger a duplicate LLM run and duplicate incident rows.

## Decision
Use Redis `SET key "1" NX EX <ttl>` (SETNX with expiry) as a distributed cooldown lock keyed by `cooldown:{endpoint_id}:{new_schema_hash}`:

- Acquired (SETNX returns OK) → first sighting of this drift; dispatch to AI worker.
- Not acquired (SETNX returns nil) → drift already being processed or recently processed; suppress the webhook.
- TTL configured via `DRIFT_COOLDOWN_SEC` (default 900s / 15 min) so locks auto-expire and do not require explicit unlock.

On Redis failure, the system **fails open** (acquired = true) to avoid blocking legitimate drift detection, and records the error in metrics/logs.

## Rationale
- **Distributed**: Redis is shared across probe and API instances, so cooldown holds across service restarts and horizontal scaling.
- **Self-healing**: TTL expiry means a stuck lock cannot permanently block re-processing.
- **Fail-open**: Prioritizes detecting drift over suppressing it when the lock store is unavailable.

## Consequences
- Positive: Prevents duplicate LLM runs and duplicate incident rows during the minutes-long inference window.
- Negative: Brief race window where two simultaneous drift events could both acquire the lock before either writes; mitigated by the idempotent D1 upsert in `update_d1_incident`.

## Related
ADR-0001 (architecture), ADR-0009 (observability metrics for cooldown outcomes)
