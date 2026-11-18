import json
from typing import Any

from deepdiff import DeepDiff


def compute_semantic_diff(
    baseline_snapshot: Any,
    current_snapshot: Any,
) -> dict[str, Any]:
    old_payload = baseline_snapshot.get("payload", baseline_snapshot) if isinstance(baseline_snapshot, dict) else baseline_snapshot
    new_payload = current_snapshot.get("payload", current_snapshot) if isinstance(current_snapshot, dict) else current_snapshot

    diff = DeepDiff(
        old_payload,
        new_payload,
        ignore_order=True,
        report_repetition=False,
        verbose_level=2,
    )

    # Convert DeepDiff object into pure JSON-serializable primitives
    raw_dict: dict[str, Any] = json.loads(diff.to_json())

    # Summarize high-signal contract categories for Workers AI
    summary = {
        "fields_removed": raw_dict.get("dictionary_item_removed", []),
        "fields_added": raw_dict.get("dictionary_item_added", []),
        "type_changes": raw_dict.get("type_changes", {}),
        "values_changed": _truncate_dict(raw_dict.get("values_changed", {}), max_items=25),
        "iterable_items_removed": _truncate_list(raw_dict.get("iterable_item_removed", []), max_items=15),
        "iterable_items_added": _truncate_list(raw_dict.get("iterable_item_added", []), max_items=15),
    }

    # Prune empty categories to save LLM context tokens
    return {k: v for k, v in summary.items() if v}


def _truncate_dict(data: dict[str, Any], max_items: int) -> dict[str, Any]:
    if len(data) <= max_items:
        return data
    items = list(data.items())[:max_items]
    truncated = dict(items)
    truncated["_truncated_count"] = len(data) - max_items
    return truncated


def _truncate_list(data: list[Any] | dict[str, Any], max_items: int) -> Any:
    if isinstance(data, dict):
        return _truncate_dict(data, max_items)
    if len(data) <= max_items:
        return data
    return data[:max_items] + [f"... ({len(data) - max_items} more items)"]