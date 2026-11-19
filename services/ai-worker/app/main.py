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

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
logger = logging.getLogger("endpoint_lighthouse.ai_worker")

app = FastAPI(
    title="Endpoint Lighthouse AI Analysis Worker",
    version="1.0.0",
    docs_url=None,
    redoc_url=None,
)

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
async def health() -> dict[str, str]:
    return {"status": "ok", "service": "ai-worker"}

@app.post("/analyze", response_model=AnalyzeResponse)
async def analyze_drift(
    payload: AnalyzeRequest,
    x_internal_secret: str | None = Header(default=None),
) -> AnalyzeResponse:
    verify_internal_secret(x_internal_secret)
    start_time = time.perf_counter()

    old_kv_key = payload.baseline_kv_key or f"snap:{payload.endpoint_id}:{payload.old_schema_hash[:16]}"
    new_kv_key = payload.drifted_kv_key or f"snap:{payload.endpoint_id}:{payload.new_schema_hash[:16]}"

    logger.info(
        "Starting AI drift analysis endpoint=%s incident=%s keys=(%s -> %s)",
        payload.endpoint_id,
        payload.incident_id,
        old_kv_key,
        new_kv_key,
    )

    timeout = httpx.Timeout(settings.http_timeout_seconds)
    transport = httpx.AsyncHTTPTransport(local_address="0.0.0.0", retries=2)
    async with httpx.AsyncClient(transport=transport, timeout=timeout) as client:
        try:
            # Fetch baseline & drifted snapshots from Cloudflare KV
            baseline_snap = await cf_client.get_kv_snapshot(client, old_kv_key)
            drifted_snap = await cf_client.get_kv_snapshot(client, new_kv_key)

            # Compute structural & type diff via DeepDiff
            diff_summary = compute_semantic_diff(baseline_snap, drifted_snap)
            if not diff_summary:
                diff_summary = {"note": "Schema hash changed due to structural normalization."}

            # Run Cloudflare Workers AI (@cf/meta/llama-3.1-8b-instruct)
            ai_result = await cf_client.run_llm_analysis(
                client=client,
                endpoint_name=payload.endpoint_name,
                endpoint_url=payload.endpoint_url,
                diff_summary=diff_summary,
            )

            severity = ai_result["severity"]
            analysis_text = ai_result["analysis"]
            patch_snippet = ai_result["patch_snippet"]
            diff_json_str = json.dumps(diff_summary)

            # Persist diff_summary, ai_analysis, and ai_patch_snippet into D1
            await cf_client.update_d1_incident(
                client=client,
                incident_id=payload.incident_id,
                endpoint_id=payload.endpoint_id,
                old_schema_hash=payload.old_schema_hash,
                new_schema_hash=payload.new_schema_hash,
                severity=severity,
                diff_summary_str=diff_json_str,
                ai_analysis_str=analysis_text,
                ai_patch_snippet=patch_snippet,
            )

            # Notify Render API Gateway to broadcast SSE `analysis_ready` event
            callback_url = f"{settings.render_api_url.rstrip('/')}/internal/analysis-completed"
            callback_payload: dict[str, Any] = {
                "incident_id": payload.incident_id,
                "endpoint_id": payload.endpoint_id,
                "old_schema_hash": payload.old_schema_hash,
                "new_schema_hash": payload.new_schema_hash,
                "severity": severity,
                "diff_summary": diff_summary,
                "ai_analysis": analysis_text,
                "ai_patch_snippet": patch_snippet,
            }
            cb_resp = await client.post(
                callback_url,
                headers={
                    "X-Internal-Secret": settings.internal_webhook_secret,
                    "Content-Type": "application/json",
                },
                json=callback_payload,
            )
            cb_resp.raise_for_status()

        except httpx.HTTPStatusError as exc:
            logger.error(
                "Upstream HTTP error analyzing endpoint=%s: %s - %s",
                payload.endpoint_id,
                exc.response.status_code,
                exc.response.text[:300],
            )
            raise HTTPException(
                status_code=status.HTTP_502_BAD_GATEWAY,
                detail=f"Upstream service error: {exc.response.status_code}",
            ) from exc
        except Exception as exc:
            logger.exception("Unexpected failure analyzing endpoint=%s", payload.endpoint_id)
            raise HTTPException(
                status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
                detail="Internal analysis worker failure",
            ) from exc

    duration_ms = int((time.perf_counter() - start_time) * 1000)
    logger.info(
        "Completed analysis endpoint=%s severity=%s in %dms",
        payload.endpoint_id,
        severity,
        duration_ms,
    )

    return AnalyzeResponse(
        status="analyzed",
        incident_id=payload.incident_id,
        endpoint_id=payload.endpoint_id,
        severity=severity,
        duration_ms=duration_ms,
    )