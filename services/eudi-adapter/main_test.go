package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestShippedSourceMetadataMatchesEnvelopeSchema(t *testing.T) {
	schema := compileSourceMetadataSchema(t)
	for _, path := range []string{
		"../graphql-server/config/gbo-source-metadata.json",
		"../brp-graphql-server/config/gbo-source-metadata.json",
	} {
		t.Run(path, func(t *testing.T) {
			metadataFile, err := os.Open(path)
			if err != nil {
				t.Fatalf("open shipped source metadata: %v", err)
			}
			defer metadataFile.Close()
			metadata, err := jsonschema.UnmarshalJSON(metadataFile)
			if err != nil {
				t.Fatalf("parse shipped source metadata: %v", err)
			}
			if err := schema.Validate(metadata); err != nil {
				t.Fatalf("shipped source metadata does not match envelope schema: %v", err)
			}
		})
	}
}

func compileSourceMetadataSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	mappingSchemaFile, err := os.Open("../../schemas/gbo-simple-v1.schema.json")
	if err != nil {
		t.Fatalf("open mapping profile schema: %v", err)
	}
	defer mappingSchemaFile.Close()
	mappingSchema, err := jsonschema.UnmarshalJSON(mappingSchemaFile)
	if err != nil {
		t.Fatalf("parse mapping profile schema: %v", err)
	}
	if err := compiler.AddResource("urn:gov:nl:gbo:schema:gbo-simple-v1:1", mappingSchema); err != nil {
		t.Fatalf("register mapping profile schema: %v", err)
	}
	schema, err := compiler.Compile("../../schemas/gbo-source-metadata-v1.schema.json")
	if err != nil {
		t.Fatalf("compile source metadata schema: %v", err)
	}
	return schema
}

func TestAttributeSchemaUnknownPropertiesRejectedByRuntimeAndEnvelopeSchema(t *testing.T) {
	raw, err := os.ReadFile("../graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatalf("read shipped source metadata: %v", err)
	}
	schema := compileSourceMetadataSchema(t)
	for property, value := range map[string]any{"scale": 0, "anything": "goes"} {
		t.Run(property, func(t *testing.T) {
			var envelope map[string]any
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatalf("decode source metadata: %v", err)
			}
			attestations := envelope["capabilities"].(map[string]any)["eudi"].(map[string]any)["attestations"].([]any)
			attestation := attestations[0].(map[string]any)
			attributes := attestation["attribute_schema"].(map[string]any)
			amount := attributes["verzamelinkomen"].(map[string]any)
			amount[property] = value
			mutated, err := json.Marshal(envelope)
			if err != nil {
				t.Fatalf("encode mutated source metadata: %v", err)
			}

			var document sourceMetadataDocument
			if err := json.Unmarshal(mutated, &document); err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("runtime decode error = %v, want unknown field rejection", err)
			}
			metadata, err := jsonschema.UnmarshalJSON(bytes.NewReader(mutated))
			if err != nil {
				t.Fatalf("parse mutated metadata for schema: %v", err)
			}
			if err := schema.Validate(metadata); err == nil {
				t.Fatal("envelope schema accepted unknown attribute_schema property")
			}
		})
	}
}

func TestAttributeSchemaUnitRejectedByRuntimeAndEnvelopeSchema(t *testing.T) {
	raw, err := os.ReadFile("../graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatalf("read shipped source metadata: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode source metadata: %v", err)
	}
	attestations := envelope["capabilities"].(map[string]any)["eudi"].(map[string]any)["attestations"].([]any)
	attestation := attestations[0].(map[string]any)
	attributes := attestation["attribute_schema"].(map[string]any)
	amount := attributes["verzamelinkomen"].(map[string]any)
	amount["unit"] = "eur"
	mutated, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("encode mutated source metadata: %v", err)
	}

	var document sourceMetadataDocument
	if err := json.Unmarshal(mutated, &document); err != nil {
		t.Fatalf("runtime decode error = %v", err)
	}
	if err := validateSourceAttestation(document.eudiAttestations()[0]); err == nil || !strings.Contains(err.Error(), "ISO 4217") {
		t.Fatalf("runtime validation error = %v, want ISO 4217 rejection", err)
	}
	metadata, err := jsonschema.UnmarshalJSON(bytes.NewReader(mutated))
	if err != nil {
		t.Fatalf("parse mutated metadata for schema: %v", err)
	}
	if err := compileSourceMetadataSchema(t).Validate(metadata); err == nil {
		t.Fatal("envelope schema accepted lowercase attribute_schema unit")
	}
}

// A source declaration fetched through FSC supplies both the query sent
// through FSC/PDP
// and the projection returned to the issuance server.
func TestAdapterUsesSourceMetadataFor2025(t *testing.T) {
	metadataPayload, err := os.ReadFile("../graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatalf("read shipped source metadata: %v", err)
	}
	var shipped sourceMetadataDocument
	if err := json.Unmarshal(metadataPayload, &shipped); err != nil {
		t.Fatalf("parse shipped source metadata: %v", err)
	}
	if len(shipped.eudiAttestations()) != 1 {
		t.Fatalf("shipped source metadata has %d EUDI attestations, want 1", len(shipped.eudiAttestations()))
	}
	metadataQuery := shipped.eudiAttestations()[0].GraphQL.Document
	metadataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/metadata/.well-known/gbo"; got != want {
			t.Errorf("metadata Outway path = %q, want %q", got, want)
		}
		if r.Header.Get("Fsc-Transaction-Id") == "" {
			t.Error("metadata request has no Fsc-Transaction-Id")
		}
		w.Header().Set("Content-Type", sourceMetadataMediaType)
		_, _ = w.Write(metadataPayload)
	}))
	defer metadataServer.Close()

	metadata, err := loadSourceMetadata(context.Background(), http.DefaultClient, sourceMetadataConfig{
		URL: metadataServer.URL + "/metadata/.well-known/gbo", MetadataTransport: sourceTransportFSC,
		DataTransport: sourceTransportFSC, ExpectedOIN: "99999999900000000200", TypeID: "inkomensverklaring",
	})
	if err != nil {
		t.Fatalf("load source metadata: %v", err)
	}

	var received proxyRequest
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode outway request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {"ingeschrevenPersoon": {"heeftBelastingjaarAangifte": [{
				"belastingjaar": 2025,
				"status": "Definitief vastgesteld",
				"indieningsdatum": "2026-04-01",
				"verzamelinkomen": {"waarde": 43000.0, "valuta": "EUR"},
				"box1Inkomen": {"waarde": 41000.0, "valuta": "EUR"},
				"box2Inkomen": {"waarde": 1000.0, "valuta": "EUR"},
				"box3Inkomen": {"waarde": 1000.0, "valuta": "EUR"}
			}]}}
		}`))
	}))
	defer outway.Close()

	cfg := config{Port: "0", OutwayURL: outway.URL, IssuerOIN: "00000004000000004000", SourceDataTransport: sourceTransportFSC, SourceDataFSCServiceReference: "bri", SourceDataFSCGrantHash: "data-grant"}
	srv := httptest.NewServer(newMux(cfg, http.DefaultClient, metadata))
	defer srv.Close()

	body := []byte(`[{
		"id": "req-1",
		"attestations": [{
			"attestation_type": "urn:eudi:pid:nl:1",
			"attributes": {"urn:eudi:pid:nl:1": {"bsn": "123456789"}}
		}]
	}]`)
	resp, err := http.Post(srv.URL+"/attestations/99999999900000000200/inkomensverklaring?jaar=2025", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, string(raw))
	}
	if received.Query != metadataQuery {
		t.Errorf("query sent through FSC was not the source query\ngot:\n%s\nwant:\n%s", received.Query, metadataQuery)
	}
	if got, want := received.Variables["bsn"], "123456789"; got != want {
		t.Errorf("bsn variable = %v, want %v", got, want)
	}
	if got, want := received.Variables["jaar"], float64(2025); got != want {
		t.Errorf("jaar variable = %v, want %v", got, want)
	}
	var docs []attestation
	if err := json.NewDecoder(resp.Body).Decode(&docs); err != nil {
		t.Fatalf("decode issuable documents: %v", err)
	}
	if len(docs) != 1 || docs[0].Attributes["aangifte_status"] != "Definitief vastgesteld" {
		t.Fatalf("source projection was not returned: %#v", docs)
	}
	if _, unmapped := docs[0].Attributes["verklaring_tekst"]; unmapped {
		t.Error("source projection unexpectedly contains an unmapped claim")
	}
}

func phase1IncomeMapping() map[string]mappingRule {
	return map[string]mappingRule{
		"belastingjaar":   {Pointer: "/belastingjaar", Datatype: "gYear"},
		"verzamelinkomen": {Pointer: "/verzamelinkomen/waarde", Datatype: "number"},
		"aangifte_status": {Pointer: "/status", Datatype: "string"},
		"indieningsdatum": {Pointer: "/indieningsdatum", Datatype: "date"},
		"inkomen_box1":    {Pointer: "/box1Inkomen/waarde", Datatype: "number"},
		"inkomen_box2":    {Pointer: "/box2Inkomen/waarde", Datatype: "number"},
		"inkomen_box3":    {Pointer: "/box3Inkomen/waarde", Datatype: "number"},
	}
}

func newIncomeSourceAdapter(t *testing.T, mapping map[string]mappingRule, bronResponse string) *httptest.Server {
	t.Helper()
	metadata := &activeSourceMetadata{
		SourceOIN: "99999999900000000200",
		TypeID:    "inkomensverklaring",
		Definition: sourceAttestationDefinition{
			GraphQL: sourceGraphQL{
				ServiceReference: "bri",
				Endpoint:         "/source-query",
				Document: `query Inkomensverklaring($bsn: BSN!, $jaar: Int!) {
  ingeschrevenPersoon(bsn: $bsn) {
    heeftBelastingjaarAangifte(belastingjaren: [$jaar]) {
      belastingjaar status indieningsdatum
      ... on AangifteIH {
        verzamelinkomen { waarde valuta }
        box1Inkomen { waarde valuta }
        box2Inkomen { waarde valuta }
        box3Inkomen { waarde valuta }
      }
    }
  }
}`,
				SubjectVariable: "bsn",
				Parameters: map[string]sourceParameter{
					"jaar": {Type: "integer", Required: true},
				},
				ResultPointer: "/data/ingeschrevenPersoon/heeftBelastingjaarAangifte",
			},
			MappingProfile: "gbo-simple-v1",
			Mapping:        mapping,
		},
	}
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got, want := request.URL.Path, "/source-query"; got != want {
			t.Errorf("source data path = %q, want metadata-defined %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bronResponse))
	}))
	t.Cleanup(outway.Close)

	cfg := config{Port: "0", OutwayURL: outway.URL, IssuerOIN: "00000004000000004000", SourceDataTransport: sourceTransportFSC, SourceDataFSCServiceReference: "bri", SourceDataFSCGrantHash: "data-grant"}
	srv := httptest.NewServer(newMux(cfg, http.DefaultClient, metadata))
	t.Cleanup(srv.Close)
	return srv
}

const completeIncomeResponse = `{
	"data": {"ingeschrevenPersoon": {"heeftBelastingjaarAangifte": [{
		"belastingjaar": 2025,
		"status": "Definitief vastgesteld",
		"indieningsdatum": "2026-04-01",
		"verzamelinkomen": {"waarde": 43000.0, "valuta": "EUR"},
		"box1Inkomen": {"waarde": 41000.0, "valuta": "EUR"},
		"box2Inkomen": {"waarde": 1000.0, "valuta": "EUR"},
		"box3Inkomen": {"waarde": 1000.0, "valuta": "EUR"}
	}]}}
}`

func TestSourceProjectionContainsOnlyMappedClaims(t *testing.T) {
	mapping := phase1IncomeMapping()
	delete(mapping, "inkomen_box3")
	responseWithoutBox3 := strings.Replace(
		completeIncomeResponse,
		`,
		"box3Inkomen": {"waarde": 1000.0, "valuta": "EUR"}`,
		"",
		1,
	)
	srv := newIncomeSourceAdapter(t, mapping, responseWithoutBox3)

	resp := postDisclosure(t, srv, "/attestations/99999999900000000200/inkomensverklaring?jaar=2025", "123456789")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, string(raw))
	}
	var docs []attestation
	if err := json.NewDecoder(resp.Body).Decode(&docs); err != nil {
		t.Fatalf("decode issuable documents: %v", err)
	}
	if _, exists := docs[0].Attributes["inkomen_box3"]; exists {
		t.Error("source projection contains a claim that is absent from the mapping")
	}
}

func TestSourceProjectionPreservesDecimalNumbers(t *testing.T) {
	responseWithCents := strings.Replace(completeIncomeResponse, "43000.0", "43000.50", 1)
	srv := newIncomeSourceAdapter(t, phase1IncomeMapping(), responseWithCents)

	resp := postDisclosure(t, srv, "/attestations/99999999900000000200/inkomensverklaring?jaar=2025", "123456789")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, string(raw))
	}
	var docs []attestation
	if err := json.NewDecoder(resp.Body).Decode(&docs); err != nil {
		t.Fatalf("decode issuable documents: %v", err)
	}
	if got, want := docs[0].Attributes["verzamelinkomen"], float64(43000.5); got != want {
		t.Errorf("verzamelinkomen = %v, want %v", got, want)
	}
}

func TestSourceMetadataRejectedDuringOnboarding(t *testing.T) {
	validQuery := `query Inkomensverklaring($bsn: BSN!, $jaar: Int!) {
  ingeschrevenPersoon(bsn: $bsn) {
    heeftBelastingjaarAangifte(belastingjaren: [$jaar]) { belastingjaar }
  }
}`
	metadataPayload := func(t *testing.T, sourceOIN, query string) []byte {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"schema_version": "1.0",
			"source_oin":     sourceOIN,
			"version":        "1.0.0",
			"capabilities": map[string]any{"eudi": map[string]any{
				"version": "1.0",
				"attestations": []any{map[string]any{
					"type_id":      "inkomensverklaring",
					"type_version": "1.0",
					"graphql": map[string]any{
						"document":         query,
						"subject_variable": "bsn",
						"parameters": map[string]any{
							"jaar": map[string]any{"type": "integer", "required": true},
						},
						"result_pointer": "/data/ingeschrevenPersoon/heeftBelastingjaarAangifte",
					},
					"mapping_profile": "gbo-simple-v1",
					"mapping": map[string]any{
						"belastingjaar": map[string]any{"pointer": "/belastingjaar", "datatype": "gYear"},
					},
					"attribute_schema": map[string]any{
						"belastingjaar": map[string]any{"type": "integer", "format": "gYear"},
					},
				}},
			}},
		})
		if err != nil {
			t.Fatalf("marshal source metadata: %v", err)
		}
		return payload
	}
	tests := []struct {
		name        string
		payload     []byte
		expectedOIN string
		wantError   string
	}{
		{
			name:        "wrong source OIN",
			payload:     metadataPayload(t, "00000001003214345001", validQuery),
			expectedOIN: "00000001003214345000",
			wantError:   "does not match registered OIN",
		},
		{
			name:        "invalid GraphQL syntax",
			payload:     metadataPayload(t, "00000001003214345000", "query {"),
			expectedOIN: "00000001003214345000",
			wantError:   "invalid GraphQL document",
		},
		{
			name: "mapping property outside closed profile",
			payload: bytes.Replace(
				metadataPayload(t, "00000001003214345000", validQuery),
				[]byte(`"datatype":"gYear"`),
				[]byte(`"datatype":"gYear","filter":"first"`),
				1,
			),
			expectedOIN: "00000001003214345000",
			wantError:   "GBO_SIMPLE_MAPPING_INVALID",
		},
		{
			name: "mapping transform outside closed profile",
			payload: bytes.Replace(
				metadataPayload(t, "00000001003214345000", validQuery),
				[]byte(`"datatype":"gYear"`),
				[]byte(`"datatype":"integer","transform":{"operator":"round"}`),
				1,
			),
			expectedOIN: "00000001003214345000",
			wantError:   "GBO_SIMPLE_MAPPING_INVALID",
		},
		{
			name:        "unsupported envelope schema version",
			payload:     bytes.Replace(metadataPayload(t, "00000001003214345000", validQuery), []byte(`"schema_version":"1.0"`), []byte(`"schema_version":"2.0"`), 1),
			expectedOIN: "00000001003214345000",
			wantError:   "unsupported source metadata schema_version",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", sourceMetadataMediaType)
				_, _ = w.Write(test.payload)
			}))
			defer server.Close()

			_, err := loadSourceMetadata(context.Background(), http.DefaultClient, sourceMetadataConfig{
				URL: server.URL, MetadataTransport: sourceTransportFSC, DataTransport: sourceTransportFSC,
				ExpectedOIN: test.expectedOIN, TypeID: "inkomensverklaring",
			})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want error containing %q", err, test.wantError)
			}
		})
	}
}

func TestAdapterUsesBRPSourceQueryAndMapping(t *testing.T) {
	raw, err := os.ReadFile("../brp-graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatalf("read BRP source metadata: %v", err)
	}
	var document sourceMetadataDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse BRP source metadata: %v", err)
	}
	definition := document.eudiAttestations()[0]
	active := &activeSourceMetadata{
		Version:      document.Version,
		SourceOIN:    document.SourceOIN,
		TypeID:       definition.TypeID,
		Definition:   definition,
		VCT:          "https://issuer.example/types/99999999900000000400/akte-van-overlijden/v1.0",
		VCTIntegrity: "sha256-test-integrity",
	}

	var received proxyRequest
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/graphql"; got != want {
			t.Errorf("outway path = %q, want %q", got, want)
		}
		if got := r.Header.Get("X-GBO-Flow"); got != "" {
			t.Errorf("adapter supplied untrusted flow header %q", got)
		}
		if got := r.Header.Get("X-GBO-Scope"); got != "" {
			t.Errorf("adapter supplied obsolete scope header %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode outway request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"akteVanOverlijden":[{
			"overledene_geslachtsnaam":"Vries","overledene_voorvoegsel":"de",
			"overledene_voornamen":"Sander Willem","overledene_geboortedatum":"1997-11-12",
			"overledene_geboorteplaats":"Rotterdam","overledene_geboorteland":"NL",
			"overledene_geslacht":"Man","overledene_ouders":"Hendrik Jan de Vries; Annelies Maria Bakker",
			"datum_overlijden":"2026-02-14","plaats_overlijden":"Rotterdam",
			"land_overlijden":"NL","soort_verbintenis":"Huwelijk",
			"echtgenoot_geslachtsnaam":"Jansen","echtgenoot_voorvoegsel":"",
			"echtgenoot_voornamen":"Frouke","verklaring_tekst":"Verklaring uit de bron"
		}]}}`))
	}))
	defer outway.Close()

	cfg := config{OutwayURL: outway.URL, SourceDataTransport: sourceTransportFSC, SourceDataFSCServiceReference: "brp", SourceDataFSCGrantHash: "data-grant"}
	srv := httptest.NewServer(newMux(cfg, http.DefaultClient, active))
	defer srv.Close()
	resp := postDisclosure(t, srv, "/attestations/99999999900000000400/akte-van-overlijden", "999991772")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, body)
	}
	if received.Query != definition.GraphQL.Document {
		t.Error("adapter did not send the source-owned BRP query")
	}
	if got, want := received.Variables["bsn"], "999991772"; got != want {
		t.Errorf("bsn variable = %v, want %v", got, want)
	}
	var documents []attestation
	if err := json.NewDecoder(resp.Body).Decode(&documents); err != nil {
		t.Fatalf("decode issuable documents: %v", err)
	}
	if got, want := documents[0].AttestationType, active.VCT; got != want {
		t.Errorf("attestation type = %q, want %q", got, want)
	}
	if got, want := documents[0].Attributes["overledene_geslachtsnaam"], "Vries"; got != want {
		t.Errorf("mapped BRP claim = %v, want %v", got, want)
	}
	if got, want := documents[0].Attributes["vct#integrity"], active.VCTIntegrity; got != want {
		t.Errorf("vct#integrity = %v, want %v", got, want)
	}
}

func postDisclosure(t *testing.T, srv *httptest.Server, path, bsn string) *http.Response {
	t.Helper()
	body := []byte(`[{"id":"req-1","attestations":[{"attestation_type":"urn:eudi:pid:nl:1","attributes":{"urn:eudi:pid:nl:1":{"bsn":"` + bsn + `"}}}]}]`)
	resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	return resp
}
