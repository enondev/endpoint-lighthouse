package severity

import (
	"testing"
)

func TestClassifierBreaking(t *testing.T) {
	c := NewClassifier()

	cases := []struct {
		kind string
		want string
	}{
		{"field_removed", Breaking},
		{"type_narrowed", Breaking},
		{"required_field_added", Breaking},
		{"enum_value_removed", Breaking},
		{"status_code_removed", Breaking},
		{"path_or_method_removed", Breaking},
	}

	for _, tc := range cases {
		got, err := c.Classify(tc.kind)
		if err != nil {
			t.Errorf("Classify(%q) unexpected error: %v", tc.kind, err)
		}
		if got != tc.want {
			t.Errorf("Classify(%q) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestClassifierAdditive(t *testing.T) {
	c := NewClassifier()

	cases := []struct {
		kind string
		want string
	}{
		{"field_added", Additive},
		{"optional_parameter_added", Additive},
		{"enum_value_added", Additive},
		{"status_code_added", Additive},
	}

	for _, tc := range cases {
		got, err := c.Classify(tc.kind)
		if err != nil {
			t.Errorf("Classify(%q) unexpected error: %v", tc.kind, err)
		}
		if got != tc.want {
			t.Errorf("Classify(%q) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestClassifierCosmetic(t *testing.T) {
	c := NewClassifier()

	cases := []string{
		"description_changed",
		"summary_changed",
		"example_changed",
		"tag_changed",
		"ordering_changed",
	}

	for _, kind := range cases {
		got, err := c.Classify(kind)
		if err != nil {
			t.Errorf("Classify(%q) unexpected error: %v", kind, err)
		}
		if got != Cosmetic {
			t.Errorf("Classify(%q) = %q, want %q", kind, got, Cosmetic)
		}
	}
}

func TestClassifierUnknownKind(t *testing.T) {
	c := NewClassifier()

	_, err := c.Classify("unknown_kind")
	if err == nil {
		t.Error("expected error for unknown kind")
	}
}

func TestClassifierUncalibrated(t *testing.T) {
	c := NewClassifier()

	_, err := c.Classify("widened_type")
	if err == nil {
		t.Error("expected error for uncalibrated widened_type")
	}

	_, err = c.Classify("multi_severity")
	if err == nil {
		t.Error("expected error for uncalibrated multi_severity")
	}
}

func TestIsBreakingAdditiveCosmetic(t *testing.T) {
	if !IsBreaking(Breaking) {
		t.Error("IsBreaking(breaking) should be true")
	}
	if IsBreaking(Additive) {
		t.Error("IsBreaking(additive) should be false")
	}
	if !IsAdditive(Additive) {
		t.Error("IsAdditive(additive) should be true")
	}
	if IsAdditive(Cosmetic) {
		t.Error("IsAdditive(cosmetic) should be false")
	}
	if !IsCosmetic(Cosmetic) {
		t.Error("IsCosmetic(cosmetic) should be true")
	}
	if IsCosmetic(Breaking) {
		t.Error("IsCosmetic(breaking) should be false")
	}
}
