from app.differ import compute_semantic_diff


def test_compute_semantic_diff_detects_breaking_and_additive_changes(
    sample_baseline_snapshot: dict,
    sample_drifted_snapshot: dict,
) -> None:
    diff = compute_semantic_diff(sample_baseline_snapshot, sample_drifted_snapshot)

    assert "fields_removed" in diff
    assert any("description" in path for path in diff["fields_removed"])

    assert "fields_added" in diff
    assert any("incident_count" in path for path in diff["fields_added"])

    assert "type_changes" in diff
    assert "root['status']['indicator']" in diff["type_changes"]


def test_compute_semantic_diff_identical_payloads_returns_empty() -> None:
    snap_a = {"payload": {"id": 1, "tags": ["a", "b"]}}
    snap_b = {"payload": {"tags": ["b", "a"], "id": 1}}

    diff = compute_semantic_diff(snap_a, snap_b)
    assert diff == {}


def test_compute_semantic_diff_handles_root_json_arrays() -> None:
    old_array = [{"id": 1, "price": "10.00"}]
    new_array = [{"id": 1, "price": 10.0, "currency": "USD"}]

    diff = compute_semantic_diff(old_array, new_array)
    assert "type_changes" in diff
    assert "fields_added" in diff


def test_compute_semantic_diff_truncates_large_value_changes() -> None:
    old_snap = {"payload": {f"key_{i}": i for i in range(40)}}
    new_snap = {"payload": {f"key_{i}": i + 100 for i in range(40)}}

    diff = compute_semantic_diff(old_snap, new_snap)
    assert len(diff["values_changed"]) == 26  # 25 items + _truncated_count
    assert diff["values_changed"]["_truncated_count"] == 15
