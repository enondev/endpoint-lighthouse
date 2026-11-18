# Endpoint Lighthouse

## Problem

When companies integrate with external APIs (like GitHub, Stripe, Twilio, or public government/financial feeds), upstream providers frequently make unannounced changes—renaming a JSON key, changing a field from an integer to a string, or altering HTTP headers. Endpoint Lighthouse continuously monitors public APIs and OpenAPI/Swagger specs, detects structural drift the moment it happens, and uses AI to generate a plain-English breaking-change report and client migration patch.

## Testing & Verification

All test suites run completely offline against local mocks.

**AI Worker Unit & Integration Tests (`12/12` passing):**
```bash
cd services/ai-worker
pytest -v
```

**Cross-Platform End-to-End Pipeline Simulator:**
```bash
python scripts/simulate_drift.py
```

**Offline Playwright Browser E2E Suite (`4/4` passing):**
```bash
pytest tests/e2e -v
```