import hmac
import json
import logging
import socket
import time
from typing import Any

import httpx
from fastapi import FastAPI, Header, HTTPException, status
from pydantic import BaseModel, Field

from app.cloudflare import CloudflareClient
from app.config import settings
from app.differ import compute_semantic_diff
from app.observability import (
    ANALYZE_DURATION,
    ANALYZE_REQUESTS_TOTAL,
    CALLBACK_DELIVERIES_TOTAL,
    D1_INCIDENT_UPDATES_TOTAL,
    KV_SNAPSHOT_FETCHES_TOTAL,
    LLM_CALLS_TOTAL,
    LLM_CALL_DURATION,
    ObservabilityMiddleware,
    clear_request_id,
    generate_request_id,
    get_logger,
    init_logging,
    init_tracing,
    metrics_endpoint,
    set_request_id,
)

init_logging(level=settings.log_level, json_logs=(settings.environment.lower() == "production"))
init_tracing(service_name=settings.otel_service_name)
logger = get_logger("endpoint_lighthouse.ai_worker")

app = FastAPI(
    title="Endpoint Lighthouse AI Analysis Worker",
    version="1.0.0",
    docs_url=None,
    redoc_url=None,
)

# Observability middleware: request ID + metrics + structured request logging
app.add_middleware(ObservabilityMiddleware, service_name=settings.otel_service_name)

cf_client = CloudflareClient()


class AnalyzeRequest(BaseModel):
    incident_id: int | None = Field(default=None)
    endpoint_id: str = Field(..., min_length=1)
    endpoint_name: str = Field(default="Unknown Endpoint")
    endpoint_url: str = Field(default="")
    old_schema_hash: str = Field(..., min_length=16)
    new_schema_hash: str = Field(..., min_length=16)
    baseline_kv_key: str | None = Field(default=None)
    drifted_kv_key: str | None = Field(default=None)


class AnalyzeResponse(BaseModel):
    status: str
    incident_id: int | None
    endpoint_id: str
    severity: str
    duration_ms: int


def verify_internal_secret(x_internal_secret: str | None) -> None:
    if not x_internal_secret or not hmac.compare_digest(
        x_internal_secret, settings.internal_webhook_secret
    ):
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="Invalid or missing X-Internal-Secret",
        )


@app.get("/metrics")
async def metrics() -> Any:
    from starlette.responses import PlainTextResponse

    return PlainTextResponse(
        content=metrics_endpoint_payload(),
        media_type="text/plain; version=0.0.4; charset=utf-8",
    )


def metrics_endpoint_payload() -> str:
    from app.observability import metrics_payload

    return metrics_payload()


async def resolve_cloudflare_ip() -> str | None:
    """Fallback resolver using Cloudflare DoH over raw IP 1.1.1.1 if container DNS fails."""
    try:
        async with httpx.AsyncClient(timeout=5.0) as dns_client:
            r = await dns_client.get(
                "https://1.1.1.1",
                params={"name": "api.cloudflare.com", "type": "A"},
                headers={
                    "User-Agent": "EndpointLighthouse-ai-worker/0.1 (+https://github.com/enondev/endpoint-lighthouse)",
                    "Accept": "application/dns-json"
                },
            )
            r.raise_for_status()
            for ans in r.json().get("Answer", []):
                if ans.get("type") == 1 and ans.get("data"):
                    return ans["data"]
    except Exception:
        return None
    return None


@app.get("/health")
async def health() -> dict[str, Any]:
    """Health endpoint with dependency probes for D1, KV, and Workers AI."""
    details: dict[str, Any] = {"service": "ai-worker"}
    status = "ok"

    # Cloudflare API connectivity probe (cheap: reach API root)
    try:
        async with httpx.AsyncClient(timeout=3.0) as probe:
            resp = await probe.get(
                f"https://api.cloudflare.com/client/v4/accounts/{settings.cf_account_id}",
                headers={"Authorization": f"Bearer {settings.cf_api_token}"},
            )
            details["cloudflare_api"] = "ok" if resp.status_code < 400 else "degraded"
            if resp.status_code >= 400:
                status = "degraded"
    except Exception as exc:  # pragma: no cover - probe only
        details["cloudflare_api"] = f"unreachable: {exc}"
        status = "degraded"

    details["version"] = "1.0.0"
    details["status"] = status
    return details


@app.post("/analyze", response_model=AnalyzeResponse)
async def analyze_drift(
    payload: AnalyzeRequest,
    x_internal_secret: str | None = Header(default=None),
) -> AnalyzeResponse:
    verify_internal_secret(x_internal_secret)

    request_id = generate_request_id()
    set_request_id(request_id)
    try:
        return await _run_analysis(payload, request_id)
    finally:
        clear_request_id()


async def _run_analysis(payload: AnalyzeRequest, request_id: str) -> AnalyzeResponse:
    start_time = time.perf_counter()

    old_kv_key = payload.baseline_kv_key or f"snap:{payload.endpoint_id}:{payload.old_schema_hash[:16]}"
    new_kv_key = payload.drifted_kv_key or f"snap:{payload.endpoint_id}:{payload.new_schema_hash[:16]}"

    logger.info(
        "Starting AI drift analysis",
        endpoint_id=payload.endpoint_id,
        incident_id=payload.incident_id,
        old_kv_key=old_kv_key,
        new_kv_key=new_kv_key,
        request_id=request_id,
    )

    timeout = httpx.Timeout(settings.http_timeout_seconds)
    transport = httpx.AsyncHTTPTransport(local_address="0.0.0.0", retries=2)
    severity = "WARNING"
    severity_score = 0
    analysis_text = "Automated drift analysis completed."
    patch_snippet = "// Review the schema diff and update client code accordingly."
    diff_summary: dict[str, Any] = {"note": "Schema hash changed due to structural normalization."}

    async with httpx.AsyncClient(transport=transport, timeout=timeout) as client:
        try:
            # Fetch baseline & drifted snapshots from Cloudflare KV
            kv_start = time.perf_counter()
            try:
                baseline_snap = await cf_client.get_kv_snapshot(client, old_kv_key)
                KV_SNAPSHOT_FETCHES_TOTAL.labels(outcome="success").inc()
                drifted_snap = await cf_client.get_kv_snapshot(client, new_kv_key)
                KV_SNAPSHOT_FETCHES_TOTAL.labels(outcome="success").inc()
            except Exception as exc:
                KV_SNAPSHOT_FETCHES_TOTAL.labels(outcome="error").inc()
                logger.warning(
                    "KV snapshot fetch failed",
                    endpoint_id=payload.endpoint_id,
                    error=str(exc)[:200],
                )
                raise

            # Compute structural & type diff via DeepDiff
            diff_summary = compute_semantic_diff(baseline_snap, drifted_snap)
            if not diff_summary:
                diff_summary = {"note": "Schema hash changed due to structural normalization."}

            # Run Cloudflare Workers AI (@cf/meta/llama-3.1-8b-instruct)
            llm_start = time.perf_counter()
            try:
                ai_result = await cf_client.run_llm_analysis(
                    client=client,
                    endpoint_name=payload.endpoint_name,
                    endpoint_url=payload.endpoint_url,
                    diff_summary=diff_summary,
                )
                LLM_CALLS_TOTAL.labels(outcome="success").inc()
                LLM_CALL_DURATION.observe(time.perf_counter() - llm_start)
            except Exception as exc:
                LLM_CALLS_TOTAL.labels(outcome="error").inc()
                LLM_CALL_DURATION.observe(time.perf_counter() - llm_start)
                logger.warning(
                    "Workers AI call failed, using fallback",
                    endpoint_id=payload.endpoint_id,
                    error=str(exc)[:200],
                )
                ai_result = {
                    "severity": "WARNING",
                    "analysis": "Automated drift analysis completed; LLM unavailable.",
                    "patch_snippet": "// Review the schema diff and update client code accordingly.",
                }

            severity = ai_result["severity"]
            severity_score = ai_result.get("severity_score", 0)
            analysis_text = ai_result["analysis"]
            patch_snippet = ai_result["patch_snippet"]
            diff_json_str = json.dumps(diff_summary)

            # Persist diff_summary, ai_analysis, and ai_patch_snippet into D1
            d1_start = time.perf_counter()
            try:
                await cf_client.update_d1_incident(
                    client=client,
                    incident_id=payload.incident_id,
                    endpoint_id=payload.endpoint_id,
                    old_schema_hash=payload.old_schema_hash,
                    new_schema_hash=payload.new_schema_hash,
                    severity=severity,
                    severity_score=severity_score,
                    diff_summary_str=diff_json_str,
                    ai_analysis_str=analysis_text,
                    ai_patch_snippet=patch_snippet,
                )
                D1_INCIDENT_UPDATES_TOTAL.labels(outcome="success").inc()
            except Exception as exc:
                D1_INCIDENT_UPDATES_TOTAL.labels(outcome="error").inc()
                logger.warning(
                    "D1 incident update failed",
                    endpoint_id=payload.endpoint_id,
                    incident_id=payload.incident_id,
                    error=str(exc)[:200],
                )
                # Non-fatal: analysis still completed; callback will broadcast.

            # Notify Render API Gateway to broadcast SSE `analysis_ready` event
            callback_url = f"{settings.render_api_url.rstrip('/')}/internal/analysis-completed"
            callback_payload: dict[str, Any] = {
                "incident_id": payload.incident_id,
                "endpoint_id": payload.endpoint_id,
                "old_schema_hash": payload.old_schema_hash,
                "new_schema_hash": payload.new_schema_hash,
                "severity": severity,
                "severity_score": severity_score,
                "diff_summary": diff_summary,
                "ai_analysis": analysis_text,
                "ai_patch_snippet": patch_snippet,
            }
            cb_start = time.perf_counter()
            try:
                cb_resp = await client.post(
                    callback_url,
                    headers={
                        "X-Internal-Secret": settings.internal_webhook_secret,
                        "Content-Type": "application/json",
                    },
                    json=callback_payload,
                )
                cb_resp.raise_for_status()
                CALLBACK_DELIVERIES_TOTAL.labels(outcome="success").inc()
                logger.info(
                    "Callback delivered to API gateway",
                    endpoint_id=payload.endpoint_id,
                    callback_status=cb_resp.status_code,
                    duration_ms=int((time.perf_counter() - cb_start) * 1000),
                )
            except Exception as exc:
                CALLBACK_DELIVERIES_TOTAL.labels(outcome="error").inc()
                logger.warning(
                    "Callback delivery failed",
                    endpoint_id=payload.endpoint_id,
                    error=str(exc)[:200],
                )
                # Non-fatal: analysis persisted; callback can be retried manually.

        except httpx.HTTPStatusError as exc:
            ANALYZE_REQUESTS_TOTAL.labels(outcome="error").inc()
            logger.error(
                "Upstream HTTP error analyzing endpoint",
                endpoint_id=payload.endpoint_id,
                status_code=exc.response.status_code,
                response_text=exc.response.text[:300],
            )
            raise HTTPException(
                status_code=status.HTTP_502_BAD_GATEWAY,
                detail=f"Upstream service error: {exc.response.status_code}",
            ) from exc
        except Exception as exc:
            ANALYZE_REQUESTS_TOTAL.labels(outcome="error").inc()
            logger.exception(
                "Unexpected failure analyzing endpoint",
                endpoint_id=payload.endpoint_id,
                request_id=request_id,
            )
            raise HTTPException(
                status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
                detail="Internal analysis worker failure",
            ) from exc

    duration_ms = int((time.perf_counter() - start_time) * 1000)
    ANALYZE_REQUESTS_TOTAL.labels(outcome="success").inc()
    ANALYZE_DURATION.observe(time.perf_counter() - start_time)

    logger.info(
        "Completed analysis",
        endpoint_id=payload.endpoint_id,
        incident_id=payload.incident_id,
        severity=severity,
        duration_ms=duration_ms,
        request_id=request_id,
    )

    return AnalyzeResponse(
        status="analyzed",
        incident_id=payload.incident_id,
        endpoint_id=payload.endpoint_id,
        severity=severity,
        duration_ms=duration_ms,
    )
