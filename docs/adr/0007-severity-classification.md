# ADR-0007: Severity Classification Strategy

## Status
Accepted

## Context
Drift events vary in impact from benign field additions to breaking type changes. The system must classify severity consistently so operators can prioritize responses and the AI worker can tailor its analysis.

## Decision
Two-tier severity classification:

1. **Probe-side** (`classifySeverity` in `runner.go`): 
   - `BREAKING` if any change has `Breaking == true`
   - `NON_BREAKING` otherwise
   - `PENDING` if no changes

2. **AI worker-side** (`ScoringEngine` in `scoring.py`): Normalized 0–100 score combining:
   - LLM severity label (BREAKING=40, WARNING=30, NON_BREAKING=20, PENDING=10)
   - Incident status (OPEN=20, ACKNOWLEDGED=10, RESOLVED=0)
   - Field change count (capped at 20 fields × 2 points)
   - 1.5× multiplier when severity is BREAKING

Weights are configurable via `scoring_weights.json` with fallback to hardcoded defaults.

## Rationale
- **Probe side** is fast and deterministic (no LLM needed), suitable for real-time classification during polling.
- **AI worker side** produces a nuanced score for dashboard prioritization, with tunable weights.
- Configurable weights let operators adapt severity to their risk tolerance without code changes.

## Consequences
- Positive: Consistent, auditable severity; dashboard can sort by score; breaking changes are escalated.
- Negative: Probe-side binary BREAKING/NON_BREAKING is coarse; the WARNING level only exists in the AI worker's scoring, not the probe's initial classification.

## Related
ADR-0006 (AST diff), ADR-0009 (observability metrics for severity)
