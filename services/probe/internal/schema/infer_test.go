package schema

import (
	"testing"
)

func TestInferFromBytes(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantOK bool
	}{
		{name: "empty object", input: `{"a":1}`, wantOK: true},
		{name: "nested object", input: `{"a":{"b":"c"}}`, wantOK: true},
		{name: "array", input: `[1,2,3]`, wantOK: true},
		{name: "invalid json", input: `{bad json`, wantOK: false},
		{name: "empty string", input: ``, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, hash, ast, err := InferFromBytes([]byte(tt.input))
			if tt.wantOK {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				if hash == "" {
					t.Fatal("expected non-empty hash")
				}
				if ast == nil {
					t.Fatal("expected non-nil AST")
				}
			} else {
				if err == nil {
					t.Fatal("expected error")
				}
				if ast != nil {
					t.Fatal("expected nil AST")
				}
			}
		})
	}
}

func TestDiffASTAddedField(t *testing.T) {
	base := &Node{Types: []string{"object"}, Properties: map[string]*Node{
		"name": {Types: []string{"string"}},
	}}
	curr := &Node{Types: []string{"object"}, Properties: map[string]*Node{
		"name":  {Types: []string{"string"}},
		"age":   {Types: []string{"integer"}},
	}}

	changes := DiffAST("$", base, curr)
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %v", len(changes), changes)
	}
	if changes[0].Kind != "added_field" {
		t.Fatalf("expected added_field, got %s", changes[0].Kind)
	}
	if changes[0].Path != "$.age" {
		t.Fatalf("expected path $.age, got %s", changes[0].Path)
	}
	if changes[0].Breaking {
		t.Fatal("expected non-breaking for added field")
	}
}

func TestDiffASTRemovedField(t *testing.T) {
	base := &Node{Types: []string{"object"}, Properties: map[string]*Node{
		"name": {Types: []string{"string"}},
		"age":  {Types: []string{"integer"}},
	}}
	curr := &Node{Types: []string{"object"}, Properties: map[string]*Node{
		"name": {Types: []string{"string"}},
	}}

	changes := DiffAST("$", base, curr)
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %v", len(changes), changes)
	}
	if changes[0].Kind != "removed_field" {
		t.Fatalf("expected removed_field, got %s", changes[0].Kind)
	}
	if changes[0].Path != "$.age" {
		t.Fatalf("expected path $.age, got %s", changes[0].Path)
	}
	if !changes[0].Breaking {
		t.Fatal("expected breaking for removed field")
	}
}

func TestDiffASTTypeChanged(t *testing.T) {
	base := &Node{Types: []string{"object"}, Properties: map[string]*Node{
		"name": {Types: []string{"string"}},
	}}
	curr := &Node{Types: []string{"object"}, Properties: map[string]*Node{
		"name": {Types: []string{"integer"}},
	}}

	changes := DiffAST("$", base, curr)
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %v", len(changes), changes)
	}
	if changes[0].Kind != "type_changed" {
		t.Fatalf("expected type_changed, got %s", changes[0].Kind)
	}
	if changes[0].Path != "$.name" {
		t.Fatalf("expected path $.name, got %s", changes[0].Path)
	}
	if !changes[0].Breaking {
		t.Fatal("expected breaking for type change")
	}
	if changes[0].Expected != "string" {
		t.Fatalf("expected expected='string', got %s", changes[0].Expected)
	}
	if changes[0].Actual != "integer" {
		t.Fatalf("expected actual='integer', got %s", changes[0].Actual)
	}
}

func TestDiffASTNoChanges(t *testing.T) {
	base := &Node{Types: []string{"object"}, Properties: map[string]*Node{
		"name": {Types: []string{"string"}},
		"age":  {Types: []string{"integer"}},
	}}
	curr := &Node{Types: []string{"object"}, Properties: map[string]*Node{
		"name": {Types: []string{"string"}},
		"age":  {Types: []string{"integer"}},
	}}

	changes := DiffAST("$", base, curr)
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes, got %d: %v", len(changes), changes)
	}
}

func TestDiffASTNested(t *testing.T) {
	base := &Node{Types: []string{"object"}, Properties: map[string]*Node{
		"user": {Types: []string{"object"}, Properties: map[string]*Node{
			"name": {Types: []string{"string"}},
		}},
	}}
	curr := &Node{Types: []string{"object"}, Properties: map[string]*Node{
		"user": {Types: []string{"object"}, Properties: map[string]*Node{
			"name":  {Types: []string{"string"}},
			"email": {Types: []string{"string"}},
		}},
	}}

	changes := DiffAST("$", base, curr)
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %v", len(changes), changes)
	}
	if changes[0].Kind != "added_field" {
		t.Fatalf("expected added_field, got %s", changes[0].Kind)
	}
	if changes[0].Path != "$.user.email" {
		t.Fatalf("expected path $.user.email, got %s", changes[0].Path)
	}
}

func TestParseSchemaJSON(t *testing.T) {
	raw := `{"type":"object","properties":{"name":{"type":"string"}}}`
	node, err := ParseSchemaJSON(raw)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if node == nil {
		t.Fatal("expected non-nil node")
	}
	if len(node.Types) != 1 || node.Types[0] != "object" {
		t.Fatalf("expected type object, got %v", node.Types)
	}
	if node.Properties["name"] == nil {
		t.Fatal("expected name property")
	}
	if len(node.Properties["name"].Types) != 1 || node.Properties["name"].Types[0] != "string" {
		t.Fatalf("expected name type string, got %v", node.Properties["name"].Types)
	}
}

func TestParseSchemaJSONEmpty(t *testing.T) {
	_, err := ParseSchemaJSON("")
	if err == nil {
		t.Fatal("expected error for empty string")
	}
}
