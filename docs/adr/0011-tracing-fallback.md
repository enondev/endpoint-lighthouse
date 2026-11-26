# ADR-0011: Distributed Tracing Falls Back to No-Op Without Collector

## Status
Accepted

## Context
OpenTelemetry tracing requires an OTLP exporter endpoint. In local development and CI, no tracing backend is available, so initializing a real exporter would fail or hang.

## Decision
When `OTEL_EXPORTER_OTLP_ENDPOINT` is unset, initialize OpenTelemetry with `sdktrace.NeverSample()` so spans are created but never exported (no-op). When the endpoint is set, use `otlptracehttp` with batch export. W3C TraceContext and Baggage propagators are configured globally in both modes.

## Rationale
- Zero-configuration local development and CI.
- `NeverSample()` avoids span creation overhead when no collector is present.
- Global propagators ensure inbound trace contexts are respected and propagated outbound to the AI worker and callback.

## Consequences
- Positive: Service starts without a tracing backend; tracing is opt-in via one env var.
- Negative: No traces exported locally; developers cannot inspect trace waterfalls without deploying a collector.

## Related
ADR-0009 (observability stack), ADR-0012 (request IDs)
