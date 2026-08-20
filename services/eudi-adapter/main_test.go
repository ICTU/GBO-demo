package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestShippedSourceMetadataMatchesEnvelopeSchema(t *testing.T) {
	schema := compileSourceMetadataSchema(t)
	for _, path := range []string{
		"../graphql-server/config/gbo-source-metadata.json",
		"../graphql-server/config/gbo-source-metadata-unsecured.json",
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

func TestRuntimeServesGeneratedIssuanceOffers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eudi-offers.json")
	if err := os.WriteFile(path, []byte(`[{"key":"income-2025"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	newMux(config{IssuanceOffersPath: path}, nil, nil).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/eudi-offers.json", nil),
	)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("offers response = %d %v", recorder.Code, recorder.Header())
	}
	if got := strings.TrimSpace(recorder.Body.String()); got != `[{"key":"income-2025"}]` {
		t.Fatalf("offers body = %s", got)
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

type testSourceMetadataConfig struct {
	URL               string
	MetadataTransport string
	MetadataGrantHash string
	DataTransport     string
	SourceID          string
	ExpectedOIN       string
	TypeID            string
}

func fetchSourceMetadataForTest(ctx context.Context, client *http.Client, cfg testSourceMetadataConfig) (*activeSourceMetadata, error) {
	if cfg.URL == "" || cfg.ExpectedOIN == "" || cfg.TypeID == "" {
		return nil, fmt.Errorf("source metadata registration is incomplete")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("create source metadata request: %w", err)
	}
	req.Header.Set("Accept", sourceMetadataMediaType)
	if cfg.MetadataTransport != sourceTransportFSC {
		return nil, fmt.Errorf("source metadata transport %q is configured but not implemented", cfg.MetadataTransport)
	}
	fscTxID, err := newFscTransactionID()
	if err != nil {
		return nil, fmt.Errorf("generate source metadata Fsc-Transaction-Id: %w", err)
	}
	req.Header.Set("Fsc-Transaction-Id", fscTxID)
	req.Header.Set("Fsc-Grant-Hash", cfg.MetadataGrantHash)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch source metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source metadata status %d", resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != sourceMetadataMediaType {
		return nil, fmt.Errorf("source metadata content type must be %s", sourceMetadataMediaType)
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read source metadata: %w", err)
	}
	document, err := decodeSourceMetadataDocument(payload)
	if err != nil {
		return nil, err
	}
	if document.SourceOIN != cfg.ExpectedOIN {
		return nil, fmt.Errorf("source metadata OIN %q does not match registered OIN %q", document.SourceOIN, cfg.ExpectedOIN)
	}
	if document.SchemaVersion != "1.0" {
		return nil, fmt.Errorf("unsupported source metadata schema_version %q", document.SchemaVersion)
	}
	if document.Capabilities.EUDI == nil || document.Capabilities.EUDI.Version != "1.0" {
		return nil, fmt.Errorf("source metadata has no supported EUDI capability")
	}
	for _, definition := range document.eudiAttestations() {
		if definition.TypeID != cfg.TypeID {
			continue
		}
		if err := validateSourceAttestation(definition); err != nil {
			return nil, fmt.Errorf("attestation %q: %w", cfg.TypeID, err)
		}
		if err := validateGraphQLEndpoint(definition.GraphQL, cfg.DataTransport); err != nil {
			return nil, fmt.Errorf("attestation %q graphql.endpoint: %w", cfg.TypeID, err)
		}
		return &activeSourceMetadata{
			Version: document.Version, SourceID: cfg.SourceID, SourceOIN: cfg.ExpectedOIN,
			TypeID: cfg.TypeID, Definition: definition,
		}, nil
	}
	return nil, fmt.Errorf("source metadata has no attestation %q", cfg.TypeID)
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
	if err := validateSourceAttestation(document.eudiAttestations()[0]); err == nil || !strings.Contains(err.Error(), "three-letter uppercase") {
		t.Fatalf("runtime validation error = %v, want uppercase alpha-3 rejection", err)
	}
	metadata, err := jsonschema.UnmarshalJSON(bytes.NewReader(mutated))
	if err != nil {
		t.Fatalf("parse mutated metadata for schema: %v", err)
	}
	if err := compileSourceMetadataSchema(t).Validate(metadata); err == nil {
		t.Fatal("envelope schema accepted lowercase attribute_schema unit")
	}
}

func TestEUDIAttributeSchemaRejectsNumberUnsupportedByWallet(t *testing.T) {
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
	attestation["mapping"].(map[string]any)["verzamelinkomen"].(map[string]any)["datatype"] = "number"
	attestation["attribute_schema"].(map[string]any)["verzamelinkomen"].(map[string]any)["type"] = "number"
	attestation["type_metadata"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)["verzamelinkomen"].(map[string]any)["type"] = "number"
	mutated, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("encode mutated source metadata: %v", err)
	}

	var document sourceMetadataDocument
	if err := json.Unmarshal(mutated, &document); err != nil {
		t.Fatalf("runtime decode error = %v", err)
	}
	if err := validateSourceAttestation(document.eudiAttestations()[0]); err == nil || !strings.Contains(err.Error(), "nl-wallet v0.5 cannot represent") {
		t.Fatalf("runtime validation error = %v, want unsupported wallet number", err)
	}
	metadata, err := jsonschema.UnmarshalJSON(bytes.NewReader(mutated))
	if err != nil {
		t.Fatalf("parse mutated metadata for schema: %v", err)
	}
	if err := compileSourceMetadataSchema(t).Validate(metadata); err == nil {
		t.Fatal("envelope schema accepted a number attribute unsupported by nl-wallet v0.5")
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

	metadata, err := fetchSourceMetadataForTest(context.Background(), http.DefaultClient, testSourceMetadataConfig{
		URL: metadataServer.URL + "/metadata/.well-known/gbo", MetadataTransport: sourceTransportFSC,
		DataTransport: sourceTransportFSC, SourceID: "belastingdienst", ExpectedOIN: "99999999900000000200", TypeID: "inkomensverklaring",
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

	cfg := config{Port: "0", OutwayURL: outway.URL, SourceDataTransport: sourceTransportFSC, SourceDataFSCServiceReference: "bri", SourceDataFSCGrantHash: "data-grant"}
	srv := httptest.NewServer(newMux(cfg, http.DefaultClient, metadata))
	defer srv.Close()

	body := []byte(`[{
		"id": "req-1",
		"attestations": [{
			"attestation_type": "urn:eudi:pid:nl:1",
			"attributes": {"urn:eudi:pid:nl:1": {"bsn": "123456789"}}
		}]
	}]`)
	resp, err := http.Post(srv.URL+"/attestations/belastingdienst/inkomensverklaring?jaar=2025", "application/json", bytes.NewReader(body))
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
		SourceID:  "belastingdienst",
		SourceOIN: "99999999900000000200",
		TypeID:    "inkomensverklaring",
		Definition: sourceAttestationDefinition{
			Offers: []sourceOffer{{
				ID: "inkomensverklaring_2025", Label: "Inkomensverklaring 2025",
				Parameters: map[string]any{"jaar": 2025},
			}},
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

	cfg := config{Port: "0", OutwayURL: outway.URL, SourceDataTransport: sourceTransportFSC, SourceDataFSCServiceReference: "bri", SourceDataFSCGrantHash: "data-grant"}
	srv := httptest.NewServer(newMux(cfg, http.DefaultClient, metadata))
	t.Cleanup(srv.Close)
	return srv
}

func TestSourceAttestationRejectsParametersOutsidePublishedOffers(t *testing.T) {
	srv := newIncomeSourceAdapter(t, phase1IncomeMapping(), completeIncomeResponse)
	resp := postDisclosure(t, srv, "/attestations/belastingdienst/inkomensverklaring?jaar=2023", "123456789")
	defer resp.Body.Close()
	if got, want := resp.StatusCode, http.StatusBadRequest; got != want {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want %d; body=%s", got, want, body)
	}
}

func TestSourceAttestationDoesNotExposeUpstreamErrorBody(t *testing.T) {
	metadata := &activeSourceMetadata{
		SourceID: "belastingdienst", SourceOIN: "99999999900000000200", TypeID: "example", VCT: "https://issuer.example/types/belastingdienst/example/v1",
		Definition: sourceAttestationDefinition{
			Offers:  []sourceOffer{{ID: "example", Label: "Example", Parameters: map[string]any{}}},
			GraphQL: sourceGraphQL{ServiceReference: "example", Endpoint: "/graphql", Document: "query Example($bsn: BSN!) { example(bsn: $bsn) { value } }", SubjectVariable: "bsn", ResultPointer: "/data/example"},
			Mapping: map[string]mappingRule{"value": {Pointer: "/value", Datatype: "string"}},
		},
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("sensitive upstream diagnostics")),
		}, nil
	})}
	mux := newMux(config{OutwayURL: "https://outway.example", SourceDataTransport: sourceTransportFSC, SourceDataFSCServiceReference: "example", SourceDataFSCGrantHash: "grant"}, client, metadata)
	disclosure := `[{
		"attestations":[{"attributes":{"urn:eudi:pid:nl:1":{"bsn":"123456789"}}}]
	}]`
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "http://adapter/attestations/belastingdienst/example", strings.NewReader(disclosure)))
	if got, want := recorder.Code, http.StatusBadGateway; got != want {
		t.Fatalf("status = %d, want %d; body=%s", got, want, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "sensitive") {
		t.Fatalf("response exposed upstream body: %s", recorder.Body.String())
	}
}

type typeRoutingRuntime struct {
	metadata activeSourceMetadata
	body     string
}

func (r *typeRoutingRuntime) current(time.Time) (*activeSourceMetadata, error) {
	return &r.metadata, nil
}

func (r *typeRoutingRuntime) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	_, _ = io.WriteString(w, r.body)
}

func TestTypeMetadataRoutingUsesSourceAndType(t *testing.T) {
	const sourceOIN = "99999999900000000200"
	bindings := []sourceRuntimeBinding{
		{config: config{SourceMetadataSourceID: "belastingdienst", SourceMetadataOIN: sourceOIN, SourceMetadataTypeID: "shared-type"}, runtime: &typeRoutingRuntime{body: "belastingdienst"}},
		{config: config{SourceMetadataSourceID: "rvig", SourceMetadataOIN: sourceOIN, SourceMetadataTypeID: "shared-type"}, runtime: &typeRoutingRuntime{body: "rvig"}},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/types/rvig/shared-type/v1", nil)
	sourceTypeMetadataHandler{bindings: bindings}.ServeHTTP(recorder, request)
	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got, want := recorder.Body.String(), "rvig"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestUnsecuredSourceDataUsesPublishedAbsoluteEndpointWithoutFSCHeaders(t *testing.T) {
	var received proxyRequest
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Fsc-Grant-Hash") != "" || request.Header.Get("Fsc-Transaction-Id") != "" {
			t.Fatalf("unsecured data request contains FSC headers: %v", request.Header)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"example":{"value":"ok"}}}`))
	}))
	defer source.Close()

	result, err := callSource(context.Background(), source.Client(), config{SourceDataTransport: sourceTransportUnsecured}, sourceQueryPlan{
		Endpoint: source.URL + "/graphql", Query: "query Example { example { value } }",
	})
	if err != nil {
		t.Fatalf("call unsecured source: %v", err)
	}
	if len(result.Raw) == 0 || received.Query == "" {
		t.Fatalf("result=%s request=%+v", result.Raw, received)
	}
}

func TestUnsecuredSourceDataRejectsRedirect(t *testing.T) {
	targetCalled := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalled = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Redirect(w, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	_, err := callSource(context.Background(), redirect.Client(), config{SourceDataTransport: sourceTransportUnsecured}, sourceQueryPlan{
		Endpoint: redirect.URL, Query: "query Example { example { value } }",
	})
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect error = %v", err)
	}
	if targetCalled {
		t.Fatal("unsecured data redirect crossed the configured endpoint boundary")
	}
}

func TestRuntimeDoesNotFetchSourceMetadata(t *testing.T) {
	activeDirectory := t.TempDir()
	activation := sourceActivation{
		SchemaVersion: "1.0",
		Source: sourceRegistration{
			SourceID: "belastingdienst", ProviderPeerID: "0000009958MINBZK0000", SourceOIN: "99999999900000000200", Name: "Belastingdienst",
			MetadataEndpoint: sourceMetadataEndpoint{Transport: sourceTransportFSC, ServiceReference: "gbo-metadata", Path: "/.well-known/gbo", GrantHash: "metadata-grant"},
			DataAccess:       sourceDataAccess{Transport: sourceTransportFSC, ServiceReference: "bri", GrantHash: "data-grant"},
		},
		Types: []activatedType{{
			TypeID: "inkomensverklaring", TypeVersion: "1.0",
			VCT: "https://issuer.example/types/belastingdienst/inkomensverklaring/v1.0", VCTIntegrity: "sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=",
			TypeMetadataReference: filepath.Join(t.TempDir(), "type.json"), Offers: []sourceOffer{{ID: "income", Label: "Income", Parameters: map[string]any{}}},
		}},
	}
	body, err := json.Marshal(activation)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(activeDirectory, "belastingdienst.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("runtime must not fetch source metadata")
	})}

	_ = newRuntimeMux(context.Background(), config{
		OutwayURL: "https://outway.example", SourceActivationsPath: activeDirectory,
		TypeMetadataStorePath: t.TempDir(),
	}, client)
	if requests != 0 {
		t.Fatalf("runtime performed %d source metadata request(s)", requests)
	}
}

func TestRuntimeFailsClosedWhenDeployedTypeDisappears(t *testing.T) {
	activationPath := filepath.Join(t.TempDir(), "belastingdienst.json")
	body, err := json.Marshal(sourceActivation{
		SchemaVersion: "1.0",
		Source: sourceRegistration{
			SourceID: "belastingdienst", ProviderPeerID: "0000009958MINBZK0000", SourceOIN: "99999999900000000200", Name: "Belastingdienst",
		},
		Types: []activatedType{{TypeID: "different-type"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(activationPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := newActivatedSourceRuntime(config{
		SourceMetadataSourceID: "belastingdienst", SourceMetadataOIN: "99999999900000000200",
		SourceMetadataTypeID: "inkomensverklaring", SourceMetadataVCT: "https://issuer.example/types/belastingdienst/inkomensverklaring/v1.0",
		SourceActivationPath: activationPath, TypeMetadataStorePath: t.TempDir(),
	})
	if err == nil {
		_, err = runtime.current(time.Now())
	}
	if err == nil {
		t.Fatal("removed or invalid deployed type did not fail closed")
	}
}

func TestActivatedRuntimeDoesNotReloadActivationOnEveryRequest(t *testing.T) {
	now := time.Now().UTC()
	activationPath := filepath.Join(t.TempDir(), "belastingdienst.json")
	if err := os.WriteFile(activationPath, []byte("not valid JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := &activatedSourceRuntime{
		config: config{
			SourceActivationPath: activationPath,
		},
		metadata: activeSourceMetadata{
			SourceID: "belastingdienst", SourceOIN: "99999999900000000200", TypeID: "inkomensverklaring",
		},
		activationPath: activationPath,
		typeID:         "inkomensverklaring",
		reloadAfter:    now.Add(activationReloadInterval),
	}
	if _, err := runtime.current(now); err != nil {
		t.Fatalf("cached request unexpectedly reloaded activation: %v", err)
	}
	if _, err := runtime.current(now.Add(activationReloadInterval)); err == nil {
		t.Fatal("activation was not reloaded after the bounded cache interval")
	}
}

func TestActivatedRuntimeCachesTypeMetadataStore(t *testing.T) {
	storePath := t.TempDir()
	newPublication := func(typeID string) *typeMetadataPublication {
		t.Helper()
		publication, err := newTypeMetadataPublication("https://issuer.example", "belastingdienst", sourceAttestationDefinition{
			TypeID: typeID, TypeVersion: "1.0",
			TypeMetadata: json.RawMessage(`{"name":"Test","schema":{"type":"object"}}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := persistTypeMetadataPublication(storePath, publication); err != nil {
			t.Fatal(err)
		}
		return publication
	}
	first := newPublication("first")
	runtime := &activatedSourceRuntime{
		storePath: storePath, publications: make(map[string]*typeMetadataPublication),
		publicationsReloadAfter: time.Now().Add(activationReloadInterval),
	}
	request := func(publication *typeMetadataPublication) int {
		t.Helper()
		recorder := httptest.NewRecorder()
		runtime.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, publication.VCT, nil))
		return recorder.Code
	}
	if got, want := request(first), http.StatusNotFound; got != want {
		t.Fatalf("first request before cache refresh = %d, want %d", got, want)
	}
	runtime.publicationsMu.Lock()
	runtime.publicationsReloadAfter = time.Now().Add(-time.Second)
	runtime.publicationsMu.Unlock()
	if got, want := request(first), http.StatusOK; got != want {
		t.Fatalf("first request after cache refresh = %d, want %d", got, want)
	}

	second := newPublication("second")
	if got, want := request(second), http.StatusNotFound; got != want {
		t.Fatalf("new publication bypassed cache interval: status = %d, want %d", got, want)
	}
	runtime.publicationsMu.Lock()
	runtime.publicationsReloadAfter = time.Now().Add(-time.Second)
	runtime.publicationsMu.Unlock()
	if got, want := request(second), http.StatusOK; got != want {
		t.Fatalf("second request after cache refresh = %d, want %d", got, want)
	}
}

func TestActivatedRuntimeReloadsUpdatedDataGrantAfterCacheInterval(t *testing.T) {
	now := time.Now().UTC()
	activationPath := filepath.Join(t.TempDir(), "belastingdienst.json")
	offer := sourceOffer{ID: "example", Label: "Example", Parameters: map[string]any{}}
	definition := sourceAttestationDefinition{
		TypeID: "example", TypeVersion: "1.0", Offers: []sourceOffer{offer},
		GraphQL: sourceGraphQL{
			ServiceReference: "bri", Document: "query Example($bsn: String!) { example(bsn: $bsn) { value } }",
			SubjectVariable: "bsn", ResultPointer: "/data/example",
		},
		MappingProfile: "gbo-simple-v1",
		Mapping:        map[string]mappingRule{"value": {Pointer: "/value", Datatype: "string"}},
		AttributeSchema: map[string]sourceAttributeSchema{
			"value": {Type: "string"},
		},
	}
	activation := sourceActivation{
		SchemaVersion: "1.0", MetadataVersion: "1.0", FreshUntil: now.Add(time.Hour), StaleUntil: now.Add(2 * time.Hour),
		Source: sourceRegistration{
			SourceID: "belastingdienst", ProviderPeerID: "0000009958MINBZK0000", SourceOIN: "99999999900000000200", Name: "Belastingdienst",
			MetadataEndpoint: sourceMetadataEndpoint{Transport: sourceTransportFSC, ServiceReference: "gbo-metadata", Path: "/.well-known/gbo", GrantHash: "metadata-grant"},
			DataAccess:       sourceDataAccess{Transport: sourceTransportFSC, ServiceReference: "bri", GrantHash: "data-grant-v1"},
		},
		Types: []activatedType{{
			TypeID: "example", TypeVersion: "1.0", VCT: "https://issuer.example/types/belastingdienst/example/v1.0",
			VCTIntegrity: "sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=", TypeMetadataReference: filepath.Join(t.TempDir(), "type.json"),
			Offers: []sourceOffer{offer}, Definition: definition,
		}},
	}
	writeActivation := func() {
		body, err := json.Marshal(activation)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(activationPath, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeActivation()
	runtime, err := newActivatedSourceRuntime(config{
		SourceMetadataSourceID: "belastingdienst", SourceMetadataOIN: "99999999900000000200", SourceMetadataTypeID: "example",
		SourceMetadataVCT: "https://issuer.example/types/belastingdienst/example/v1.0", SourceActivationPath: activationPath,
		TypeMetadataStorePath: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, resolved, err := runtime.currentSource(now)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resolved.SourceDataFSCGrantHash, "data-grant-v1"; got != want {
		t.Fatalf("initial data grant = %q, want %q", got, want)
	}
	activation.Source.DataAccess.GrantHash = "data-grant-v2"
	writeActivation()
	reloadAt := runtime.reloadAfter
	_, cached, err := runtime.currentSource(reloadAt.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cached.SourceDataFSCGrantHash, "data-grant-v1"; got != want {
		t.Fatalf("cached data grant = %q, want %q", got, want)
	}
	_, refreshed, err := runtime.currentSource(reloadAt)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := refreshed.SourceDataFSCGrantHash, "data-grant-v2"; got != want {
		t.Fatalf("refreshed data grant = %q, want %q", got, want)
	}
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

	resp := postDisclosure(t, srv, "/attestations/belastingdienst/inkomensverklaring?jaar=2025", "123456789")
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

	resp := postDisclosure(t, srv, "/attestations/belastingdienst/inkomensverklaring?jaar=2025", "123456789")
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
					"offers": []any{map[string]any{
						"id": "inkomensverklaring_2024", "label": "Inkomensverklaring 2024",
						"parameters": map[string]any{"jaar": 2024},
					}},
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

			_, err := fetchSourceMetadataForTest(context.Background(), http.DefaultClient, testSourceMetadataConfig{
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
		SourceID:     "rvig",
		SourceOIN:    document.SourceOIN,
		TypeID:       definition.TypeID,
		Definition:   definition,
		VCT:          "https://issuer.example/types/rvig/akte-van-overlijden/v1.0",
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
	resp := postDisclosure(t, srv, "/attestations/rvig/akte-van-overlijden", "999991772")
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
	for _, internalClaim := range []string{"vct", "vct#integrity"} {
		if value, exists := documents[0].Attributes[internalClaim]; exists {
			t.Errorf("internal claim %q was sent as an IssuableDocument attribute: %v", internalClaim, value)
		}
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
