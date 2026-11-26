# ADR-0013: Health Check Dependency Probing

## Status
Accepted

## Context
The `/health` endpoint previously returned a static `{"status":"ok"}` with no dependency validation. Load balancers could not detect when Redis or D1 were unreachable, routing traffic to degraded instances.

## Decision
Extend `/health` to probe critical dependencies with a 3-second deadline:
- **Redis**: `PING` command
- **D1**: `SELECT 1 AS ok` query
- **AI worker**: Cloudflare API reachability check

Return HTTP 503 with `{"status":"degraded","details":{...}}` when any dependency fails; HTTP 200 with `{"status":"ok",...}` when all are healthy. Per-dependency detail is included in the response body.

## Rationale
- Synthetic probes verify actual connectivity, not just configuration presence.
- 3-second deadline prevents the health endpoint from hanging on unresponsive dependencies.
- Per-dependency details enable operators to identify which dependency is degraded.

## Consequences
- Positive: Load balancers detect and route around degraded instances; operators get dependency-level visibility.
- Negative: Health checks add latency (up to 3s per dependency) and generate synthetic traffic to Redis/D1/Cloudflare.

## Related
ADR-0009 (observability stack)
