// Package scrub implements the normative scrub policy from AGENTS.md.
// Governing principle: high-cardinality fields are reduced to their type;
// low-cardinality fields (enums, status codes, booleans) are kept as-is
// because they are the contract.
package scrub

import (
	"fmt"
	"strings"
)

// ScrubValue applies the scrub policy to a parsed JSON value.
// Maps are scrubbed by key name; arrays are scrubbed element-wise.
// High-cardinality fields (timestamps, UUIDs, request IDs, cursors, ETags,
// tokens, free text) are replaced by their type string.
// Low-cardinality fields (enums, status codes, booleans, bounded sets) are kept.
func ScrubValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		return scrubObject(val)
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = ScrubValue(item)
		}
		return out
	case string:
		return val // strings are kept as-is; callers decide based on key name
	case bool, float64, nil:
		return val
	default:
		return fmt.Sprintf("%T", v)
	}
}

// scrubObject scrubs a JSON object by applying key-based rules.
func scrubObject(obj map[string]any) any {
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		key := strings.ToLower(k)
		action := classifyKey(key)
		switch action {
		case drop:
			// Drop entirely (etag, tokens, secrets, signed URLs)
			continue
		case typeOnly:
			// Replace value with its type
			out[k] = typeOf(v)
		case keep:
			out[k] = ScrubValue(v)
		default:
			out[k] = ScrubValue(v)
		}
	}
	return out
}

type scrubAction int

const (
	keep scrubAction = iota
	typeOnly
	drop
)

// classifyKey determines the scrub action for a given (lowercased) key.
func classifyKey(key string) scrubAction {
	// Drop entirely
	if isDrop(key) {
		return drop
	}
	// Type only
	if isTypeOnly(key) {
		return typeOnly
	}
	// Keep
	return keep
}

func isDrop(key string) bool {
	switch {
	case key == "etag":
		return true
	case strings.HasSuffix(key, "_etag"):
		return true
	case key == "token" || key == "access_token" || key == "secret" || key == "api_key":
		return true
	case strings.HasSuffix(key, "_url") && strings.Contains(key, "signature"):
		return true
	}
	return false
}

func isTypeOnly(key string) bool {
	switch {
	case key == "id":
		return true
	case strings.HasSuffix(key, "_id"):
		return true
	case key == "uuid" || key == "guid":
		return true
	case strings.HasSuffix(key, "_at") || strings.HasSuffix(key, "_time"):
		return true
	case key == "date" || key == "timestamp":
		return true
	case key == "cursor" || key == "next_cursor" || key == "after" || key == "before" || key == "page_token":
		return true
	case key == "request_id" || key == "trace_id" || key == "correlation_id":
		return true
	case key == "description" || key == "name" || key == "title" || key == "body" || key == "message":
		return true
	case key == "count" || key == "total" || key == "size":
		return true
	case strings.HasSuffix(key, "_count"):
		return true
	}
	return false
}

func typeOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
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
