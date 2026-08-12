package gbosimplev1

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed testdata/cases.json
var conformanceCases []byte

type conformanceFile struct {
	ValidMappings   []mappingCase    `json:"valid_mappings"`
	InvalidMappings []mappingCase    `json:"invalid_mappings"`
	Projections     []projectionCase `json:"projections"`
}

type mappingCase struct {
	Name      string          `json:"name"`
	Mapping   json.RawMessage `json:"mapping"`
	ErrorCode ErrorCode       `json:"error_code"`
}

type projectionCase struct {
	Name          string          `json:"name"`
	Input         any             `json:"input"`
	ResultPointer string          `json:"result_pointer"`
	MappingRef    string          `json:"mapping_ref"`
	Mapping       json.RawMessage `json:"mapping"`
	Outcome       Outcome         `json:"outcome"`
	Claims        map[string]any  `json:"claims"`
	ErrorCode     ErrorCode       `json:"error_code"`
}

func TestConformanceMappings(t *testing.T) {
	cases := loadConformance(t)
	schema, err := jsonschema.NewCompiler().Compile("../../../../schemas/gbo-simple-v1.schema.json")
	if err != nil {
		t.Fatalf("compile gbo-simple-v1 schema: %v", err)
	}

	for _, test := range cases.ValidMappings {
		t.Run("valid/"+test.Name, func(t *testing.T) {
			mapping := decodeMapping(t, test.Mapping)
			if err := Validate(mapping); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			validateRawMapping(t, schema, test.Mapping, true)
		})
	}
	for _, test := range cases.InvalidMappings {
		t.Run("invalid/"+test.Name, func(t *testing.T) {
			var mapping Mapping
			err := json.Unmarshal(test.Mapping, &mapping)
			if err == nil {
				err = Validate(mapping)
			}
			gotCode := ErrorCodeOf(err)
			if gotCode == "" && err != nil {
				gotCode = CodeMappingInvalid
			}
			if gotCode != test.ErrorCode {
				t.Fatalf("error code = %q (%v), want %q", gotCode, err, test.ErrorCode)
			}
			validateRawMapping(t, schema, test.Mapping, false)
		})
	}
}

func TestConformanceProjections(t *testing.T) {
	cases := loadConformance(t)
	mappings := make(map[string]Mapping, len(cases.ValidMappings))
	for _, test := range cases.ValidMappings {
		mappings[test.Name] = decodeMapping(t, test.Mapping)
	}

	for _, test := range cases.Projections {
		t.Run(test.Name, func(t *testing.T) {
			mapping := mappings[test.MappingRef]
			if len(test.Mapping) > 0 {
				mapping = decodeMapping(t, test.Mapping)
			}
			projection, err := Project(test.Input, test.ResultPointer, mapping)
			if gotCode := ErrorCodeOf(err); gotCode != test.ErrorCode {
				t.Fatalf("error code = %q (%v), want %q", gotCode, err, test.ErrorCode)
			}
			if test.ErrorCode != "" {
				if len(projection.Claims) != 0 {
					t.Fatalf("failed projection returned partial claims: %#v", projection.Claims)
				}
				return
			}
			if projection.Outcome != test.Outcome {
				t.Fatalf("outcome = %q, want %q", projection.Outcome, test.Outcome)
			}
			if !EqualJSON(projection.Claims, test.Claims) {
				t.Fatalf("claims = %#v, want %#v", projection.Claims, test.Claims)
			}
		})
	}
}

func TestEqualJSONNormalisesNumericGoTypes(t *testing.T) {
	if !EqualJSON(int64(2025), float64(2025)) {
		t.Fatal("equal JSON integers with different Go types did not compare equal")
	}
	if EqualJSON(json.Number("1.01"), float64(1)) {
		t.Fatal("different JSON numbers compared equal")
	}
}

func TestProjectPreservesDecimalNumber(t *testing.T) {
	root, err := DecodeJSON([]byte(`{"rows":[{"amount":43000.50}]}`))
	if err != nil {
		t.Fatalf("DecodeJSON() error = %v", err)
	}
	projection, err := Project(root, "/rows", Mapping{
		"amount": {Pointer: "/amount", Datatype: "number"},
	})
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	amount, ok := projection.Claims["amount"].(json.Number)
	if !ok || amount.String() != "43000.50" {
		t.Fatalf("amount = %#v, want unchanged json.Number(43000.50)", projection.Claims["amount"])
	}
}

func TestProjectRejectsNumbersNotProducedByDecodeJSON(t *testing.T) {
	var root any
	if err := json.Unmarshal([]byte(`{"rows":[{"value":43000.50}]}`), &root); err != nil {
		t.Fatalf("plain json.Unmarshal() error = %v", err)
	}
	for _, datatype := range []string{"number", "integer", "gYear"} {
		t.Run(datatype, func(t *testing.T) {
			_, err := Project(root, "/rows", Mapping{
				"value": {Pointer: "/value", Datatype: datatype},
			})
			if got := ErrorCodeOf(err); got != CodeTypeMismatch {
				t.Fatalf("error code = %q (%v), want %q", got, err, CodeTypeMismatch)
			}
		})
	}
}

func TestProjectRejectsNonCanonicalArrayIndices(t *testing.T) {
	root, err := DecodeJSON([]byte(`{"rows":[{"list":["zero"]}]}`))
	if err != nil {
		t.Fatalf("DecodeJSON() error = %v", err)
	}
	for _, pointer := range []string{"/list/+0", "/list/-0", "/list/00"} {
		t.Run(pointer, func(t *testing.T) {
			_, err := Project(root, "/rows", Mapping{
				"value": {Pointer: pointer, Datatype: "string"},
			})
			if got := ErrorCodeOf(err); got != CodePathMissing {
				t.Fatalf("error code = %q (%v), want %q", got, err, CodePathMissing)
			}
		})
	}
}

func TestEqualJSONDoesNotPanicForNonJSONDynamicValues(t *testing.T) {
	left := map[string]string{"answer": "same"}
	right := map[string]string{"answer": "same"}
	if !EqualJSON(left, right) {
		t.Fatal("equal comparable maps did not compare equal")
	}
}

func loadConformance(t *testing.T) conformanceFile {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(conformanceCases))
	decoder.UseNumber()
	var cases conformanceFile
	if err := decoder.Decode(&cases); err != nil {
		t.Fatalf("decode conformance cases: %v", err)
	}
	return cases
}

func decodeMapping(t *testing.T, raw json.RawMessage) Mapping {
	t.Helper()
	var mapping Mapping
	if err := json.Unmarshal(raw, &mapping); err != nil {
		t.Fatalf("decode mapping: %v", err)
	}
	return mapping
}

func validateRawMapping(t *testing.T, schema *jsonschema.Schema, raw json.RawMessage, wantValid bool) {
	t.Helper()
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse mapping as JSON: %v", err)
	}
	err = schema.Validate(value)
	if wantValid && err != nil {
		t.Fatalf("mapping rejected by schema: %v", err)
	}
	if !wantValid && err == nil {
		t.Fatal("invalid mapping accepted by schema")
	}
}
