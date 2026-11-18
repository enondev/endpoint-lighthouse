#!/usr/bin/env python3
"""
SpecWatch Cross-Platform End-to-End Drift Simulator
===================================================
Validates drift detection, Redis SETNX cooldowns, AI patch generation,
and SSE event delivery across Windows, macOS, and Linux with zero cloud
quota usage by default.
"""

import argparse
import hashlib
import json
import os
import socket
import sys
import threading
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Dict, List, Optional


@dataclass
class SandboxState:
    """In-memory state for zero-quota local simulation."""
    redis_locks: Dict[str, float] = field(default_factory=dict)
    d1_incidents: List[Dict[str, Any]] = field(default_factory=list)
    kv_snapshots: Dict[str, str] = field(default_factory=dict)
    sse_events: List[Dict[str, Any]] = field(default_factory=list)
    sse_condition: threading.Condition = field(default_factory=threading.Condition)
    internal_secret: str = "local-dev-secret"
    worker_url: str = ""

    def setnx(self, key: str, ttl_seconds: int = 300) -> bool:
        now = time.time()
        exp = self.redis_locks.get(key)
        if exp and exp > now:
            return False
        self.redis_locks[key] = now + ttl_seconds
        return True

    def publish_sse(self, event_type: str, payload: Dict[str, Any]) -> None:
        with self.sse_condition:
            self.sse_events.append({"event": event_type, "data": payload})
            self.sse_condition.notify_all()


def find_free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return int(s.getsockname()[1])
def make_sandbox_handler(state: SandboxState):
    class SpecWatchSandboxHandler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, format: str, *args: Any) -> None:
            pass

        def _send_json(self, code: int, body: Dict[str, Any]) -> None:
            raw = json.dumps(body).encode("utf-8")
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def _read_json(self) -> Dict[str, Any]:
            length = int(self.headers.get("Content-Length", "0"))
            if length == 0:
                return {}
            return json.loads(self.rfile.read(length).decode("utf-8"))

        def do_GET(self) -> None:
            if self.path.startswith("/api/v1/stream"):
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-cache")
                self.send_header("Connection", "keep-alive")
                self.end_headers()

                sent_idx = 0
                deadline = time.time() + 10.0
                try:
                    while time.time() < deadline:
                        with state.sse_condition:
                            while sent_idx >= len(state.sse_events):
                                remaining = deadline - time.time()
                                if remaining <= 0:
                                    break
                                state.sse_condition.wait(timeout=min(0.25, remaining))
                            batch = state.sse_events[sent_idx:]
                            sent_idx = len(state.sse_events)

                        for item in batch:
                            msg = (
                                f"event: {item['event']}\n"
                                f"data: {json.dumps(item['data'])}\n\n"
                            ).encode("utf-8")
                            self.wfile.write(msg)
                            self.wfile.flush()
                            if item["event"] == "analysis_ready":
                                return
                except (BrokenPipeError, ConnectionResetError):
                    return
                return

            if self.path == "/health":
                self._send_json(200, {"status": "ok", "mode": "zero-quota-sandbox"})
                return

            self._send_json(404, {"error": "not_found"})

        def do_POST(self) -> None:
            if self.path == "/internal/drift":
                secret = self.headers.get("X-Internal-Secret", "")
                if secret != state.internal_secret:
                    self._send_json(401, {"error": "unauthorized"})
                    return

                payload = self._read_json()
                endpoint_id = payload.get("endpoint_id", 999)
                lock_key = f"drift:cooldown:{endpoint_id}"

                if not state.setnx(lock_key, ttl_seconds=300):
                    self._send_json(202, {"status": "suppressed_by_redis_setnx"})
                    return

                incident_id = len(state.d1_incidents) + 1
                incident = {
                    "id": incident_id,
                    "endpoint_id": endpoint_id,
                    "severity": payload.get("severity", "BREAKING"),
                    "diff_summary": payload.get("diff_summary", "Field type changed"),
                }
                state.d1_incidents.append(incident)
                state.publish_sse("drift_detected", incident)

                worker_endpoint = f"{state.worker_url}/analyze"
                req = urllib.request.Request(
                    worker_endpoint,
                    data=json.dumps({**payload, "incident_id": incident_id}).encode("utf-8"),
                    headers={
                        "Content-Type": "application/json",
                        "X-Internal-Secret": state.internal_secret,
                    },
                    method="POST",
                )
                try:
                    with urllib.request.urlopen(req, timeout=10) as resp:
                        analysis = json.loads(resp.read().decode("utf-8"))
                except Exception as exc:
                    self._send_json(502, {"error": f"worker_failed: {exc}"})
                    return

                local_port = self.server.server_address[1]
                cb_req = urllib.request.Request(
                    f"http://127.0.0.1:{local_port}/internal/analysis-completed",
                    data=json.dumps(analysis).encode("utf-8"),
                    headers={
                        "Content-Type": "application/json",
                        "X-Internal-Secret": state.internal_secret,
                    },
                    method="POST",
                )
                with urllib.request.urlopen(cb_req, timeout=5):
                    pass

                self._send_json(200, {"status": "processed", "incident_id": incident_id})
                return

            if self.path == "/analyze":
                payload = self._read_json()
                incident_id = payload.get("incident_id", 1)
                endpoint_id = payload.get("endpoint_id", 999)
                new_schema = payload.get("new_schema", {})
                schema_hash = hashlib.sha256(
                    json.dumps(new_schema, sort_keys=True).encode("utf-8")
                ).hexdigest()[:16]

                kv_key = f"snap:{endpoint_id}:{schema_hash}"
                state.kv_snapshots[kv_key] = json.dumps(new_schema)

                result = {
                    "incident_id": incident_id,
                    "endpoint_id": endpoint_id,
                    "kv_key": kv_key,
                    "ai_analysis": (
                        "Breaking change detected: 'status_code' shifted from integer to string "
                        "and 'deprecated_flag' was removed."
                    ),
                    "ai_patch_snippet": (
                        "# Auto-generated migration adapter\n"
                        "def normalize_payload(data: dict) -> dict:\n"
                        "    data['status_code'] = int(data.get('status_code', 200))\n"
                        "    data.setdefault('deprecated_flag', False)\n"
                        "    return data"
                    ),
                }
                self._send_json(200, result)
                return

            if self.path == "/internal/analysis-completed":
                secret = self.headers.get("X-Internal-Secret", "")
                if secret != state.internal_secret:
                    self._send_json(401, {"error": "unauthorized"})
                    return

                payload = self._read_json()
                for inc in state.d1_incidents:
                    if inc["id"] == payload.get("incident_id"):
                        inc["ai_analysis"] = payload.get("ai_analysis")
                        inc["ai_patch_snippet"] = payload.get("ai_patch_snippet")
                state.publish_sse("analysis_ready", payload)
                self._send_json(200, {"status": "acknowledged"})
                return

            self._send_json(404, {"error": "not_found"})

    return SpecWatchSandboxHandler
def build_synthetic_drift_payload(endpoint_id: int) -> Dict[str, Any]:
    old_schema = {
        "type": "object",
        "properties": {
            "id": {"type": "integer"},
            "status_code": {"type": "integer"},
            "deprecated_flag": {"type": "boolean"},
        },
        "required": ["id", "status_code"],
    }
    new_schema = {
        "type": "object",
        "properties": {
            "id": {"type": "integer"},
            "status_code": {"type": "string"},
            "region": {"type": "string"},
        },
        "required": ["id", "status_code", "region"],
    }
    return {
        "endpoint_id": endpoint_id,
        "endpoint_name": "canary-synthetic-probe",
        "severity": "BREAKING",
        "diff_summary": "Type changed: root.properties.status_code.type (integer -> string); Removed: deprecated_flag",
        "old_schema": old_schema,
        "new_schema": new_schema,
    }


def listen_sse(sse_url: str, received: List[Dict[str, Any]], ready_event: threading.Event, stop_event: threading.Event) -> None:
    req = urllib.request.Request(sse_url, headers={"Accept": "text/event-stream"})
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            ready_event.set()
            current_event = "message"
            for raw_line in resp:
                if stop_event.is_set():
                    break
                line = raw_line.decode("utf-8").strip()
                if not line:
                    continue
                if line.startswith("event:"):
                    current_event = line.split(":", 1)[1].strip()
                elif line.startswith("data:"):
                    data_str = line.split(":", 1)[1].strip()
                    try:
                        parsed = json.loads(data_str)
                    except json.JSONDecodeError:
                        parsed = {"raw": data_str}
                    received.append({"event": current_event, "data": parsed})
                    if current_event == "analysis_ready":
                        break
    except Exception as exc:
        received.append({"event": "error", "data": {"message": str(exc)}})
        ready_event.set()


def cleanup_cloudflare_live(endpoint_id: int) -> None:
    """Purges synthetic canary rows from Cloudflare D1 if credentials are configured."""
    account_id = os.getenv("CF_ACCOUNT_ID")
    d1_db_id = os.getenv("CF_D1_DATABASE_ID")
    api_token = os.getenv("CF_API_TOKEN")

    if not all([account_id, d1_db_id, api_token]):
        print("[cleanup] Skipping D1 API cleanup (CF_ACCOUNT_ID / CF_D1_DATABASE_ID / CF_API_TOKEN not set).")
        return

    url = f"https://cloudflare.com{account_id}/d1/database/{d1_db_id}/query"
    sql_payload = json.dumps({
        "sql": "DELETE FROM drift_incidents WHERE endpoint_id = ?;",
        "params": [endpoint_id],
    }).encode("utf-8")

    req = urllib.request.Request(
        url,
        data=sql_payload,
        headers={
            "Authorization": f"Bearer {api_token}",
            "Content-Type": "application/json",
        },
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            if resp.status == 200:
                print(f"[cleanup] Purged synthetic D1 incidents for canary endpoint_id={endpoint_id}.")
    except Exception as exc:
        print(f"[cleanup] Warning: D1 cleanup failed: {exc}")


def main() -> int:
    parser = argparse.ArgumentParser(description="SpecWatch Cross-Platform Drift Simulator")
    parser.add_argument("--live", action="store_true", help="Target live production deployment instead of local sandbox.")
    parser.add_argument("--confirm-quota", action="store_true", help="Required with --live to acknowledge free-tier quota usage.")
    parser.add_argument("--gateway-url", default=os.getenv("SPECWATCH_GATEWAY_URL", ""), help="Custom Render/local Go gateway URL.")
    parser.add_argument("--worker-url", default=os.getenv("SPECWATCH_WORKER_URL", ""), help="Custom SnapDeploy/local AI worker URL.")
    parser.add_argument("--secret", default=os.getenv("INTERNAL_SECRET", "local-dev-secret"), help="X-Internal-Secret header value.")
    parser.add_argument("--endpoint-id", type=int, default=999, help="Canary endpoint ID (default: 999).")
    parser.add_argument("--skip-cleanup", action="store_true", help="Skip automatic D1 cleanup in --live mode.")
    args = parser.parse_args()

    if args.live and not args.confirm_quota:
        print("Refusing to run --live without --confirm-quota to protect your daily free-tier limits.")
        print("Run without --live for zero-quota local simulation, or pass: python scripts/simulate_drift.py --live --confirm-quota")
        return 1

    server: Optional[ThreadingHTTPServer] = None
    state = SandboxState(internal_secret=args.secret)

    if not args.live:
        port = find_free_port()
        base_url = f"http://127.0.0.1:{port}"
        state.worker_url = args.worker_url.rstrip("/") if args.worker_url else base_url
        gateway_url = args.gateway_url.rstrip("/") if args.gateway_url else base_url

        server = ThreadingHTTPServer(("127.0.0.1", port), make_sandbox_handler(state))
        server_thread = threading.Thread(target=server.serve_forever, daemon=True)
        server_thread.start()
        print(f"[mode] Zero-Quota Local Sandbox running on {base_url} (0 cloud quota burned)")
    else:
        gateway_url = args.gateway_url.rstrip("/")
        if not gateway_url:
            print("Error: --gateway-url or SPECWATCH_GATEWAY_URL is required when using --live.")
            return 1
        print(f"[mode] LIVE CANARY against {gateway_url} (endpoint_id={args.endpoint_id})")

    sse_events: List[Dict[str, Any]] = []
    sse_ready = threading.Event()
    sse_stop = threading.Event()

    sse_thread = threading.Thread(
        target=listen_sse,
        args=(f"{gateway_url}/api/v1/stream", sse_events, sse_ready, sse_stop),
        daemon=True,
    )
    sse_thread.start()
    sse_ready.wait(timeout=3.0)

    payload = build_synthetic_drift_payload(args.endpoint_id)
    print(f"[step 1] Dispatching synthetic drift event to {gateway_url}/internal/drift ...")

    drift_req = urllib.request.Request(
        f"{gateway_url}/internal/drift",
        data=json.dumps(payload).encode("utf-8"),
        headers={
            "Content-Type": "application/json",
            "X-Internal-Secret": args.secret,
        },
        method="POST",
    )

    try:
        with urllib.request.urlopen(drift_req, timeout=15) as resp:
            resp_body = json.loads(resp.read().decode("utf-8"))
            print(f"[step 1] Gateway accepted drift ({resp.status}): {resp_body}")
    except urllib.error.HTTPError as err:
        print(f"[error] Gateway rejected drift ({err.code}): {err.read().decode('utf-8', errors='replace')}")
        sse_stop.set()
        if server:
            server.shutdown()
        return 1

    if not args.live and not args.gateway_url:
        dup_req = urllib.request.Request(
            f"{gateway_url}/internal/drift",
            data=json.dumps(payload).encode("utf-8"),
            headers={"Content-Type": "application/json", "X-Internal-Secret": args.secret},
            method="POST",
        )
        with urllib.request.urlopen(dup_req, timeout=5) as dup_resp:
            dup_body = json.loads(dup_resp.read().decode("utf-8"))
            assert dup_body.get("status") == "suppressed_by_redis_setnx", f"Expected SETNX suppression, got {dup_body}"
            print("[step 2] Verified Redis SETNX cooldown gate suppressed duplicate alert.")

    print("[step 3] Waiting for SSE events (drift_detected -> analysis_ready)...")
    sse_thread.join(timeout=12.0)
    sse_stop.set()

    event_names = [e["event"] for e in sse_events]
    print(f"[step 3] Received SSE sequence: {event_names}")

    if "drift_detected" not in event_names or "analysis_ready" not in event_names:
        print(f"[fail] Missing expected SSE events. Captured: {sse_events}")
        if server:
            server.shutdown()
        return 1

    analysis_event = next(e["data"] for e in sse_events if e["event"] == "analysis_ready")
    print("[pass] End-to-end drift simulation succeeded!")
    print(f"       AI Patch Snippet Preview:\n{analysis_event.get('ai_patch_snippet', '').strip()}")

    if args.live and not args.skip_cleanup:
        cleanup_cloudflare_live(args.endpoint_id)

    if server:
        server.shutdown()

    return 0


if __name__ == "__main__":
    sys.exit(main())