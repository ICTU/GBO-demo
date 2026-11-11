package verifier_test

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"gbo-demo/eudi-wallet-verifier/internal/verifier"
	"gbo-demo/oid4vc-kit/lote"
	"gbo-demo/oid4vc-kit/testpki"
)

func TestLoadCentralPIDTrust(t *testing.T) {
	dir := t.TempDir()
	root := testpki.NewRoot(t, "LoTE pin")
	reader := root.Issue(t, testpki.Options{Subject: testpki.OrganizationSubject("reader", "GBO demo", "NTRNL-99990001")})
	key, err := x509.MarshalPKCS8PrivateKey(reader.Key)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"root.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Cert.Raw}), "reader.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: reader.Cert.Raw}), "key.pem": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), "api-key": []byte("test"), "registration.jwt": []byte("fixture")}
	for name, raw := range files {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f := verifier.File{PublicURL: "https://verifier.test", APIKeyFile: "api-key", PIDTrustList: &lote.File{URL: "https://trust.test/lote/PIDProviders.jwt", SignerRootFile: "root.pem"}, Usecases: map[string]verifier.UsecaseFile{"login": {KeyFile: "key.pem", ChainFile: "reader.pem", RegistrationCertificateFile: "registration.jwt", PIDType: pidType, Claims: loginClaims}}}
	path := filepath.Join(dir, "config.json")
	write := func() {
		t.Helper()
		raw, _ := json.Marshal(f)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	cfg, err := verifier.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PIDTrust == nil || len(cfg.PIDAnchors) > 0 {
		t.Fatal("dynamic trust not wired exclusively")
	}
	f.PIDAnchors = []verifier.AnchorFile{{Name: "legacy", RootsFile: "root.pem"}}
	write()
	if _, err := verifier.Load(path); err == nil {
		t.Fatal("static and central trust accepted together")
	}
	f.PIDAnchors = nil
	f.PIDTrustList.SignerRootFile = "missing.pem"
	write()
	if _, err := verifier.Load(path); err == nil {
		t.Fatal("missing independent pin accepted")
	}
}
