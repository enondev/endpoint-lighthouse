import json
import pytest

from app.scoring import ScoringEngine


@pytest.fixture
def scorer():
    return ScoringEngine.from_file()


def test_basic_severity_levels(scorer):
    breaking = scorer.calculate(severity="breaking", status="OPEN", diff_summary={})
    additive = scorer.calculate(severity="additive", status="OPEN", diff_summary={})
    cosmetic = scorer.calculate(severity="cosmetic", status="OPEN", diff_summary={})
    pending = scorer.calculate(severity="pending", status="OPEN", diff_summary={})

    # breaking: (40 + 20) * 1.5 = 90
    # additive: (30 + 20) * 1.0 = 50
    # cosmetic: (20 + 20) * 1.0 = 40
    # pending: (10 + 20) * 1.0 = 30
    assert breaking == 90
    assert additive == 50
    assert cosmetic == 40
    assert pending == 30
    assert breaking > additive > cosmetic > pending


def test_status_weights(scorer):
    open_score = scorer.calculate(severity="additive", status="OPEN", diff_summary={})
    acknowledged_score = scorer.calculate(severity="additive", status="ACKNOWLEDGED", diff_summary={})
    resolved_score = scorer.calculate(severity="additive", status="RESOLVED", diff_summary={})

    # additive + OPEN: (30 + 20) = 50
    # additive + ACKNOWLEDGED: (30 + 10) = 40
    # additive + RESOLVED: (30 + 0) = 30
    assert open_score == 50
    assert acknowledged_score == 40
    assert resolved_score == 30
    assert open_score > acknowledged_score > resolved_score


def test_field_change_weights(scorer):
    no_change = scorer.calculate(severity="additive", status="OPEN", diff_summary={})
    some_change = scorer.calculate(
        severity="additive", status="OPEN", diff_summary={"added": ["a", "b"], "removed": ["c"]}
    )

    # No change: (30 + 20 + 0) = 50
    # 3 fields * 2 = 6: (30 + 20 + 6) = 56
    assert no_change == 50
    assert some_change == 56
    assert some_change > no_change


def test_field_change_cap(scorer):
    many_changes = scorer.calculate(
        severity="additive",
        status="OPEN",
        diff_summary={"added": [f"field_{i}" for i in range(50)]},
    )
    # 50 * 2 = 100, capped at 20: (30 + 20 + 20) = 70
    assert many_changes == 70


def test_breaking_multiplier(scorer):
    breaking = scorer.calculate(severity="breaking", status="OPEN", diff_summary={})
    additive_score = scorer.calculate(severity="additive", status="OPEN", diff_summary={})

    # breaking: (40 + 20) * 1.5 = 90
    # additive: (30 + 20) * 1.0 = 50
    assert breaking == 90
    assert additive_score == 50
    assert breaking > additive_score


def test_score_clamped_to_max(scorer):
    # Everything maxed out should not exceed max_score (100)
    maxed = scorer.calculate(
        severity="breaking",
        status="OPEN",
        diff_summary={"added": [f"field_{i}" for i in range(1000)]},
    )
    assert maxed <= 100


def test_score_clamped_to_min(scorer):
    # Empty/minimal input should not go below 0
    min_score = scorer.calculate(severity="cosmetic", status="RESOLVED", diff_summary={})
    # cosmetic (20) + RESOLVED (0) + 0 field changes = 20
    assert min_score >= 0


def test_from_file_uses_defaults_when_missing(tmp_path):
    weights_path = tmp_path / "scoring_weights.json"
    weights_path.write_text("{}")
    scorer = ScoringEngine.from_file(str(weights_path))
    # Should fall back to defaults: breaking = 40, OPEN = 20, *1.5 = 90
    assert scorer.calculate(severity="breaking", status="OPEN", diff_summary={}) == 90


def test_from_file_overrides_weights(tmp_path):
    weights_path = tmp_path / "scoring_weights.json"
    weights_path.write_text(json.dumps({"severity_level": {"breaking": 10}}))
    scorer = ScoringEngine.from_file(str(weights_path))
    breaking = scorer.calculate(severity="breaking", status="OPEN", diff_summary={})
    # (10 + 20) * 1.5 = 45
    assert breaking == 45


def test_register_additive_extractor(scorer):
    def custom_extractor(**ctx):
        return 5.0

    scorer.register_additive_extractor("custom_feature", custom_extractor)
    scorer.weights["custom_feature"] = 3
    score = scorer.calculate(severity="additive", status="OPEN", diff_summary={})
    # (30 + 20 + 15) = 65
    assert score == 65


def test_register_multiplicative_extractor(scorer):
    def custom_multiplier(**ctx):
        return 2.0

    scorer.register_multiplicative_extractor("custom_multiplier", custom_multiplier)
    score = scorer.calculate(severity="additive", status="OPEN", diff_summary={})
    # (30 + 20) * 2.0 = 100
    assert score == 100


def test_invalid_severity_defaults_to_zero(scorer):
    score = scorer.calculate(severity="unknown_severity", status="OPEN", diff_summary={})
    # Unknown severity -> 0 from dict weight, status OPEN = 20
    assert score == 20