// Package diff computes contract diffs between spec and live shapes.
// Every emitted diff is reproducible from spec_side and live_side alone.
// If it isn't reproducible, the projection is lossy — fix the projection,
// don't widen the stored payload.
package diff

import (
	"fmt"
	"sort"
)

// DiffKind represents a single contract change.
type DiffKind struct {
	Kind     string // field_added, field_removed, type_changed, required_added, enum_removed, status_removed, path_removed, path_added, enum_value_added, enum_value_removed, status_code_added, status_code_removed, description_changed, etc.
	Severity string // breaking, additive, cosmetic (set by severity classifier)
	Path     string // JSON path to the changed field
	SpecVal  any    // value from spec side (reproducible)
	LiveVal  any    // value from live side (reproducible)
}

// Differ computes contract diffs between two normalized shapes.
type Differ interface {
	Diff(specShape, liveShape any) ([]DiffKind, error)
}

// DefaultDiffer implements a structural comparison between spec and live shapes.
type DefaultDiffer struct{}

// Diff compares specShape against liveShape and returns the contract diffs.
// If both shapes are nil, no diffs. If specShape is nil, path_added.
// If liveShape is nil, path_removed (breaking).
func (d *DefaultDiffer) Diff(specShape, liveShape any) ([]DiffKind, error) {
	if specShape == nil && liveShape == nil {
		return nil, nil
	}
	if specShape == nil {
		return []DiffKind{{
			Kind:     "path_added",
			Severity: "additive",
			Path:     "",
			SpecVal:  nil,
			LiveVal:  liveShape,
		}}, nil
	}
	if liveShape == nil {
		return []DiffKind{{
			Kind:     "path_removed",
			Severity: "breaking",
			Path:     "",
			SpecVal:  specShape,
			LiveVal:  nil,
		}}, nil
	}

	specMap, specOk := specShape.(map[string]any)
	liveMap, liveOk := liveShape.(map[string]any)
	if specOk && liveOk {
		return d.diffMaps(specMap, liveMap, "")
	}

	specArr, specIsArr := specShape.([]any)
	liveArr, liveIsArr := liveShape.([]any)
	if specIsArr && liveIsArr {
		return d.diffArrays(specArr, liveArr, "")
	}

	// Primitive comparison
	if fmt.Sprintf("%v", specShape) != fmt.Sprintf("%v", liveShape) ||
		typeOf(specShape) != typeOf(liveShape) {
		return []DiffKind{{
			Kind:     "type_changed",
			Severity: "breaking",
			Path:     "",
			SpecVal:  specShape,
			LiveVal:  liveShape,
		}}, nil
	}

	return nil, nil
}

func (d *DefaultDiffer) diffMaps(specMap, liveMap map[string]any, prefix string) ([]DiffKind, error) {
	var diffs []DiffKind

	specKeys := make(map[string]bool)
	for k := range specMap {
		specKeys[k] = true
	}
	liveKeys := make(map[string]bool)
	for k := range liveMap {
		liveKeys[k] = true
	}

	// Fields removed (in spec but not live) — breaking
	for k := range specKeys {
		if !liveKeys[k] {
			path := joinPath(prefix, k)
			diffs = append(diffs, DiffKind{
				Kind:     "field_removed",
				Severity: "breaking",
				Path:     path,
				SpecVal:  specMap[k],
				LiveVal:  nil,
			})
		}
	}

	// Fields added (in live but not spec) — additive
	for k := range liveKeys {
		if !specKeys[k] {
			path := joinPath(prefix, k)
			diffs = append(diffs, DiffKind{
				Kind:     "field_added",
				Severity: "additive",
				Path:     path,
				SpecVal:  nil,
				LiveVal:  liveMap[k],
			})
		}
	}

	// Common fields — check for type changes or nested diffs
	for k := range specKeys {
		if !liveKeys[k] {
			continue
		}
		path := joinPath(prefix, k)
		subDiffs, err := d.Diff(specMap[k], liveMap[k])
		if err != nil {
			return nil, fmt.Errorf("diff %s: %w", path, err)
		}
		diffs = append(diffs, subDiffs...)
	}

	// Sort diffs by path for determinism
	sort.Slice(diffs, func(i, j int) bool {
		return diffs[i].Path < diffs[j].Path
	})

	return diffs, nil
}

func (d *DefaultDiffer) diffArrays(specArr, liveArr []any, prefix string) ([]DiffKind, error) {
	var diffs []DiffKind

	minLen := len(specArr)
	if len(liveArr) < minLen {
		minLen = len(liveArr)
	}

	for i := 0; i < minLen; i++ {
		path := joinPath(prefix, fmt.Sprintf("[%d]", i))
		subDiffs, err := d.Diff(specArr[i], liveArr[i])
		if err != nil {
			return nil, fmt.Errorf("diff %s: %w", path, err)
		}
		diffs = append(diffs, subDiffs...)
	}

	// Extra elements in live — additive
	for i := minLen; i < len(liveArr); i++ {
		path := joinPath(prefix, fmt.Sprintf("[%d]", i))
		diffs = append(diffs, DiffKind{
			Kind:     "field_added",
			Severity: "additive",
			Path:     path,
			SpecVal:  nil,
			LiveVal:  liveArr[i],
		})
	}

	// Missing elements in live — breaking
	for i := minLen; i < len(specArr); i++ {
		path := joinPath(prefix, fmt.Sprintf("[%d]", i))
		diffs = append(diffs, DiffKind{
			Kind:     "field_removed",
			Severity: "breaking",
			Path:     path,
			SpecVal:  specArr[i],
			LiveVal:  nil,
		})
	}

	return diffs, nil
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func typeOf(v any) string {
	switch v := v.(type) {
	case nil:
		return "nil"
	case bool:
		return "bool"
	case int:
		return "int"
	case int64:
		return "int64"
	case float64:
		return "float64"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// compile-time check
var _ Differ = (*DefaultDiffer)(nil)
