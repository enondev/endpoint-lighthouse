import re
from playwright.sync_api import Page, expect
from mocks import setup_offline_api_mocks


def test_hydrates_endpoints_and_incidents(page: Page, frontend_base_url: str) -> None:
    setup_offline_api_mocks(page)
    page.goto(frontend_base_url)

    expect(page.locator("#endpoints-list").get_by_text("GitHub Zen API")).to_be_visible()
    expect(page.locator("#endpoints-list").get_by_text("PyPI FastAPI Metadata")).to_be_visible()
    expect(page.locator(".incident-title", has_text="PyPI FastAPI Metadata")).to_be_visible()
    expect(page.get_by_text(re.compile(r"scalar string will fail JSON unmarshaling", re.I))).to_be_visible()


def test_processes_live_sse_drift_and_analysis_events(page: Page, frontend_base_url: str) -> None:
    live_sse_events = [
        {
            "event": "drift_detected",
            "data": {
                "id": 202,
                "endpoint_id": 1,
                "endpoint_name": "GitHub Zen API",
                "severity": "BREAKING",
                "diff_summary": "Removed required field: zen_quote",
            },
        },
        {
            "event": "analysis_ready",
            "data": {
                "id": 202,
                "incident_id": 202,
                "endpoint_id": 1,
                "endpoint_name": "GitHub Zen API",
                "severity": "BREAKING",
                "diff_summary": "Removed required field: zen_quote",
                "ai_analysis": "Field zen_quote was replaced by message_text in v2 response.",
                "ai_patch_snippet": 'payload["zen_quote"] = payload.get("message_text", "")',
            },
        },
    ]

    setup_offline_api_mocks(page, sse_events=live_sse_events)
    page.goto(frontend_base_url)

    expect(page.get_by_text(re.compile(r"zen_quote", re.I)).first).to_be_visible(timeout=8000)


def test_opens_migration_patch_modal(page: Page, frontend_base_url: str) -> None:
    setup_offline_api_mocks(page)
    page.goto(frontend_base_url)

    inspect_trigger = (
        page.locator('button, [role="button"], .incident-card, tr')
        .filter(has_text=re.compile(r"PyPI FastAPI Metadata|requires_python|Inspect|View Patch|Diff", re.I))
        .first
    )
    inspect_trigger.click()

    expect(page.get_by_text(re.compile(r"migrate_requires_python", re.I))).to_be_visible()


def test_displays_sanitized_error_without_stack_traces(page: Page, frontend_base_url: str) -> None:
    setup_offline_api_mocks(page, fail_endpoints=True)
    page.goto(frontend_base_url)

    body_text = page.locator("body").inner_text()
    assert not re.search(
        r"goroutine|panic:|Traceback \(most recent call last\)|SQLITE_ERROR",
        body_text,
        re.I,
    )

    error_banner = page.locator('[role="alert"], .error, .toast, .banner, #error-banner').first
    expect(error_banner).to_be_visible()