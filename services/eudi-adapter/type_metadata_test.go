package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestPublishedTypeMetadataIsBoundToItsExactBytes(t *testing.T) {
	definition := sourceAttestationDefinition{
		TypeID:      "inkomensverklaring",
		TypeVersion: "1.0",
		Display:     testDisplay("Inkomensverklaring"),
		Claims:      testClaims(map[string]mappingRule{"belastingjaar": {Pointer: "/belastingjaar", Datatype: "gYear"}}),
	}
	publication, err := newTypeMetadataPublication(
		"https://issuer.example",
		"belastingdienst",
		definition,
	)
	if err != nil {
		t.Fatalf("new type metadata publication: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, publication.VCT, nil)
	recorder := httptest.NewRecorder()
	publication.ServeHTTP(recorder, req)
	resp := recorder.Result()
	defer resp.Body.Close()
	if got, want := resp.StatusCode, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got, want := resp.Header.Get("Content-Type"), "application/json"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if etag := resp.Header.Get("ETag"); etag == "" {
		t.Error("published Type Metadata has no ETag")
	} else {
		conditional := httptest.NewRequest(http.MethodGet, publication.VCT, nil)
		conditional.Header.Set("If-None-Match", etag)
		conditionalRecorder := httptest.NewRecorder()
		publication.ServeHTTP(conditionalRecorder, conditional)
		if got, want := conditionalRecorder.Code, http.StatusNotModified; got != want {
			t.Errorf("conditional status = %d, want %d", got, want)
		}
	}
	if got, want := publication.VCT, "https://issuer.example/types/belastingdienst/inkomensverklaring/v1.0"; got != want {
		t.Errorf("VCT = %q, want %q", got, want)
	}

	body := recorder.Body.Bytes()
	var metadata map[string]any
	if err := json.Unmarshal(body, &metadata); err != nil {
		t.Fatalf("decode published Type Metadata: %v", err)
	}
	if got := metadata["vct"]; got != publication.VCT {
		t.Errorf("published vct = %v, want %q", got, publication.VCT)
	}
	schema := metadata["schema"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	if properties["vct"] == nil || properties["vct#integrity"] == nil {
		t.Fatalf("managed credential claims are missing from schema: %v", properties)
	}
	digest := sha256.Sum256(body)
	wantIntegrity := "sha256-" + base64.StdEncoding.EncodeToString(digest[:])
	if got := publication.Integrity; got != wantIntegrity {
		t.Errorf("integrity = %q, want %q", got, wantIntegrity)
	}
}

func TestTypeMetadataIdentityUsesSourceIDInsteadOfSharedOIN(t *testing.T) {
	definition := sourceAttestationDefinition{
		TypeID: "shared-type", TypeVersion: "1.0", Display: testDisplay("Shared"),
		Claims: testClaims(map[string]mappingRule{"value": {Pointer: "/value", Datatype: "string"}}),
	}
	belastingdienst, err := newTypeMetadataPublication("https://issuer.example", "belastingdienst", definition)
	if err != nil {
		t.Fatal(err)
	}
	rvig, err := newTypeMetadataPublication("https://issuer.example", "rvig", definition)
	if err != nil {
		t.Fatal(err)
	}
	if belastingdienst.VCT == rvig.VCT || belastingdienst.path == rvig.path {
		t.Fatalf("shared type collided: belastingdienst=%q rvig=%q", belastingdienst.VCT, rvig.VCT)
	}
}

func TestPublishedSchemaAcceptsIssuerManagedVCTClaims(t *testing.T) {
	definition := sourceAttestationDefinition{
		TypeID: "example", TypeVersion: "1.0", Display: testDisplay("Example"),
		Claims: testClaims(map[string]mappingRule{"name": {Pointer: "/name", Datatype: "string"}}),
	}
	publication, err := newTypeMetadataPublication("https://issuer.example", "99999999900000000200", definition)
	if err != nil {
		t.Fatal(err)
	}
	schema := compilePublishedSchema(t, publication)
	credential := map[string]any{
		"vct":           publication.VCT,
		"vct#integrity": publication.Integrity,
		"name":          "Example",
	}
	if err := schema.Validate(credential); err != nil {
		t.Fatalf("credential with issuer-managed vct claims did not validate: %v", err)
	}
}

// The generated schema is exactly the mapping: a claim the mapping always
// fills is required, an optional one is not, and every value type follows
// from the datatype the mapping copies unchanged.
func TestGeneratedSchemaFollowsTheClaims(t *testing.T) {
	definition := sourceAttestationDefinition{
		TypeID: "example", TypeVersion: "1.0", Display: testDisplay("Example"),
		Claims: testClaims(map[string]mappingRule{
			"naam":          {Pointer: "/naam", Datatype: "string"},
			"voorvoegsel":   {Pointer: "/voorvoegsel", Datatype: "string", Optional: true},
			"geboortedatum": {Pointer: "/geboortedatum", Datatype: "date"},
			"belastingjaar": {Pointer: "/belastingjaar", Datatype: "gYear"},
			"bedrag":        {Pointer: "/bedrag", Datatype: "integer"},
			"actief":        {Pointer: "/actief", Datatype: "boolean"},
		}),
	}
	publication, err := newTypeMetadataPublication("https://issuer.example", "99999999900000000200", definition)
	if err != nil {
		t.Fatal(err)
	}
	metadata := decodePublishedMetadata(t, publication)
	schema := metadata["schema"].(map[string]any)
	required := make([]string, 0)
	for _, claim := range schema["required"].([]any) {
		required = append(required, claim.(string))
	}
	if want := []string{"actief", "bedrag", "belastingjaar", "geboortedatum", "naam", "vct", "vct#integrity"}; !slices.Equal(required, want) {
		t.Errorf("required = %v, want %v", required, want)
	}
	properties := schema["properties"].(map[string]any)
	for claim, want := range map[string]map[string]any{
		"naam":          {"type": "string"},
		"voorvoegsel":   {"type": "string"},
		"geboortedatum": {"type": "string", "format": "date"},
		"belastingjaar": {"type": "integer"},
		"bedrag":        {"type": "integer"},
		"actief":        {"type": "boolean"},
	} {
		if got := properties[claim]; !reflect.DeepEqual(got, want) {
			t.Errorf("property %q = %v, want %v", claim, got, want)
		}
	}

	compiled := compilePublishedSchema(t, publication)
	withoutOptional := map[string]any{
		"vct": publication.VCT, "vct#integrity": publication.Integrity,
		"naam": "Jansen", "geboortedatum": "1990-01-01", "belastingjaar": 2025, "bedrag": 43000, "actief": true,
	}
	if err := compiled.Validate(withoutOptional); err != nil {
		t.Errorf("credential without the optional claim did not validate: %v", err)
	}
	delete(withoutOptional, "naam")
	if err := compiled.Validate(withoutOptional); err == nil {
		t.Error("credential without a non-optional claim validated")
	}
}

func TestGeneratedClaimsCarryLabelsSDAndSVGID(t *testing.T) {
	definition := sourceAttestationDefinition{
		TypeID: "example", TypeVersion: "1.0",
		Display: sourceDisplays{{Lang: "nl-NL", Name: "Inkomen", Summary: "€{{verzamelinkomen}}"}},
		Claims: sourceClaims{
			{
				Name: "verzamelinkomen", Source: mappingRule{Pointer: "/waarde", Datatype: "integer"},
				Label:       localizedText{{Lang: "nl-NL", Text: "Verzamelinkomen"}, {Lang: "en-US", Text: "Aggregate income"}},
				Description: localizedText{{Lang: "nl-NL", Text: "Bedrag in euro's"}},
			},
			{
				Name: "belastingjaar", Source: mappingRule{Pointer: "/jaar", Datatype: "gYear"},
				Label: localizedText{{Lang: "nl-NL", Text: "Belastingjaar"}}, SD: "never",
			},
		},
	}
	publication, err := newTypeMetadataPublication("https://issuer.example", "99999999900000000200", definition)
	if err != nil {
		t.Fatal(err)
	}
	claims := decodePublishedMetadata(t, publication)["claims"]
	want := []any{
		map[string]any{
			"path": []any{"verzamelinkomen"}, "sd": "always", "svg_id": "verzamelinkomen",
			"display": []any{
				map[string]any{"lang": "nl-NL", "label": "Verzamelinkomen", "description": "Bedrag in euro's"},
				map[string]any{"lang": "en-US", "label": "Aggregate income"},
			},
		},
		map[string]any{
			"path": []any{"belastingjaar"}, "sd": "never", "svg_id": "belastingjaar",
			"display": []any{map[string]any{"lang": "nl-NL", "label": "Belastingjaar"}},
		},
	}
	if !reflect.DeepEqual(claims, want) {
		got, _ := json.MarshalIndent(claims, "", "  ")
		t.Fatalf("claims =\n%s", got)
	}
}

func TestTypeMetadataBaseURLRequiresHTTPSOutsideLoopback(t *testing.T) {
	for _, raw := range []string{"http://issuer.example", "http://192.0.2.1"} {
		if err := validateTypeMetadataBaseURL(raw); err == nil {
			t.Errorf("validateTypeMetadataBaseURL(%q) accepted public HTTP", raw)
		}
	}
	for _, raw := range []string{"https://issuer.example", "http://localhost:9409", "http://127.0.0.1:9409"} {
		if err := validateTypeMetadataBaseURL(raw); err != nil {
			t.Errorf("validateTypeMetadataBaseURL(%q) = %v", raw, err)
		}
	}
}

func TestClaimCannotUseAnIssuerReservedName(t *testing.T) {
	for _, reserved := range []string{"vct", "iss", "status"} {
		definition := validTestAttestation()
		definition.Claims = append(definition.Claims, testClaims(map[string]mappingRule{reserved: {Pointer: "/x", Datatype: "string"}})...)
		if err := validateSourceAttestation(definition); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("claim %q: error = %v, want reserved-name rejection", reserved, err)
		}
	}
}

func TestSummaryPlaceholderMustNameAClaim(t *testing.T) {
	definition := validTestAttestation()
	definition.Display[0].Summary = "{{value}}"
	if err := validateSourceAttestation(definition); err != nil {
		t.Fatalf("placeholder naming a claim was rejected: %v", err)
	}
	for _, summary := range []string{"{{onbekend}}", "{{ value }}"} {
		definition.Display[0].Summary = summary
		if err := validateSourceAttestation(definition); err == nil || !strings.Contains(err.Error(), "is not a claim") {
			t.Errorf("summary %q: error = %v, want unknown-placeholder rejection", summary, err)
		}
	}
}

func TestClaimDescriptionNeedsALabelInTheSameLanguage(t *testing.T) {
	definition := validTestAttestation()
	definition.Claims[0].Description = localizedText{{Lang: "en-US", Text: "Value"}}
	if err := validateSourceAttestation(definition); err == nil || !strings.Contains(err.Error(), "no label in that language") {
		t.Fatalf("error = %v, want missing-label rejection", err)
	}
}

// Claims keep the order the source wrote them in, through the registry
// snapshot too, because that is the order the wallet shows them.
func TestClaimsKeepDocumentOrderThroughSerialization(t *testing.T) {
	raw := []byte(`{
		"zeta":  {"source": {"pointer": "/z", "datatype": "string"}, "label": {"nl-NL": "Z", "en-US": "Zed"}},
		"alpha": {"source": {"pointer": "/a", "datatype": "string", "optional": true}, "label": {"nl-NL": "A"}, "sd": "allowed"}
	}`)
	var claims sourceClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	var roundTripped sourceClaims
	if err := json.Unmarshal(encoded, &roundTripped); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roundTripped, claims) {
		t.Fatalf("round trip changed claims:\n got %+v\nwant %+v", roundTripped, claims)
	}
	if roundTripped[0].Name != "zeta" || roundTripped[0].Label[1].Lang != "en-US" || !roundTripped[1].Source.Optional {
		t.Fatalf("claims lost document order or content: %+v", roundTripped)
	}
}

func TestClaimRejectsFieldsOutsideTheClosedFormat(t *testing.T) {
	for name, raw := range map[string]string{
		"claim field":   `{"value": {"source": {"pointer": "/v", "datatype": "string"}, "label": {"nl-NL": "V"}, "svg_id": "v"}}`,
		"source field":  `{"value": {"source": {"pointer": "/v", "datatype": "string", "unit": "EUR"}, "label": {"nl-NL": "V"}}}`,
		"duplicate key": `{"value": {"source": {"pointer": "/v", "datatype": "string"}, "label": {"nl-NL": "V"}}, "value": {}}`,
	} {
		var claims sourceClaims
		if err := json.Unmarshal([]byte(raw), &claims); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func validTestAttestation() sourceAttestationDefinition {
	return sourceAttestationDefinition{
		TypeID: "example", TypeVersion: "1.0",
		Offers:         []sourceOffer{{ID: "example", Label: "Example", Parameters: map[string]any{}}},
		GraphQL:        sourceGraphQL{Endpoint: "/graphql", Document: "query Example($bsn: String!) { example(bsn: $bsn) { value } }", SubjectVariable: "bsn", ResultPointer: "/data/example"},
		MappingProfile: "gbo-simple-v1", Display: testDisplay("Example"),
		Claims: testClaims(map[string]mappingRule{"value": {Pointer: "/value", Datatype: "string"}}),
	}
}

func decodePublishedMetadata(t *testing.T, publication *typeMetadataPublication) map[string]any {
	t.Helper()
	var metadata map[string]any
	if err := json.Unmarshal(publication.body, &metadata); err != nil {
		t.Fatal(err)
	}
	return metadata
}

func compilePublishedSchema(t *testing.T, publication *typeMetadataPublication) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("credential.schema.json", decodePublishedMetadata(t, publication)["schema"]); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("credential.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}
