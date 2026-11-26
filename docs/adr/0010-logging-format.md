# ADR-0010: Structured Logging Format — JSON in Production, Text Locally

## Status
Accepted

## Context
Unstructured `log.Printf` output is difficult to parse in log aggregation systems. We need structured logs that are human-readable locally and machine-parseable in production.

## Decision
Use `slog` (Go) / `structlog` (Python) with:
- **JSON handler** when `ENVIRONMENT=production`
- **Text/console handler** for all other environments
- Log level controlled via `LOG_LEVEL` env var (default `info`)

Every log line includes `service`, `level`, `ts`, `msg`, plus optional structured fields (`trace_id`, `request_id`, `endpoint_id`, `duration_ms`).

## Rationale
- JSON output is directly ingestible by Loki, ELK, CloudWatch, Datadog.
- Text output is human-readable during local development and CI.
- Both packages are standard-library-adjacent (slog is stdlib in Go 1.21+; structlog is lightweight for Python).

## Consequences
- Positive: Structured fields enable filtering and alerting in log aggregators.
- Negative: JSON logs are less readable locally; developers must set `LOG_LEVEL=debug` for verbose output.

## Related
ADR-0009 (observability stack)
