package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// provisionQTSPMaterial makes the reference QTSP's certificate set and
// callback path, as make provision-qtsp-issuance-certificates does.
func provisionQTSPMaterial(t *testing.T, f credentialIssuerFixture) {
	t.Helper()
	provider := newDevelopmentCAProvider(f.options.secretsDir, "https://issuer.example")
	provider.now = f.registration.now
	registration := qtspRegistration("99999999900000001300", qtspName)
	if _, err := provider.Provision(registration); err != nil {
		t.Fatal(err)
	}
	if err := provider.ProvisionTLSServerCertificate(registration, "qtsp-reference"); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureQTSPCallbackToken(f.options.secretsDir); err != nil {
		t.Fatal(err)
	}
}

func registerQTSP(t *testing.T, f credentialIssuerFixture) {
	t.Helper()
	registration := f.registration
	registration.kvkNumber = qtspKvKNumber
	if err := provisionQTSPWalletRegistration(registration); err != nil {
		t.Fatal(err)
	}
}

// P1: the QTSP is registered as a QEAA provider for its statement, as a
// private party, and asks for no PID.
func TestQTSPWalletRegistration(t *testing.T) {
	f := newCredentialIssuerFixture(t)
	registration := f.registration
	registration.kvkNumber = qtspKvKNumber
	if err := provisionQTSPWalletRegistration(registration); err == nil || !strings.Contains(err.Error(), "provision-qtsp-issuance-certificates") {
		t.Fatalf("without its certificates: %v", err)
	}
	provisionQTSPMaterial(t, f)
	registerQTSP(t, f)
	dir := filepath.Join(f.options.secretsDir, qtspCertificateSetID)
	rc := jwtPayload(t, readFile(t, filepath.Join(dir, "wrprc.jwt")))
	provides, _ := rc["provides_attestations"].([]any)
	if rc["sub"] != "NTRNL-"+qtspKvKNumber || rc["entitlements"].([]any)[0] != wrprcQEAAEntitlement || rc["public_body"] != false ||
		len(provides) != 1 || provides[0].(map[string]any)["meta"].(map[string]any)["vct_values"].([]any)[0] != qtspVCT {
		t.Fatalf("WRPRC %v", rc)
	}
	if fileExists(filepath.Join(dir, verifierRegistrationFile)) {
		t.Fatal("the QTSP asks for no PID")
	}
	if !fileExists(filepath.Join(dir, requestEncryptionKeyFile)) {
		t.Fatal("the QTSP needs a request encryption key on the combined profile")
	}
}

// P2: with its registration, the QTSP is a provider of the issuer whose
// bronpoort is its callback over its own TLS certificate; without it, the
// issuer runs without the QTSP.
func TestMaterializeQTSP(t *testing.T) {
	f := newCredentialIssuerFixture(t)
	f.options.qtspAttestationURL = "https://qtsp-reference:4443/attestations"
	f.options.qtspMetadataPath = "config/qtsp_attestation_metadata.json"
	provisionQTSPMaterial(t, f)
	for _, p := range f.materialized(t).Providers {
		if p.Name == qtspCertificateSetID {
			t.Fatal("an unregistered QTSP must be skipped")
		}
	}
	registerQTSP(t, f)
	var qtsp *issuerProviderFile
	out := f.materialized(t)
	for i := range out.Providers {
		if out.Providers[i].Name == qtspCertificateSetID {
			qtsp = &out.Providers[i]
		}
	}
	if qtsp == nil || len(qtsp.CredentialConfigurations) != 1 || qtsp.VerifierRegistrationCertificateFile != "" {
		t.Fatalf("QTSP provider %+v", qtsp)
	}
	c := qtsp.CredentialConfigurations[0]
	token := strings.TrimSpace(string(readFile(t, filepath.Join(f.options.secretsDir, qtspCertificateSetID, qtspCallbackPathFile))))
	if c.ID != qtspConfigurationID || c.VCT != qtspVCT || c.ValidDays != qtspValidDays ||
		c.SourceURL != "https://qtsp-reference:4443/attestations/"+token || c.SourceCAFile != qtspCertificateSetID+"/callback-ca.pem" {
		t.Fatalf("configuration %+v", c)
	}
	if string(readFile(t, filepath.Join(f.options.outputDir, c.SourceCAFile))) !=
		string(readFile(t, filepath.Join(f.options.secretsDir, "development-ca", "issuer-ca-cert.pem"))) {
		t.Fatal("the callback CA is not the development issuer CA")
	}
	if len(c.Claims) != 8 || !c.Claims[0].Mandatory || c.Display[0].Name == "" {
		t.Fatalf("claims %+v display %+v", c.Claims, c.Display)
	}
	// Registered, but its certificate set is partial.
	if err := os.Remove(filepath.Join(f.options.secretsDir, qtspCertificateSetID, "tls-cert.der.b64")); err != nil {
		t.Fatal(err)
	}
	if err := materializeCredentialIssuer(f.options, f.release); err == nil || !strings.Contains(err.Error(), "partial") {
		t.Fatalf("a partial certificate set: %v", err)
	}
}

// The combined profile comes with the eudi profile; the NL Wallet's
// providers join it when they are mounted.
func TestMaterializeCombinedProfile(t *testing.T) {
	f := newCredentialIssuerFixture(t)
	copyTestFile(t, filepath.Join(f.options.secretsDir, "development-ca", "reader-ca-cert.pem"), filepath.Join(f.options.rootsDir, eudiWalletProvidersFile))
	f.options.eudiKeyStorage, f.options.eudiUserAuthentication = []string{"iso_18045_high"}, []string{"iso_18045_high"}
	profiles := map[string]issuerProfileFile{}
	for _, p := range f.materialized(t).Profiles {
		profiles[p.Name] = p
	}
	combined, ok := profiles[combinedProfileName]
	if !ok || combined.Kind != "combined" || combined.WalletProviderRootsFile != eudiWalletProvidersFile ||
		combined.PlainJWTWalletProvidersFile != nlWalletProvidersFile || combined.KeyStorage[0] != "iso_18045_high" ||
		combined.UniversalLinkBase == "" {
		t.Fatalf("profiles %+v", profiles)
	}
	// A root in both files cannot tell whether its wallets must attest.
	copyTestFile(t, filepath.Join(f.options.rootsDir, nlWalletProvidersFile), filepath.Join(f.options.rootsDir, eudiWalletProvidersFile))
	if err := materializeCredentialIssuer(f.options, f.release); err == nil || !strings.Contains(err.Error(), "is in both") {
		t.Fatalf("a root in both files: %v", err)
	}
	copyTestFile(t, filepath.Join(f.options.secretsDir, "development-ca", "reader-ca-cert.pem"), filepath.Join(f.options.rootsDir, eudiWalletProvidersFile))
	if err := os.Remove(filepath.Join(f.options.rootsDir, nlWalletProvidersFile)); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.materialized(t).Profiles {
		if p.Name == combinedProfileName && (p.PlainJWTWalletProvidersFile != "" || p.UniversalLinkBase != "") {
			t.Fatal("without the NL Wallet's providers the combined profile serves the EUDI wallets only")
		}
	}
}

// A source may not take the QTSP's name.
func TestQTSPIsReserved(t *testing.T) {
	f := newCredentialIssuerFixture(t)
	source, err := walletRegistrationSourceFromRelease(f.release, "belastingdienst")
	if err != nil {
		t.Fatal(err)
	}
	source.SourceID = qtspCertificateSetID
	if err := provisionWalletRegistration(f.registration, source); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("a source named %s: %v", qtspCertificateSetID, err)
	}
}
