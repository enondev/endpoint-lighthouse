// Package severity implements the AGENTS.md severity classifier contract table.
//
// Contract — decided, do not change without review:
//
//	| Change                  | Severity   |
//	|-------------------------|------------|
//	| Field removed           | breaking   |
//	| Type narrowed           | breaking   |
//	| Required field added    | breaking   |
//	| Enum value removed      | breaking   |
//	| Status code removed     | breaking   |
//	| Path or method removed  | breaking   |
//	| Field added             | additive   |
//	| Optional parameter added| additive   |
//	| Enum value added        | additive   |
//	| Status code added       | additive   |
//	| Description/example/etc.| cosmetic   |
//
// `cosmetic` diffs are suppressed from the default feed but still recorded.
//
// Tuning (widened type scoring, multi-severity ranking, etc.) is deliberately
// deferred and marked TODO(calibration).
package severity

import (
	"fmt"
)

// Severity values per AGENTS.md.
const (
	Breaking = "breaking"
	Additive = "additive"
	Cosmetic = "cosmetic"
)

// Classifier maps a contract change kind to its severity.
type Classifier struct{}

// NewClassifier creates a Classifier with the AGENTS.md contract table.
func NewClassifier() *Classifier {
	return &Classifier{}
}

// Classify returns the severity for a given change kind.
// Unknown kinds return an error — severity is business context and must not
// be guessed (see AGENTS.md "Never delegate").
func (c *Classifier) Classify(kind string) (string, error) {
	switch kind {
	case "field_removed":
		return Breaking, nil
	case "type_narrowed":
		return Breaking, nil
	case "required_field_added":
		return Breaking, nil
	case "enum_value_removed":
		return Breaking, nil
	case "status_code_removed":
		return Breaking, nil
	case "path_or_method_removed":
		return Breaking, nil
	case "field_added":
		return Additive, nil
	case "optional_parameter_added":
		return Additive, nil
	case "enum_value_added":
		return Additive, nil
	case "status_code_added":
		return Additive, nil
	case "description_changed":
		return Cosmetic, nil
	case "summary_changed":
		return Cosmetic, nil
	case "example_changed":
		return Cosmetic, nil
	case "tag_changed":
		return Cosmetic, nil
	case "ordering_changed":
		return Cosmetic, nil
	case "widened_type": // TODO(calibration): breaking or additive?
		return "", fmt.Errorf("severity for widened_type not yet calibrated")
	case "multi_severity": // TODO(calibration): ranking when multiple severities
		return "", fmt.Errorf("severity for multi_severity not yet calibrated")
	default:
		return "", fmt.Errorf("unknown change kind %q — severity must be decided by human review", kind)
	}
}

// IsCosmetic reports whether the severity is cosmetic.
func IsCosmetic(sev string) bool {
	return sev == Cosmetic
}

// IsBreaking reports whether the severity is breaking.
func IsBreaking(sev string) bool {
	return sev == Breaking
}

// IsAdditive reports whether the severity is additive.
func IsAdditive(sev string) bool {
	return sev == Additive
}
