package diff

import (
	"testing"
)

func TestDiffNilBoth(t *testing.T) {
	d := &DefaultDiffer{}
	diffs, err := d.Diff(nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diffs) != 0 {
		t.Fatalf("expected no diffs, got %d", len(diffs))
	}
}

func TestDiffPathAdded(t *testing.T) {
	d := &DefaultDiffer{}
	diffs, err := d.Diff(nil, map[string]any{"status": "ok"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if diffs[0].Kind != "path_added" {
		t.Errorf("expected path_added, got %s", diffs[0].Kind)
	}
	if diffs[0].Severity != "additive" {
		t.Errorf("expected additive, got %s", diffs[0].Severity)
	}
}

func TestDiffPathRemoved(t *testing.T) {
	d := &DefaultDiffer{}
	diffs, err := d.Diff(map[string]any{"status": "ok"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if diffs[0].Kind != "path_removed" {
		t.Errorf("expected path_removed, got %s", diffs[0].Kind)
	}
	if diffs[0].Severity != "breaking" {
		t.Errorf("expected breaking, got %s", diffs[0].Severity)
	}
}

func TestDiffFieldRemoved(t *testing.T) {
	d := &DefaultDiffer{}
	spec := map[string]any{"name": "old", "status": "active"}
	live := map[string]any{"status": "active"}

	diffs, err := d.Diff(spec, live)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if diffs[0].Kind != "field_removed" {
		t.Errorf("expected field_removed, got %s", diffs[0].Kind)
	}
	if diffs[0].Path != "name" {
		t.Errorf("expected path 'name', got %s", diffs[0].Path)
	}
	if diffs[0].Severity != "breaking" {
		t.Errorf("expected breaking, got %s", diffs[0].Severity)
	}
	if diffs[0].SpecVal != "old" {
		t.Errorf("expected SpecVal 'old', got %v", diffs[0].SpecVal)
	}
}

func TestDiffFieldAdded(t *testing.T) {
	d := &DefaultDiffer{}
	spec := map[string]any{"status": "active"}
	live := map[string]any{"status": "active", "version": "2.0"}

	diffs, err := d.Diff(spec, live)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if diffs[0].Kind != "field_added" {
		t.Errorf("expected field_added, got %s", diffs[0].Kind)
	}
	if diffs[0].Severity != "additive" {
		t.Errorf("expected additive, got %s", diffs[0].Severity)
	}
	if diffs[0].LiveVal != "2.0" {
		t.Errorf("expected LiveVal '2.0', got %v", diffs[0].LiveVal)
	}
}

func TestDiffTypeChanged(t *testing.T) {
	d := &DefaultDiffer{}
	spec := map[string]any{"count": 42}
	live := map[string]any{"count": "42"}

	diffs, err := d.Diff(spec, live)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if diffs[0].Kind != "type_changed" {
		t.Errorf("expected type_changed, got %s", diffs[0].Kind)
	}
	if diffs[0].Severity != "breaking" {
		t.Errorf("expected breaking, got %s", diffs[0].Severity)
	}
}

func TestDiffNested(t *testing.T) {
	d := &DefaultDiffer{}
	spec := map[string]any{
		"user": map[string]any{
			"id":   "uuid-123",
			"name": "John",
		},
	}
	live := map[string]any{
		"user": map[string]any{
			"id":   "uuid-123",
			"name": "Jane",
			"age":  30,
		},
	}

	diffs, err := d.Diff(spec, live)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// name changed (type_changed), age added (additive)
	kinds := map[string]bool{}
	for _, d := range diffs {
		kinds[d.Kind] = true
	}
	if !kinds["type_changed"] {
		t.Error("expected type_changed diff")
	}
	if !kinds["field_added"] {
		t.Error("expected field_added diff")
	}
}

func TestDiffArray(t *testing.T) {
	d := &DefaultDiffer{}
	spec := []any{"a", "b"}
	live := []any{"a", "b", "c"}

	diffs, err := d.Diff(spec, live)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 diff, got %d", len(diffs))
	}
	if diffs[0].Kind != "field_added" {
		t.Errorf("expected field_added, got %s", diffs[0].Kind)
	}
	if diffs[0].Path != "[2]" {
		t.Errorf("expected path '[2]', got %s", diffs[0].Path)
	}
}

func TestDiffReproducible(t *testing.T) {
	d := &DefaultDiffer{}
	spec := map[string]any{"b": 2, "a": 1}
	live := map[string]any{"a": 1, "b": 2}

	diffs1, _ := d.Diff(spec, live)
	diffs2, _ := d.Diff(spec, live)

	if len(diffs1) != len(diffs2) {
		t.Error("diff should be deterministic")
	}
}
