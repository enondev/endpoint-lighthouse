# ADR-0012: Request ID Correlation Strategy

## Status
Accepted

## Context
A single drift incident flows through probe → API (`/internal/drift`) → AI worker (`/analyze`) → API callback (`/internal/analysis-completed`) → SSE broadcast. Without a correlation ID, logs and traces cannot be correlated across service boundaries.

## Decision
Generate an `X-Request-ID` at API ingress via `withRequestID` middleware:
- Reuse inbound `X-Request-ID` if present (supports distributed tracing from callers).
- Otherwise generate a random 16-hex-character ID via `crypto/rand`.
- Store in request context (unexported `requestIDKey`), set as response header, include in every structured log line.
- Forward the same ID to the AI worker and callback as part of webhook payload context.

## Rationale
- Reusing inbound IDs enables end-to-end tracing from external callers.
- Generating when absent ensures every request is traceable without caller instrumentation.
- Unexported context key prevents middleware collisions.

## Consequences
- Positive: Every request has a deterministic correlation ID; logs across services join by `request_id`.
- Negative: The probe and AI worker must also propagate the ID for full end-to-end correlation (currently a gap; future work).

## Related
ADR-0009 (observability stack), ADR-0011 (tracing fallback)
