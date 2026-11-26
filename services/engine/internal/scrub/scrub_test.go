package scrub

import (
	"testing"
)

func TestScrubDropsSensitiveFields(t *testing.T) {
	obj := map[string]any{
		"name":         "John",
		"etag":         `W/"abc"`,
		"api_key":      "sk_live_123",
		"access_token": "token_abc",
		"request_id":   "req_8f3a2c",
		"timestamp":    "2026-11-26T00:21:55Z",
		"status":       "active",
		"count":        float64(42),
		"active":       true,
	}

	out := ScrubValue(obj)
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", out)
	}

	// Dropped fields
	if _, exists := m["etag"]; exists {
		t.Error("etag should be dropped")
	}
	if _, exists := m["api_key"]; exists {
		t.Error("api_key should be dropped")
	}
	if _, exists := m["access_token"]; exists {
		t.Error("access_token should be dropped")
	}

	// Type-only fields
	if m["name"] != "string" {
		t.Errorf("name should be 'string', got %v", m["name"])
	}
	if m["request_id"] != "string" {
		t.Errorf("request_id should be 'string', got %v", m["request_id"])
	}
	if m["timestamp"] != "string" {
		t.Errorf("timestamp should be 'string', got %v", m["timestamp"])
	}
	if m["count"] != "number" {
		t.Errorf("count should be 'number', got %v", m["count"])
	}

	// Kept fields
	if m["status"] != "active" {
		t.Errorf("status should be kept, got %v", m["status"])
	}
	if m["active"] != true {
		t.Errorf("active should be kept, got %v", m["active"])
	}
}

func TestScrubArray(t *testing.T) {
	arr := []any{"active", "pending", "req_123"}
	out := ScrubValue(arr)
	m, ok := out.([]any)
	if !ok {
		t.Fatalf("expected array, got %T", out)
	}
	if len(m) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(m))
	}
	if m[0] != "active" {
		t.Errorf("first element should be kept as-is, got %v", m[0])
	}
	if m[1] != "pending" {
		t.Errorf("second element should be kept as-is, got %v", m[1])
	}
	if m[2] != "req_123" {
		t.Errorf("array elements keep values as-is (no key context), got %v", m[2])
	}
}

func TestScrubNested(t *testing.T) {
	obj := map[string]any{
		"user": map[string]any{
			"id":         "uuid-123",
			"email":      "user@example.com",
			"created_at": "2026-01-01T00:00:00Z",
		},
	}
	out := ScrubValue(obj)
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", out)
	}
	user, ok := m["user"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested map, got %T", m["user"])
	}
	if user["id"] != "string" {
		t.Errorf("nested id should be 'string', got %v", user["id"])
	}
	if user["email"] != "user@example.com" {
		t.Errorf("nested email should be kept, got %v", user["email"])
	}
	if user["created_at"] != "string" {
		t.Errorf("nested created_at should be 'string', got %v", user["created_at"])
	}
}
