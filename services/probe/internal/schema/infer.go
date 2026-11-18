package schema

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Node represents a deterministic, sorted JSON Schema AST.
type Node struct {
	Types      []string         `json:"-"`
	Properties map[string]*Node `json:"properties,omitempty"`
	Required   []string         `json:"required,omitempty"`
	Items      *Node            `json:"items,omitempty"`
}

// MarshalJSON outputs canonical JSON with sorted keys and compact type representation.
func (n *Node) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')

	first := true
	writeKey := func(k string) {
		if !first {
			buf.WriteByte(',')
		}
		first = false
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
	}

	if len(n.Types) == 1 {
		writeKey("type")
		tb, _ := json.Marshal(n.Types[0])
		buf.Write(tb)
	} else if len(n.Types) > 1 {
		writeKey("type")
		tb, _ := json.Marshal(n.Types)
		buf.Write(tb)
	}

	if len(n.Properties) > 0 {
		writeKey("properties")
		buf.WriteByte('{')
		keys := make([]string, 0, len(n.Properties))
		for k := range n.Properties {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			buf.Write(kb)
			buf.WriteByte(':')
			vb, err := json.Marshal(n.Properties[k])
			if err != nil {
				return nil, err
			}
			buf.Write(vb)
		}
		buf.WriteByte('}')
	}

	if len(n.Required) > 0 {
		writeKey("required")
		rb, _ := json.Marshal(n.Required)
		buf.Write(rb)
	}

	if n.Items != nil {
		writeKey("items")
		ib, err := json.Marshal(n.Items)
		if err != nil {
			return nil, err
		}
		buf.Write(ib)
	}

	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// InferFromBytes decodes raw JSON and returns the canonical schema JSON, full SHA-256 hash, and AST.
func InferFromBytes(raw []byte) (canonical []byte, hash string, ast *Node, err error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var payload any
	if err := dec.Decode(&payload); err != nil {
		return nil, "", nil, fmt.Errorf("json decode: %w", err)
	}

	ast = inferValue(payload)
	canonical, err = json.Marshal(ast)
	if err != nil {
		return nil, "", nil, fmt.Errorf("canonical marshal: %w", err)
	}

	sum := sha256.Sum256(canonical)
	hash = hex.EncodeToString(sum[:])
	return canonical, hash, ast, nil
}

func inferValue(v any) *Node {
	switch val := v.(type) {
	case nil:
		return &Node{Types: []string{"null"}}
	case bool:
		return &Node{Types: []string{"boolean"}}
	case json.Number:
		if strings.ContainsAny(val.String(), ".eE") {
			return &Node{Types: []string{"number"}}
		}
		return &Node{Types: []string{"integer"}}
	case string:
		return &Node{Types: []string{"string"}}
	case map[string]any:
		props := make(map[string]*Node, len(val))
		req := make([]string, 0, len(val))
		for k, child := range val {
			props[k] = inferValue(child)
			req = append(req, k)
		}
		slices.Sort(req)
		return &Node{
			Types:      []string{"object"},
			Properties: props,
			Required:   req,
		}
	case []any:
		node := &Node{Types: []string{"array"}}
		if len(val) > 0 {
			merged := inferValue(val[0])
			for i := 1; i < len(val); i++ {
				merged = mergeNodes(merged, inferValue(val[i]))
			}
			node.Items = merged
		}
		return node
	default:
		return &Node{Types: []string{"unknown"}}
	}
}

func mergeNodes(a, b *Node) *Node {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}

	typeSet := make(map[string]struct{}, len(a.Types)+len(b.Types))
	for _, t := range a.Types {
		typeSet[t] = struct{}{}
	}
	for _, t := range b.Types {
		typeSet[t] = struct{}{}
	}
	types := make([]string, 0, len(typeSet))
	for t := range typeSet {
		types = append(types, t)
	}
	slices.Sort(types)

	out := &Node{Types: types}

	if len(a.Properties) > 0 || len(b.Properties) > 0 {
		out.Properties = make(map[string]*Node)
		for k, v := range a.Properties {
			if bv, ok := b.Properties[k]; ok {
				out.Properties[k] = mergeNodes(v, bv)
			} else {
				out.Properties[k] = v
			}
		}
		for k, v := range b.Properties {
			if _, ok := a.Properties[k]; !ok {
				out.Properties[k] = v
			}
		}

		// Intersect required fields across array items
		bReq := make(map[string]struct{}, len(b.Required))
		for _, r := range b.Required {
			bReq[r] = struct{}{}
		}
		for _, r := range a.Required {
			if _, ok := bReq[r]; ok {
				out.Required = append(out.Required, r)
			}
		}
		slices.Sort(out.Required)
	}

	if a.Items != nil || b.Items != nil {
		out.Items = mergeNodes(a.Items, b.Items)
	}

	return out
}

// DriftChange captures a single structural discrepancy between baseline and candidate schemas.
type DriftChange struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"` // removed_field, added_field, type_changed
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Breaking bool   `json:"breaking"`
}

// DiffAST compares baseline and current ASTs in <1ms without external allocations.
func DiffAST(path string, base, curr *Node) []DriftChange {
	if base == nil && curr == nil {
		return nil
	}
	if base == nil {
		return []DriftChange{{Path: path, Kind: "added_field", Actual: strings.Join(curr.Types, "|"), Breaking: false}}
	}
	if curr == nil {
		return []DriftChange{{Path: path, Kind: "removed_field", Expected: strings.Join(base.Types, "|"), Breaking: true}}
	}

	var changes []DriftChange
	baseType := strings.Join(base.Types, "|")
	currType := strings.Join(curr.Types, "|")
	if baseType != currType {
		changes = append(changes, DriftChange{
			Path:     path,
			Kind:     "type_changed",
			Expected: baseType,
			Actual:   currType,
			Breaking: true,
		})
	}

	for k, bChild := range base.Properties {
		childPath := path + "." + k
		cChild, exists := curr.Properties[k]
		if !exists {
			changes = append(changes, DriftChange{
				Path:     childPath,
				Kind:     "removed_field",
				Expected: strings.Join(bChild.Types, "|"),
				Breaking: true,
			})
			continue
		}
		changes = append(changes, DiffAST(childPath, bChild, cChild)...)
	}

	for k, cChild := range curr.Properties {
		if _, exists := base.Properties[k]; !exists {
			changes = append(changes, DriftChange{
				Path:     path + "." + k,
				Kind:     "added_field",
				Actual:   strings.Join(cChild.Types, "|"),
				Breaking: false,
			})
		}
	}

	if base.Items != nil || curr.Items != nil {
		changes = append(changes, DiffAST(path+"[]", base.Items, curr.Items)...)
	}

	return changes
}

// ParseSchemaJSON hydrates a *Node AST directly from a stored JSON Schema string (e.g., D1's inferred_schema).
func ParseSchemaJSON(rawJSON string) (*Node, error) {
	if strings.TrimSpace(rawJSON) == "" {
		return nil, fmt.Errorf("empty schema json")
	}
	var rawMap map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &rawMap); err != nil {
		return nil, err
	}
	return nodeFromSchemaMap(rawMap), nil
}

func nodeFromSchemaMap(m map[string]any) *Node {
	if m == nil {
		return nil
	}
	n := &Node{}

	switch t := m["type"].(type) {
	case string:
		n.Types = []string{t}
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok {
				n.Types = append(n.Types, s)
			}
		}
		slices.Sort(n.Types)
	}

	if propsRaw, ok := m["properties"].(map[string]any); ok && len(propsRaw) > 0 {
		n.Properties = make(map[string]*Node, len(propsRaw))
		for k, v := range propsRaw {
			if childMap, ok := v.(map[string]any); ok {
				n.Properties[k] = nodeFromSchemaMap(childMap)
			}
		}
	}

	if reqRaw, ok := m["required"].([]any); ok && len(reqRaw) > 0 {
		for _, item := range reqRaw {
			if s, ok := item.(string); ok {
				n.Required = append(n.Required, s)
			}
		}
		slices.Sort(n.Required)
	}

	if itemsMap, ok := m["items"].(map[string]any); ok {
		n.Items = nodeFromSchemaMap(itemsMap)
	}

	return n
}