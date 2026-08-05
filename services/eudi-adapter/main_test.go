package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

var testBase64URL = base64.RawURLEncoding

func TestShippedSourceMetadataMatchesEnvelopeSchema(t *testing.T) {
	schema := compileSourceMetadataSchema(t)
	metadataFile, err := os.Open("../graphql-server/config/gbo-attestations.json")
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
	schema, err := compiler.Compile("../../schemas/gbo-attestations-v1.schema.json")
	if err != nil {
		t.Fatalf("compile source metadata schema: %v", err)
	}
	return schema
}

func TestAttributeSchemaUnknownPropertiesRejectedByRuntimeAndEnvelopeSchema(t *testing.T) {
	raw, err := os.ReadFile("../graphql-server/config/gbo-attestations.json")
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
			attestations := envelope["attestations"].([]any)
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
	raw, err := os.ReadFile("../graphql-server/config/gbo-attestations.json")
	if err != nil {
		t.Fatalf("read shipped source metadata: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode source metadata: %v", err)
	}
	attestations := envelope["attestations"].([]any)
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
	if err := validateSourceAttestation(document.Attestations[0]); err == nil || !strings.Contains(err.Error(), "ISO 4217") {
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

func signSourceMetadataForTest(t *testing.T, payload []byte, privateKey ed25519.PrivateKey) string {
	return signSourceMetadataWithHeaderForTest(t, payload, privateKey, nil)
}

func signSourceMetadataWithHeaderForTest(t *testing.T, payload []byte, privateKey ed25519.PrivateKey, extra map[string]any) string {
	t.Helper()
	publicKey := privateKey.Public().(ed25519.PublicKey)
	x := testBase64URL.EncodeToString(publicKey)
	thumbprintInput := fmt.Sprintf(`{"crv":"Ed25519","kty":"OKP","x":"%s"}`, x)
	thumbprint := sha256.Sum256([]byte(thumbprintInput))
	protected := map[string]any{
		"alg": "EdDSA",
		"kid": testBase64URL.EncodeToString(thumbprint[:]),
		"typ": "gbo-attestations+jws",
	}
	for name, value := range extra {
		protected[name] = value
	}
	header, err := json.Marshal(protected)
	if err != nil {
		t.Fatalf("marshal protected header: %v", err)
	}
	signingInput := testBase64URL.EncodeToString(header) + "." + testBase64URL.EncodeToString(payload)
	signature := ed25519.Sign(privateKey, []byte(signingInput))
	return signingInput + "." + testBase64URL.EncodeToString(signature)
}

// Happy-path integration test: POST an issuance-server disclosure to the
// adapter's per-usecase endpoint. The FSC-Outway is stubbed with an
// httptest.Server returning a canned graphql-server response (BD-schema);
// the adapter should extract the BSN from the disclosure, call the
// (stubbed) outway, select the usecase's belastingjaar, and return an
// IssuableDocument list in the bri-mock shape.
func TestAdapterEndToEnd(t *testing.T) {
	// Stub outway — returns a canned BD-graphql response with two aangiften.
	var received proxyRequest
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bri/graphql" {
			t.Errorf("outway path = %q, want /bri/graphql", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode outway request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"ingeschrevenPersoon": {
					"heeftBelastingjaarAangifte": [
						{
							"belastingjaar": 2023,
							"status": "Definitief vastgesteld",
							"indieningsdatum": "2024-04-01",
							"verzamelinkomen": {"waarde": 40000.0, "valuta": "EUR"}
						},
						{
							"belastingjaar": 2024,
							"status": "Voorlopig vastgesteld",
							"indieningsdatum": "2025-05-01",
							"verzamelinkomen": {"waarde": 42000.0, "valuta": "EUR"},
							"box1Inkomen": {"waarde": 40000.0, "valuta": "EUR"},
							"box2Inkomen": {"waarde": 1000.0, "valuta": "EUR"},
							"box3Inkomen": {"waarde": 1000.0, "valuta": "EUR"}
						}
					]
				}
			}
		}`))
	}))
	defer outway.Close()

	cfg := config{
		Port:      "0",
		OutwayURL: outway.URL,
		IssuerOIN: "00000004000000004000",
	}
	catalog := &Catalog{
		Usecases: map[string]Usecase{
			"inkomensverklaring_2024": {
				AttestationType: "nl.gbo.belastingdienst.inkomensverklaring",
				Scope:           "bd:ib:2024",
				Belastingjaren:  []int{2024},
				OutwayPath:      "/bri/graphql",
			},
		},
	}
	srv := httptest.NewServer(newMux(cfg, catalog, http.DefaultClient))
	defer srv.Close()

	// Issuance-server request shape: one item, one attestation with a nested PID.
	body := []byte(`[{
		"id": "req-1",
		"attestations": [{
			"attestation_type": "urn:eudi:pid:nl:1",
			"attributes": {"urn:eudi:pid:nl:1": {"bsn": "123456789"}}
		}]
	}]`)
	resp, err := http.Post(srv.URL+"/inkomensverklaring_2024/", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, string(raw))
	}

	var docs []attestation
	if err := json.NewDecoder(resp.Body).Decode(&docs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("docs len = %d, want 1", len(docs))
	}
	if docs[0].AttestationType != "nl.gbo.belastingdienst.inkomensverklaring" {
		t.Errorf("attestation_type = %q", docs[0].AttestationType)
	}
	// Usecase belastingjaren [2024] must select the 2024 aangifte, not the
	// first (2023) one in the bron response.
	if got, want := docs[0].Attributes["belastingjaar"], float64(2024); got != want {
		t.Errorf("belastingjaar = %v, want %v", got, want)
	}
	// verzamelinkomen 42000.0 -> 42000 whole euros. JSON round-trips numbers
	// as float64 into map[string]any, so compare via float.
	if got, want := docs[0].Attributes["verzamelinkomen"], float64(42000); got != want {
		t.Errorf("verzamelinkomen = %v (%T), want %v", got, got, want)
	}
	if got, want := docs[0].Attributes["aangifte_status"], "Voorlopig vastgesteld"; got != want {
		t.Errorf("aangifte_status = %v, want %v", got, want)
	}
	if got, want := docs[0].Attributes["indieningsdatum"], "2025-05-01"; got != want {
		t.Errorf("indieningsdatum = %v, want %v", got, want)
	}
	// With the feature flag off, rollback is immediate: the original query
	// with its literal catalog year is used and no metadata parameter exists.
	if !strings.Contains(received.Query, "belastingjaren: [2024]") {
		t.Errorf("feature-flag-off query does not contain the legacy year: %s", received.Query)
	}
	if _, ok := received.Variables["jaar"]; ok {
		t.Errorf("feature-flag-off variables unexpectedly contain jaar: %v", received.Variables)
	}
}

// Phase 1 walking skeleton: the source publishes one signed declaration for
// income year 2025. With shadow mode enabled, that declaration supplies the
// query sent through the existing FSC/PDP route, while the adapter still
// returns the legacy IssuableDocument. The response header makes the shadow
// comparison observable without changing the issuance-server contract.
func TestAdapterUsesSignedSourceMetadataFor2025Shadow(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	metadataPayload, err := os.ReadFile("../graphql-server/config/gbo-attestations.json")
	if err != nil {
		t.Fatalf("read shipped source metadata: %v", err)
	}
	var shipped sourceMetadataDocument
	if err := json.Unmarshal(metadataPayload, &shipped); err != nil {
		t.Fatalf("parse shipped source metadata: %v", err)
	}
	if len(shipped.Attestations) != 1 {
		t.Fatalf("shipped source metadata has %d attestations, want 1", len(shipped.Attestations))
	}
	metadataQuery := shipped.Attestations[0].GraphQL.Document
	metadataJWS := signSourceMetadataForTest(t, metadataPayload, privateKey)
	metadataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/metadata/.well-known/gbo-attestations"; got != want {
			t.Errorf("metadata Outway path = %q, want %q", got, want)
		}
		if r.Header.Get("Fsc-Transaction-Id") == "" {
			t.Error("metadata request has no Fsc-Transaction-Id")
		}
		w.Header().Set("Content-Type", "application/jose")
		_, _ = w.Write([]byte(metadataJWS))
	}))
	defer metadataServer.Close()

	publicJWK, err := json.Marshal(map[string]string{
		"kty": "OKP",
		"crv": "Ed25519",
		"x":   testBase64URL.EncodeToString(publicKey),
	})
	if err != nil {
		t.Fatalf("marshal public JWK: %v", err)
	}
	publicJWKPath := filepath.Join(t.TempDir(), "source-metadata-public.jwk")
	if err := os.WriteFile(publicJWKPath, publicJWK, 0o600); err != nil {
		t.Fatalf("write public JWK: %v", err)
	}
	shadow, err := loadConfiguredSourceMetadataShadow(context.Background(), http.DefaultClient, config{
		OutwayURL:                   metadataServer.URL,
		SourceMetadataShadowEnabled: true,
		SourceMetadataOutwayPath:    "/metadata/.well-known/gbo-attestations",
		SourceMetadataOIN:           "99999999900000000200",
		SourceMetadataPublicJWKPath: publicJWKPath,
		SourceMetadataTypeID:        "inkomensverklaring",
		SourceMetadataUsecaseKey:    "inkomensverklaring_2025",
	})
	if err != nil {
		t.Fatalf("load signed source metadata: %v", err)
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

	cfg := config{Port: "0", OutwayURL: outway.URL, IssuerOIN: "00000004000000004000"}
	catalog := &Catalog{Usecases: map[string]Usecase{
		"inkomensverklaring_2025": {
			AttestationType: "nl.gbo.belastingdienst.inkomensverklaring",
			Scope:           "bd:ib:2025",
			Belastingjaren:  []int{2025},
			OutwayPath:      "/bri/graphql",
		},
	}}
	srv := httptest.NewServer(newMux(cfg, catalog, http.DefaultClient, shadow))
	defer srv.Close()

	body := []byte(`[{
		"id": "req-1",
		"attestations": [{
			"attestation_type": "urn:eudi:pid:nl:1",
			"attributes": {"urn:eudi:pid:nl:1": {"bsn": "123456789"}}
		}]
	}]`)
	resp, err := http.Post(srv.URL+"/inkomensverklaring_2025/", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, string(raw))
	}
	if got, want := resp.Header.Get("X-GBO-Metadata-Shadow"), "match"; got != want {
		t.Errorf("X-GBO-Metadata-Shadow = %q, want %q", got, want)
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
}

func TestMetadataPilotDoesNotApplyToAnotherBDUsecase(t *testing.T) {
	const metadataQuery = "query metadataShouldNotRun"
	shadow := &sourceMetadataShadow{
		UsecaseKey: "inkomensverklaring_2025",
		Definition: sourceAttestationDefinition{
			GraphQL: sourceGraphQL{Document: metadataQuery},
		},
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
				"verzamelinkomen": {"waarde": 43000.0, "valuta": "EUR"}
			}]}}
		}`))
	}))
	defer outway.Close()

	cfg := config{Port: "0", OutwayURL: outway.URL, IssuerOIN: "00000004000000004000"}
	catalog := &Catalog{Usecases: map[string]Usecase{
		"andere_bd_attestatie_2025": {
			AttestationType: "nl.gbo.belastingdienst.andere-attestatie",
			Scope:           "bd:ib:2025",
			Belastingjaren:  []int{2025},
			OutwayPath:      "/bri/graphql",
		},
	}}
	srv := httptest.NewServer(newMux(cfg, catalog, http.DefaultClient, shadow))
	defer srv.Close()

	resp := postDisclosure(t, srv, "/andere_bd_attestatie_2025/", "123456789")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, string(raw))
	}
	if received.Query == metadataQuery {
		t.Fatal("metadata query was applied to an unrelated BD usecase")
	}
	if got := resp.Header.Get("X-GBO-Metadata-Shadow"); got != "" {
		t.Errorf("unrelated usecase got shadow header %q", got)
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

func newIncomeShadowAdapter(t *testing.T, mapping map[string]mappingRule, bronResponse string) *httptest.Server {
	t.Helper()
	uc := Usecase{
		AttestationType: "nl.gbo.belastingdienst.inkomensverklaring",
		Scope:           "bd:ib:2025",
		Belastingjaren:  []int{2025},
		OutwayPath:      "/bri/graphql",
	}
	shadow := &sourceMetadataShadow{
		UsecaseKey: "inkomensverklaring_2025",
		Definition: sourceAttestationDefinition{
			GraphQL: sourceGraphQL{
				Document:        buildQuery(uc),
				SubjectVariable: "bsn",
				ResultPointer:   "/data/ingeschrevenPersoon/heeftBelastingjaarAangifte",
				Cardinality:     "exactly_one",
			},
			MappingProfile: "gbo-simple-v1",
			Mapping:        mapping,
		},
	}
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bronResponse))
	}))
	t.Cleanup(outway.Close)

	cfg := config{Port: "0", OutwayURL: outway.URL, IssuerOIN: "00000004000000004000"}
	catalog := &Catalog{Usecases: map[string]Usecase{"inkomensverklaring_2025": uc}}
	srv := httptest.NewServer(newMux(cfg, catalog, http.DefaultClient, shadow))
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

func TestMetadataPilotReportsMismatchWhenSourceOmitsLegacyClaim(t *testing.T) {
	mapping := phase1IncomeMapping()
	delete(mapping, "inkomen_box3")
	responseWithoutBox3 := strings.Replace(
		completeIncomeResponse,
		`,
		"box3Inkomen": {"waarde": 1000.0, "valuta": "EUR"}`,
		"",
		1,
	)
	srv := newIncomeShadowAdapter(t, mapping, responseWithoutBox3)

	resp := postDisclosure(t, srv, "/inkomensverklaring_2025/", "123456789")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, string(raw))
	}
	if got, want := resp.Header.Get("X-GBO-Metadata-Shadow"), "mismatch"; got != want {
		t.Errorf("X-GBO-Metadata-Shadow = %q, want %q", got, want)
	}
}

func TestMetadataPilotReportsMismatchWhenLegacyTruncatesCents(t *testing.T) {
	responseWithCents := strings.Replace(completeIncomeResponse, "43000.0", "43000.50", 1)
	srv := newIncomeShadowAdapter(t, phase1IncomeMapping(), responseWithCents)

	resp := postDisclosure(t, srv, "/inkomensverklaring_2025/", "123456789")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, string(raw))
	}
	if got, want := resp.Header.Get("X-GBO-Metadata-Shadow"), "mismatch"; got != want {
		t.Errorf("X-GBO-Metadata-Shadow = %q, want %q", got, want)
	}
}

func TestMetadataPilotFetchFailureFallsBackToLegacy(t *testing.T) {
	var received proxyRequest
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/metadata/.well-known/gbo-attestations" {
			http.Error(w, "metadata temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode outway request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(completeIncomeResponse))
	}))
	defer outway.Close()

	publicJWKPath := filepath.Join(t.TempDir(), "source-metadata-public.jwk")
	if err := os.WriteFile(publicJWKPath, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write public JWK: %v", err)
	}
	cfg := config{
		Port:                        "0",
		OutwayURL:                   outway.URL,
		IssuerOIN:                   "00000004000000004000",
		SourceMetadataShadowEnabled: true,
		SourceMetadataOutwayPath:    "/metadata/.well-known/gbo-attestations",
		SourceMetadataOIN:           "99999999900000000200",
		SourceMetadataPublicJWKPath: publicJWKPath,
		SourceMetadataTypeID:        "inkomensverklaring",
		SourceMetadataUsecaseKey:    "inkomensverklaring_2025",
	}
	uc := Usecase{
		AttestationType: "nl.gbo.belastingdienst.inkomensverklaring",
		Scope:           "bd:ib:2025",
		Belastingjaren:  []int{2025},
		OutwayPath:      "/bri/graphql",
	}
	catalog := &Catalog{Usecases: map[string]Usecase{"inkomensverklaring_2025": uc}}
	srv := httptest.NewServer(newRuntimeMux(context.Background(), cfg, catalog, http.DefaultClient))
	defer srv.Close()

	resp := postDisclosure(t, srv, "/inkomensverklaring_2025/", "123456789")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, string(raw))
	}
	if got := resp.Header.Get("X-GBO-Metadata-Shadow"); got != "" {
		t.Errorf("fallback response got shadow header %q", got)
	}
	if !strings.Contains(received.Query, "belastingjaren: [2025]") {
		t.Errorf("fallback did not use the legacy query: %s", received.Query)
	}
}

func TestSourceMetadataRejectedDuringOnboarding(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
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
					"cardinality":    "exactly_one",
				},
				"mapping_profile": "gbo-simple-v1",
				"mapping": map[string]any{
					"belastingjaar": map[string]any{"pointer": "/belastingjaar", "datatype": "gYear"},
				},
				"attribute_schema": map[string]any{
					"belastingjaar": map[string]any{"type": "integer", "format": "gYear"},
				},
			}},
		})
		if err != nil {
			t.Fatalf("marshal source metadata: %v", err)
		}
		return payload
	}
	publicJWK := func(t *testing.T, key ed25519.PublicKey) json.RawMessage {
		t.Helper()
		raw, err := json.Marshal(map[string]string{
			"kty": "OKP",
			"crv": "Ed25519",
			"x":   testBase64URL.EncodeToString(key),
		})
		if err != nil {
			t.Fatalf("marshal public JWK: %v", err)
		}
		return raw
	}

	tests := []struct {
		name        string
		payload     []byte
		pinnedKey   json.RawMessage
		expectedOIN string
		wantError   string
		corruptJWS  bool
		criticalJWS bool
	}{
		{
			name:        "invalid signature",
			payload:     metadataPayload(t, "00000001003214345000", validQuery),
			pinnedKey:   publicJWK(t, publicKey),
			expectedOIN: "00000001003214345000",
			wantError:   "invalid signature",
			corruptJWS:  true,
		},
		{
			name:        "wrong source OIN",
			payload:     metadataPayload(t, "00000001003214345001", validQuery),
			pinnedKey:   publicJWK(t, publicKey),
			expectedOIN: "00000001003214345000",
			wantError:   "does not match registered OIN",
		},
		{
			name:        "invalid GraphQL syntax",
			payload:     metadataPayload(t, "00000001003214345000", "query {"),
			pinnedKey:   publicJWK(t, publicKey),
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
			pinnedKey:   publicJWK(t, publicKey),
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
			pinnedKey:   publicJWK(t, publicKey),
			expectedOIN: "00000001003214345000",
			wantError:   "GBO_SIMPLE_MAPPING_INVALID",
		},
		{
			name:        "unsupported envelope schema version",
			payload:     bytes.Replace(metadataPayload(t, "00000001003214345000", validQuery), []byte(`"schema_version":"1.0"`), []byte(`"schema_version":"2.0"`), 1),
			pinnedKey:   publicJWK(t, publicKey),
			expectedOIN: "00000001003214345000",
			wantError:   "unsupported source metadata schema_version",
		},
		{
			name:        "unknown critical JWS header",
			payload:     metadataPayload(t, "00000001003214345000", validQuery),
			pinnedKey:   publicJWK(t, publicKey),
			expectedOIN: "00000001003214345000",
			wantError:   "critical JWS header",
			criticalJWS: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compact := signSourceMetadataForTest(t, test.payload, privateKey)
			if test.criticalJWS {
				compact = signSourceMetadataWithHeaderForTest(t, test.payload, privateKey, map[string]any{
					"crit": []string{"exp"},
					"exp":  true,
				})
			}
			if test.corruptJWS {
				signatureStart := strings.LastIndex(compact, ".") + 1
				replacement := byte('A')
				if compact[signatureStart] == replacement {
					replacement = 'B'
				}
				compact = compact[:signatureStart] + string(replacement) + compact[signatureStart+1:]
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/jose")
				_, _ = w.Write([]byte(compact))
			}))
			defer server.Close()

			_, err := loadSourceMetadataShadow(context.Background(), http.DefaultClient, sourceMetadataConfig{
				URL:         server.URL,
				ExpectedOIN: test.expectedOIN,
				PublicJWK:   test.pinnedKey,
				TypeID:      "inkomensverklaring",
			})
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want error containing %q", err, test.wantError)
			}
		})
	}
}

// brpUsecase is the akte-van-overlijden catalog entry as a Go literal, so
// the tests do not depend on config/usecase_catalog.json.
var brpUsecase = Usecase{
	AttestationType: "nl.gbo.brp.akte-van-overlijden",
	Bron:            bronBRP,
	Scope:           "brp:akte:overlijden",
	OutwayPath:      "/brp/graphql",
}

// brpResponse is a canned BRP bron response for Frouke Jansen: one marriage,
// dissolved by the death of her partner, with `partners` symmetric (both
// spouses listed) exactly as the bronprofiel prescribes.
const brpResponse = `{
	"data": {
		"ingeschrevenPersoon": {
			"id": "018f2c4a-0001-7000-8000-000000000001",
			"bsn": "999991772",
			"geslachtsnaam": "Jansen",
			"voorvoegsel": null,
			"voornamen": "Frouke",
			"heeftHuwelijk": [{
				"soortVerbintenis": "Huwelijk",
				"datumVoltrekking": "2023-06-16",
				"plaatsVoltrekking": "Rotterdam",
				"landVoltrekking": "NL",
				"datumOntbinding": "2026-02-14",
				"redenOntbinding": "Overlijden",
				"partners": [
					{
						"id": "018f2c4a-0001-7000-8000-000000000001",
						"geslachtsnaam": "Jansen",
						"voornamen": "Frouke",
						"geboortedatum": "2000-03-24",
						"geslacht": "Vrouw",
						"datumOverlijden": null
					},
					{
						"id": "018f2c4a-0002-7000-8000-000000000002",
						"geslachtsnaam": "Vries",
						"voorvoegsel": "de",
						"voornamen": "Sander Willem",
						"geboortedatum": "1997-11-12",
						"geboorteplaats": "Rotterdam",
						"geboorteland": "NL",
						"geslacht": "Man",
						"datumOverlijden": "2026-02-14",
						"plaatsOverlijden": "Rotterdam",
						"landOverlijden": "NL",
						"heeftOuder": [
							{"geslachtsnaam": "Vries", "voorvoegsel": "de", "voornamen": "Hendrik Jan"},
							{"geslachtsnaam": "Bakker", "voornamen": "Annelies Maria"}
						]
					}
				]
			}]
		}
	}
}`

func brpAdapter(t *testing.T, bronResponse string) (*httptest.Server, *httptest.Server) {
	t.Helper()
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/brp/graphql" {
			t.Errorf("outway path = %q, want /brp/graphql", r.URL.Path)
		}
		// The BRP flow must reach the PDP as its own flow, otherwise the
		// PDP resolves the query against the BD mirror schema.
		if got, want := r.Header.Get("X-GBO-Flow"), "eudi:attestation:brp"; got != want {
			t.Errorf("X-GBO-Flow = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("X-GBO-Scope"), "brp:akte:overlijden"; got != want {
			t.Errorf("X-GBO-Scope = %q, want %q", got, want)
		}
		raw, _ := io.ReadAll(r.Body)
		var req proxyRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("outway received unparseable body: %v", err)
		}
		if !strings.Contains(req.Query, "heeftHuwelijk") {
			t.Errorf("outway received a non-BRP query: %s", req.Query)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bronResponse))
	}))
	cfg := config{Port: "0", OutwayURL: outway.URL, IssuerOIN: "00000004000000004000"}
	catalog := &Catalog{Usecases: map[string]Usecase{"akte_van_overlijden": brpUsecase}}
	srv := httptest.NewServer(newMux(cfg, catalog, http.DefaultClient))
	return outway, srv
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

// Akte-van-overlijden happy path: the adapter must pick the deceased spouse
// out of the symmetric `partners` list and map the BRP fields onto the
// attestation attributes.
func TestAdapterAkteVanOverlijden(t *testing.T) {
	outway, srv := brpAdapter(t, brpResponse)
	defer outway.Close()
	defer srv.Close()

	resp := postDisclosure(t, srv, "/akte_van_overlijden/", "999991772")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, string(raw))
	}

	var docs []attestation
	if err := json.NewDecoder(resp.Body).Decode(&docs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("docs len = %d, want 1", len(docs))
	}
	if got, want := docs[0].AttestationType, "nl.gbo.brp.akte-van-overlijden"; got != want {
		t.Errorf("attestation_type = %q, want %q", got, want)
	}
	attrs := docs[0].Attributes
	want := map[string]any{
		"overledene_geslachtsnaam":  "Vries",
		"overledene_voorvoegsel":    "de",
		"overledene_voornamen":      "Sander Willem",
		"overledene_geboortedatum":  "1997-11-12",
		"overledene_geboorteplaats": "Rotterdam",
		"overledene_geboorteland":   "NL",
		"overledene_geslacht":       "Man",
		"overledene_ouders":         "Hendrik Jan de Vries; Annelies Maria Bakker",
		"datum_overlijden":          "2026-02-14",
		"plaats_overlijden":         "Rotterdam",
		"land_overlijden":           "NL",
		"soort_verbintenis":         "Huwelijk",
		"echtgenoot_geslachtsnaam":  "Jansen",
		"echtgenoot_voornamen":      "Frouke",
	}
	for k, v := range want {
		if got := attrs[k]; got != v {
			t.Errorf("attribute %s = %v, want %v", k, got, v)
		}
	}
	// The echtgenoot has no voorvoegsel — an akte must not assert an empty
	// claim for it.
	if _, ok := attrs["echtgenoot_voorvoegsel"]; ok {
		t.Errorf("empty echtgenoot_voorvoegsel should be omitted, got %v", attrs["echtgenoot_voorvoegsel"])
	}
	if got, want := attrs["verklaring_tekst"], "Op 2026-02-14 is te Rotterdam overleden Sander Willem de Vries, geboren op 1997-11-12 te Rotterdam, gehuwd met Frouke Jansen."; got != want {
		t.Errorf("verklaring_tekst = %q, want %q", got, want)
	}
}

// The query fetches more than an akte carries: the huwelijk's voltrekking and
// ontbinding are needed to select the right marriage and to establish that it
// ended in death, but they belong on the huwelijksakte. The BSN and the
// persoonslijst-ids are not on an akte either. None of them may leak into the
// credential.
func TestAkteVanOverlijdenOmitsNonAkteFields(t *testing.T) {
	outway, srv := brpAdapter(t, brpResponse)
	defer outway.Close()
	defer srv.Close()

	resp := postDisclosure(t, srv, "/akte_van_overlijden/", "999991772")
	defer resp.Body.Close()
	var docs []attestation
	if err := json.NewDecoder(resp.Body).Decode(&docs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, forbidden := range []string{
		"datum_voltrekking", "plaats_voltrekking", "land_voltrekking",
		"datum_ontbinding", "reden_ontbinding",
		"bsn", "id", "nabestaande_geslachtsnaam",
	} {
		if v, ok := docs[0].Attributes[forbidden]; ok {
			t.Errorf("attribute %q must not be on an akte van overlijden, got %v", forbidden, v)
		}
	}

	// Every attribute the adapter emits must be declared in the credential
	// metadata, otherwise the issuance-server rejects the document.
	raw, err := os.ReadFile("../eudi-issuance-server/config/akte_van_overlijden_metadata.json.example")
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var meta struct {
		VCT    string `json:"vct"`
		Claims []struct {
			Path []string `json:"path"`
		} `json:"claims"`
		Schema struct {
			Properties map[string]any `json:"properties"`
		} `json:"schema"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("parse metadata: %v", err)
	}
	if meta.VCT != docs[0].AttestationType {
		t.Errorf("metadata vct = %q, adapter emits %q", meta.VCT, docs[0].AttestationType)
	}
	declared := map[string]bool{}
	for _, c := range meta.Claims {
		declared[c.Path[0]] = true
	}
	for name := range docs[0].Attributes {
		if !declared[name] {
			t.Errorf("attribute %q is not declared in the credential metadata", name)
		}
		if _, ok := meta.Schema.Properties[name]; !ok {
			t.Errorf("attribute %q is not in the credential JSON-schema", name)
		}
	}
}

// A geregistreerd partnerschap is worded differently on the akte than a
// huwelijk.
func TestAkteVerklaringGeregistreerdPartnerschap(t *testing.T) {
	persoon := brpPersoon{Geslachtsnaam: "Jansen", Voornamen: "Frouke"}
	huwelijk := brpHuwelijk{SoortVerbintenis: "GeregistreerdPartnerschap"}
	overledene := brpPartner{
		Geslachtsnaam: "Vries", Voorvoegsel: "de", Voornamen: "Sander Willem",
		DatumOverlijden: "2026-02-14", PlaatsOverlijden: "Rotterdam",
	}
	got := akteVerklaring(persoon, huwelijk, overledene)
	want := "Op 2026-02-14 is te Rotterdam overleden Sander Willem de Vries, geregistreerd partner van Frouke Jansen."
	if got != want {
		t.Errorf("verklaring = %q, want %q", got, want)
	}
}

// Married to a living partner: the bron answers, but there is nothing to
// certify — 404, not a 200 with an empty akte.
func TestAdapterAkteVanOverlijdenNoDeceasedPartner(t *testing.T) {
	response := `{"data":{"ingeschrevenPersoon":{
		"id":"p-1","bsn":"123456789","geslachtsnaam":"Bakker","voornamen":"Joost Pieter",
		"heeftHuwelijk":[{"soortVerbintenis":"Huwelijk","datumVoltrekking":"2016-08-20","partners":[
			{"id":"p-1","geslachtsnaam":"Bakker","voornamen":"Joost Pieter"},
			{"id":"p-2","geslachtsnaam":"Smit","voornamen":"Lisa"}
		]}]}}}`
	outway, srv := brpAdapter(t, response)
	defer outway.Close()
	defer srv.Close()

	resp := postDisclosure(t, srv, "/akte_van_overlijden/", "123456789")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 404; body = %s", resp.StatusCode, string(raw))
	}
}

// Unknown BSN: the bron returns null for ingeschrevenPersoon.
func TestAdapterAkteVanOverlijdenUnknownBSN(t *testing.T) {
	outway, srv := brpAdapter(t, `{"data":{"ingeschrevenPersoon":null}}`)
	defer outway.Close()
	defer srv.Close()

	resp := postDisclosure(t, srv, "/akte_van_overlijden/", "000000000")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// Twice widowed: the most recent overlijden is the one being certified.
func TestSelectOverledenPartnerPicksMostRecent(t *testing.T) {
	persoon := brpPersoon{
		ID: "self",
		HeeftHuwelijk: []brpHuwelijk{
			{DatumVoltrekking: "1998-01-01", DatumOntbinding: "2005-03-02", RedenOntbinding: "Overlijden",
				Partners: []brpPartner{
					{ID: "self"}, {ID: "a", Geslachtsnaam: "Eerste", DatumOverlijden: "2005-03-02"},
				}},
			{DatumVoltrekking: "2010-01-01", DatumOntbinding: "2022-09-30", RedenOntbinding: "Overlijden",
				Partners: []brpPartner{
					{ID: "self"}, {ID: "b", Geslachtsnaam: "Tweede", DatumOverlijden: "2022-09-30"},
				}},
		},
	}
	huwelijk, overledene, ok := selectOverledenPartner(persoon)
	if !ok {
		t.Fatal("expected an overleden partner")
	}
	if overledene.Geslachtsnaam != "Tweede" {
		t.Errorf("overledene = %q, want Tweede (most recent)", overledene.Geslachtsnaam)
	}
	if huwelijk.DatumVoltrekking != "2010-01-01" {
		t.Errorf("huwelijk = %q, want the marriage of the most recent overledene", huwelijk.DatumVoltrekking)
	}
}

// Divorced first, ex-partner died years later: the BRP still carries that
// marriage and that datumOverlijden, but the echtscheiding — not this death —
// ended the verbintenis, so there is no akte to hand the ex-spouse.
func TestSelectOverledenPartnerIgnoresEchtscheiding(t *testing.T) {
	persoon := brpPersoon{
		ID: "self",
		HeeftHuwelijk: []brpHuwelijk{
			{DatumVoltrekking: "1998-01-01", DatumOntbinding: "2004-06-11", RedenOntbinding: "Echtscheiding",
				Partners: []brpPartner{
					{ID: "self"}, {ID: "a", Geslachtsnaam: "Ex", DatumOverlijden: "2021-07-19"},
				}},
		},
	}
	if _, overledene, ok := selectOverledenPartner(persoon); ok {
		t.Errorf("selected %q from a marriage ended by echtscheiding, want no match", overledene.Geslachtsnaam)
	}
}

// Ontbonden door overlijden, but on a different day than the partner died:
// the two do not describe the same event, so the akte is not issued.
func TestSelectOverledenPartnerRejectsMismatchedDates(t *testing.T) {
	persoon := brpPersoon{
		ID: "self",
		HeeftHuwelijk: []brpHuwelijk{
			{DatumVoltrekking: "1998-01-01", DatumOntbinding: "2005-03-02", RedenOntbinding: "Overlijden",
				Partners: []brpPartner{
					{ID: "self"}, {ID: "a", Geslachtsnaam: "Eerste", DatumOverlijden: "2011-08-23"},
				}},
		},
	}
	if _, overledene, ok := selectOverledenPartner(persoon); ok {
		t.Errorf("selected %q on mismatched ontbinding/overlijden dates, want no match", overledene.Geslachtsnaam)
	}
}

// A DatumIncompleet the bron only knows to the year cannot be compared
// day-for-day. Refusing the akte over the bron's date precision would be
// worse than accepting it, so the partial is let through.
func TestSelectOverledenPartnerAcceptsPartialDates(t *testing.T) {
	persoon := brpPersoon{
		ID: "self",
		HeeftHuwelijk: []brpHuwelijk{
			{DatumVoltrekking: "1998-01-01", DatumOntbinding: "2005", RedenOntbinding: "Overlijden",
				Partners: []brpPartner{
					{ID: "self"}, {ID: "a", Geslachtsnaam: "Eerste", DatumOverlijden: "2005-03-02"},
				}},
		},
	}
	_, overledene, ok := selectOverledenPartner(persoon)
	if !ok {
		t.Fatal("expected an overleden partner on a partial datumOntbinding")
	}
	if overledene.Geslachtsnaam != "Eerste" {
		t.Errorf("overledene = %q, want Eerste", overledene.Geslachtsnaam)
	}
}

// Widowed once and divorced once, the ex dying later: the akte must be about
// the partner whose death actually ended a marriage — end to end, through the
// adapter, not just through the selection helper.
func TestAdapterAkteVanOverlijdenSkipsLaterDeathOfExPartner(t *testing.T) {
	response := `{"data":{"ingeschrevenPersoon":{
		"id":"p-1","bsn":"999991772","geslachtsnaam":"Jansen","voornamen":"Frouke",
		"heeftHuwelijk":[
			{"soortVerbintenis":"Huwelijk","datumVoltrekking":"1998-01-01",
			 "datumOntbinding":"2004-06-11","redenOntbinding":"Echtscheiding","partners":[
				{"id":"p-1","geslachtsnaam":"Jansen","voornamen":"Frouke"},
				{"id":"p-9","geslachtsnaam":"Ex","voornamen":"Oude","datumOverlijden":"2026-05-01"}
			]},
			{"soortVerbintenis":"Huwelijk","datumVoltrekking":"2010-01-01",
			 "datumOntbinding":"2022-09-30","redenOntbinding":"Overlijden","partners":[
				{"id":"p-1","geslachtsnaam":"Jansen","voornamen":"Frouke"},
				{"id":"p-2","geslachtsnaam":"Vries","voorvoegsel":"de","voornamen":"Sander Willem","datumOverlijden":"2022-09-30"}
			]}
		]}}}`
	outway, srv := brpAdapter(t, response)
	defer outway.Close()
	defer srv.Close()

	resp := postDisclosure(t, srv, "/akte_van_overlijden/", "999991772")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, string(raw))
	}
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)
	// The ex-partner died most recently, so a naive "latest datumOverlijden"
	// selection would certify him instead.
	if strings.Contains(body, "2026-05-01") || strings.Contains(body, `"Oude"`) {
		t.Errorf("akte certifies the ex-partner's later death; body = %s", body)
	}
	if !strings.Contains(body, "2022-09-30") {
		t.Errorf("akte does not certify the death that ended the marriage; body = %s", body)
	}
}

// The catalog shipped with the service must stay consistent with the code:
// every usecase names a bron the adapter knows, and its flow must match a
// flow the PDP has a mirror-schema for.
func TestShippedCatalogIsConsistent(t *testing.T) {
	catalog, err := loadCatalog("config/usecase_catalog.json")
	if err != nil {
		t.Fatalf("loadCatalog: %v", err)
	}
	for key, uc := range catalog.Usecases {
		switch uc.bron() {
		case bronBD:
			if uc.flow() != flowEudiBD {
				t.Errorf("usecase %s: flow = %q, want %q", key, uc.flow(), flowEudiBD)
			}
			if len(uc.Belastingjaren) == 0 {
				t.Errorf("usecase %s: BD usecase without belastingjaren", key)
			}
		case bronBRP:
			if uc.flow() != flowEudiBRP {
				t.Errorf("usecase %s: flow = %q, want %q", key, uc.flow(), flowEudiBRP)
			}
		default:
			t.Errorf("usecase %s: unknown bron %q", key, uc.bron())
		}
		if uc.Scope == "" || uc.OutwayPath == "" || uc.AttestationType == "" {
			t.Errorf("usecase %s: incomplete catalog entry %+v", key, uc)
		}
	}
}
