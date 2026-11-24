import json

import httpx
import pytest
import respx

from app.cloudflare import CloudflareClient
from app.config import settings


def test_parse_llm_json_clean_valid_payload() -> None:
    cf = CloudflareClient()
    raw = json.dumps(
        {
            "severity": "BREAKING",
            "analysis": "Field 'description' was removed from status object.",
            "patch_snippet": "const desc = data.status?.description ?? 'Unknown';",
        }
    )

    result = cf._parse_llm_json(raw)
    assert result["severity"] == "BREAKING"
    assert "description" in result["analysis"]
    assert "data.status" in result["patch_snippet"]


def test_parse_llm_json_strips_markdown_fences_and_normalizes_severity() -> None:
    cf = CloudflareClient()
    raw = """```json
    {
      "severity": "critical_error",
      "analysis": "Type changed from str to int.",
      "patch_snippet": "String(data.status.indicator)"
    }
    ```"""

    result = cf._parse_llm_json(raw)
    assert result["severity"] == "WARNING"  # Unknown severity safely coerces to WARNING
    assert result["analysis"] == "Type changed from str to int."


def test_parse_llm_json_handles_non_json_fallback() -> None:
    cf = CloudflareClient()
    result = cf._parse_llm_json("Sorry, I cannot parse this schema right now.")

    assert result["severity"] == "WARNING"
    assert "Sorry, I cannot parse" in result["analysis"]
    assert "Review the schema diff" in result["patch_snippet"]


@respx.mock
@pytest.mark.asyncio
async def test_update_d1_incident_uses_hash_fallback_when_incident_id_none() -> None:
    cf = CloudflareClient()
    cf_base = f"https://api.cloudflare.com/client/v4/accounts/{settings.cf_account_id}"
    d1_url = f"{cf_base}/d1/database/{settings.cf_d1_database_id}/query"
    route = respx.post(d1_url).mock(return_value=httpx.Response(200, json={"success": True}))

    async with httpx.AsyncClient() as client:
        await cf.update_d1_incident(
            client=client,
            incident_id=None,
            endpoint_id="ep-1",
            old_schema_hash="oldhash1234567890",
            new_schema_hash="newhash1234567890",
            severity="BREAKING",
            diff_summary_str='{"fields_removed":["root[\'x\']"]}',
            ai_analysis_str="Removed x",
            ai_patch_snippet="// patch",
        )

    assert route.called
    sent_body = json.loads(route.calls[0].request.content)
    assert "WHERE endpoint_id = ?" in sent_body["sql"]
    assert sent_body["params"] == [
        "BREAKING",
        '{"fields_removed":["root[\'x\']"]}',
        "Removed x",
        "// patch",
        "ep-1",
        "oldhash1234567890",
        "newhash1234567890",
    ]
