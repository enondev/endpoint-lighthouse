// Package hash computes load-bearing content hashes for contract drift
// detection per AGENTS.md. contract_hash and shape_hash let us detect change
// without storing or diffing full documents. Never compare raw JSON.
package hash

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// ContractHash computes a SHA-256 hash of normalized spec operations.
// The operations slice must be pre-sorted for determinism.
func ContractHash(operations []any) string {
	return sha256Hex(canonicalJSON(operations))
}

// ShapeHash computes a SHA-256 hash of a normalized/scrubbed live response shape.
func ShapeHash(shape any) string {
	return sha256Hex(canonicalJSON(shape))
}

// canonicalJSON produces deterministic, sorted JSON bytes for hashing.
func canonicalJSON(v any) []byte {
	sorted := SortMapKeys(v)
	b, err := json.Marshal(sorted)
	if err != nil {
		// Should not happen for JSON-decoded values; fall back to string
		return []byte(sortedString(sorted))
	}
	return b
}

// sha256Hex returns the lowercase hex SHA-256 digest of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SortMapKeys recursively sorts map keys in a JSON-decoded value for
// deterministic serialization. Returns a new value with all maps key-sorted.
func SortMapKeys(v any) any {
	switch val := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(val))
		for _, k := range keys {
			out[k] = SortMapKeys(val[k])
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = SortMapKeys(item)
		}
		return out
	default:
		return val
	}
}

func sortedString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	default:
		return "?"
	}
}
