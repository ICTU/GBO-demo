package main

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/graphql-go/graphql"
	"github.com/graphql-go/graphql/language/ast"
	"github.com/graphql-go/graphql/language/parser"
	"github.com/graphql-go/graphql/language/source"
	"go.opentelemetry.io/otel"
)

// The BRP publishes in its source metadata how its fields make up the
// attributes the ASIP verifies. Onboarding checks only the form of that
// mapping; these tests hold it against this mock's own schema and against
// the policy rule that grants the ASIP its fields.

type asiField struct {
	Pointer string   `json:"pointer"`
	Join    []string `json:"join"`
	Each    string   `json:"each"`
}

type asiPublication struct {
	Capabilities struct {
		ASI struct {
			GraphQL struct {
				Document string `json:"document"`
			} `json:"graphql"`
			Attributes []struct {
				Identifier string              `json:"identifier"`
				On         string              `json:"on"`
				Fields     map[string]asiField `json:"fields"`
			} `json:"attributes"`
		} `json:"asi"`
	} `json:"capabilities"`
}

func readASIPublication(t *testing.T) asiPublication {
	t.Helper()
	raw, err := os.ReadFile("config/gbo-source-metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var publication asiPublication
	if err := json.Unmarshal(raw, &publication); err != nil {
		t.Fatal(err)
	}
	if len(publication.Capabilities.ASI.Attributes) == 0 {
		t.Fatal("the BRP publishes no asi attributes")
	}
	return publication
}

func testSchema(t *testing.T) graphql.Schema {
	t.Helper()
	store, err := loadMockData("mockdata/personen.json")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := buildSchema(otel.Tracer("brp-graphql-server-test"), store)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

// fieldUse is a field the mapping asks for, keyed like the policy rule:
// the type that declares it and the field name.
type fieldUse struct {
	key  string
	list bool
	leaf bool
	next graphql.Type
}

func namedType(t graphql.Type) graphql.Type {
	for {
		switch wrapped := t.(type) {
		case *graphql.NonNull:
			t = wrapped.OfType
		case *graphql.List:
			t = wrapped.OfType
		default:
			return t
		}
	}
}

func isList(t graphql.Type) bool {
	if nonNull, ok := t.(*graphql.NonNull); ok {
		t = nonNull.OfType
	}
	_, ok := t.(*graphql.List)
	return ok
}

func lookupField(parent graphql.Type, name string) (fieldUse, bool) {
	var fields graphql.FieldDefinitionMap
	switch p := parent.(type) {
	case *graphql.Object:
		fields = p.Fields()
	case *graphql.Interface:
		fields = p.Fields()
	default:
		return fieldUse{}, false
	}
	field, ok := fields[name]
	if !ok {
		return fieldUse{}, false
	}
	next := namedType(field.Type)
	_, scalar := next.(*graphql.Scalar)
	_, enum := next.(*graphql.Enum)
	return fieldUse{key: parent.Name() + "." + name, list: isList(field.Type), leaf: scalar || enum, next: next}, true
}

// walk follows a pointer from parent and returns the fields on its way.
func walk(t *testing.T, parent graphql.Type, pointer string) []fieldUse {
	t.Helper()
	var uses []fieldUse
	for _, segment := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		use, ok := lookupField(parent, segment)
		if !ok {
			t.Fatalf("pointer %s: %s has no field %q", pointer, parent.Name(), segment)
		}
		uses = append(uses, use)
		parent = use.next
	}
	return uses
}

// rootPath returns the fields from Query to ...Selected and the type
// ...Selected stands in.
func rootPath(t *testing.T, schema graphql.Schema, document string) ([]fieldUse, graphql.Type) {
	t.Helper()
	parsed, err := parser.Parse(parser.ParseParams{Source: source.NewSource(&source.Source{Body: []byte(document)})})
	if err != nil {
		t.Fatal(err)
	}
	operation := parsed.Definitions[0].(*ast.OperationDefinition)
	var uses []fieldUse
	var parent graphql.Type = schema.QueryType()
	set := operation.SelectionSet
	for {
		field, ok := set.Selections[0].(*ast.Field)
		if !ok {
			return uses, parent
		}
		use, found := lookupField(parent, field.Name.Value)
		if !found {
			t.Fatalf("document: %s has no field %q", parent.Name(), field.Name.Value)
		}
		uses = append(uses, use)
		parent, set = use.next, field.SelectionSet
	}
}

// mappedFields returns every field the BRP's asi mapping asks for, root
// path included, after checking each against the schema.
func mappedFields(t *testing.T) []string {
	t.Helper()
	schema := testSchema(t)
	publication := readASIPublication(t)
	root, selected := rootPath(t, schema, publication.Capabilities.ASI.GraphQL.Document)
	var keys []string
	for _, use := range root {
		keys = append(keys, use.key)
	}
	for _, attribute := range publication.Capabilities.ASI.Attributes {
		parent := selected
		if attribute.On != "" {
			on := schema.Type(attribute.On)
			object, ok := on.(*graphql.Object)
			if !ok {
				t.Fatalf("%s: on %q is not an object type", attribute.Identifier, attribute.On)
			}
			abstract, ok := selected.(graphql.Abstract)
			if !ok || !schema.IsPossibleType(abstract, object) {
				t.Fatalf("%s: on %q is not a possible type of %s", attribute.Identifier, attribute.On, selected.Name())
			}
			parent = object
		}
		for member, field := range attribute.Fields {
			var leaves [][]fieldUse
			switch {
			case field.Each != "":
				list := walk(t, parent, field.Each)
				last := list[len(list)-1]
				if !last.list {
					t.Fatalf("%s %s: each %s is not a list", attribute.Identifier, member, field.Each)
				}
				for _, use := range list {
					keys = append(keys, use.key)
				}
				leaves = append(leaves, walk(t, last.next, field.Pointer))
			case len(field.Join) > 0:
				for _, part := range field.Join {
					leaves = append(leaves, walk(t, parent, part))
				}
			case field.Pointer != "":
				leaves = append(leaves, walk(t, parent, field.Pointer))
			}
			for _, uses := range leaves {
				last := uses[len(uses)-1]
				if !last.leaf || last.list {
					t.Fatalf("%s %s: %s is not a single value", attribute.Identifier, member, last.key)
				}
				for _, use := range uses {
					keys = append(keys, use.key)
				}
			}
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// B2: every pointer of the mapping is a field of this mock's schema.
func TestASIMappingFitsTheSchema(t *testing.T) {
	if keys := mappedFields(t); len(keys) == 0 {
		t.Fatal("the mapping asks for no fields")
	}
}

// B3: the policy rule that grants the ASIP its fields allows every field
// the mapping asks for, so the ASIP's derived query passes the PDP.
func TestASIMappingFitsThePolicy(t *testing.T) {
	rule, err := os.ReadFile("../../policies/gbo/rules/asi/asi0001.rego")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)covers_fields := \{(.*?)\}`).FindSubmatch(rule)
	if block == nil {
		t.Fatal("asi0001.rego has no covers_fields")
	}
	covered := map[string]bool{}
	for _, match := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(block[1], -1) {
		covered[string(match[1])] = true
	}
	for _, key := range mappedFields(t) {
		if !covered[key] {
			t.Errorf("ASI0001 does not cover %s", key)
		}
	}
}
