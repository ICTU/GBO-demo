package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandRequiredEnvironmentRejectsUnsetVariable(t *testing.T) {
	t.Setenv("GBO_TEST_REQUIRED_ENV", "")
	_, err := expandRequiredEnvironment(`value = "${GBO_TEST_REQUIRED_ENV}"`)
	if err == nil || !strings.Contains(err.Error(), "GBO_TEST_REQUIRED_ENV") {
		t.Fatalf("error = %v, want missing environment variable", err)
	}
}

func TestAttestationStatusPathUsesFullVCTDigest(t *testing.T) {
	const vct = "https://issuer.example/types/99999999900000000200/example/v1"
	var settings strings.Builder
	appendAttestationSettings(&settings, vct, issuanceCertificateMaterial{})
	digest := sha256.Sum256([]byte(vct))
	want := `/tsl/` + hex.EncodeToString(digest[:])
	if !strings.Contains(settings.String(), want) {
		t.Fatalf("generated settings do not contain full status digest %q", want)
	}
}

func TestLoadIssuanceActivationsRejectsUnsafeTypeID(t *testing.T) {
	root := t.TempDir()
	activeDir := filepath.Join(root, "active")
	writeTestActivation(t, activeDir, root, "test-source", "99999999900000000200", "../escape", "https://issuer.example/type", "test", []sourceOffer{{ID: "example", Label: "Example", Parameters: map[string]any{}}})
	if _, err := loadIssuanceActivations(activeDir); err == nil {
		t.Fatal("unsafe activation type_id was accepted")
	}
}

func TestLoadIssuanceActivationsRejectsMissingQuerySnapshot(t *testing.T) {
	root := t.TempDir()
	activeDir := filepath.Join(root, "active")
	writeTestActivation(t, activeDir, root, "test-source", "99999999900000000200", "example", "https://issuer.example/type", "test", []sourceOffer{{ID: "example", Label: "Example", Parameters: map[string]any{}}})
	path := filepath.Join(activeDir, "test-source.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var activation sourceActivation
	if err := json.Unmarshal(body, &activation); err != nil {
		t.Fatal(err)
	}
	activation.Types[0].Definition = sourceAttestationDefinition{}
	body, _ = json.Marshal(activation)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadIssuanceActivations(activeDir); err == nil || !strings.Contains(err.Error(), "definition") {
		t.Fatalf("missing query snapshot error = %v", err)
	}
}

func TestLoadIssuanceActivationsAllowsSharedOINAndTypeAcrossSourceIDs(t *testing.T) {
	root := t.TempDir()
	activeDir := filepath.Join(root, "active")
	for _, sourceID := range []string{"belastingdienst", "rvig"} {
		writeTestActivation(
			t, activeDir, root, sourceID, "99999999900000000200", "shared-type",
			"https://issuer.example/types/"+sourceID+"/shared-type/v1.0", sourceID,
			[]sourceOffer{{ID: sourceID + "-offer", Label: sourceID, Parameters: map[string]any{}}},
		)
	}
	activations, err := loadIssuanceActivations(activeDir)
	if err != nil {
		t.Fatalf("load shared-OID activations: %v", err)
	}
	if len(activations) != 2 {
		t.Fatalf("activations = %d, want 2", len(activations))
	}
}

func TestFormatOfferParameterSupportsFiniteNumbers(t *testing.T) {
	for _, value := range []any{float64(1.25), int64(2), json.Number("3.5")} {
		if _, err := formatOfferParameter("amount", "number", value); err != nil {
			t.Errorf("formatOfferParameter(%v) = %v", value, err)
		}
	}
}

func TestGenerateIssuanceConfigFromAllActivatedOffers(t *testing.T) {
	root := t.TempDir()
	activeDir := filepath.Join(root, "active")
	configDir := filepath.Join(root, "config")
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	templatePath := filepath.Join(root, "issuance.toml.example")
	template := `public_url = "${EUDI_PUBLIC_URL}"
issuer_trust_anchors = [
${EUDI_ONBOARDING_ISSUER_TRUST_ANCHOR}
]
reader_trust_anchors = [
${EUDI_ONBOARDING_READER_TRUST_ANCHOR}
]
metadata = [
{{TYPE_METADATA_FILES}}
]
# GBO_GENERATOR_ADAPTER_trust_anchors = ["adapter-ca"]
{{GENERATED_ISSUANCE_SETTINGS}}
`
	writeTestFile(t, templatePath, []byte(template))

	writeTestActivation(t, activeDir, root, "belastingdienst", "99999999900000000200", "inkomensverklaring", "https://issuer.example/types/belastingdienst/inkomensverklaring/v1.0", "bd", []sourceOffer{
		{ID: "inkomensverklaring_2024", Label: "Inkomensverklaring 2024", Parameters: map[string]any{"jaar": float64(2024)}},
		{ID: "inkomensverklaring_2025", Label: "Inkomensverklaring 2025", Parameters: map[string]any{"jaar": float64(2025)}},
	})
	writeTestActivation(t, activeDir, root, "rvig", "99999999900000000200", "akte-van-overlijden", "https://issuer.example/types/rvig/akte-van-overlijden/v1.0", "brp", []sourceOffer{
		{ID: "akte_van_overlijden", Label: "Akte van overlijden", Parameters: map[string]any{}},
	})

	t.Setenv("EUDI_PUBLIC_URL", "https://issuer.example")
	outputPath := filepath.Join(configDir, "issuance_server.toml")
	offersPath := filepath.Join(configDir, "eudi-offers.json")
	if err := generateIssuanceConfig(issuanceConfigOptions{
		activationsDir: activeDir,
		templatePath:   templatePath,
		adapterBaseURL: "https://adapter.example/base",
		outputPath:     outputPath,
		offersPath:     offersPath,
	}); err != nil {
		t.Fatalf("generate issuance config: %v", err)
	}

	generated, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(generated)
	for _, expected := range []string{
		`public_url = "https://issuer.example"`,
		`[disclosure_settings."inkomensverklaring_2024"]`,
		`base_url = "https://adapter.example/base/attestations/belastingdienst/inkomensverklaring?jaar=2024"`,
		`[disclosure_settings."inkomensverklaring_2025"]`,
		`[disclosure_settings."akte_van_overlijden"]`,
		`base_url = "https://adapter.example/base/attestations/rvig/akte-van-overlijden"`,
		`[attestation_settings."https://issuer.example/types/belastingdienst/inkomensverklaring/v1.0"]`,
		`private_key = "bd-issuer-key"`,
		`[attestation_settings."https://issuer.example/types/rvig/akte-van-overlijden/v1.0"]`,
		`private_key = "brp-issuer-key"`,
		`"/config/type-belastingdienst-inkomensverklaring-v1.0.json"`,
		`"/config/type-rvig-akte-van-overlijden-v1.0.json"`,
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("generated config is missing %q\n%s", expected, text)
		}
	}
	if strings.Contains(text, "inkomensverklaring_2023") {
		t.Error("generated config contains a non-offered 2023 issuance product")
	}

	var offers []publicIssuanceOffer
	offersBody, err := os.ReadFile(offersPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(offersBody, &offers); err != nil {
		t.Fatalf("decode offers: %v", err)
	}
	if got, want := len(offers), 3; got != want {
		t.Fatalf("offers = %d, want %d", got, want)
	}
	for _, offer := range offers {
		if offer.SourceID == "" || offer.SourceOIN != "99999999900000000200" {
			t.Errorf("offer source binding = %+v", offer)
		}
	}
}

func TestGenerateIssuanceConfigRejectsBlockedConfiguredSource(t *testing.T) {
	root := t.TempDir()
	candidatesDir := filepath.Join(root, "candidates")
	sourcesDir := filepath.Join(root, "sources")
	statusDir := filepath.Join(root, "status")
	writeTestActivation(t, candidatesDir, root, "demo", "99999999900000000900", "example", "https://issuer.example/types/demo/example/v1.0", "demo", []sourceOffer{{ID: "example", Label: "Example", Parameters: map[string]any{}}})
	writeTestFile(t, filepath.Join(sourcesDir, "demo.yaml"), []byte(`source_id: demo
source_oin: "99999999900000000900"
name: Demo
certificate_set: demo
metadata_endpoint:
  transport: unsecured
  endpoint: http://demo-source:4000/.well-known/gbo
data_access:
  transport: unsecured
`))
	statusBody, err := json.Marshal(sourceReconcileStatus{SourceID: "demo", State: sourceStateBlocked, Reason: sourceReasonMetadataFetchFailed})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(statusDir, "demo.json"), statusBody)

	err = generateIssuanceConfig(issuanceConfigOptions{
		activationsDir: candidatesDir, sourcesDir: sourcesDir, statusDir: statusDir,
		templatePath: filepath.Join(root, "unused-template"), adapterBaseURL: "https://adapter.example",
		outputPath: filepath.Join(root, "output.toml"), offersPath: filepath.Join(root, "offers.json"),
	})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("generation error = %v", err)
	}
}

func TestSuccessfulIssuanceGenerationPromotesCandidate(t *testing.T) {
	root := t.TempDir()
	candidatesDir := filepath.Join(root, "candidates")
	activeDir := filepath.Join(root, "active")
	sourcesDir := filepath.Join(root, "sources")
	statusDir := filepath.Join(root, "status")
	writeTestActivation(t, candidatesDir, root, "demo", "99999999900000000900", "example", "https://issuer.example/types/demo/example/v1.0", "demo", []sourceOffer{{ID: "example", Label: "Example", Parameters: map[string]any{}}})
	writeTestFile(t, filepath.Join(sourcesDir, "demo.yaml"), []byte(`source_id: demo
source_oin: "99999999900000000900"
name: Demo
certificate_set: demo
metadata_endpoint:
  transport: unsecured
  endpoint: http://demo-source:4000/.well-known/gbo
data_access:
  transport: unsecured
`))
	statusBody, err := json.Marshal(sourceReconcileStatus{SourceID: "demo", State: sourceStateRolloutRequired})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(statusDir, "demo.json"), statusBody)
	templatePath := filepath.Join(root, "issuance.toml.example")
	writeTestFile(t, templatePath, []byte(`public_url = "${EUDI_PUBLIC_URL}"
issuer_trust_anchors = [${EUDI_ONBOARDING_ISSUER_TRUST_ANCHOR}]
reader_trust_anchors = [${EUDI_ONBOARDING_READER_TRUST_ANCHOR}]
metadata = [{{TYPE_METADATA_FILES}}]
# GBO_GENERATOR_ADAPTER_trust_anchors = ["adapter-ca"]
{{GENERATED_ISSUANCE_SETTINGS}}
`))
	t.Setenv("EUDI_PUBLIC_URL", "https://issuer.example")
	if err := generateIssuanceConfig(issuanceConfigOptions{
		activationsDir: candidatesDir, activeDir: activeDir, sourcesDir: sourcesDir, statusDir: statusDir,
		templatePath: templatePath, adapterBaseURL: "https://adapter.example",
		outputPath: filepath.Join(root, "output.toml"), offersPath: filepath.Join(root, "offers.json"),
	}); err != nil {
		t.Fatalf("generate and promote: %v", err)
	}
	if _, err := os.Stat(filepath.Join(activeDir, "demo.json")); err != nil {
		t.Fatalf("deployed activation: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(statusDir, "demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	var status sourceReconcileStatus
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatal(err)
	}
	if status.State != sourceStateActive || status.DeploymentDigest == "" {
		t.Fatalf("deployed status = %+v", status)
	}
}

func writeTestActivation(t *testing.T, activeDir, root, sourceID, oin, typeID, vct, certificatePrefix string, offers []sourceOffer) {
	t.Helper()
	certificateDir := filepath.Join(root, "certificates", sourceID)
	metadataPath := filepath.Join(root, "metadata", sourceID+".json")
	metadataBody := []byte(`{"name":"test"}`)
	writeTestFile(t, metadataPath, metadataBody)
	metadataDigest := sha256.Sum256(metadataBody)
	metadataIntegrity := "sha256-" + base64.StdEncoding.EncodeToString(metadataDigest[:])
	writeTestFile(t, filepath.Join(certificateDir, "issuer.key"), []byte(certificatePrefix+"-issuer-key"))
	writeTestFile(t, filepath.Join(certificateDir, "issuer.cert"), []byte(certificatePrefix+"-issuer-cert"))
	writeTestFile(t, filepath.Join(certificateDir, "reader.key"), []byte(certificatePrefix+"-reader-key"))
	writeTestFile(t, filepath.Join(certificateDir, "reader.cert"), []byte(certificatePrefix+"-reader-cert"))
	writeTestFile(t, filepath.Join(certificateDir, "status.key"), []byte(certificatePrefix+"-status-key"))
	writeTestFile(t, filepath.Join(certificateDir, "status.cert"), []byte(certificatePrefix+"-status-cert"))
	issuerCA := filepath.Join(certificateDir, "issuer-ca.pem")
	readerCA := filepath.Join(certificateDir, "reader-ca.pem")
	writeTestFile(t, issuerCA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte(certificatePrefix + "-issuer-ca")}))
	writeTestFile(t, readerCA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte(certificatePrefix + "-reader-ca")}))
	parameters := map[string]sourceParameter{}
	for _, offer := range offers {
		for name := range offer.Parameters {
			parameters[name] = sourceParameter{Type: "integer", Required: true}
		}
	}

	activation := sourceActivation{
		SchemaVersion: "1.0",
		Source:        sourceRegistration{SourceID: sourceID, SourceOIN: oin, Name: certificatePrefix, CertificateSet: sourceID},
		Types: []activatedType{{
			TypeID: typeID, TypeVersion: "1.0", VCT: vct,
			VCTIntegrity: metadataIntegrity, TypeMetadataReference: metadataPath, Offers: offers,
			Definition: sourceAttestationDefinition{
				TypeID: typeID, TypeVersion: "1.0", Offers: offers,
				GraphQL:        sourceGraphQL{Endpoint: "/graphql", Document: "query Example($bsn: String!) { example(bsn: $bsn) { value } }", SubjectVariable: "bsn", Parameters: parameters, ResultPointer: "/data/example"},
				MappingProfile: "gbo-simple-v1", Mapping: map[string]mappingRule{"value": {Pointer: "/value", Datatype: "string"}},
				AttributeSchema: map[string]sourceAttributeSchema{"value": {Type: "string"}},
			},
		}},
		Certificates: certificateArtifacts{
			IssuerKeyReference: issuerCAPath(certificateDir, "issuer.key"), IssuerCertReference: issuerCAPath(certificateDir, "issuer.cert"),
			ReaderKeyReference: issuerCAPath(certificateDir, "reader.key"), ReaderCertReference: issuerCAPath(certificateDir, "reader.cert"),
			StatusKeyReference: issuerCAPath(certificateDir, "status.key"), StatusCertReference: issuerCAPath(certificateDir, "status.cert"),
			IssuerCACertReference: issuerCA, ReaderCACertReference: readerCA,
		},
	}
	body, err := json.Marshal(activation)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(activeDir, sourceID+".json"), body)
}

func issuerCAPath(directory, name string) string {
	return filepath.Join(directory, name)
}

func writeTestFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}
