"""Critical observability tests for the AI worker."""

import httpx
from fastapi.testclient import TestClient

from app.main import metrics_endpoint_payload
from app.observability import (
    init_tracing,
)


def test_metrics_endpoint_exposes_prometheus_format(client: TestClient) -> None:
    resp = client.get("/metrics")
    assert resp.status_code == 200
    assert "text/plain" in resp.headers.get("content-type", "")
    body = resp.text
    assert "endpoint_lighthouse_aiworker_http_requests_total" in body
    assert "endpoint_lighthouse_aiworker_analyze_requests_total" in body
    assert "endpoint_lighthouse_aiworker_llm_calls_total" in body
    assert "endpoint_lighthouse_aiworker_kv_snapshot_fetches_total" in body
    assert "endpoint_lighthouse_aiworker_d1_incident_updates_total" in body
    assert "endpoint_lighthouse_aiworker_callback_deliveries_total" in body


def test_metrics_payload_contains_expected_metrics() -> None:
    payload = metrics_endpoint_payload()
    assert "endpoint_lighthouse_aiworker_http_requests_total" in payload
    assert "endpoint_lighthouse_aiworker_analyze_duration_seconds" in payload
    assert "endpoint_lighthouse_aiworker_llm_call_duration_seconds" in payload


def test_init_tracing_is_noop_without_endpoint(monkeypatch) -> None:
    monkeypatch.delenv("OTEL_EXPORTER_OTLP_ENDPOINT", raising=False)
    # Should not raise; tracing degrades gracefully to no-op.
    init_tracing(service_name="test-ai-worker")


def test_health_endpoint_degraded_when_cloudflare_unreachable(
    monkeypatch, client: TestClient
) -> None:
    orig_get = httpx.AsyncClient.get

    async def failing_get(self, url, *args, **kwargs):
        raise httpx.ConnectError("simulated failure")

    monkeypatch.setattr(httpx.AsyncClient, "get", failing_get)

    resp = client.get("/health")
    body = resp.json()
    assert body["status"] == "degraded"
    assert body["cloudflare_api"] != "ok"
