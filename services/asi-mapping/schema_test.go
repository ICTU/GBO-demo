package asimapping

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// The cases in this file hold the source metadata schema itself: what any
// validator rejects before GBO's own checks run.

const (
	metadataSchemaPath = "../../schemas/gbo-source-metadata-v1.schema.json"
	simpleSchemaPath   = "../../schemas/gbo-simple-v1.schema.json"
	brpMetadataPath    = "../brp-graphql-server/config/gbo-source-metadata.json"
)

// compileMetadataSchema compiles the schema the way the reconciler does,
// with the mapping profile registered under its URN.
func compileMetadataSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	simple, err := os.ReadFile(simpleSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := jsonschema.UnmarshalJSON(bytes.NewReader(simple))
	if err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource("urn:gov:nl:gbo:schema:gbo-simple-v1:1", resource); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(metadataSchemaPath)
	if err != nil {
		t.Fatalf("compile source metadata schema: %v", err)
	}
	return schema
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return value
}

// brpDocument is the BRP's published metadata: eudi and asi, at 1.1.
func brpDocument(t *testing.T) map[string]any {
	t.Helper()
	return readJSON(t, brpMetadataPath)
}

func asiOf(document map[string]any) map[string]any {
	return document["capabilities"].(map[string]any)["asi"].(map[string]any)
}

// attributeOf returns the attribute with the identifier's name, e.g.
// "address" for https://gbo.example/attributes/address/1.
func attributeOf(document map[string]any, name string) map[string]any {
	for _, item := range asiOf(document)["attributes"].([]any) {
		attribute := item.(map[string]any)
		if strings.Contains(attribute["identifier"].(string), "/attributes/"+name+"/") {
			return attribute
		}
	}
	panic("no attribute " + name)
}

func fieldsOf(document map[string]any, name string) map[string]any {
	return attributeOf(document, name)["fields"].(map[string]any)
}

func validateDocument(schema *jsonschema.Schema, document any) error {
	// Round-trip so the validator sees JSON values, not Go maps of any.
	data, err := json.Marshal(document)
	if err != nil {
		return err
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	return schema.Validate(value)
}

func TestMetadataSchemaAccepts(t *testing.T) {
	schema := compileMetadataSchema(t)

	t.Run("A1 eudi and asi at 1.1", func(t *testing.T) {
		document := brpDocument(t)
		if document["schema_version"] != "1.1" {
			t.Fatalf("schema_version = %v, want 1.1", document["schema_version"])
		}
		if err := validateDocument(schema, document); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("A2 published 1.0 documents", func(t *testing.T) {
		paths := []string{
			"../graphql-server/config/gbo-source-metadata.json",
			"../graphql-server/config/gbo-source-metadata-unsecured.json",
		}
		operatorManaged, err := filepath.Glob("../../source-metadata/*/*.json")
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range append(paths, operatorManaged...) {
			t.Run(path, func(t *testing.T) {
				document := readJSON(t, path)
				if document["schema_version"] != "1.0" {
					t.Fatalf("schema_version = %v, want 1.0", document["schema_version"])
				}
				if err := validateDocument(schema, document); err != nil {
					t.Fatal(err)
				}
			})
		}
	})

	t.Run("A3 asi only", func(t *testing.T) {
		document := brpDocument(t)
		delete(document["capabilities"].(map[string]any), "eudi")
		if err := validateDocument(schema, document); err != nil {
			t.Fatal(err)
		}
	})
}

func TestMetadataSchemaRejects(t *testing.T) {
	schema := compileMetadataSchema(t)

	cases := []struct {
		name   string
		mutate func(document map[string]any)
	}{
		{"W1 asi at 1.0", func(d map[string]any) {
			d["schema_version"] = "1.0"
		}},
		{"W9 pointer and const", func(d map[string]any) {
			fieldsOf(d, "address")["country"] = map[string]any{"const": "NL", "pointer": "/woontOp/land"}
		}},
		{"W9 pointer and join", func(d map[string]any) {
			fieldsOf(d, "family_name")["family_name"].(map[string]any)["pointer"] = "/geslachtsnaam"
		}},
		{"W9 no operation", func(d map[string]any) {
			fieldsOf(d, "given_name")["given_name"] = map[string]any{}
		}},
		{"W9 each without pointer", func(d map[string]any) {
			fieldsOf(d, "nationality")["nationalities"] = map[string]any{"each": "/heeftNationaliteit"}
		}},
		{"W10 pointer without leading slash", func(d map[string]any) {
			fieldsOf(d, "address")["street"] = map[string]any{"pointer": "woontOp/straatnaam"}
		}},
		{"W10 pointer with array index", func(d map[string]any) {
			fieldsOf(d, "nationality")["nationalities"] = map[string]any{"each": "/heeftNationaliteit/0", "pointer": "/nationaliteit"}
		}},
		{"W10 pointer segment not a GraphQL name", func(d map[string]any) {
			fieldsOf(d, "address")["street"] = map[string]any{"pointer": "/woont-op/straatnaam"}
		}},
		{"W10 empty pointer", func(d map[string]any) {
			fieldsOf(d, "given_name")["given_name"] = map[string]any{"pointer": ""}
		}},
		{"W10 join part not a pointer", func(d map[string]any) {
			fieldsOf(d, "family_name")["family_name"] = map[string]any{"join": []any{"/voorvoegsel", "geslachtsnaam"}, "separator": " "}
		}},
		{"W10 each item pointer with array index", func(d map[string]any) {
			fieldsOf(d, "nationality")["nationalities"] = map[string]any{"each": "/heeftNationaliteit", "pointer": "/0"}
		}},
		{"W11 join with one part", func(d map[string]any) {
			fieldsOf(d, "family_name")["family_name"] = map[string]any{"join": []any{"/geslachtsnaam"}, "separator": " "}
		}},
		{"W11 join without separator", func(d map[string]any) {
			fieldsOf(d, "family_name")["family_name"] = map[string]any{"join": []any{"/voorvoegsel", "/geslachtsnaam"}}
		}},
		{"W14 unknown key in asi", func(d map[string]any) {
			asiOf(d)["policy"] = "ASI0001"
		}},
		{"W14 unknown key in attribute", func(d map[string]any) {
			attributeOf(d, "address")["fuzzy"] = true
		}},
		{"W14 unknown key in graphql", func(d map[string]any) {
			asiOf(d)["graphql"].(map[string]any)["parameters"] = map[string]any{}
		}},
		{"W14 on not a GraphQL type name", func(d map[string]any) {
			attributeOf(d, "address")["on"] = "Ingezetene { woontOp }"
		}},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			document := brpDocument(t)
			test.mutate(document)
			if err := validateDocument(schema, document); err == nil {
				t.Fatal("schema accepted the document")
			}
		})
	}
}
