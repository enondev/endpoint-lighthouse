#!/usr/bin/env python3
"""
Endpoint Lighthouse: Structural Schema Differ & AI Migration Analyzer Prototype
"""

import argparse
import asyncio
import copy
import hashlib
import json
import os
import sqlite3
import time
import tracemalloc
from pathlib import Path
from typing import Any

import httpx
from deepdiff import DeepDiff
from dotenv import load_dotenv
from genson import SchemaBuilder

load_dotenv()

ROOT_DIR = Path(__file__).resolve().parent.parent
DB_PATH = ROOT_DIR / "data" / "local_endpoint_lighthouse.db"
FIXTURES_DIR = ROOT_DIR / "fixtures"
# 512 for detailed changes with explainations (~14 neurons) or 1024 for comprehensive report (~28 neurons)
MAX_TOKENS = 256 # (~7 neurons)

CF_ACCOUNT_ID = os.getenv("CF_ACCOUNT_ID", "")
CF_API_TOKEN = os.getenv("CF_API_TOKEN", "")
CF_AI_MODEL = os.getenv("CF_AI_MODEL", "@cf/google/gemma-4-26b-a4b-it")
CF_D1_DATABASE_ID = os.getenv("CF_D1_DATABASE_ID", "")

async def sync_incident_to_d1(
    endpoint_id: str,
    old_hash: str,
    new_hash: str,
    severity: str,
    diff_summary_json: str,
    ai_analysis: str,
    ai_patch: str,
) -> None:
    """Pushes the recorded drift incident and Workers AI output to Cloudflare D1."""
    if not all([CF_ACCOUNT_ID, CF_API_TOKEN, CF_D1_DATABASE_ID]):
        print("  [!] Missing CF_D1_DATABASE_ID / credentials; skipping remote D1 sync.")
        return

    d1_url = (
        f"https://cloudflare.com{CF_ACCOUNT_ID}"
        f"/d1/database/{CF_D1_DATABASE_ID}/query"
    )
    headers = {
        "Authorization": f"Bearer {CF_API_TOKEN}",
        "Content-Type": "application/json",
    }
    sql = """
        INSERT INTO drift_incidents
        (endpoint_id, old_schema_hash, new_schema_hash, severity, diff_summary, ai_analysis, ai_patch_snippet, status)
        VALUES (?, ?, ?, ?, ?, ?, ?, 'OPEN');
    """
    params = [
        endpoint_id,
        old_hash,
        new_hash,
        severity,
        diff_summary_json,
        ai_analysis,
        ai_patch,
    ]

    async with httpx.AsyncClient(timeout=15.0) as client:
        resp = await client.post(d1_url, headers=headers, json={"sql": sql, "params": params})
        resp.raise_for_status()
    print("    [+] Synced drift incident & AI patch to remote Cloudflare D1.")

def infer_canonical_schema(payload: Any) -> tuple[str, dict[str, Any]]:
    """Infers a canonical JSON Schema and returns (sha256_hash, schema_dict)."""
    builder = SchemaBuilder(schema_uri=None)
    builder.add_object(payload)
    schema_dict = builder.to_schema()
    canonical_json = json.dumps(schema_dict, sort_keys=True, separators=(",", ":"))
    schema_hash = hashlib.sha256(canonical_json.encode("utf-8")).hexdigest()
    return schema_hash, schema_dict

def simulate_breaking_drift(raw_payload: Any) -> Any:
    """Creates a realistic upstream breaking mutation on a captured fixture."""
    mutated = copy.deepcopy(raw_payload)

    if isinstance(mutated, dict):
        keys = list(mutated.keys())
        if keys:
            first_key = keys[0]
            mutated[f"{first_key}_v2"] = mutated.pop(first_key)

        for k, v in list(mutated.items()):
            if isinstance(v, dict) and v:
                sub_key = next(iter(v.keys()))
                sub_val = v[sub_key]
                if isinstance(sub_val, (int, float)):
                    v[sub_key] = str(sub_val)
                elif isinstance(sub_val, str):
                    v[sub_key] = {"raw_value": sub_val, "deprecated": True}
                break
            elif isinstance(v, (int, float)):
                mutated[k] = str(v)
                break

    return mutated

def classify_deterministic_diff(
    old_schema: dict[str, Any], new_schema: dict[str, Any]
) -> tuple[str, dict[str, Any]]:
    """Runs DeepDiff on canonical JSON Schemas and assigns a severity level."""
    diff = DeepDiff(old_schema, new_schema, ignore_order=True, view="tree")

    removed_items = [
        str(item.path(output_format="js"))
        for item in diff.get("dictionary_item_removed", [])
    ]
    added_items = [
        str(item.path(output_format="js"))
        for item in diff.get("dictionary_item_added", [])
    ]
    type_changes = [
        {
            "path": str(item.path(output_format="js")),
            "old_type": item.t1,
            "new_type": item.t2,
        }
        for item in diff.get("values_changed", [])
    ] + [
        {
            "path": str(item.path(output_format="js")),
            "old_type": str(type(item.t1).__name__),
            "new_type": str(type(item.t2).__name__),
        }
        for item in diff.get("type_changes", [])
    ]

    summary = {
        "removed_paths": removed_items,
        "added_paths": added_items,
        "type_mutations": type_changes,
    }

    if removed_items or type_changes:
        severity = "BREAKING"
    elif added_items:
        severity = "NON_BREAKING"
    else:
        severity = "WARNING"

    return severity, summary

async def analyze_with_workers_ai(
    endpoint_id: str,
    severity: str,
    diff_summary: dict[str, Any],
) -> tuple[str, str]:
    """Calls Cloudflare Workers AI to generate impact summary and client patch."""
    if not CF_ACCOUNT_ID or not CF_API_TOKEN:
        return (
            "AI analysis skipped (CF_ACCOUNT_ID / CF_API_TOKEN not set in .env).",
            "// Add CF_ACCOUNT_ID and CF_API_TOKEN to generate Go/Python patches.",
        )

    # Cap large diffs (e.g., PyPI's 7,800+ keys) so prompts stay fast and within token limits
    compact_diff = {
        "removed_paths": diff_summary.get("removed_paths", [])[:20],
        "added_paths": diff_summary.get("added_paths", [])[:20],
        "type_mutations": diff_summary.get("type_mutations", [])[:20],
    }

    prompt = (
        f"You are a senior backend integration engineer. Upstream API '{endpoint_id}' "
        f"had a {severity} schema drift:\n{json.dumps(compact_diff, indent=2)}\n\n"
        "Respond strictly in valid JSON with two keys:\n"
        '1. "impact_summary": Crisp 2-sentence explanation of why this breaks clients.\n'
        '2. "patch_snippet": Short Go or Python defensive parsing snippet handling both shapes.'
    )

    cf_host = "api." + "cloudflare.com"
    url = f"https://{cf_host}/client/v4/accounts/{CF_ACCOUNT_ID.strip()}/ai/run/{CF_AI_MODEL.strip()}"
    headers = {
        "Authorization": f"Bearer {CF_API_TOKEN.strip()}",
        "Content-Type": "application/json",
    }
    body = {
        "messages": [
            {"role": "system", "content": f"Output concise, valid JSON only. No markdown fences. CONSTRAINT: fit response into {MAX_TOKENS}"},
            {"role": "user", "content": prompt},
        ],
        "max_tokens": MAX_TOKENS,
        "temperature": 0.2,
    }

    try:
        async with httpx.AsyncClient(timeout=45.0) as client:
            resp = await client.post(url, headers=headers, json=body)
            if resp.is_error:
                print(f"    [!] Cloudflare AI HTTP {resp.status_code}: {resp.text}")
                resp.raise_for_status()
            data = resp.json()
    except httpx.TimeoutException:
        print("    [!] Cloudflare Workers AI timed out after 45s.")
        return "AI analysis timed out.", ""

    result_obj = data.get("result", {})
    raw_text = ""

    if isinstance(result_obj, dict):
        if result_obj.get("response"):
            raw_text = str(result_obj["response"]).strip()
        elif isinstance(result_obj.get("choices"), list) and result_obj["choices"]:
            first_choice = next(iter(result_obj["choices"]), {})
            msg = first_choice.get("message", {}) if isinstance(first_choice, dict) else {}
            raw_text = str(msg.get("content") or msg.get("reasoning_content") or "").strip()

    if not raw_text:
        print(f"    [!] Debug raw CF AI response: {json.dumps(data)[:400]}")
        return "Empty response from Workers AI.", ""

    if raw_text.startswith("```"):
        raw_text = raw_text.strip("`").removeprefix("json").strip()

    try:
        parsed = json.loads(raw_text)
        return parsed.get("impact_summary", raw_text), parsed.get("patch_snippet", "")
    except json.JSONDecodeError:
        return raw_text, ""

async def run_diff(
    endpoint_id: str, candidate_path: Path | None, simulate: bool, use_ai: bool
) -> None:
    tracemalloc.start()
    wall_start = time.perf_counter()

    if not DB_PATH.exists():
        raise FileNotFoundError(f"Database not found at {DB_PATH}. Run collector.py first.")

    conn = sqlite3.connect(DB_PATH)
    conn.row_factory = sqlite3.Row

    baseline = conn.execute(
        "SELECT * FROM schema_baselines WHERE endpoint_id = ? AND is_current = 1",
        (endpoint_id,),
    ).fetchone()
    if not baseline:
        raise ValueError(f"No active baseline found for '{endpoint_id}'.")

    old_hash = baseline["schema_hash"]
    old_schema = json.loads(baseline["inferred_schema"])

    if candidate_path:
        candidate_payload = json.loads(candidate_path.read_text(encoding="utf-8"))
    elif simulate:
        fixtures = sorted(FIXTURES_DIR.glob(f"{endpoint_id}_*.json"))
        if not fixtures:
            raise FileNotFoundError(f"No fixture found for {endpoint_id} in {FIXTURES_DIR}")
        original_payload = json.loads(fixtures[-1].read_text(encoding="utf-8"))
        candidate_payload = simulate_breaking_drift(original_payload)
    else:
        raise ValueError("Specify either --simulate or --candidate <path_to_json>.")

    new_hash, new_schema = infer_canonical_schema(candidate_payload)
    if old_hash == new_hash:
        print(f"[=] {endpoint_id}: No structural drift detected (hash={old_hash[:12]}).")
        conn.close()
        return

    severity, diff_summary = classify_deterministic_diff(old_schema, new_schema)

    ai_analysis = ""
    ai_patch = ""
    if use_ai:
        print(f"[*] Calling Cloudflare Workers AI ({CF_AI_MODEL})...")
        ai_analysis, ai_patch = await analyze_with_workers_ai(
            endpoint_id, severity, diff_summary
        )

    conn.execute(
        """
        INSERT INTO drift_incidents
        (endpoint_id, old_schema_hash, new_schema_hash, severity, diff_summary, ai_analysis, ai_patch_snippet, status)
        VALUES (?, ?, ?, ?, ?, ?, ?, 'OPEN')
        """,
        (
            endpoint_id,
            old_hash,
            new_hash,
            severity,
            json.dumps(diff_summary),
            ai_analysis,
            ai_patch,
        ),
    )
    conn.commit()
    conn.close()

    _, peak_bytes = tracemalloc.get_traced_memory()
    tracemalloc.stop()
    elapsed_ms = round((time.perf_counter() - wall_start) * 1000, 1)
    peak_mb = round(peak_bytes / (1024 * 1024), 2)

    print(f"\n[!] DRIFT INCIDENT RECORDED: {endpoint_id}")
    print(f"    Severity : {severity}")
    print(f"    Old Hash : {old_hash[:16]} -> New Hash: {new_hash[:16]}")
    print(f"    Diff     : {json.dumps(diff_summary, indent=2)}")
    if use_ai:
        print(f"\n    AI Impact Summary:\n    {ai_analysis}")
        if ai_patch:
            print(f"\n    Suggested Client Patch:\n{ai_patch}")
    print(f"\nCompleted in {elapsed_ms} ms | Python Peak Traced Memory: {peak_mb} MB")

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Endpoint Lighthouse Schema Differ")
    parser.add_argument("--endpoint", default="github-status", help="Endpoint ID")
    parser.add_argument("--simulate", action="store_true", help="Simulate breaking drift")
    parser.add_argument("--candidate", type=Path, help="Path to candidate JSON file")
    parser.add_argument("--use-ai", action="store_true", help="Use Cloudflare Workers AI")
    args = parser.parse_args()

    asyncio.run(
        run_diff(
            endpoint_id=args.endpoint,
            candidate_path=args.candidate,
            simulate=args.simulate,
            use_ai=args.use_ai,
        )
    )