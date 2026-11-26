# ADR-0008: Self-Healing Reconciliation Sweeper for Orphaned Incidents

## Status
Accepted

## Context
Drift incidents can be left in an incomplete state: an OPEN incident whose AI analysis never completed (worker crash, callback failure, network partition). These orphaned incidents would never get AI analysis or resolution.

## Decision
Implement a periodic self-healing reconciliation sweeper in the API:

- On startup (after a 15s delay) and every `ReconcilePeriod` thereafter, query D1 for incidents where `status='OPEN'` and (`ai_analysis IS NULL OR TRIM(ai_analysis)=''` OR `ai_analysis LIKE '%Automated drift analysis completed.%'`).
- For each pending incident, acquire a Redis cooldown lock (`reconcile:incident:{id}`, 5-minute TTL) to prevent concurrent sweeps from double-dispatching.
- Dispatch the incident to the AI worker's `/analyze` endpoint with the standard retry/backoff policy.
- An on-demand `POST /internal/reconcile` admin endpoint triggers an immediate sweep.

## Rationale
- **Self-healing**: Orphaned incidents are eventually processed without operator intervention.
- **Cooldown locks**: Prevent the periodic sweep and the on-demand endpoint from double-dispatching the same incident.
- **Idempotent dispatch**: The AI worker's `update_d1_incident` upserts by `endpoint_id + old_schema_hash + new_schema_hash`, so re-processing is safe.

## Consequences
- Positive: No permanently stuck incidents; resilience to worker/callback failures.
- Negative: Periodic sweeps generate synthetic D1 queries and potential duplicate dispatches if locks fail; bounded by TTL and idempotent upserts.

## Related
ADR-0001 (architecture), ADR-0003 (Redis cooldown), ADR-0002 (D1)
