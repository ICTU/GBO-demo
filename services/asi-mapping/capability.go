package asimapping

import (
	"encoding/json"
	"fmt"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// selectedFragment is the spread in the source's document that the ASIP
// replaces with the selection it derives from the requested attributes.
const selectedFragment = "Selected"

// Capability is the asi capability of a source's metadata.
type Capability struct {
	Version         string          `json:"version"`
	AuthenticSource AuthenticSource `json:"authentic_source"`
	GraphQL         GraphQL         `json:"graphql"`
	Attributes      []Attribute     `json:"attributes"`
}

// AuthenticSource names the source in the ASIP's dataService records.
type AuthenticSource struct {
	LegalName string `json:"legal_name"`
	Country   string `json:"country"`
}

// GraphQL is the query the ASIP sends to the source, with one ...Selected.
type GraphQL struct {
	ServiceReference string `json:"service_reference,omitempty"`
	Endpoint         string `json:"endpoint"`
	Document         string `json:"document"`
	SubjectVariable  string `json:"subject_variable"`
	ResultPointer    string `json:"result_pointer"`
}

// Attribute is an attribute the source offers, with per member of its value
// the source fields it is made of. On names the GraphQL type the pointers
// apply to, when the result is an interface or union.
type Attribute struct {
	Identifier string           `json:"identifier"`
	On         string           `json:"on,omitempty"`
	Fields     map[string]Field `json:"fields"`
}

// Field makes one member of the value: a copied field (Pointer), fields
// joined with Separator (Join), a fixed value (Const), or per element of the
// list at Each the field at Pointer within it.
type Field struct {
	Pointer   string          `json:"pointer,omitempty"`
	Join      []string        `json:"join,omitempty"`
	Separator *string         `json:"separator,omitempty"`
	Const     json.RawMessage `json:"const,omitempty"`
	Each      string          `json:"each,omitempty"`
}

// Validate checks the GraphQL document and each attribute's mapping against
// the catalogue. It assumes the capability matches the metadata schema.
func (c Capability) Validate(catalogue Catalogue) error {
	if err := c.GraphQL.validate(); err != nil {
		return err
	}
	seen := make(map[string]bool, len(c.Attributes))
	for _, attribute := range c.Attributes {
		if seen[attribute.Identifier] {
			return fmt.Errorf("duplicate attribute %q", attribute.Identifier)
		}
		seen[attribute.Identifier] = true
		schema, ok := catalogue[attribute.Identifier]
		if !ok {
			return fmt.Errorf("attribute %q is not in GBO's attribute catalogue", attribute.Identifier)
		}
		if err := attribute.validate(schema); err != nil {
			return fmt.Errorf("attribute %q: %w", attribute.Identifier, err)
		}
	}
	return nil
}

func (g GraphQL) validate() error {
	document, err := parser.ParseQuery(&ast.Source{Name: "asi", Input: g.Document})
	if err != nil {
		return fmt.Errorf("graphql.document does not parse: %w", err)
	}
	if len(document.Operations) != 1 {
		return fmt.Errorf("graphql.document must hold exactly one operation, has %d", len(document.Operations))
	}
	operation := document.Operations[0]
	if operation.Operation != ast.Query {
		return fmt.Errorf("graphql.document must be a query, is a %s", operation.Operation)
	}
	if operation.VariableDefinitions.ForName(g.SubjectVariable) == nil {
		return fmt.Errorf("graphql.subject_variable %q is not a variable of the query", g.SubjectVariable)
	}
	if len(document.Fragments) > 0 {
		return fmt.Errorf("graphql.document must not hold a fragment definition")
	}
	if len(operation.Directives) > 0 {
		return fmt.Errorf("graphql.document must not hold a directive")
	}
	return validatePath(operation.SelectionSet)
}

// validatePath checks that the query is one path of fields that ends in
// ...Selected alone, so the ASIP asks the source for nothing but the fields
// it derives from the requested attributes.
func validatePath(set ast.SelectionSet) error {
	const shape = "graphql.document must be one path of fields that ends in ...%s alone"
	for {
		if len(set) != 1 {
			return fmt.Errorf(shape+": a selection on the path holds %d selections, not one", selectedFragment, len(set))
		}
		switch s := set[0].(type) {
		case *ast.Field:
			if len(s.Directives) > 0 {
				return fmt.Errorf(shape+": field %q has a directive", selectedFragment, s.Name)
			}
			if len(s.SelectionSet) == 0 {
				return fmt.Errorf(shape+": the path ends in field %q", selectedFragment, s.Name)
			}
			set = s.SelectionSet
		case *ast.FragmentSpread:
			if s.Name != selectedFragment {
				return fmt.Errorf(shape+": the path ends in ...%s", selectedFragment, s.Name)
			}
			if len(s.Directives) > 0 {
				return fmt.Errorf(shape+": ...%s has a directive", selectedFragment, s.Name)
			}
			return nil
		default:
			return fmt.Errorf(shape+": the path holds an inline fragment; use on", selectedFragment)
		}
	}
}

func (a Attribute) validate(schema *ValueSchema) error {
	for member, field := range a.Fields {
		memberSchema, ok := schema.Properties[member]
		if !ok {
			return fmt.Errorf("field %q is not a member of the value_schema", member)
		}
		if err := field.validate(memberSchema); err != nil {
			return fmt.Errorf("field %q: %w", member, err)
		}
	}
	for _, member := range schema.Required {
		if _, ok := a.Fields[member]; !ok {
			return fmt.Errorf("required member %q has no mapping", member)
		}
	}
	return nil
}

func (f Field) validate(member *ValueSchema) error {
	if member.Type == "array" {
		if f.Each == "" {
			return fmt.Errorf("a list member needs each")
		}
		return nil
	}
	if f.Each != "" {
		return fmt.Errorf("each needs a list member, the member is %s", member.Type)
	}
	if f.Const != nil {
		var value any
		if err := json.Unmarshal(f.Const, &value); err != nil {
			return fmt.Errorf("const: %w", err)
		}
		if !hasType(value, member.Type) {
			return fmt.Errorf("const %s is not of type %s", f.Const, member.Type)
		}
	}
	return nil
}

func hasType(value any, jsonType string) bool {
	switch v := value.(type) {
	case string:
		return jsonType == "string"
	case bool:
		return jsonType == "boolean"
	case float64:
		return jsonType == "number" || (jsonType == "integer" && v == float64(int64(v)))
	}
	return false
}
