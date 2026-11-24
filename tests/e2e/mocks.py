import json
from typing import Any, Dict, List, Optional
from playwright.sync_api import Page, Route

MOCK_ENDPOINTS = [
    {
        "id": 1,
        "name": "GitHub Zen API",
        "url": "https://github.com",
        "status": "HEALTHY",
        "last_checked": "2026-11-17T20:00:00Z",
        "latency_ms": 48,
    },
    {
        "id": 2,
        "name": "PyPI FastAPI Metadata",
        "url": "https://pypi.org",
        "status": "DRIFT_DETECTED",
        "last_checked": "2026-11-17T20:01:00Z",
        "latency_ms": 62,
    },
]

MOCK_INCIDENTS = [
    {
        "id": 101,
        "endpoint_id": 2,
        "endpoint_name": "PyPI FastAPI Metadata",
        "severity": "BREAKING",
        "diff_summary": "Type changed: info.requires_python (string -> array)",
        "ai_analysis": "Breaking contract change: consumers expecting a scalar string will fail JSON unmarshaling.",
        "ai_patch_snippet": "def migrate_requires_python(val):\n    return val if isinstance(val, list) else [val]",
        "created_at": "2026-11-17T20:01:05Z",
    },
]


def format_sse_payload(events: List[Dict[str, Any]]) -> str:
    return "".join(
        f"event: {evt['event']}\ndata: {json.dumps(evt['data'])}\n\n"
        for evt in events
    )


def setup_offline_api_mocks(
    page: Page,
    endpoints: Optional[List[Dict[str, Any]]] = None,
    incidents: Optional[List[Dict[str, Any]]] = None,
    sse_events: Optional[List[Dict[str, Any]]] = None,
    fail_endpoints: bool = False,
) -> None:
    ep_data = MOCK_ENDPOINTS if endpoints is None else endpoints
    inc_data = MOCK_INCIDENTS if incidents is None else incidents
    stream_data = [] if sse_events is None else sse_events

    cors_headers = {
        "Access-Control-Allow-Origin": "*",
        "Access-Control-Allow-Methods": "GET, OPTIONS",
        "Access-Control-Allow-Headers": "Content-Type, Accept",
    }

    def handle_endpoints(route: Route) -> None:
        if fail_endpoints:
            route.fulfill(
                status=503,
                headers={**{"Content-Type": "application/json"}, **cors_headers},
                body=json.dumps({"error": "Upstream D1 timeout"}),
            )
            return
        route.fulfill(
            status=200,
            headers={**{"Content-Type": "application/json"}, **cors_headers},
            body=json.dumps({"endpoints": ep_data, "data": ep_data}),
        )

    def handle_incidents(route: Route) -> None:
        route.fulfill(
            status=200,
            headers={**{"Content-Type": "application/json"}, **cors_headers},
            body=json.dumps({"incidents": inc_data, "data": inc_data}),
        )

    def handle_stream(route: Route) -> None:
        route.fulfill(
            status=200,
            headers={
                **{
                    "Content-Type": "text/event-stream",
                    "Cache-Control": "no-cache",
                    "Connection": "keep-alive",
                },
                **cors_headers,
            },
            body=format_sse_payload(stream_data),
        )

    page.route("**/api/v1/endpoints**", handle_endpoints)
    page.route("**/api/v1/incidents**", handle_incidents)
    page.route("**/api/v1/stream**", handle_stream)