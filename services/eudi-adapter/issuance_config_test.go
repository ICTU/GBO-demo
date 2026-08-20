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

func TestGenerateIssuanceConfigRejectsOnlyBlockedSourceWithoutDeployedFallback(t *testing.T) {
	root := t.TempDir()
	candidatesDir := filepath.Join(root, "candidates")
	sourcesDir := filepath.Join(root, "sources")
	statusDir := filepath.Join(root, "status")
	writeTestActivation(t, candidatesDir, root, "demo", "99999999900000000900", "example", "https://issuer.example/types/demo/example/v1.0", "demo", []sourceOffer{{ID: "example", Label: "Example", Parameters: map[string]any{}}})
	writeTestFile(t, filepath.Join(sourcesDir, "demo.yaml"), []byte(`metadata_endpoint:
  transport: unsecured
  endpoint: http://demo-source:4000/.well-known/gbo
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
	if err == nil || !strings.Contains(err.Error(), "no configured source has a deployable activation") {
		t.Fatalf("generation error = %v", err)
	}
}

func TestRolloutRefusesToPruneStateWithoutConfiguredSources(t *testing.T) {
	for _, test := range []struct {
		name             string
		createSourcesDir bool
	}{
		{name: "missing directory"},
		{name: "empty directory", createSourcesDir: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			candidatesDir := filepath.Join(root, "candidates")
			activeDir := filepath.Join(root, "active")
			statusDir := filepath.Join(root, "status")
			sourcesDir := filepath.Join(root, "sources")
			if test.createSourcesDir {
				if err := os.MkdirAll(sourcesDir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			paths := []string{
				filepath.Join(candidatesDir, "keep.json"),
				filepath.Join(activeDir, "keep.json"),
				filepath.Join(statusDir, "keep.json"),
			}
			for _, path := range paths {
				writeTestFile(t, path, []byte(`{"keep":true}`))
			}

			_, err := resolveIssuanceRolloutActivations(issuanceConfigOptions{
				activationsDir: candidatesDir, activeDir: activeDir, sourcesDir: sourcesDir, statusDir: statusDir,
			})
			if err == nil || !strings.Contains(err.Error(), "contains no configured sources; refusing to prune") {
				t.Fatalf("rollout error = %v", err)
			}
			for _, path := range paths {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("state file %s was removed: %v", path, err)
				}
			}
		})
	}
}

func TestRolloutReplacesCorruptDesiredActiveAndPrunesLegacyActive(t *testing.T) {
	root := t.TempDir()
	candidatesDir := filepath.Join(root, "candidates")
	activeDir := filepath.Join(root, "active")
	sourcesDir := filepath.Join(root, "sources")
	statusDir := filepath.Join(root, "status")
	writeTestActivation(t, candidatesDir, root, "healthy", "99999999900000000200", "healthy-type", "https://issuer.example/types/healthy/v2.0", "healthy", []sourceOffer{{ID: "healthy", Label: "Healthy", Parameters: map[string]any{}}})
	writeTestFile(t, filepath.Join(activeDir, "healthy.json"), []byte(`{"legacy":"missing definition"}`))
	legacyPath := filepath.Join(activeDir, "99999999900000000200.json")
	writeTestFile(t, legacyPath, []byte(`{"legacy":"OIN-named activation"}`))
	writeTestFile(t, filepath.Join(sourcesDir, "healthy.yaml"), []byte(`metadata_endpoint:
  transport: unsecured
  endpoint: http://healthy:4000/.well-known/gbo
`))
	statusBody, err := json.Marshal(sourceReconcileStatus{SourceID: "healthy", State: sourceStateRolloutRequired})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(statusDir, "healthy.json"), statusBody)

	selected, err := resolveIssuanceRolloutActivations(issuanceConfigOptions{
		activationsDir: candidatesDir, activeDir: activeDir, sourcesDir: sourcesDir, statusDir: statusDir,
	})
	if err != nil {
		t.Fatalf("resolve rollout with legacy active state: %v", err)
	}
	if got, want := len(selected), 1; got != want {
		t.Fatalf("selected activations = %d, want %d", got, want)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy OIN-named activation still exists: %v", err)
	}
	if err := promoteActivationCandidates(activeDir, statusDir, selected); err != nil {
		t.Fatalf("promote replacement activation: %v", err)
	}
	active, err := loadIssuanceActivations(activeDir)
	if err != nil {
		t.Fatalf("strictly load repaired active state: %v", err)
	}
	if got, want := active[0].Source.SourceID, "healthy"; got != want {
		t.Fatalf("active source = %q, want %q", got, want)
	}
}

func TestRolloutUsesDeployedFallbackForStaleSourceAndPrunesDeletedState(t *testing.T) {
	root := t.TempDir()
	candidatesDir := filepath.Join(root, "candidates")
	activeDir := filepath.Join(root, "active")
	sourcesDir := filepath.Join(root, "sources")
	statusDir := filepath.Join(root, "status")
	writeTestActivation(t, candidatesDir, root, "healthy", "99999999900000000200", "healthy-type", "https://issuer.example/types/healthy/v2.0", "healthy", []sourceOffer{{ID: "healthy", Label: "Healthy", Parameters: map[string]any{}}})
	writeTestActivation(t, candidatesDir, root, "degraded", "99999999900000000300", "degraded-type", "https://issuer.example/types/degraded/v2.0", "degraded-new", []sourceOffer{{ID: "degraded-new", Label: "Degraded new", Parameters: map[string]any{}}})
	writeTestActivation(t, activeDir, root, "degraded", "99999999900000000300", "degraded-type", "https://issuer.example/types/degraded/v1.0", "degraded-old", []sourceOffer{{ID: "degraded-old", Label: "Degraded old", Parameters: map[string]any{}}})
	writeTestActivation(t, candidatesDir, root, "blocked", "99999999900000000500", "blocked-type", "https://issuer.example/types/blocked/v2.0", "blocked-new", []sourceOffer{{ID: "blocked-new", Label: "Blocked new", Parameters: map[string]any{}}})
	writeTestActivation(t, activeDir, root, "blocked", "99999999900000000500", "blocked-type", "https://issuer.example/types/blocked/v1.0", "blocked-old", []sourceOffer{{ID: "blocked-old", Label: "Blocked old", Parameters: map[string]any{}}})
	writeTestActivation(t, candidatesDir, root, "deleted", "99999999900000000400", "deleted-type", "https://issuer.example/types/deleted/v1.0", "deleted", []sourceOffer{{ID: "deleted", Label: "Deleted", Parameters: map[string]any{}}})
	for _, source := range []struct {
		id string
	}{
		{id: "healthy"},
		{id: "degraded"},
		{id: "blocked"},
	} {
		writeTestFile(t, filepath.Join(sourcesDir, source.id+".yaml"), []byte("metadata_endpoint:\n  transport: unsecured\n  endpoint: http://"+source.id+":4000/.well-known/gbo\n"))
	}
	for sourceID, state := range map[string]string{"healthy": sourceStateRolloutRequired, "degraded": sourceStateStale, "blocked": sourceStateBlocked, "deleted": sourceStateActive} {
		body, err := json.Marshal(sourceReconcileStatus{SourceID: sourceID, State: state})
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(statusDir, sourceID+".json"), body)
	}

	selected, err := resolveIssuanceRolloutActivations(issuanceConfigOptions{
		activationsDir: candidatesDir, activeDir: activeDir, sourcesDir: sourcesDir, statusDir: statusDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(selected), 2; got != want {
		t.Fatalf("selected activations = %d, want %d", got, want)
	}
	bySource := make(map[string]sourceActivation, len(selected))
	for _, activation := range selected {
		bySource[activation.Source.SourceID] = activation
	}
	if got, want := bySource["degraded"].Types[0].VCT, "https://issuer.example/types/degraded/v1.0"; got != want {
		t.Fatalf("degraded VCT = %q, want deployed fallback %q", got, want)
	}
	if _, exists := bySource["blocked"]; exists {
		t.Fatal("blocked source was included in rollout")
	}
	for _, path := range []string{filepath.Join(candidatesDir, "deleted.json"), filepath.Join(statusDir, "deleted.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("deleted source state %s still exists: %v", path, err)
		}
	}
}

func TestSuccessfulIssuanceGenerationPromotesCandidate(t *testing.T) {
	root := t.TempDir()
	candidatesDir := filepath.Join(root, "candidates")
	activeDir := filepath.Join(root, "active")
	sourcesDir := filepath.Join(root, "sources")
	statusDir := filepath.Join(root, "status")
	writeTestFile(t, filepath.Join(activeDir, "bind-mount-marker"), []byte("keep"))
	if err := os.Chmod(activeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestActivation(t, candidatesDir, root, "demo", "99999999900000000900", "example", "https://issuer.example/types/demo/example/v1.0", "demo", []sourceOffer{{ID: "example", Label: "Example", Parameters: map[string]any{}}})
	writeTestFile(t, filepath.Join(sourcesDir, "demo.yaml"), []byte(`metadata_endpoint:
  transport: unsecured
  endpoint: http://demo-source:4000/.well-known/gbo
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
	if _, err := os.Stat(filepath.Join(activeDir, "bind-mount-marker")); err != nil {
		t.Fatalf("active directory was replaced instead of updated in place: %v", err)
	}
	info, err := os.Stat(activeDir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o755); got != want {
		t.Fatalf("active directory mode = %o, want %o", got, want)
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
		Source:        sourceRegistration{SourceID: sourceID, SourceOIN: oin, Name: certificatePrefix},
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
