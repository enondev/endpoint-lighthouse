# ADR-0006: JSON Schema Inference + AST Structural Diff for Drift Detection

## Status
Accepted

## Context
The probe must determine whether an external API response has structurally changed since the last poll, without prior knowledge of the schema. Responses are arbitrary JSON from public endpoints.

## Decision
Use a two-stage approach:

1. **Schema inference** (`schema.InferFromBytes`): Parse the JSON body into an AST (`schema.Node`) and compute a canonical SHA-256 hash of the inferred schema. This normalizes key order and formatting so semantically identical responses produce the same hash.

2. **AST structural diff** (`schema.DiffAST`): When the hash differs from baseline, walk both ASTs and emit `DriftChange` records (added/removed/changed fields, type changes, iterable changes). Classify severity from the change set.

Truncation is applied to large diffs (`max_items` limits) to bound LLM context tokens.

## Rationale
- **Schema-agnostic**: Works on any JSON response without a pre-defined OpenAPI spec.
- **Hash-first fast path**: Comparing SHA-256 hashes is O(1) and avoids the costlier AST diff when nothing changed.
- **Structural precision**: AST diff captures field-level additions, removals, and type changes that a raw byte diff would miss.

## Consequences
- Positive: Generic drift detection across arbitrary public APIs; field-level change visibility for the AI worker.
- Negative: JSON-only — non-JSON responses (HTML, XML, plain text) are recorded as contract errors rather than drifted. Schema inference may normalize away semantically meaningful whitespace/ordering.

## Related
ADR-0005 (conditional requests), ADR-0007 (severity classification), ADR-0002 (D1 schema storage)
