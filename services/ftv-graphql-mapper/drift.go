package ftvgraphql

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// Guarantee H3 (Sections 7.4 and 11): for every element of the bundled
// schema, the source's running schema has the same definition. The source
// may have more types, and more fields on a type; it may not differ on what
// the bundle names. Descriptions, deprecations and other directives are not
// compared. Enum values are, as a set: a value the bundle does not know can
// still come back in a field the policy released.

// CapabilityQuery asks a source whether its introspection takes
// includeDeprecated on arguments and input fields. Where it does, a
// deprecated argument or input field is hidden unless asked for, and could
// slip past the check. Where it does not, the server predates deprecating
// them and hides none; graphql-go, which the demo sources use, is such a
// server, and rejects the argument.
const CapabilityQuery = `query FTVSchemaCheckCapabilities {
  field: __type(name: "__Field") { fields { name args { name } } }
  type: __type(name: "__Type") { fields { name args { name } } }
}`

// Capabilities are what a source's introspection supports.
type Capabilities struct {
	DeprecatedArgs        bool // __Field.args(includeDeprecated:)
	DeprecatedInputFields bool // __Type.inputFields(includeDeprecated:)
}

// ParseCapabilities reads the answer to CapabilityQuery.
func ParseCapabilities(data []byte) (Capabilities, error) {
	type meta struct {
		Fields []struct {
			Name string `json:"name"`
			Args []struct {
				Name string `json:"name"`
			} `json:"args"`
		} `json:"fields"`
	}
	var resp struct {
		Data struct {
			Field *meta `json:"field"`
			Type  *meta `json:"type"`
		} `json:"data"`
		Errors json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return Capabilities{}, fmt.Errorf("capability response: %w", err)
	}
	if len(resp.Errors) > 0 && string(resp.Errors) != "null" {
		return Capabilities{}, fmt.Errorf("capability response carries errors: %s", resp.Errors)
	}
	if resp.Data.Field == nil || resp.Data.Type == nil {
		return Capabilities{}, fmt.Errorf("capability response lacks __Field or __Type")
	}
	takes := func(m *meta, field string) bool {
		for _, f := range m.Fields {
			if f.Name != field {
				continue
			}
			for _, a := range f.Args {
				if a.Name == "includeDeprecated" {
					return true
				}
			}
		}
		return false
	}
	return Capabilities{
		DeprecatedArgs:        takes(resp.Data.Field, "args"),
		DeprecatedInputFields: takes(resp.Data.Type, "inputFields"),
	}, nil
}

// IntrospectionQuery asks a source for what Drift compares, deprecated
// elements included wherever the source can hide them.
func IntrospectionQuery(c Capabilities) string {
	args, inputFields := "args", "inputFields"
	if c.DeprecatedArgs {
		args = "args(includeDeprecated: true)"
	}
	if c.DeprecatedInputFields {
		inputFields = "inputFields(includeDeprecated: true)"
	}
	return `query FTVSchemaCheck {
  __schema {
    queryType { name }
    mutationType { name }
    subscriptionType { name }
    types {
      kind
      name
      fields(includeDeprecated: true) { name ` + args + ` { ...InputValue } type { ...TypeRef } }
      ` + inputFields + ` { ...InputValue }
      possibleTypes { name }
      enumValues(includeDeprecated: true) { name }
    }
  }
}
fragment InputValue on __InputValue { name defaultValue type { ...TypeRef } }
fragment TypeRef on __Type {
  kind name
  ofType { kind name ofType { kind name ofType { kind name ofType { kind name
    ofType { kind name ofType { kind name ofType { kind name } } } } } } }
}`
}

// SourceSchema is a source's running schema, as its introspection reports it.
type SourceSchema struct {
	model schemaModel
}

// schemaModel is what H3 compares, in one form for both sides.
type schemaModel struct {
	roots map[string]string // operation → root type name
	types map[string]typeModel
}

type typeModel struct {
	kind    string
	fields  map[string]fieldModel // object and interface fields
	inputs  map[string]inputModel // input object fields
	members []string              // interface implementers, union members: sorted
	values  []string              // enum values: sorted
}

type fieldModel struct {
	typ  string
	args map[string]inputModel
}

type inputModel struct {
	typ string
	def string // canonical default literal, "" when there is none
}

// Drift lists every difference H3 forbids between this bundled schema and
// the source's, sorted. None means the check passes.
func (s *Schema) Drift(source *SourceSchema) []string {
	return drift(modelFromAST(s.schema), source.model)
}

// ParseIntrospection reads an introspection response, with or without its
// "data" envelope. A response that carries errors is refused: a partial
// schema cannot vouch for anything.
func ParseIntrospection(data []byte) (*SourceSchema, error) {
	var envelope struct {
		Data   *introspection  `json:"data"`
		Errors json.RawMessage `json:"errors"`
		introspection
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("introspection response: %w", err)
	}
	if len(envelope.Errors) > 0 && string(envelope.Errors) != "null" {
		return nil, fmt.Errorf("introspection response carries errors: %s", envelope.Errors)
	}
	in := envelope.introspection
	if envelope.Data != nil {
		in = *envelope.Data
	}
	if in.Schema == nil || in.Schema.QueryType == nil {
		return nil, fmt.Errorf("introspection response has no __schema with a query type")
	}
	model, err := modelFromIntrospection(in.Schema)
	if err != nil {
		return nil, err
	}
	return &SourceSchema{model: model}, nil
}

func drift(bundled, source schemaModel) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }

	for op, name := range bundled.roots {
		if got := source.roots[op]; got != name {
			add("%s root type: %s in copy, %s in source", op, name, orNone(got))
		}
	}
	for name, b := range bundled.types {
		src, ok := source.types[name]
		if !ok {
			add("type %s: missing in source", name)
			continue
		}
		if b.kind != src.kind {
			add("type %s: %s in copy, %s in source", name, b.kind, src.kind)
			continue
		}
		for fname, bf := range b.fields {
			sf, ok := src.fields[fname]
			if !ok {
				add("%s.%s: missing in source", name, fname)
				continue
			}
			if bf.typ != sf.typ {
				add("%s.%s: returns %s in copy, %s in source", name, fname, bf.typ, sf.typ)
			}
			out = append(out, inputDrift(name+"."+fname, "argument", bf.args, sf.args)...)
		}
		out = append(out, inputDrift(name, "input field", b.inputs, src.inputs)...)
		if !equalStrings(b.members, src.members) {
			add("type %s: members [%s] in copy, [%s] in source", name, strings.Join(b.members, ", "), strings.Join(src.members, ", "))
		}
		if !equalStrings(b.values, src.values) {
			add("enum %s: values [%s] in copy, [%s] in source", name, strings.Join(b.values, ", "), strings.Join(src.values, ", "))
		}
	}
	sort.Strings(out)
	return out
}

// inputDrift compares arguments or input fields: the same names, no more and
// no fewer, each with the same type and the same default.
func inputDrift(owner, what string, bundled, source map[string]inputModel) []string {
	var out []string
	for name, b := range bundled {
		s, ok := source[name]
		if !ok {
			out = append(out, fmt.Sprintf("%s(%s): %s missing in source", owner, name, what))
			continue
		}
		if b.typ != s.typ {
			out = append(out, fmt.Sprintf("%s(%s): %s of type %s in copy, %s in source", owner, name, what, b.typ, s.typ))
		}
		if b.def != s.def {
			out = append(out, fmt.Sprintf("%s(%s): default %s in copy, %s in source", owner, name, orNone(b.def), orNone(s.def)))
		}
	}
	for name := range source {
		if _, ok := bundled[name]; !ok {
			out = append(out, fmt.Sprintf("%s(%s): %s only in source", owner, name, what))
		}
	}
	return out
}

func modelFromAST(s *ast.Schema) schemaModel {
	m := schemaModel{roots: map[string]string{}, types: map[string]typeModel{}}
	for op, def := range map[string]*ast.Definition{"query": s.Query, "mutation": s.Mutation, "subscription": s.Subscription} {
		if def != nil {
			m.roots[op] = def.Name
		}
	}
	for name, def := range s.Types {
		if def.BuiltIn || strings.HasPrefix(name, "__") {
			continue
		}
		t := typeModel{kind: string(def.Kind)}
		switch def.Kind {
		case ast.Object, ast.Interface:
			t.fields = map[string]fieldModel{}
			for _, f := range def.Fields {
				if strings.HasPrefix(f.Name, "__") {
					continue
				}
				args := map[string]inputModel{}
				for _, a := range f.Arguments {
					args[a.Name] = inputModel{typ: a.Type.String(), def: canonicalValue(a.DefaultValue)}
				}
				t.fields[f.Name] = fieldModel{typ: f.Type.String(), args: args}
			}
		case ast.InputObject:
			t.inputs = map[string]inputModel{}
			for _, f := range def.Fields {
				t.inputs[f.Name] = inputModel{typ: f.Type.String(), def: canonicalValue(f.DefaultValue)}
			}
		case ast.Enum:
			for _, v := range def.EnumValues {
				t.values = append(t.values, v.Name)
			}
			sort.Strings(t.values)
		}
		// Object types only, as introspection reports them: an interface that
		// implements this one is no member of its own, its objects are.
		if def.Kind == ast.Interface || def.Kind == ast.Union {
			for _, p := range s.PossibleTypes[name] {
				if p.Kind == ast.Object {
					t.members = append(t.members, p.Name)
				}
			}
			sort.Strings(t.members)
		}
		m.types[name] = t
	}
	return m
}

type introspection struct {
	Schema *introspectionSchema `json:"__schema"`
}

type introspectionSchema struct {
	QueryType        *namedRef           `json:"queryType"`
	MutationType     *namedRef           `json:"mutationType"`
	SubscriptionType *namedRef           `json:"subscriptionType"`
	Types            []introspectionType `json:"types"`
}

type namedRef struct {
	Name string `json:"name"`
}

type introspectionType struct {
	Kind          string               `json:"kind"`
	Name          string               `json:"name"`
	Fields        []introspectionField `json:"fields"`
	InputFields   []introspectionInput `json:"inputFields"`
	PossibleTypes []namedRef           `json:"possibleTypes"`
	EnumValues    []namedRef           `json:"enumValues"`
}

type introspectionField struct {
	Name string               `json:"name"`
	Args []introspectionInput `json:"args"`
	Type typeRef              `json:"type"`
}

type introspectionInput struct {
	Name         string  `json:"name"`
	DefaultValue *string `json:"defaultValue"`
	Type         typeRef `json:"type"`
}

type typeRef struct {
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	OfType *typeRef `json:"ofType"`
}

// String renders a type reference as SDL does: [Aangifte!]!.
func (r typeRef) String() string {
	switch {
	case r.Kind == "NON_NULL" && r.OfType != nil:
		return r.OfType.String() + "!"
	case r.Kind == "LIST" && r.OfType != nil:
		return "[" + r.OfType.String() + "]"
	}
	return r.Name
}

func modelFromIntrospection(s *introspectionSchema) (schemaModel, error) {
	m := schemaModel{roots: map[string]string{}, types: map[string]typeModel{}}
	for op, ref := range map[string]*namedRef{"query": s.QueryType, "mutation": s.MutationType, "subscription": s.SubscriptionType} {
		if ref != nil && ref.Name != "" {
			m.roots[op] = ref.Name
		}
	}
	inputs := func(owner string, in []introspectionInput) (map[string]inputModel, error) {
		out := map[string]inputModel{}
		for _, a := range in {
			def := ""
			if a.DefaultValue != nil {
				v, err := parseLiteral(*a.DefaultValue)
				if err != nil {
					return nil, fmt.Errorf("%s(%s): default %q: %w", owner, a.Name, *a.DefaultValue, err)
				}
				def = canonicalValue(v)
			}
			out[a.Name] = inputModel{typ: a.Type.String(), def: def}
		}
		return out, nil
	}
	for _, it := range s.Types {
		if strings.HasPrefix(it.Name, "__") {
			continue
		}
		t := typeModel{kind: it.Kind}
		switch it.Kind {
		case "OBJECT", "INTERFACE":
			t.fields = map[string]fieldModel{}
			for _, f := range it.Fields {
				args, err := inputs(it.Name+"."+f.Name, f.Args)
				if err != nil {
					return schemaModel{}, err
				}
				t.fields[f.Name] = fieldModel{typ: f.Type.String(), args: args}
			}
		case "INPUT_OBJECT":
			in, err := inputs(it.Name, it.InputFields)
			if err != nil {
				return schemaModel{}, err
			}
			t.inputs = in
		case "ENUM":
			for _, v := range it.EnumValues {
				t.values = append(t.values, v.Name)
			}
			sort.Strings(t.values)
		}
		if it.Kind == "INTERFACE" || it.Kind == "UNION" {
			for _, p := range it.PossibleTypes {
				t.members = append(t.members, p.Name)
			}
			sort.Strings(t.members)
		}
		m.types[it.Name] = t
	}
	return m, nil
}

// parseLiteral reads a GraphQL value literal, as introspection reports a
// default.
func parseLiteral(literal string) (*ast.Value, error) {
	doc, err := parser.ParseQuery(&ast.Source{Input: "{ f(a: " + literal + ") }"})
	if err != nil {
		return nil, err
	}
	op := doc.Operations[0]
	if len(op.SelectionSet) != 1 {
		return nil, fmt.Errorf("not a single value")
	}
	field, ok := op.SelectionSet[0].(*ast.Field)
	if !ok || len(field.Arguments) != 1 {
		return nil, fmt.Errorf("not a single value")
	}
	return field.Arguments[0].Value, nil
}

// canonicalValue renders a value so that equal values compare equal: no
// whitespace, block strings as strings, object fields in name order.
func canonicalValue(v *ast.Value) string {
	if v == nil {
		return ""
	}
	switch v.Kind {
	case ast.StringValue, ast.BlockValue:
		b, _ := json.Marshal(v.Raw)
		return string(b)
	case ast.ListValue:
		parts := make([]string, len(v.Children))
		for i, c := range v.Children {
			parts[i] = canonicalValue(c.Value)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case ast.ObjectValue:
		parts := make([]string, len(v.Children))
		for i, c := range v.Children {
			parts[i] = c.Name + ":" + canonicalValue(c.Value)
		}
		sort.Strings(parts)
		return "{" + strings.Join(parts, ",") + "}"
	case ast.Variable:
		return "$" + v.Raw
	}
	return v.Raw
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
