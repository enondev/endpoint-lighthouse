import json
import logging
from typing import Any

import httpx

from app.config import settings

logger = logging.getLogger("endpoint_lighthouse.cloudflare")

VALID_SEVERITIES = {"NON_BREAKING", "WARNING", "BREAKING"}
MAX_TOKENS = 750

class CloudflareClient:
    def __init__(self) -> None:
        self.base_url = f"https://api.cloudflare.com/client/v4/accounts/{settings.cf_account_id}"
        self.headers = {
            "Authorization": f"Bearer {settings.cf_api_token}",
            "Content-Type": "application/json",
        }

    async def get_kv_snapshot(self, client: httpx.AsyncClient, kv_key: str) -> Any:
        """Fetches immutable raw JSON snapshot from ENDPOINT_LIGHTHOUSE_SNAPSHOTS KV."""
        url = f"{self.base_url}/storage/kv/namespaces/{settings.cf_kv_namespace_id}/values/{kv_key}"
        resp = await client.get(url, headers={"Authorization": f"Bearer {settings.cf_api_token}"})
        resp.raise_for_status()
        return resp.json()

    async def run_llm_analysis(
        self,
        client: httpx.AsyncClient,
        endpoint_name: str,
        endpoint_url: str,
        diff_summary: dict[str, Any],
    ) -> dict[str, Any]:
        """Invokes Cloudflare Workers AI to generate structured migration & risk analysis."""
        url = f"{self.base_url}/ai/run/{settings.cf_ai_model}"

        system_prompt = (
            "You are Endpoint Lighthouse AI, an expert API contract and schema reliability engineer. "
            "Analyze the provided JSON DeepDiff output and respond ONLY with valid JSON. "
            f"Do not include markdown fences or text outside the JSON object. Fit your response in {MAX_TOKENS} tokens"
            "Required JSON format:\n"
            "{\n"
            '  "severity": "BREAKING" | "WARNING" | "NON_BREAKING",\n'
            '  "analysis": "Concise 2-3 sentence summary of what changed and its downstream client impact.",\n'
            '  "patch_snippet": "Short TypeScript or Python client adapter/fallback snippet showing how consumers can safely handle the schema change."\n'
            "}"
        )

        user_prompt = (
            f"Endpoint: {endpoint_name} ({endpoint_url})\n"
            f"Structured Diff:\n{json.dumps(diff_summary, indent=2)[:settings.max_diff_chars]}"
        )

        payload = {
            "messages": [
                {"role": "system", "content": system_prompt},
                {"role": "user", "content": user_prompt},
            ],
            "temperature": 0.1,
            "max_tokens": MAX_TOKENS,
            "response_format": {"type": "json_object"},
        }

        resp = await client.post(url, headers=self.headers, json=payload)
        resp.raise_for_status()
        data = resp.json()

        result = data.get("result") or {}
        raw_text = result.get("response") or ""
        if not raw_text and isinstance(result.get("choices"), list) and result["choices"]:
            msg = result["choices"][0].get("message") or {}
            raw_text = msg.get("content") or ""

        return self._parse_llm_json(str(raw_text).strip())

    def _parse_llm_json(self, raw_text: str) -> dict[str, Any]:
        """Safely parses LLM JSON output with deterministic fallback."""
        cleaned = raw_text.strip()
        start = cleaned.find("{")
        end = cleaned.rfind("}")
        if start != -1 and end != -1 and end > start:
            cleaned = cleaned[start : end + 1]

        try:
            parsed = json.loads(cleaned)
            raw_sev = str(parsed.get("severity", "WARNING")).upper().strip()
            severity = raw_sev if raw_sev in VALID_SEVERITIES else "WARNING"
            return {
                "severity": severity,
                "analysis": str(parsed.get("analysis", "Schema drift detected.")),
                "patch_snippet": str(parsed.get("patch_snippet", "")),
            }
        except json.JSONDecodeError:
            logger.warning("Workers AI returned non-JSON output (len=%d): %r", len(raw_text), raw_text[:120])
            return {
                "severity": "WARNING",
                "analysis": raw_text[:500] if raw_text else "Automated drift analysis completed.",
                "patch_snippet": "// Manual client schema update recommended based on diff_summary.",
            }

    async def update_d1_incident(
        self,
        client: httpx.AsyncClient,
        incident_id: int | None,
        endpoint_id: str,
        old_schema_hash: str,
        new_schema_hash: str,
        severity: str,
        diff_summary_str: str,
        ai_analysis_str: str,
        ai_patch_snippet: str,
    ) -> None:
        """Updates drift_incidents row in Cloudflare D1 matching schema."""
        url = f"{self.base_url}/d1/database/{settings.cf_d1_database_id}/query"

        if incident_id is not None:
            sql = """
                UPDATE drift_incidents
                SET severity = ?,
                    diff_summary = ?,
                    ai_analysis = ?,
                    ai_patch_snippet = ?
                WHERE id = ?;
            """
            params: list[Any] = [
                severity,
                diff_summary_str,
                ai_analysis_str,
                ai_patch_snippet,
                incident_id,
            ]
        else:
            sql = """
                UPDATE drift_incidents
                SET severity = ?,
                    diff_summary = ?,
                    ai_analysis = ?,
                    ai_patch_snippet = ?
                WHERE endpoint_id = ?
                  AND old_schema_hash = ?
                  AND new_schema_hash = ?;
            """
            params = [
                severity,
                diff_summary_str,
                ai_analysis_str,
                ai_patch_snippet,
                endpoint_id,
                old_schema_hash,
                new_schema_hash,
            ]

        resp = await client.post(
            url,
            headers=self.headers,
            json={"sql": sql.strip(), "params": params},
        )
        resp.raise_for_status()