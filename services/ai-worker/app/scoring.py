import json
import logging
import math
from pathlib import Path
from typing import Any

logger = logging.getLogger("endpoint_lighthouse.scoring")

DEFAULT_WEIGHTS_PATH = Path(__file__).resolve().parent / "scoring_weights.json"

DEFAULT_WEIGHTS = {
    "severity_level": {
        "breaking": 40,
        "additive": 30,
        "cosmetic": 20,
        "pending": 10,
    },
    "status": {
        "OPEN": 20,
        "ACKNOWLEDGED": 10,
        "RESOLVED": 0,
    },
    "field_change": 1,
    "field_change_per_field": 2,
    "field_change_max": 20,
    "multipliers": {
        "is_breaking": 1.5,
    },
    "base_score": 0,
    "max_score": 100,
}


class ScoringEngine:
    """Modular severity scoring engine.

    Features are extracted by individual callables and combined via configurable weights.
    New features can be added by registering a new extractor function and its weight.
    """

    def __init__(self, weights: dict[str, Any] | None = None) -> None:
        self.weights = weights or dict(DEFAULT_WEIGHTS)
        self._additive_extractors: list[tuple[str, callable]] = [
            ("severity_level", self._extract_severity_level),
            ("status", self._extract_status),
            ("field_change", self._extract_field_change),
        ]
        self._multiplicative_extractors: list[tuple[str, callable]] = [
            ("is_breaking", self._extract_is_breaking_multiplier),
        ]

    @classmethod
    def from_file(cls, path: str | Path = DEFAULT_WEIGHTS_PATH) -> "ScoringEngine":
        """Load weights from a JSON file, falling back to defaults on missing keys."""
        weights = dict(DEFAULT_WEIGHTS)
        try:
            with open(path, "r", encoding="utf-8") as f:
                file_weights = json.load(f)
            for key, val in file_weights.items():
                if isinstance(val, dict) and isinstance(weights.get(key), dict):
                    merged = dict(weights[key])
                    merged.update(val)
                    weights[key] = merged
                else:
                    weights[key] = val
            logger.info("Loaded scoring weights from %s", path)
        except FileNotFoundError:
            logger.warning("Scoring weights file not found (%s); using defaults", path)
        except json.JSONDecodeError as exc:
            logger.warning("Invalid scoring weights JSON (%s); using defaults: %s", path, exc)
        return cls(weights)

    def register_additive_extractor(self, name: str, func: callable) -> None:
        """Register an additive feature extractor. Value * weight is added to the raw score."""
        self._additive_extractors.append((name, func))

    def register_multiplicative_extractor(self, name: str, func: callable) -> None:
        """Register a multiplicative feature extractor. Value is multiplied into the raw score."""
        self._multiplicative_extractors.append((name, func))

    def calculate(
        self,
        severity: str,
        status: str = "OPEN",
        diff_summary: dict[str, Any] | None = None,
        endpoint_category: str | None = None,
        **extra: Any,
    ) -> int:
        """Calculate a normalized severity score (0-100).

        Additive features:
          - severity_level: base weight from the LLM severity label
          - status: OPEN > ACKNOWLEDGED > RESOLVED
          - field_change: count of changed/added/removed fields in diff_summary

        Multiplicative features:
          - is_breaking: 1.5x bump when severity is BREAKING

        Final = clamp(base_score + sum(additive * weights) * product(multipliers), 0, max_score)
        """
        ctx = {
            "severity": severity,
            "status": status,
            "diff_summary": diff_summary,
            "endpoint_category": endpoint_category,
            **extra,
        }

        raw = 0.0
        for name, extractor in self._additive_extractors:
            try:
                value = extractor(**ctx)
            except Exception as exc:
                logger.warning("Scoring extractor %s failed: %s", name, exc)
                value = 0.0
            weight_cfg = self.weights.get(name, 1)
            if isinstance(weight_cfg, dict):
                # Dict weight: value is a category key; contribution is the mapped score.
                contribution = float(weight_cfg.get(str(value), 0))
            else:
                # Scalar weight: contribution is value * weight.
                contribution = float(value) * float(weight_cfg)
            raw += contribution

        multiplier = 1.0
        for name, extractor in self._multiplicative_extractors:
            try:
                value = extractor(**ctx)
            except Exception as exc:
                logger.warning("Scoring extractor %s failed: %s", name, exc)
                value = 1.0
            multiplier *= value

        base = self.weights.get("base_score", 0)
        max_score = self.weights.get("max_score", 100)
        score = base + (raw * multiplier)
        score = max(0, min(max_score, score))
        return int(math.floor(score))

    # --- Feature extractors ---

    def _extract_severity_level(self, severity: str, **_: Any) -> str:
        return (severity or "pending").lower()

    def _extract_status(self, status: str, **_: Any) -> str:
        return (status or "OPEN").upper()

    def _extract_field_change(
        self, diff_summary: dict[str, Any] | None = None, **_: Any
    ) -> float:
        if not isinstance(diff_summary, dict):
            return 0.0
        changed = 0
        for key in (
            "added",
            "removed",
            "changed",
            "values_changed",
            "iterable_item_added",
            "iterable_item_removed",
            "dictionary_item_added",
            "dictionary_item_removed",
        ):
            val = diff_summary.get(key)
            if isinstance(val, (list, dict)):
                changed += len(val)
        per_field = self.weights.get("field_change_per_field", 2)
        cap = self.weights.get("field_change_max", 20)
        return min(changed * per_field, cap)

    def _extract_is_breaking_multiplier(self, severity: str, **_: Any) -> float:
        if (severity or "").lower() == "breaking":
            return float(self.weights.get("multipliers", {}).get("is_breaking", 1.5))
        return 1.0
