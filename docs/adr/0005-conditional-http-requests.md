# ADR-0005: Conditional HTTP Requests (ETag / Last-Modified) for Bandwidth Optimization

## Status
Accepted

## Context
The probe polls dozens of external endpoints on fixed intervals. Most responses are unchanged between polls. Downloading full bodies every time wastes bandwidth and increases schema-inference work.

## Decision
The probe sends `If-None-Match` (ETag) and `If-Modified-Since` (Last-Modified) headers on every request after the first:

- **304 Not Modified** → record a probe run with `drift_detected=0`, skip schema inference entirely, update metrics (`NotModifiedHits`).
- **200 OK with new body** → infer schema, compare against baseline, persist run.
- **3xx redirect** (other than 304) → treated as a contract error and recorded.

ETag/Last-Modified values are cached in-memory per endpoint and also persisted to D1 (`endpoints.last_etag`, `endpoints.last_modified`) so they survive restarts.

## Rationale
- **Bandwidth**: 304 responses have no body, dramatically reducing egress.
- **Speed**: Skipping schema inference on unchanged responses reduces CPU and latency.
- **Correctness**: Conditional headers are standard HTTP and widely supported by public APIs.

## Consequences
- Positive: Reduced bandwidth and compute; faster poll cycles.
- Negative: First poll after a baseline reset or cache miss always downloads the full body. Some origins ignore conditional headers and always return 200.

## Related
ADR-0001 (architecture), ADR-0006 (schema inference)
