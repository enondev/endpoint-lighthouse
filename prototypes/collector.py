#!/usr/bin/env python3
"""
Endpoint Lighthouse: Baseline Collector & Benchmark Prototype
- Probes seeded public APIs asynchronously
- Infers canonical JSON Schemas and computes structural SHA-256 hashes
- Persists baselines & telemetry to local SQLite + ./fixtures/
- Optionally syncs directly to Cloudflare D1 & KV via REST API
"""

import argparse
import asyncio
import hashlib
import json
import os
import random
import sqlite3
import time
import tracemalloc
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import httpx
from dotenv import load_dotenv
from genson import SchemaBuilder

load_dotenv()

ROOT_DIR = Path(__file__).resolve().parent.parent
DB_PATH = ROOT_DIR / "data" / "local_endpoint_lighthouse.db"
MIGRATION_PATH = ROOT_DIR / "migrations" / "schema.sql"
FIXTURES_DIR = ROOT_DIR / "fixtures"

CF_ACCOUNT_ID = os.getenv("CF_ACCOUNT_ID", "")
CF_API_TOKEN = os.getenv("CF_API_TOKEN", "")
CF_D1_DATABASE_ID = os.getenv("CF_D1_DATABASE_ID", "")
CF_KV_NAMESPACE_ID = os.getenv("CF_KV_NAMESPACE_ID", "")

def init_local_db() -> sqlite3.Connection:
    DB_PATH.parent.mkdir(parents=True, exist_ok=True)
    FIXTURES_DIR.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(DB_PATH)
    conn.row_factory = sqlite3.Row
    with open(MIGRATION_PATH, "r", encoding="utf-8") as f:
        conn.executescript(f.read())
    conn.commit()
    return conn

def infer_canonical_schema(payload: Any) -> tuple[str, str, int]:
    """Builds a canonical JSON schema and returns (schema_hash, schema_json, field_count)."""
    builder = SchemaBuilder(schema_uri=None)
    builder.add_object(payload)
    schema_dict = builder.to_schema()

    def count_keys(node: Any) -> int:
        if isinstance(node, dict):
            total = len(node.get("properties", {}))
            for v in node.values():
                total += count_keys(v)
            return total
        if isinstance(node, list):
            return sum(count_keys(item) for item in node)
        return 0

    canonical_json = json.dumps(schema_dict, sort_keys=True, separators=(",", ":"))
    schema_hash = hashlib.sha256(canonical_json.encode("utf-8")).hexdigest()
    return schema_hash, canonical_json, count_keys(schema_dict)

async def sync_to_cloudflare(
    client: httpx.AsyncClient,
    kv_key: str,
    raw_payload: str,
    sql_statements: list[tuple[str, list[Any]]],
) -> None:
    """Writes raw JSON snapshot to Cloudflare KV and executes parameterized SQL on D1."""
    if not all([CF_ACCOUNT_ID, CF_API_TOKEN, CF_D1_DATABASE_ID, CF_KV_NAMESPACE_ID]):
        print("  [!] Missing Cloudflare credentials in .env; skipping remote sync.")
        return

    cf_host = "api." + "cloudflare.com"
    acct_id = CF_ACCOUNT_ID.strip()
    headers = {"Authorization": f"Bearer {CF_API_TOKEN.strip()}"}

    # Write raw snapshot to KV
    kv_url = (
        f"https://{cf_host}/client/v4/accounts/{acct_id}"
        f"/storage/kv/namespaces/{CF_KV_NAMESPACE_ID.strip()}/values/{kv_key}"
    )
    kv_resp = await client.put(
        kv_url,
        headers={**headers, "Content-Type": "application/json"},
        content=raw_payload.encode("utf-8"),
    )
    if kv_resp.is_error:
        print(f"  [!] KV Sync HTTP {kv_resp.status_code}: {kv_resp.text}")
        kv_resp.raise_for_status()

    # Execute parameterized SQL statements on D1
    d1_url = (
        f"https://{cf_host}/client/v4/accounts/{acct_id}"
        f"/d1/database/{CF_D1_DATABASE_ID.strip()}/query"
    )
    for sql, params in sql_statements:
        d1_resp = await client.post(
            d1_url,
            headers={**headers, "Content-Type": "application/json"},
            json={"sql": sql, "params": params},
        )
        if d1_resp.is_error:
            print(f"  [!] D1 Sync HTTP {d1_resp.status_code}: {d1_resp.text}")
            d1_resp.raise_for_status()

async def probe_endpoint(
    client: httpx.AsyncClient,
    conn: sqlite3.Connection,
    endpoint: sqlite3.Row,
    sync_cf: bool,
) -> None:
    ep_id = endpoint["id"]
    url = endpoint["url"]
    req_headers = {
        "User-Agent": "EndpointLighthouse-Prober/0.1 (Python) - please get in touch to remove this endpoint from the drift observation (+https://github.com/enondev/endpoint-lighthouse)",
        "Accept": "application/json",
    }
    if endpoint["last_etag"]:
        req_headers["If-None-Match"] = endpoint["last_etag"]

    start = time.perf_counter()
    try:
        resp = await client.request(endpoint["method"], url, headers=req_headers, timeout=15.0)
        latency_ms = round((time.perf_counter() - start) * 1000, 2)
    except Exception as exc:
        latency_ms = round((time.perf_counter() - start) * 1000, 2)
        conn.execute(
            """
            INSERT INTO probe_runs (endpoint_id, status_code, latency_ms, response_size_bytes, error_message)
            VALUES (?, 0, ?, 0, ?)
            """,
            (ep_id, latency_ms, str(exc)),
        )
        conn.commit()
        print(f"[-] {ep_id}: FAILED ({exc})")
        return

    raw_bytes = resp.content
    size_bytes = len(raw_bytes)
    content_type = resp.headers.get("content-type", "")
    new_etag = resp.headers.get("etag")

    if resp.status_code == 304:
        conn.execute(
            """
            INSERT INTO probe_runs (endpoint_id, status_code, latency_ms, response_size_bytes, content_type)
            VALUES (?, 304, ?, 0, ?)
            """,
            (ep_id, latency_ms, content_type),
        )
        conn.commit()
        print(f"[=] {ep_id}: 304 Not Modified ({latency_ms} ms)")
        return
    if 300 <= resp.status_code < 400:
        loc = resp.headers.get("location", "none")
        print(f"[>] {ep_id:20s} | 3xx ({resp.status_code}) | url={url} -> Location={loc}")
        return
    try:
        payload = resp.json()
    except json.JSONDecodeError as exc:
        preview = resp.text[:120].replace("\n", " ")
        conn.execute(
            """
            INSERT INTO probe_runs (endpoint_id, status_code, latency_ms, response_size_bytes, content_type, error_message)
            VALUES (?, ?, ?, ?, ?, ?)
            """,
            (ep_id, resp.status_code, latency_ms, size_bytes, content_type, f"Invalid JSON: {exc} | {preview}"),
        )
        conn.commit()
        print(f"[!] {ep_id}: Non-JSON ({resp.status_code}, {latency_ms} ms) -> {preview}")
        return

    schema_hash, schema_json, field_count = infer_canonical_schema(payload)
    pretty_payload = json.dumps(payload, indent=2, sort_keys=True)
    kv_key = f"snap:{ep_id}:{schema_hash[:16]}"

    current_baseline = conn.execute(
        "SELECT schema_hash FROM schema_baselines WHERE endpoint_id = ? AND is_current = 1",
        (ep_id,),
    ).fetchone()

    drift_detected = 0
    is_new_schema = False

    if current_baseline is None:
        is_new_schema = True
        status_label = "BASELINE CREATED"
    elif current_baseline["schema_hash"] != schema_hash:
        is_new_schema = True
        drift_detected = 1
        status_label = "DRIFT DETECTED"
        conn.execute(
            """
            INSERT INTO drift_incidents (endpoint_id, old_schema_hash, new_schema_hash, severity, diff_summary)
            VALUES (?, ?, ?, 'PENDING', ?)
            """,
            (ep_id, current_baseline["schema_hash"], schema_hash, "Structural schema hash changed."),
        )
    else:
        status_label = "OK (MATCH)"

    if is_new_schema:
        # Save local fixture snapshot
        fixture_file = FIXTURES_DIR / f"{ep_id}_{schema_hash[:12]}.json"
        fixture_file.write_text(pretty_payload, encoding="utf-8")

        conn.execute(
            "UPDATE schema_baselines SET is_current = 0 WHERE endpoint_id = ?",
            (ep_id,),
        )
        conn.execute(
            """
            INSERT OR REPLACE INTO schema_baselines
            (endpoint_id, schema_hash, inferred_schema, kv_snapshot_key, field_count, is_current)
            VALUES (?, ?, ?, ?, ?, 1)
            """,
            (ep_id, schema_hash, schema_json, kv_key, field_count),
        )

    if new_etag:
        conn.execute("UPDATE endpoints SET last_etag = ? WHERE id = ?", (new_etag, ep_id))

    conn.execute(
        """
        INSERT INTO probe_runs
        (endpoint_id, status_code, latency_ms, response_size_bytes, content_type, schema_hash, drift_detected)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        """,
        (ep_id, resp.status_code, latency_ms, size_bytes, content_type, schema_hash, drift_detected),
    )
    conn.commit()

    if sync_cf and is_new_schema:
        cf_sql = [
            ("UPDATE schema_baselines SET is_current = 0 WHERE endpoint_id = ?;", [ep_id]),
            (
                """
                INSERT OR REPLACE INTO schema_baselines
                (endpoint_id, schema_hash, inferred_schema, kv_snapshot_key, field_count, is_current)
                VALUES (?, ?, ?, ?, ?, 1);
                """,
                [ep_id, schema_hash, schema_json, kv_key, field_count],
            ),
            (
                """
                INSERT INTO probe_runs
                (endpoint_id, status_code, latency_ms, response_size_bytes, content_type, schema_hash, drift_detected)
                VALUES (?, ?, ?, ?, ?, ?, ?);
                """,
                [ep_id, resp.status_code, latency_ms, size_bytes, content_type, schema_hash, drift_detected],
            ),
        ]
        await sync_to_cloudflare(client, kv_key, pretty_payload, cf_sql)

    print(
        f"[+] {ep_id:20s} | {status_label:16s} | {resp.status_code} | "
        f"{latency_ms:6.1f} ms | fields={field_count:3d} | hash={schema_hash[:12]}"
    )

def compute_next_run(interval_sec: int, jitter_pct: float = 0.10) -> float:
    """Returns a monotonic timestamp with +/- jitter_pct randomized spread."""
    jitter_factor = random.uniform(1.0 - jitter_pct, 1.0 + jitter_pct)
    return time.monotonic() + (interval_sec * jitter_factor)

async def run_single_pass(conn: sqlite3.Connection, sync_cf: bool) -> None:
    """Runs one polite pass over all active endpoints with small inter-request jitter."""
    endpoints = conn.execute("SELECT * FROM endpoints WHERE is_active = 1").fetchall()
    print(f"Probing {len(endpoints)} active endpoints (sync_cloudflare={sync_cf})...\n")

    async with httpx.AsyncClient(follow_redirects=False) as client:
        for i, ep in enumerate(endpoints):
            await probe_endpoint(client, conn, ep, sync_cf)
            if i < len(endpoints) - 1:
                await asyncio.sleep(random.uniform(0.2, 0.8))

async def run_daemon_loop(conn: sqlite3.Connection, sync_cf: bool) -> None:
    """Continuous scheduler using per-endpoint +/-10% jitter to prevent thundering herds."""
    endpoints = conn.execute("SELECT * FROM endpoints WHERE is_active = 1").fetchall()
    # Stagger initial startup across 0-15 seconds instead of firing all at once
    schedule: dict[str, float] = {
        ep["id"]: time.monotonic() + random.uniform(0, 15.0) for ep in endpoints
    }

    print(f"Starting jittered daemon loop for {len(endpoints)} endpoints (Ctrl+C to stop)...\n")
    async with httpx.AsyncClient(follow_redirects=False) as client:
        while True:
            now = time.monotonic()
            for ep in endpoints:
                ep_id = ep["id"]
                if now >= schedule[ep_id]:
                    await probe_endpoint(client, conn, ep, sync_cf)
                    next_ts = compute_next_run(ep["poll_interval_sec"], jitter_pct=0.10)
                    schedule[ep_id] = next_ts
                    delay = round(next_ts - time.monotonic(), 1)
                    print(f"    -> Next probe for {ep_id} scheduled in {delay}s")
            await asyncio.sleep(1.0)

async def main(sync_cf: bool, daemon: bool) -> None:
    tracemalloc.start()
    wall_start = time.perf_counter()
    conn = init_local_db()

    try:
        if daemon:
            await run_daemon_loop(conn, sync_cf)
        else:
            await run_single_pass(conn, sync_cf)
    except KeyboardInterrupt:
        print("\nShutting down collector gracefully...")
    finally:
        _, peak_bytes = tracemalloc.get_traced_memory()
        tracemalloc.stop()
        total_ms = round((time.perf_counter() - wall_start) * 1000, 1)
        peak_mb = round(peak_bytes / (1024 * 1024), 2)
        print(f"\nElapsed: {total_ms} ms | Python Peak Traced Memory: {peak_mb} MB")
        conn.close()

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Endpoint Lighthouse Baseline Collector")
    parser.add_argument(
        "--sync-cloudflare",
        action="store_true",
        help="Push new baselines & snapshots to Cloudflare D1 and KV",
    )
    parser.add_argument(
        "--daemon",
        action="store_true",
        help="Run continuously using +/-10%% per-endpoint schedule jitter",
    )
    args = parser.parse_args()
    asyncio.run(main(sync_cf=args.sync_cloudflare, daemon=args.daemon))