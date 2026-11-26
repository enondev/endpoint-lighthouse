# ADR-0004: Server-Sent Events (SSE) for Real-Time Frontend Updates

## Status
Accepted

## Context
The frontend dashboard must reflect drift incidents and analysis results as they happen, without polling. Long-polling or short-interval polling would waste bandwidth and add latency.

## Decision
The API service exposes `GET /api/v1/stream` as a Server-Sent Events endpoint. The SSE hub maintains an in-memory map of connected clients (`map[chan Event]struct{}`) and broadcasts two event types:

- `drift_detected` — emitted when the probe reports drift and the API dispatches to the AI worker.
- `analysis_ready` — emitted when the AI worker calls back with completed analysis.

Broadcasts are non-blocking: slow consumers are dropped (channel full → skip) rather than blocking the hub. A 25-second keep-alive ping prevents idle connection timeouts.

## Rationale
- **Server-push**: The frontend receives updates instantly with a single long-lived HTTP connection.
- **Simple**: SSE is standard HTTP with `text/event-stream`; no WebSocket upgrade or custom protocol.
- **Non-blocking**: Dropping slow consumers protects the hub from a single stalled client.

## Consequences
- Positive: Low-latency UI updates; minimal client complexity (native `EventSource`).
- Negative: SSE connections are not recoverable across restarts (in-memory hub); clients must reconnect and re-subscribe. No message replay for missed events while disconnected.

## Related
ADR-0001 (architecture), ADR-0009 (SSE metrics)
