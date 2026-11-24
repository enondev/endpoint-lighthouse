import json
import logging
from typing import Any

import httpx

from app.config import settings
from app.scoring import ScoringEngine

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
        self.scorer = ScoringEngine.from_file()

    def _build_payload(
        self,
        endpoint_name: str,
        endpoint_url: str,
        diff_str: str,
        strict_json: bool = True
    ) -> dict:
        system_prompt = (
            "You are Endpoint Lighthouse AI, an API contract reliability engineer. "
            "Respond ONLY with valid JSON, no markdown fences, no extra text. "
            f"Total output must fit within {MAX_TOKENS} tokens. Be extremely concise.\n"
            "Required shape:\n"
            '{\n'
            '  "severity": "BREAKING" | "WARNING" | "NON_BREAKING",\n'
            '  "analysis": "One sentence, max 25 words, describing the change and client impact.",\n'
            '  "patch_snippet": "One short plain-English action step (no code blocks, no CLI commands)."\n'
            '}\n'
            "Example:\n"
            '{"severity":"WARNING","analysis":"Field status.indicator changed from string to integer.","patch_snippet":"Wrap status.indicator in String() before display."}'
        )
        body = {
            "messages": [
                {"role": "system", "content": system_prompt},
                {
                    "role": "user",
                    "content": f"Endpoint: {endpoint_name} ({endpoint_url})\n<diff>{diff_str}</diff>",
                },
            ],
            "temperature": 0.1,
            "max_tokens": MAX_TOKENS,
        }
        if strict_json:
            body["response_format"] = {"type": "json_object"}
        return body

    def _log_envelope(self, data: Any, label: str) -> None:
        """Emit structured observability for the Workers AI response envelope."""
        if not isinstance(data, dict):
            logger.warning(
                "Workers AI %s returned non-dict body (type=%s): %r",
                label,
                type(data).__name__,
                str(data)[:200],
            )
            return
        success = data.get("success")
        errors = data.get("errors")
        messages = data.get("messages")
        result = data.get("result")
        result_keys = list(result.keys()) if isinstance(result, dict) else None
        logger.warning(
            "Workers AI %s envelope: success=%s errors=%s messages=%s result_keys=%s body_preview=%r",
            label,
            success,
            errors,
            messages,
            result_keys,
            str(data)[:300],
        )

    def _extract_raw(self, data: Any) -> str:
        if not isinstance(data, dict):
            return ""
        result = data.get("result") or {}
        if not isinstance(result, dict):
            result = {}
        text = result.get("response") or ""
        if not text and isinstance(result.get("choices"), list) and result["choices"]:
            msg = result["choices"][0].get("message") or {}
            text = msg.get("content") or ""
        return str(text).strip()

    def calculate_severity_score(
        self,
        severity: str,
        status: str = "OPEN",
        diff_summary: dict[str, Any] | None = None,
    ) -> int:
        """Calculate a normalized severity score (0-100) using the configured ScoringEngine."""
        return self.scorer.calculate(
            severity=severity,
            status=status,
            diff_summary=diff_summary,
        )

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

        compact_diff = json.dumps(diff_summary, separators=(",", ":"))
        if len(compact_diff) > settings.max_diff_chars:
            compact_diff = compact_diff[:settings.max_diff_chars] + "...[truncated]"

        payload = self._build_payload(endpoint_name, endpoint_url, compact_diff, strict_json=True)
        resp = await client.post(url, headers=self.headers, json=payload)
        resp.raise_for_status()
        data = resp.json()

        self._log_envelope(data, "initial_response")

        if not data.get("success", True):
            errors = data.get("errors") or []
            raise RuntimeError(
                f"Workers AI upstream reported failure (success=False): {errors}"
            )

        raw_text = self._extract_raw(data)

        if not raw_text:
            logger.warning(
                "Workers AI returned empty response after initial call; retrying with shorter diff and strict_json=False"
            )
            payload = self._build_payload(endpoint_name, endpoint_url, compact_diff[:2000], strict_json=False)
            resp = await client.post(url, headers=self.headers, json=payload)
            resp.raise_for_status()
            retry_data = resp.json()
            self._log_envelope(retry_data, "retry_response")

            if not retry_data.get("success", True):
                errors = retry_data.get("errors") or []
                raise RuntimeError(
                    f"Workers AI retry reported failure (success=False): {errors}"
                )

            raw_text = self._extract_raw(retry_data)

        if not raw_text:
            logger.error(
                "Workers AI returned empty text on both attempts; cannot produce analysis. "
                "Check model availability, response_format support, and diff size."
            )

        ai_result = self._parse_llm_json(raw_text, diff_summary=diff_summary)
        ai_result["severity_score"] = self.calculate_severity_score(
            severity=ai_result["severity"],
            status="OPEN",
            diff_summary=diff_summary,
        )
        return ai_result

    def _fallback_patch_snippet(self, diff_summary: dict[str, Any] | None) -> str:
        if not diff_summary:
            return "// Review the schema diff and update client code accordingly."
        keys = " | ".join(str(k) for k in diff_summary.keys()).lower()
        if "removed" in keys:
            return "// Check for references to removed fields and update client code accordingly."
        if "added" in keys:
            return "// Review new fields and update client schemas to include them."
        if "changed" in keys or "type" in keys:
            return "// Update client type definitions to match the new schema types."
        return "// Review the schema diff and update client code accordingly."

    def _parse_llm_json(
        self, raw_text: str, diff_summary: dict[str, Any] | None = None
    ) -> dict[str, Any]:
        """Safely parses LLM JSON output with deterministic fallback."""
        cleaned = raw_text.strip()
        start = cleaned.find("{")
        end = cleaned.rfind("}")
        if start != -1 and end != -1 and end > start:
            cleaned = cleaned[start : end + 1]

        if not cleaned:
            logger.warning("Workers AI returned empty text; applying empty-response fallback")
            return {
                "severity": "WARNING",
                "analysis": "Automated drift analysis completed; LLM returned no usable output.",
                "patch_snippet": self._fallback_patch_snippet(diff_summary),
            }

        try:
            parsed = json.loads(cleaned)
            raw_sev = str(parsed.get("severity", "WARNING")).upper().strip()
            severity = raw_sev if raw_sev in VALID_SEVERITIES else "WARNING"
            patch_snippet = str(parsed.get("patch_snippet", "")).strip()
            if not patch_snippet:
                patch_snippet = self._fallback_patch_snippet(diff_summary)
            return {
                "severity": severity,
                "analysis": str(parsed.get("analysis", "Schema drift detected.")),
                "patch_snippet": patch_snippet,
            }
        except json.JSONDecodeError:
            logger.warning("Workers AI returned non-JSON output (len=%d): %r", len(raw_text), raw_text[:120])
            return {
                "severity": "WARNING",
                "analysis": raw_text[:500] if raw_text else "Automated drift analysis completed.",
                "patch_snippet": self._fallback_patch_snippet(diff_summary),
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
        severity_score: int = 0,
    ) -> None:
        """Updates drift_incidents row in Cloudflare D1 matching schema."""
        url = f"{self.base_url}/d1/database/{settings.cf_d1_database_id}/query"

        if incident_id is not None:
            sql = """
                UPDATE drift_incidents
                SET severity = ?,
                    severity_score = ?,
                    diff_summary = ?,
                    ai_analysis = ?,
                    ai_patch_snippet = ?
                WHERE id = ?;
            """
            params: list[Any] = [
                severity,
                severity_score,
                diff_summary_str,
                ai_analysis_str,
                ai_patch_snippet,
                incident_id,
            ]
        else:
            sql = """
                UPDATE drift_incidents
                SET severity = ?,
                    severity_score = ?,
                    diff_summary = ?,
                    ai_analysis = ?,
                    ai_patch_snippet = ?
                WHERE endpoint_id = ?
                  AND old_schema_hash = ?
                  AND new_schema_hash = ?;
            """
            params = [
                severity,
                severity_score,
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
