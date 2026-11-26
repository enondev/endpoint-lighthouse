// Package normalize converts OpenAPI/Swagger specs and live responses into
// deterministic, comparable representations per AGENTS.md pipeline steps
// 5 (normalize + scrub) and 6 (project to shape, hash).
package normalize

import (
	"fmt"
	"sort"

	"github.com/getkin/kin-openapi/openapi3"
)

// Operation represents a normalized API operation extracted from a spec.
// The join key between spec and live is (Method, Path).
type Operation struct {
	Method      string              `json:"method"`
	Path        string              `json:"path"`
	OperationID string              `json:"operation_id,omitempty"`
	Summary     string              `json:"summary,omitempty"`
	Parameters  []Parameter         `json:"parameters,omitempty"`
	RequestBody *RequestBody        `json:"request_body,omitempty"`
	Responses   map[string]Response `json:"responses,omitempty"` // keyed by status code string
}

// Parameter represents an operation parameter.
type Parameter struct {
	Name     string      `json:"name"`
	In       string      `json:"in"` // query, path, header, cookie
	Required bool        `json:"required"`
	Schema   *SchemaNode `json:"schema,omitempty"`
}

// RequestBody represents an operation request body.
type RequestBody struct {
	Content map[string]*SchemaNode `json:"content,omitempty"` // keyed by content type
}

// Response represents an operation response.
type Response struct {
	Description string                 `json:"description,omitempty"`
	Content     map[string]*SchemaNode `json:"content,omitempty"`
}

// SchemaNode represents a normalized JSON Schema node after scrubbing.
type SchemaNode struct {
	Type       []string               `json:"type,omitempty"`
	Properties map[string]*SchemaNode `json:"properties,omitempty"`
	Required   []string               `json:"required,omitempty"`
	Items      *SchemaNode            `json:"items,omitempty"`
	Enum       []string               `json:"enum,omitempty"` // kept: low cardinality, is the contract
}

// NormalizedSpec is the complete normalized representation of a spec.
type NormalizedSpec struct {
	Operations []Operation `json:"operations"` // sorted by method, then path
}

// SpecLoader parses OpenAPI/Swagger specs.
type SpecLoader struct {
	loader *openapi3.Loader
}

// NewSpecLoader creates a loader that handles OpenAPI 3.x and Swagger 2.0.
func NewSpecLoader() *SpecLoader {
	return &SpecLoader{
		loader: openapi3.NewLoader(),
	}
}

// LoadFromData parses spec bytes (JSON or YAML) into an openapi3.T.
func (l *SpecLoader) LoadFromData(data []byte) (*openapi3.T, error) {
	doc, err := l.loader.LoadFromData(data)
	if err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}
	return doc, nil
}

// NormalizeSpec converts an OpenAPI 3.x T into a NormalizedSpec.
// All maps are key-sorted and arrays are element-sorted for determinism.
func NormalizeSpec(doc *openapi3.T) *NormalizedSpec {
	if doc == nil || doc.Paths == nil {
		return &NormalizedSpec{Operations: []Operation{}}
	}

	var ops []Operation

	// Sort paths for deterministic output
	paths := doc.Paths.Keys()
	sort.Strings(paths)

	for _, path := range paths {
		item := doc.Paths.Value(path)
		if item == nil {
			continue
		}

		// Collect methods in sorted order using Operations() map
		opMap := item.Operations()
		methods := make([]string, 0, len(opMap))
		for m := range opMap {
			methods = append(methods, m)
		}
		sort.Strings(methods)

		for _, method := range methods {
			op := opMap[method]
			if op == nil {
				continue
			}

			ops = append(ops, normalizeOperation(method, path, op))
		}
	}

	return &NormalizedSpec{Operations: ops}
}

func normalizeOperation(method, path string, op *openapi3.Operation) Operation {
	o := Operation{
		Method:      method,
		Path:        path,
		OperationID: op.OperationID,
		Summary:     op.Summary,
	}

	// Parameters
	if len(op.Parameters) > 0 {
		o.Parameters = make([]Parameter, 0, len(op.Parameters))
		for _, p := range op.Parameters {
			if p == nil || p.Value == nil {
				continue
			}
			o.Parameters = append(o.Parameters, Parameter{
				Name:     p.Value.Name,
				In:       p.Value.In,
				Required: p.Value.Required,
				Schema:   schemaNodeFromSchemaRef(p.Value.Schema),
			})
		}
		sort.Slice(o.Parameters, func(i, j int) bool {
			if o.Parameters[i].In != o.Parameters[j].In {
				return o.Parameters[i].In < o.Parameters[j].In
			}
			return o.Parameters[i].Name < o.Parameters[j].Name
		})
	}

	// Request body
	if op.RequestBody != nil && op.RequestBody.Value != nil {
		o.RequestBody = &RequestBody{
			Content: make(map[string]*SchemaNode),
		}
		for ct, media := range op.RequestBody.Value.Content {
			if media == nil || media.Schema == nil {
				continue
			}
			o.RequestBody.Content[ct] = schemaNodeFromSchemaRef(media.Schema)
		}
	}

	// Responses
	if op.Responses != nil && op.Responses.Len() > 0 {
		o.Responses = make(map[string]Response)
		for _, code := range op.Responses.Keys() {
			respRef := op.Responses.Value(code)
			if respRef == nil || respRef.Value == nil {
				continue
			}
			r := Response{}
			if respRef.Value.Description != nil {
				r.Description = *respRef.Value.Description
			}
			if len(respRef.Value.Content) > 0 {
				r.Content = make(map[string]*SchemaNode)
				for ct, media := range respRef.Value.Content {
					if media == nil || media.Schema == nil {
						continue
					}
					r.Content[ct] = schemaNodeFromSchemaRef(media.Schema)
				}
			}
			o.Responses[code] = r
		}
	}

	return o
}

func schemaNodeFromSchemaRef(sr *openapi3.SchemaRef) *SchemaNode {
	if sr == nil || sr.Value == nil {
		return nil
	}
	return schemaNodeFromSchema(sr.Value)
}

func schemaNodeFromSchema(s *openapi3.Schema) *SchemaNode {
	if s == nil {
		return nil
	}

	node := &SchemaNode{
		Type:     derefTypes(s.Type),
		Enum:     enumToStrings(s.Enum),
		Required: append([]string(nil), s.Required...),
	}
	sort.Strings(node.Required)

	if len(s.Properties) > 0 {
		node.Properties = make(map[string]*SchemaNode, len(s.Properties))
		for name, prop := range s.Properties {
			node.Properties[name] = schemaNodeFromSchemaRef(prop)
		}
	}

	if s.Items != nil {
		node.Items = schemaNodeFromSchemaRef(s.Items)
	}

	return node
}

func derefTypes(t *openapi3.Types) []string {
	if t == nil {
		return nil
	}
	return *t
}

func enumToStrings(e []any) []string {
	if e == nil {
		return nil
	}
	out := make([]string, 0, len(e))
	for _, v := range e {
		out = append(out, fmt.Sprintf("%v", v))
	}
	return out
}
