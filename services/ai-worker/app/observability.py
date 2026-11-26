"""Observability for the AI worker: structured JSON logging, Prometheus metrics,
and OpenTelemetry distributed tracing with W3C context propagation."""

from __future__ import annotations

import json
import logging
import os
import time
import uuid
from typing import Any, Callable, cast

import structlog
from prometheus_client import CONTENT_TYPE_LATEST, Counter, Gauge, Histogram, generate_latest

# ---------------------------------------------------------------------------
# Structured logging (structlog -> JSON)
# ---------------------------------------------------------------------------

_LOGGER_INITIALIZED = False


def init_logging(level: str = "INFO", json_logs: bool = True) -> None:
    """Configure structlog to emit JSON (or key=value) structured logs."""
    global _LOGGER_INITIALIZED
    if _LOGGER_INITIALIZED:
        return
    _LOGGER_INITIALIZED = True

    log_level = getattr(logging, level.upper(), logging.INFO)

    timestamper = structlog.processors.TimeStamper(fmt="iso", utc=True)
    shared_processors: list[Any] = [
        structlog.contextvars.merge_contextvars,
        structlog.processors.add_log_level,
        structlog.processors.StackInfoRenderer(),
        timestamper,
    ]

    if json_logs:
        shared_processors.append(structlog.processors.dict_tracebacks)
        shared_processors.append(structlog.processors.JSONRenderer())
    else:
        shared_processors.append(structlog.dev.ConsoleRenderer(colors=False))

    structlog.configure(
        processors=shared_processors,
        wrapper_class=structlog.make_filtering_bound_logger(log_level),
        context_class=dict,
        logger_factory=structlog.PrintLoggerFactory(),
        cache_logger_on_first_use=False,
    )


def get_logger(name: str) -> structlog.BoundLogger:
    return structlog.get_logger(name)


# ---------------------------------------------------------------------------
# Prometheus metrics
# ---------------------------------------------------------------------------

HTTP_REQUESTS_TOTAL = Counter(
    "endpoint_lighthouse_aiworker_http_requests_total",
    "Total HTTP requests handled by the AI worker.",
    ["method", "path", "status"],
)

HTTP_REQUEST_DURATION = Histogram(
    "endpoint_lighthouse_aiworker_http_request_duration_seconds",
    "Duration of HTTP requests handled by the AI worker.",
    ["method", "path"],
    buckets=(0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10),
)

ANALYZE_REQUESTS_TOTAL = Counter(
    "endpoint_lighthouse_aiworker_analyze_requests_total",
    "Total /analyze requests.",
    ["outcome"],
)

ANALYZE_DURATION = Histogram(
    "endpoint_lighthouse_aiworker_analyze_duration_seconds",
    "Duration of /analyze requests.",
    buckets=(0.1, 0.5, 1, 2.5, 5, 10, 25, 60),
)

KV_SNAPSHOT_FETCHES_TOTAL = Counter(
    "endpoint_lighthouse_aiworker_kv_snapshot_fetches_total",
    "Total Cloudflare KV snapshot fetches.",
    ["outcome"],
)

LLM_CALLS_TOTAL = Counter(
    "endpoint_lighthouse_aiworker_llm_calls_total",
    "Total Cloudflare Workers AI calls.",
    ["outcome"],
)

LLM_CALL_DURATION = Histogram(
    "endpoint_lighthouse_aiworker_llm_call_duration_seconds",
    "Duration of Workers AI calls.",
    buckets=(0.5, 1, 2.5, 5, 10, 25, 60),
)

D1_INCIDENT_UPDATES_TOTAL = Counter(
    "endpoint_lighthouse_aiworker_d1_incident_updates_total",
    "Total D1 incident updates.",
    ["outcome"],
)

CALLBACK_DELIVERIES_TOTAL = Counter(
    "endpoint_lighthouse_aiworker_callback_deliveries_total",
    "Total callback deliveries to the API gateway.",
    ["outcome"],
)


def metrics_payload() -> str:
    return generate_latest().decode("utf-8")


def metrics_content_type() -> str:
    return CONTENT_TYPE_LATEST


# ---------------------------------------------------------------------------
# OpenTelemetry tracing
# ---------------------------------------------------------------------------

_OTEL_INITIALIZED = False


def init_tracing(service_name: str) -> None:
    """Initialize OpenTelemetry with an OTLP HTTP exporter when configured.

    When OTEL_EXPORTER_OTLP_ENDPOINT is unset, tracing is a no-op so the
    service runs without a collector.
    """
    global _OTEL_INITIALIZED
    if _OTEL_INITIALIZED:
        return
    _OTEL_INITIALIZED = True

    endpoint = os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
    if not endpoint:
        return

    try:
        from opentelemetry import trace as otel_trace
        from opentelemetry.exporter.otlp.proto.http.trace_exporter import (
            OTLPSpanExporter,
        )
        from opentelemetry.sdk.resources import Resource
        from opentelemetry.sdk.trace import TracerProvider
        from opentelemetry.sdk.trace.export import BatchSpanProcessor
        from opentelemetry.propagate import set_global_textmap
        from opentelemetry.propagators.composite import CompositePropagator
        from opentelemetry.propagators.tracecontext import TraceContextPropagator
        from opentelemetry.propagators.baggage import BaggagePropagator

        resource_attrs = Resource.create(
            {
                "service.name": service_name,
                "service.namespace": "endpoint-lighthouse",
            }
        )
        provider = TracerProvider(resource=resource_attrs)
        exporter = OTLPSpanExporter(endpoint=endpoint)
        provider.add_span_processor(BatchSpanProcessor(exporter))
        otel_trace.set_tracer_provider(provider)
        set_global_textmap(
            CompositePropagator(
                [TraceContextPropagator(), BaggagePropagator()]
            )
        )
    except Exception:  # pragma: no cover - otel optional
        # Tracing is optional; do not crash the worker if deps are missing.
        pass


# ---------------------------------------------------------------------------
# Correlation / request ID helpers
# ---------------------------------------------------------------------------

REQUEST_ID_HEADER = "X-Request-ID"


def generate_request_id() -> str:
    return uuid.uuid4().hex


def set_request_id(request_id: str | None) -> None:
    import structlog.contextvars

    if request_id:
        structlog.contextvars.bind_contextvars(request_id=request_id)


def clear_request_id() -> None:
    import structlog.contextvars

    structlog.contextvars.clear_contextvars()


# ---------------------------------------------------------------------------
# ASGI middleware (FastAPI / Starlette)
# ---------------------------------------------------------------------------

class ObservabilityMiddleware:
    """Middleware that assigns request IDs, logs requests, and records metrics."""

    def __init__(self, app: Callable, service_name: str = "ai-worker") -> None:
        self.app = app
        self.service_name = service_name

    async def __call__(self, scope: Any, receive: Any, send: Any) -> None:
        if scope["type"] != "http":
            await self.app(scope, receive, send)
            return

        from starlette.requests import Request
        from starlette.responses import Response

        request = Request(scope, receive)
        request_id = request.headers.get(REQUEST_ID_HEADER) or generate_request_id()
        set_request_id(request_id)

        path = request.url.path
        method = request.method
        start = time.perf_counter()

        async def _send(message: Any) -> None:
            if message["type"] == "http.response.start":
                status = str(message.get("status", 200))
                HTTP_REQUESTS_TOTAL.labels(method=method, path=path, status=status).inc()
                message.setdefault("headers", [])
                message["headers"].append(
                    (REQUEST_ID_HEADER.encode(), request_id.encode())
                )
            await send(message)

        try:
            await self.app(scope, receive, _send)
        finally:
            duration = time.perf_counter() - start
            status = "unknown"
            HTTP_REQUEST_DURATION.labels(method=method, path=path).observe(duration)
            clear_request_id()


def metrics_endpoint() -> Any:
    """Return an ASGI-compatible metrics endpoint."""
    from starlette.responses import PlainTextResponse

    async def _metrics(scope: Any, receive: Any, send: Any) -> None:
        body = metrics_payload().encode("utf-8")
        headers = [(b"content-type", metrics_content_type().encode("utf-8"))]
        response = PlainTextResponse(content=body, status_code=200, headers=headers)
        await response(scope, receive, send)

    return _metrics


__all__ = [
    "init_logging",
    "get_logger",
    "init_tracing",
    "generate_request_id",
    "set_request_id",
    "clear_request_id",
    "ObservabilityMiddleware",
    "metrics_endpoint",
    "HTTP_REQUESTS_TOTAL",
    "HTTP_REQUEST_DURATION",
    "ANALYZE_REQUESTS_TOTAL",
    "ANALYZE_DURATION",
    "KV_SNAPSHOT_FETCHES_TOTAL",
    "LLM_CALLS_TOTAL",
    "LLM_CALL_DURATION",
    "D1_INCIDENT_UPDATES_TOTAL",
    "CALLBACK_DELIVERIES_TOTAL",
]
