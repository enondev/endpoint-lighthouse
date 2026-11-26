# ADR-0009: Observability Stack Selection

## Status
Accepted

## Context
The system previously emitted unstructured `log.Printf` statements with no metrics, tracing, or dependency health checks. With three services communicating via webhooks, it was impossible to alert on error rates, trace a drift incident end-to-end (probe → api → ai-worker → callback), or detect degraded Redis/D1/Cloudflare dependencies.

## Decision
Adopt a three-pillar observability stack per service:

- **Logging**: Go standard library `log/slog` (API/probe); Python `structlog` (AI worker). Structured, leveled, JSON in production / text locally.
- **Metrics**: Prometheus client libraries with a `/metrics` endpoint on a dedicated port.
- **Tracing**: OpenTelemetry SDK with OTLP HTTP exporter and W3C TraceContext/Baggage propagation.

## Rationale
- `slog`/`structlog` are standard structured-logging packages requiring minimal external dependencies.
- Prometheus is the industry standard for service metrics; scraping `/metrics` enables alerting on error rates, latency, and queue depth.
- OpenTelemetry is vendor-neutral; W3C propagation lets trace IDs flow across service boundaries.

## Consequences
- Positive: Structured JSON logs are ingestible by Loki/ELK/CloudWatch; Prometheus metrics enable alerting; OpenTelemetry enables end-to-end trace correlation.
- Negative: Additional dependencies increase binary/package size and startup complexity. Tracing is a no-op locally unless an OTLP endpoint is configured.

## Related
ADR-0010 (logging format), ADR-0011 (tracing fallback), ADR-0012 (request IDs), ADR-0013 (health probes)
