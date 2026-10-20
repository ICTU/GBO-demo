package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gbo-demo/eudi-adapter/internal/onboarding"
)

func TestMaterializeIssuanceReleaseUsesMountedSecretsWithoutPersistingThem(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	secrets := t.TempDir()
	writeTestDevelopmentCAs(t, secrets, now)
	registration := sourceRegistration{
		SourceID: "belastingdienst", SourceOIN: "99999999900000000200", Name: "Belastingdienst",
		MetadataEndpoint: sourceMetadataEndpoint{Transport: sourceTransportUnsecured, Endpoint: "https://metadata.example"},
		DataAccess:       sourceDataAccess{Transport: sourceTransportUnsecured},
	}
	provider := newDevelopmentCAProvider(secrets, "https://issuer.example")
	provider.now = func() time.Time { return now }
	artifacts, err := provider.Provision(registration)
	if err != nil {
		t.Fatal(err)
	}
	qtspArtifacts, err := provider.Provision(qtspRegistration("99999999900000001300", "GBO demo test QTSP"))
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.ProvisionTLSServerCertificate(qtspRegistration("99999999900000001300", "GBO demo test QTSP"), "qtsp-reference"); err != nil {
		t.Fatal(err)
	}
	callbackToken, err := ensureQTSPCallbackToken(secrets)
	if err != nil {
		t.Fatal(err)
	}
	certificateSet, err := publicCertificateSet(registration.certificateSetID(), artifacts)
	if err != nil {
		t.Fatal(err)
	}
	metadataBody := []byte(`{"display":[],"schema":{"type":"object","properties":{},"required":[]}}`)
	metadataDigest := sha256.Sum256(metadataBody)
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(metadataDigest[:])
	offer := sourceOffer{ID: "inkomensverklaring", Label: "Inkomensverklaring", Parameters: map[string]any{}}
	definition := sourceAttestationDefinition{
		TypeID: "inkomensverklaring", TypeVersion: "1.0", Offers: []sourceOffer{offer},
		GraphQL:        sourceGraphQL{Endpoint: "/graphql", Document: "query Income($bsn: String!) { income(bsn: $bsn) { value } }", SubjectVariable: "bsn", ResultPointer: "/data/income"},
		MappingProfile: "gbo-simple-v1", Display: testDisplay("Example"),
		Claims: testClaims(map[string]mappingRule{"value": {Pointer: "/value", Datatype: "string"}}),
	}
	activation := &sourceActivation{
		SchemaVersion: registrySnapshotSchemaVersion, Source: registration, MetadataURL: "https://metadata.example",
		MetadataVersion: "1.0", MetadataPayloadDigest: strings.Repeat("a", 64), CheckedAt: now,
		ExpiresAt: now.Add(2 * time.Hour), FreshUntil: now.Add(15 * time.Minute), StaleUntil: now.Add(time.Hour),
		Types: []activatedType{{
			TypeID: definition.TypeID, TypeVersion: definition.TypeVersion,
			VCT:          "https://adapter.example/types/belastingdienst/inkomensverklaring/v1.0",
			VCTIntegrity: integrity, Offers: []sourceOffer{offer}, Definition: definition,
		}},
	}
	typeMetadata := []onboarding.TypeMetadata{{
		VCT: activation.Types[0].VCT, Version: "1.0", Integrity: integrity,
		MediaType: "application/json", Bytes: metadataBody,
	}}
	candidate, err := registryCandidateFromActivation(activation, certificateSet, typeMetadata)
	if err != nil {
		t.Fatal(err)
	}
	release, err := onboarding.NewSourceRelease(now, []onboarding.SourceCandidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	releaseJSON, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(releaseJSON), "key_reference") || strings.Contains(string(releaseJSON), "PRIVATE KEY") {
		t.Fatalf("release contains private-key data: %s", releaseJSON)
	}
	var persistedRelease onboarding.SourceRelease
	if err := json.Unmarshal(releaseJSON, &persistedRelease); err != nil {
		t.Fatal(err)
	}

	t.Setenv("EUDI_PUBLIC_URL", "https://issuer.example")
	runtimeDir := t.TempDir()
	output := filepath.Join(runtimeDir, "issuance_server.toml")
	options := issuanceReleaseOptions{
		secretsDir:     secrets,
		templatePath:   filepath.Join("config", "issuance_server.toml.example"),
		adapterBaseURL: "https://adapter.example", outputPath: output,
		qtspAttestationURL: "https://qtsp-reference:4443/attestations",
		qtspMetadataPath:   filepath.Join("config", "qtsp_attestation_metadata.json"),
	}
	if err := materializeIssuanceRelease(options, persistedRelease); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("issuance TOML mode = %o, want 600", info.Mode().Perm())
	}
	rendered, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "inkomensverklaring") || !strings.Contains(string(rendered), "private_key =") {
		t.Fatal("materialized issuance TOML is missing release settings or secret material")
	}
	for _, want := range []string{
		`[disclosure_settings."qtsp-attestation"]`, `base_url = "https://qtsp-reference:4443/attestations/` + callbackToken + `"`,
		`path = ["family_name"]`, `path = ["nationalities"]`, `[attestation_settings."https://gbo.ictu.nl/credentials/qtsp-attestation/v1"]`,
		`valid_days = 30`, `copies_per_format = { "dc+sd-jwt" = 4 }`, `attestation_qualification = "EAA"`, strings.TrimSpace(readTestFile(t, qtspArtifacts.ReaderCertReference)),
	} {
		if !strings.Contains(string(rendered), want) {
			t.Errorf("materialized issuance TOML does not contain %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "type-belastingdienst-inkomensverklaring-v1.0.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, qtspMetadataFilename)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "source-release.json")); err != nil {
		t.Fatal(err)
	}

	assertMaterializesWithoutOptionalQTSP(t, secrets, options, persistedRelease)

	if err := os.Remove(artifacts.IssuerKeyReference); err != nil {
		t.Fatal(err)
	}
	missingOutput := filepath.Join(t.TempDir(), "issuance_server.toml")
	options.outputPath = missingOutput
	if err := materializeIssuanceRelease(options, persistedRelease); err == nil || !strings.Contains(err.Error(), "issuer-key.der.b64") {
		t.Fatalf("materialize without issuer key error = %v", err)
	}
}

func assertMaterializesWithoutOptionalQTSP(t *testing.T, secrets string, options issuanceReleaseOptions, release onboarding.SourceRelease) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(secrets, qtspCertificateSetID)); err != nil {
		t.Fatal(err)
	}
	var warnings bytes.Buffer
	output := filepath.Join(t.TempDir(), "issuance_server.toml")
	options.outputPath, options.warnings = output, &warnings
	if err := materializeIssuanceRelease(options, release); err != nil {
		t.Fatalf("materialize without optional QTSP certificates: %v", err)
	}
	rendered := readTestFile(t, output)
	if strings.Contains(rendered, qtspUsecase) || !strings.Contains(warnings.String(), "QTSP certificates are absent") {
		t.Fatalf("optional QTSP result contains usecase=%t warning=%q", strings.Contains(rendered, qtspUsecase), warnings.String())
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
