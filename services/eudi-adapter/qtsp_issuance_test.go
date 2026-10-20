package main

import (
	"crypto/x509"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestProvisionQTSPCertificatesAuthorizesPIDClaimsAndInternalTLS(t *testing.T) {
	root := t.TempDir()
	writeTestDevelopmentCAs(t, root, time.Now().UTC())
	handled, err := runQTSPIssuanceProvisionCommand([]string{
		"provision-qtsp-issuance-certificates",
		"--qtsp-oin=99999999900000001300",
		"--qtsp-name=GBO demo test QTSP",
		"--reader-public-url=https://issuance.example",
		"--secrets-dir=" + root,
		"--tls-host=qtsp-reference",
	}, os.Stdout, os.Stderr)
	if !handled || err != nil {
		t.Fatalf("provision command: handled=%t err=%v", handled, err)
	}
	directory := filepath.Join(root, qtspCertificateSetID)
	reader := loadCertificateArtifact(t, filepath.Join(directory, "reader-key.der.b64"), filepath.Join(directory, "reader-cert.der.b64"))
	payload, err := json.Marshal(readerAuthorizationPayload(t, reader)["authorizedAttributes"])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"family_name", "given_name", "birthdate", "nationalities"} {
		if !strings.Contains(string(payload), want) {
			t.Errorf("authorizedAttributes = %s, missing %s", payload, want)
		}
	}
	if strings.Contains(string(payload), "bsn") {
		t.Errorf("authorizedAttributes = %s, must not contain BSN", payload)
	}
	tlsCert := loadCertificateArtifact(t, filepath.Join(directory, "tls-key.der.b64"), filepath.Join(directory, "tls-cert.der.b64"))
	if len(tlsCert.DNSNames) != 1 || tlsCert.DNSNames[0] != "qtsp-reference" {
		t.Fatalf("TLS DNS names = %v", tlsCert.DNSNames)
	}
	if !certificateHasEKU(tlsCert, serverAuthEKUOID) || !containsUsage(tlsCert.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		t.Fatalf("TLS EKUs = %v/%v, want serverAuth", tlsCert.ExtKeyUsage, tlsCert.UnknownExtKeyUsage)
	}
	callbackPath := filepath.Join(directory, qtspCallbackPathFile)
	callbackBody, err := os.ReadFile(callbackPath)
	if err != nil {
		t.Fatal(err)
	}
	callbackToken, err := parseQTSPCallbackToken(string(callbackBody))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(callbackPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("callback path mode = %v, want 0600", info.Mode().Perm())
	}
	if second, err := ensureQTSPCallbackToken(root); err != nil || second != callbackToken {
		t.Fatalf("callback token changed: second=%q err=%v", second, err)
	}
}

func containsUsage(usages []x509.ExtKeyUsage, want x509.ExtKeyUsage) bool {
	for _, usage := range usages {
		if usage == want {
			return true
		}
	}
	return false
}

func TestQTSPCallbackRequiresHTTPS(t *testing.T) {
	for _, value := range []string{"http://qtsp-reference:4443/attestations", "https://user@example.test/attestations", "not a URL"} {
		if err := validateQTSPCallbackURL(value); err == nil {
			t.Errorf("callback %q accepted", value)
		}
	}
	if err := validateQTSPCallbackURL("https://qtsp-reference:4443/attestations"); err != nil {
		t.Fatal(err)
	}
	if got, err := qtspCallbackURL("https://qtsp-reference:4443/attestations", "secret"); err != nil || got != "https://qtsp-reference:4443/attestations/secret" {
		t.Fatalf("secret callback URL = %q, err %v", got, err)
	}
}

func TestQTSPMaterialDetectsAbsentAndPartialSets(t *testing.T) {
	root := t.TempDir()
	available, err := qtspMaterialAvailable(root)
	if err != nil || available {
		t.Fatalf("absent material: available=%t err=%v", available, err)
	}
	directory := filepath.Join(root, qtspCertificateSetID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "issuer-key.der.b64"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := qtspMaterialAvailable(root); err == nil {
		t.Fatal("partial material was accepted")
	}
}

func TestQTSPTypeMetadataAcceptsTheIssuedTestStatement(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("config", "qtsp_attestation_metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(body, &metadata); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("qtsp.schema.json", metadata["schema"]); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("qtsp.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	credential := map[string]any{
		"vct": qtspVCT, "vct#integrity": "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"subject_family_name": "Jansen", "subject_given_name": "Frouke", "subject_birthdate": "2000-03-24",
		"attribute_evidence": `Familienaam: Match — QTSP value {"family_name":"Jansen"}`,
		"source_legal_name":  "RvIG", "issuer_name": "GBO demo test QTSP",
		"verification_date": "2026-10-20T12:00:00Z", "qualification": "TEST — not a qualified electronic attestation of attributes",
	}
	if err := schema.Validate(credential); err != nil {
		t.Fatalf("issued statement does not fit Type Metadata: %v", err)
	}
}
