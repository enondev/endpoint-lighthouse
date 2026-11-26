# Architecture Decision Records (ADRs)

This directory contains Architecture Decision Records for the Endpoint Lighthouse system. Each ADR captures a significant design decision, its context, rationale, and consequences.

## ADR Index

| ADR | Title | Status |
|-----|-------|--------|
| [0001](0001-multi-service-webhook-architecture.md) | Multi-Service Webhook-Decoupled Architecture | Accepted |
| [0002](0002-cloudflare-d1-kv-data-plane.md) | Cloudflare D1 + KV as Data Plane (Black-Box, No Server Mods) | Accepted |
| [0003](0003-redis-setnx-cooldown.md) | Redis SETNX Cooldown for Deduplication | Accepted |
| [0004](0004-sse-real-time-updates.md) | Server-Sent Events (SSE) for Real-Time Frontend Updates | Accepted |
| [0005](0005-conditional-http-requests.md) | Conditional HTTP Requests (ETag / Last-Modified) for Bandwidth Optimization | Accepted |
| [0006](0006-schema-inference-ast-diff.md) | JSON Schema Inference + AST Structural Diff for Drift Detection | Accepted |
| [0007](0007-severity-classification.md) | Severity Classification Strategy | Accepted |
| [0008](0008-reconciliation-sweeper.md) | Self-Healing Reconciliation Sweeper for Orphaned Incidents | Accepted |
| [0009](0009-observability-stack.md) | Observability Stack Selection | Accepted |
| [0010](0010-logging-format.md) | Structured Logging Format — JSON in Production, Text Locally | Accepted |
| [0011](0011-tracing-fallback.md) | Distributed Tracing Falls Back to No-Op Without Collector | Accepted |
| [0012](0012-request-id.md) | Request ID Correlation Strategy | Accepted |
| [0013](0013-health-probes.md) | Health Check Dependency Probing | Accepted |

## How to Read

ADRs are numbered in the order they were accepted. The observability ADRs (0009–0013) were added last, after the core architecture ADRs (0001–0008), reflecting the decision to first establish the system's structure before layering observability on top.

Each ADR is standalone: read its **Context**, **Decision**, **Rationale**, and **Consequences** sections to understand why a particular approach was chosen and what trade-offs it entails.

## Contributing

When proposing a new architectural decision, create a new ADR file with the next sequential number, following the same format. Update this index to include the new entry.
