package config_test

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gbo-demo/eudi-credential-issuer/internal/config"
	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/oid4vc-kit/testpki"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	root := testpki.NewRoot(t, "root")
	issuer := root.Issue(t, testpki.Options{})
	write := func(name string, blocks ...*pem.Block) {
		var b strings.Builder
		for _, blk := range blocks {
			b.Write(pem.EncodeToMemory(blk))
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	der, err := x509.MarshalPKCS8PrivateKey(issuer.Key)
	if err != nil {
		t.Fatal(err)
	}
	write("key.pem", &pem.Block{Type: "PRIVATE KEY", Bytes: der})
	write("chain.pem", &pem.Block{Type: "CERTIFICATE", Bytes: issuer.Cert.Raw})
	write("roots.pem", &pem.Block{Type: "CERTIFICATE", Bytes: root.Cert.Raw})
	write("other-chain.pem", &pem.Block{Type: "CERTIFICATE", Bytes: root.Cert.Raw})
	for name, body := range map[string]string{"wrprc.jwt": "wrprc\n", "verifier-wrprc.jwt": "verifier-wrprc\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	file := `{
	  "batch_size": 4, "offer_ttl": "15m",
	  "providers": [{"name": "tax",
	    "access": {"key_file": "key.pem", "chain_file": "chain.pem"},
	    "issuer": {"key_file": "key.pem", "chain_file": "chain.pem"},
	    "status": {"key_file": "key.pem", "chain_file": "chain.pem"},
	    "registration_certificate_file": "wrprc.jwt",
	    "verifier_registration_certificate_file": "verifier-wrprc.jwt",
	    "request_encryption_key_file": "key.pem",
	    "credential_configurations": [{"id": "income", "scope": "income", "vct": "https://x.test/income", "valid_days": 365,
	      "source_url": "https://src.test/a", "status_list": "income",
	      "claims": [{"path": ["year"], "mandatory": true}], "display": [{"name": "Income", "locale": "en"}]}]}],
	  "profiles": [{"name": "eudi", "kind": "eudi", "wallet_provider_roots_file": "roots.pem",
	    "key_attestation_roots_file": "roots.pem",
	    "key_storage": ["iso_18045_high"], "user_authentication": ["iso_18045_high"]}],
	  "disclosure_offers": [{"key": "income-2025", "provider": "tax", "credential_configuration_ids": ["income"], "parameters": {"jaar": "2025"}}],
	  "pid_trust_anchors": [{"name": "test-pid", "roots_file": "roots.pem"}],
	  "disclosure_ttl": "3m"
	}`
	path := filepath.Join(dir, "issuer.json")
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := config.Load(path, "https://issuer.test")
	if err != nil {
		t.Fatal(err)
	}
	cfg := loaded.Issuer
	if loaded.SourceEndpoints["income"] != "https://src.test/a" {
		t.Fatalf("source endpoints %v", loaded.SourceEndpoints)
	}
	if len(cfg.Profiles) != 1 || cfg.Profiles[0].Kind != issuance.KindEUDI || len(cfg.Profiles[0].KeyAttestationProviders) != 1 || cfg.Profiles[0].KeyStorage[0] != "iso_18045_high" {
		t.Fatalf("profiles %+v", cfg.Profiles)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "tax" || string(cfg.Providers[0].RegistrationCertificate) != "wrprc" ||
		cfg.Providers[0].IssuerKey == nil || cfg.Providers[0].StatusKey == nil || cfg.Providers[0].AccessKey == nil ||
		cfg.Providers[0].RequestEncryptionKey == nil {
		t.Fatalf("providers %+v", cfg.Providers)
	}
	if len(cfg.Configurations) != 1 || cfg.Configurations[0].Provider != "tax" || cfg.Configurations[0].Scope != "income" || !cfg.Configurations[0].Claims[0].Mandatory {
		t.Fatalf("configurations %+v", cfg.Configurations)
	}
	if cfg.OfferTTL.String() != "15m0s" || cfg.BatchSize != 4 || cfg.DisclosureTTL.String() != "3m0s" {
		t.Fatalf("durations %v %v %d", cfg.OfferTTL, cfg.DisclosureTTL, cfg.BatchSize)
	}
	checkDisclosureLoaded(t, cfg)

	// A key that does not belong to its certificate is refused.
	bad := strings.Replace(file, `"issuer": {"key_file": "key.pem", "chain_file": "chain.pem"}`,
		`"issuer": {"key_file": "key.pem", "chain_file": "other-chain.pem"}`, 1)
	if bad == file {
		t.Fatal("the test did not change the issuer chain")
	}
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path, "https://issuer.test"); err == nil {
		t.Fatal("a mismatched key and certificate must be refused")
	}
}

// checkDisclosureLoaded checks the disclosure settings of TestLoad's file.
func checkDisclosureLoaded(t *testing.T, cfg issuance.Config) {
	t.Helper()
	if string(cfg.Providers[0].VerifierRegistrationCertificate) != "verifier-wrprc" {
		t.Fatalf("verifier registration certificate %q", cfg.Providers[0].VerifierRegistrationCertificate)
	}
	if len(cfg.DisclosureOffers) != 1 || cfg.DisclosureOffers[0].Key != "income-2025" || cfg.DisclosureOffers[0].Provider != "tax" ||
		cfg.DisclosureOffers[0].ConfigurationIDs[0] != "income" || cfg.DisclosureOffers[0].Parameters["jaar"] != "2025" {
		t.Fatalf("disclosure offers %+v", cfg.DisclosureOffers)
	}
	if len(cfg.PIDAnchors) != 1 || cfg.PIDAnchors[0].Name != "test-pid" {
		t.Fatalf("PID anchors %+v", cfg.PIDAnchors)
	}
}

// A combined profile trusts a second set of wallet providers whose wallets
// may skip key attestation; a root in both files is refused.
func TestLoadCombinedProfile(t *testing.T) {
	dir := t.TempDir()
	root := testpki.NewRoot(t, "root")
	nlRoot := testpki.NewRoot(t, "nl wallet provider")
	key := root.Issue(t, testpki.Options{})
	der, err := x509.MarshalPKCS8PrivateKey(key.Key)
	if err != nil {
		t.Fatal(err)
	}
	for name, blk := range map[string]*pem.Block{
		"key.pem": {Type: "PRIVATE KEY", Bytes: der}, "chain.pem": {Type: "CERTIFICATE", Bytes: key.Cert.Raw},
		"roots.pem": {Type: "CERTIFICATE", Bytes: root.Cert.Raw}, "nl-roots.pem": {Type: "CERTIFICATE", Bytes: nlRoot.Cert.Raw},
	} {
		if err := os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(blk), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	file := func(plain string) string {
		return `{"providers": [{"name": "tax",
		    "access": {"key_file": "key.pem", "chain_file": "chain.pem"}, "issuer": {"key_file": "key.pem", "chain_file": "chain.pem"},
		    "status": {"key_file": "key.pem", "chain_file": "chain.pem"},
		    "credential_configurations": [{"id": "income", "scope": "income", "vct": "https://x.test/income", "valid_days": 1,
		      "source_url": "https://src.test/a", "status_list": "income", "source_ca_file": "roots.pem"}]}],
		  "profiles": [{"name": "wallet", "kind": "combined", "wallet_provider_roots_file": "roots.pem",
		    "plain_jwt_wallet_providers_file": "` + plain + `", "key_attestation_roots_file": "roots.pem"}]}`
	}
	path := filepath.Join(dir, "issuer.json")
	if err := os.WriteFile(path, []byte(file("nl-roots.pem")), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path, "https://issuer.test")
	if err != nil {
		t.Fatal(err)
	}
	p := loaded.Issuer.Profiles[0]
	if len(p.WalletProviders) != 2 || len(p.PlainJWTWalletProviders) != 1 || p.PlainJWTWalletProviders[0] != p.WalletProviders[1].Name {
		t.Fatalf("profile %+v", p)
	}
	if loaded.SourceRoots["income"] == nil {
		t.Fatal("the source CA of the configuration is not loaded")
	}
	if err := os.WriteFile(path, []byte(file("roots.pem")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path, "https://issuer.test"); err == nil || !strings.Contains(err.Error(), "also a wallet provider that must attest") {
		t.Fatalf("a root in both files: %v", err)
	}
}
