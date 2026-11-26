package hash

import (
	"testing"
)

func TestHashDeterministic(t *testing.T) {
	obj1 := map[string]any{"b": 2, "a": 1}
	obj2 := map[string]any{"a": 1, "b": 2}

	h1 := ShapeHash(obj1)
	h2 := ShapeHash(obj2)
	if h1 != h2 {
		t.Errorf("hash should be deterministic for same content, got %s vs %s", h1, h2)
	}
}

func TestHashDifferentContent(t *testing.T) {
	obj1 := map[string]any{"a": 1}
	obj2 := map[string]any{"a": 2}

	h1 := ShapeHash(obj1)
	h2 := ShapeHash(obj2)
	if h1 == h2 {
		t.Error("hash should differ for different content")
	}
}

func TestHashArrayOrderMatters(t *testing.T) {
	arr1 := []any{"a", "b"}
	arr2 := []any{"b", "a"}

	h1 := ShapeHash(arr1)
	h2 := ShapeHash(arr2)
	if h1 == h2 {
		t.Error("hash should differ for different array order")
	}
}
