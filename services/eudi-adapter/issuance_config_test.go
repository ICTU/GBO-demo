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
	writeTestActivation(t, activeDir, root, "99999999900000000200", "../escape", "https://issuer.example/type", "test", []sourceOffer{{ID: "example", Label: "Example", Parameters: map[string]any{}}})
	if _, err := loadIssuanceActivations(activeDir); err == nil {
		t.Fatal("unsafe activation type_id was accepted")
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

	writeTestActivation(t, activeDir, root, "99999999900000000200", "inkomensverklaring", "nl.gbo.bd.inkomen", "bd", []sourceOffer{
		{ID: "inkomensverklaring_2024", Label: "Inkomensverklaring 2024", Parameters: map[string]any{"jaar": float64(2024)}},
		{ID: "inkomensverklaring_2025", Label: "Inkomensverklaring 2025", Parameters: map[string]any{"jaar": float64(2025)}},
	})
	writeTestActivation(t, activeDir, root, "99999999900000000400", "akte-van-overlijden", "nl.gbo.brp.akte", "brp", []sourceOffer{
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
		`base_url = "https://adapter.example/base/attestations/99999999900000000200/inkomensverklaring?jaar=2024"`,
		`[disclosure_settings."inkomensverklaring_2025"]`,
		`[disclosure_settings."akte_van_overlijden"]`,
		`base_url = "https://adapter.example/base/attestations/99999999900000000400/akte-van-overlijden"`,
		`[attestation_settings."nl.gbo.bd.inkomen"]`,
		`private_key = "bd-issuer-key"`,
		`[attestation_settings."nl.gbo.brp.akte"]`,
		`private_key = "brp-issuer-key"`,
		`"/config/type-99999999900000000200-inkomensverklaring-v1.0.json"`,
		`"/config/type-99999999900000000400-akte-van-overlijden-v1.0.json"`,
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
}

func writeTestActivation(t *testing.T, activeDir, root, oin, typeID, vct, certificatePrefix string, offers []sourceOffer) {
	t.Helper()
	certificateDir := filepath.Join(root, "certificates", oin)
	metadataPath := filepath.Join(root, "metadata", oin+".json")
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

	activation := sourceActivation{
		SchemaVersion: "1.0",
		Source:        sourceRegistration{SourceOIN: oin, Name: certificatePrefix},
		Types: []activatedType{{
			TypeID: typeID, TypeVersion: "1.0", VCT: vct,
			VCTIntegrity: metadataIntegrity, TypeMetadataReference: metadataPath, Offers: offers,
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
	writeTestFile(t, filepath.Join(activeDir, oin+".json"), body)
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
