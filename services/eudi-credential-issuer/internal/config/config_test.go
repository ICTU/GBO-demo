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
	if err := os.WriteFile(filepath.Join(dir, "wrprc.jwt"), []byte("wrprc"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := `{
	  "issuer": {"key_file": "key.pem", "chain_file": "chain.pem"},
	  "status": {"key_file": "key.pem", "chain_file": "chain.pem"},
	  "batch_size": 4, "offer_ttl": "15m",
	  "profiles": [{"name": "eudi", "kind": "eudi", "wallet_provider_roots_file": "roots.pem",
	    "key_attestation_roots_file": "roots.pem", "registration_certificate_file": "wrprc.jwt",
	    "key_storage": ["iso_18045_high"], "user_authentication": ["iso_18045_high"],
	    "access": {"key_file": "key.pem", "chain_file": "chain.pem"}}],
	  "credential_configurations": [{"id": "income", "vct": "https://x.test/income", "valid_days": 365,
	    "source_url": "https://src.test/a", "status_list": "income",
	    "claims": [{"path": ["year"], "mandatory": true}], "display": [{"name": "Income", "locale": "en"}]}]
	}`
	path := filepath.Join(dir, "issuer.json")
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path, "https://issuer.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles) != 1 || cfg.Profiles[0].Kind != issuance.KindEUDI || len(cfg.Profiles[0].KeyAttestationProviders) != 1 || cfg.Profiles[0].KeyStorage[0] != "iso_18045_high" ||
		string(cfg.Profiles[0].RegistrationCertificate) != "wrprc" {
		t.Fatalf("profiles %+v", cfg.Profiles)
	}
	if len(cfg.Configurations) != 1 || cfg.Configurations[0].SourceURL != "https://src.test/a" || !cfg.Configurations[0].Claims[0].Mandatory {
		t.Fatalf("configurations %+v", cfg.Configurations)
	}
	if cfg.OfferTTL.String() != "15m0s" || cfg.BatchSize != 4 {
		t.Fatalf("durations %v %d", cfg.OfferTTL, cfg.BatchSize)
	}

	// A key that does not belong to its certificate is refused.
	bad := strings.Replace(file, `"issuer": {"key_file": "key.pem", "chain_file": "chain.pem"}`,
		`"issuer": {"key_file": "key.pem", "chain_file": "other-chain.pem"}`, 1)
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path, "https://issuer.test"); err == nil {
		t.Fatal("a mismatched key and certificate must be refused")
	}
}
