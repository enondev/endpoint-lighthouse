import os

# Inject required settings BEFORE importing app.config.settings
os.environ.setdefault("INTERNAL_WEBHOOK_SECRET", "test-secret-token-12345")
os.environ.setdefault("RENDER_API_URL", "https://specwatch.internal")
os.environ.setdefault("CF_ACCOUNT_ID", "test-cf-account-id")
os.environ.setdefault("CF_API_TOKEN", "test-cf-api-token")
os.environ.setdefault("CF_D1_DATABASE_ID", "test-d1-db-uuid")
os.environ.setdefault("CF_KV_NAMESPACE_ID", "test-kv-ns-id")

import pytest
from fastapi.testclient import TestClient

from app.main import app


@pytest.fixture
def client() -> TestClient:
    return TestClient(app)


@pytest.fixture
def valid_headers() -> dict[str, str]:
    return {"X-Internal-Secret": "test-secret-token-12345"}


@pytest.fixture
def sample_baseline_snapshot() -> dict:
    return {
        "endpoint_id": "ep-github-status",
        "schema_hash": "aaaa1111bbbb2222cccc3333dddd4444",
        "payload": {
            "page": {"id": "kctbh9vrtdwd", "name": "GitHub"},
            "status": {"indicator": "none", "description": "All Systems Operational"},
            "components": [
                {"id": "comp-1", "name": "Git Operations", "status": "operational"},
                {"id": "comp-2", "name": "API Requests", "status": "operational"},
            ],
        },
    }


@pytest.fixture
def sample_drifted_snapshot() -> dict:
    return {
        "endpoint_id": "ep-github-status",
        "schema_hash": "ffff9999eeee8888dddd7777cccc6666",
        "payload": {
            "page": {"id": "kctbh9vrtdwd", "name": "GitHub"},
            # Breaking: 'description' removed, 'indicator' changed to int, 'incident_count' added
            "status": {"indicator": 1, "incident_count": 2},
            "components": [
                {"id": "comp-1", "name": "Git Operations", "status": "degraded_performance"},
            ],
        },
    }
