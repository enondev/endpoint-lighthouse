import json

import httpx
import respx
from fastapi.testclient import TestClient

from app.config import settings


def test_health_endpoint(client: TestClient) -> None:
    resp = client.get("/health")
    assert resp.status_code == 200
    assert resp.json() == {"status": "ok", "service": "ai-worker"}


def test_analyze_rejects_missing_or_invalid_secret(client: TestClient) -> None:
    payload = {
        "endpoint_id": "ep-github-status",
        "old_schema_hash": "aaaa1111bbbb2222cccc3333dddd4444",
        "new_schema_hash": "ffff9999eeee8888dddd7777cccc6666",
    }

    resp_missing = client.post("/analyze", json=payload)
    assert resp_missing.status_code == 401

    resp_wrong = client.post(
        "/analyze",
        json=payload,
        headers={"X-Internal-Secret": "wrong-secret"},
    )
    assert resp_wrong.status_code == 401


@respx.mock
def test_analyze_full_pipeline_success(
    client: TestClient,
    valid_headers: dict[str, str],
    sample_baseline_snapshot: dict,
    sample_drifted_snapshot: dict,
) -> None:
    old_hash = "aaaa1111bbbb2222cccc3333dddd4444"
    new_hash = "ffff9999eeee8888dddd7777cccc6666"
    old_kv_key = f"snap:ep-github-status:{old_hash[:16]}"
    new_kv_key = f"snap:ep-github-status:{new_hash[:16]}"

    cf_base = f"https://api.cloudflare.com/client/v4/accounts/{settings.cf_account_id}"

    # 1. Mock KV snapshot fetches
    respx.get(
        f"{cf_base}/storage/kv/namespaces/{settings.cf_kv_namespace_id}/values/{old_kv_key}"
    ).mock(return_value=httpx.Response(200, json=sample_baseline_snapshot))

    respx.get(
        f"{cf_base}/storage/kv/namespaces/{settings.cf_kv_namespace_id}/values/{new_kv_key}"
    ).mock(return_value=httpx.Response(200, json=sample_drifted_snapshot))

    # 2. Mock Workers AI inference
    respx.post(f"{cf_base}/ai/run/{settings.cf_ai_model}").mock(
        return_value=httpx.Response(
            200,
            json={
                "success": True,
                "result": {
                    "response": json.dumps(
                        {
                            "severity": "BREAKING",
                            "analysis": "status.description removed and status.indicator changed to integer.",
                            "patch_snippet": "const indicator = String(res.status.indicator);",
                        }
                    )
                },
            },
        )
    )

    # 3. Mock D1 update
    d1_route = respx.post(f"{cf_base}/d1/database/{settings.cf_d1_database_id}/query").mock(
        return_value=httpx.Response(200, json={"success": True})
    )

    # 4. Mock Render gateway callback
    cb_route = respx.post(f"{settings.render_api_url}/internal/analysis-completed").mock(
        return_value=httpx.Response(200, json={"status": "broadcasted"})
    )

    resp = client.post(
        "/analyze",
        headers=valid_headers,
        json={
            "incident_id": 42,
            "endpoint_id": "ep-github-status",
            "endpoint_name": "GitHub Status",
            "endpoint_url": "https://githubstatus.com",
            "old_schema_hash": old_hash,
            "new_schema_hash": new_hash,
        },
    )

    assert resp.status_code == 200
    body = resp.json()
    assert body["status"] == "analyzed"
    assert body["incident_id"] == 42
    assert body["severity"] == "BREAKING"
    assert isinstance(body["duration_ms"], int)

    # Verify D1 was updated by incident_id = 42
    assert d1_route.called
    d1_payload = json.loads(d1_route.calls[0].request.content)
    assert "WHERE id = ?" in d1_payload["sql"]
    assert d1_payload["params"][-1] == 42

    # Verify Render callback received expected SSE broadcast payload
    assert cb_route.called
    cb_payload = json.loads(cb_route.calls[0].request.content)
    assert cb_payload["incident_id"] == 42
    assert cb_payload["severity"] == "BREAKING"
    assert "ai_patch_snippet" in cb_payload


@respx.mock
def test_analyze_returns_502_when_kv_fetch_fails(
    client: TestClient,
    valid_headers: dict[str, str],
) -> None:
    cf_base = f"https://api.cloudflare.com/client/v4/accounts/{settings.cf_account_id}"
    respx.get(
        f"{cf_base}/storage/kv/namespaces/{settings.cf_kv_namespace_id}/values/snap:ep-1:1111111111111111"
    ).mock(return_value=httpx.Response(404, text="Key not found"))

    resp = client.post(
        "/analyze",
        headers=valid_headers,
        json={
            "endpoint_id": "ep-1",
            "old_schema_hash": "1111111111111111aaaa",
            "new_schema_hash": "2222222222222222bbbb",
        },
    )
    assert resp.status_code == 502
    assert "Upstream service error: 404" in resp.json()["detail"]
