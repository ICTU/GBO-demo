package walletverifier

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"maps"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeCA(t *testing.T, dir, name string) (keyPath, certPath string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	keyPath, certPath = filepath.Join(dir, name+"-key.pem"), filepath.Join(dir, name+"-cert.pem")
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600)
	_ = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	return keyPath, certPath
}

// The reader certificate is issued by the reader CA for the verifier's host,
// with the reader EKU and reader authorisation metadata that allow exactly
// the requested PID claims; the configuration has one usecase, memory
// storage and the internal API behind the API key.
func TestProvision(t *testing.T) {
	dir := t.TempDir()
	readerKey, readerCert := writeCA(t, dir, "reader-ca")
	_, issuerCert := writeCA(t, dir, "issuer-ca")
	out := filepath.Join(dir, "out")
	keyFile := filepath.Join(dir, "client", "api-key")
	// An API key of the earlier layout, next to the configuration, goes.
	_ = os.MkdirAll(out, 0o700)
	_ = os.WriteFile(filepath.Join(out, "api-key"), []byte("old"), 0o600)
	if err := Provision([]string{"--ca-key", readerKey, "--ca-cert", readerCert, "--issuer-ca", issuerCert,
		"--public-url", "https://verifier.test", "--request-origin", "https://as.test", "--out-dir", out, "--api-key-file", keyFile}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(out, "verification_server.toml"))
	config := string(raw)
	// The authorization server mounts only the API key, apart from the
	// verifier's configuration with the reader's private key.
	apiKey, err := os.ReadFile(keyFile)
	if err != nil || len(apiKey) == 0 {
		t.Fatalf("API key file: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(keyFile)); len(entries) != 1 {
		t.Errorf("the API key's directory holds %d files, want only the key", len(entries))
	}
	if _, err := os.Stat(filepath.Join(out, "api-key")); err == nil {
		t.Error("the API key is also next to the verifier's configuration")
	}
	for _, want := range []string{`[usecases.asi-login]`, `url = "memory://"`, `public_url = "https://verifier.test/"`, `api_key = "` + string(apiKey) + `"`} {
		if !strings.Contains(config, want) {
			t.Errorf("configuration lacks %s", want)
		}
	}
	if strings.Contains(config, "[usecases."+QTSPUsecase+"]") {
		t.Error("the QTSP's usecase without --qtsp-request-origin")
	}
	m := regexp.MustCompile(`(?m)^certificate = "([^"]+)"`).FindStringSubmatch(config)
	if m == nil {
		t.Fatal("no usecase certificate")
	}
	der, _ := base64.StdEncoding.DecodeString(m[1])
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	caPEM, _ := os.ReadFile(readerCert)
	block, _ := pem.Decode(caPEM)
	ca, _ := x509.ParseCertificate(block.Bytes)
	if err := cert.CheckSignatureFrom(ca); err != nil || !slices.Equal(cert.DNSNames, []string{"verifier.test"}) {
		t.Fatalf("certificate: %v, DNS names %v; want issued by the reader CA for verifier.test", err, cert.DNSNames)
	}
	var eku, auth bool
	for _, ext := range cert.Extensions {
		switch {
		case ext.Id.Equal(extendedKeyUsageOID):
			var oids []asn1.ObjectIdentifier
			_, _ = asn1.Unmarshal(ext.Value, &oids)
			eku = len(oids) == 1 && oids[0].Equal(readerEKUOID)
		case ext.Id.Equal(readerAuthExtensionOID):
			var payload string
			_, _ = asn1.Unmarshal(ext.Value, &payload)
			var p struct {
				Origin     string                `json:"requestOriginBaseUrl"`
				Authorized map[string][][]string `json:"authorizedAttributes"`
				Purpose    map[string]string     `json:"purposeStatement"`
			}
			_ = json.Unmarshal([]byte(payload), &p)
			// The wallet names the authorization server, not the QTSP: the
			// purpose says the citizen approves a request of a trust service.
			auth = p.Origin == "https://as.test" && len(p.Authorized[PIDType]) == 2*len(PIDClaims) &&
				p.Purpose["nl"] == "Inloggen bij de bron om een verzoek van een vertrouwensdienst goed te keuren" &&
				slices.ContainsFunc(p.Authorized[PIDType], func(path []string) bool { return slices.Equal(path, []string{PIDType, "bsn"}) })
		}
	}
	if !eku || !auth {
		t.Errorf("reader EKU %v, reader authorisation for the PID claims %v; want both", eku, auth)
	}
}

// Slice 14: with --qtsp-request-origin the verifier also gets the QTSP's
// usecase, with its own reader certificate: the QTSP's organisation and
// purpose, and the four PID attributes it may ask, not the BSN.
func TestProvisionQTSPUsecase(t *testing.T) {
	dir := t.TempDir()
	readerKey, readerCert := writeCA(t, dir, "reader-ca")
	_, issuerCert := writeCA(t, dir, "issuer-ca")
	out := filepath.Join(dir, "out")
	if err := Provision([]string{"--ca-key", readerKey, "--ca-cert", readerCert, "--issuer-ca", issuerCert,
		"--public-url", "https://verifier.test", "--request-origin", "https://as.test", "--qtsp-request-origin", "https://qtsp.test",
		"--out-dir", out}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(out, "verification_server.toml"))
	certs := map[string]string{}
	for _, m := range regexp.MustCompile(`(?s)\[usecases\.([a-z-]+)\].*?\ncertificate = "([^"]+)"`).FindAllStringSubmatch(string(raw), -1) {
		certs[m[1]] = m[2]
	}
	if len(certs) != 2 || certs[Usecase] == "" || certs[QTSPUsecase] == "" || certs[Usecase] == certs[QTSPUsecase] {
		t.Fatalf("usecases %v, want %s and %s with their own certificate", slices.Collect(maps.Keys(certs)), Usecase, QTSPUsecase)
	}
	der, _ := base64.StdEncoding.DecodeString(certs[QTSPUsecase])
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Origin       string                `json:"requestOriginBaseUrl"`
		Authorized   map[string][][]string `json:"authorizedAttributes"`
		Purpose      map[string]string     `json:"purposeStatement"`
		Organization struct {
			DisplayName map[string]string `json:"displayName"`
		} `json:"organization"`
	}
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(readerAuthExtensionOID) {
			var payload string
			_, _ = asn1.Unmarshal(ext.Value, &payload)
			_ = json.Unmarshal([]byte(payload), &p)
		}
	}
	bsn := slices.ContainsFunc(p.Authorized[PIDType], func(path []string) bool { return slices.Contains(path, "bsn") })
	if p.Origin != "https://qtsp.test" || p.Organization.DisplayName["nl"] != "GBO demo test QTSP" ||
		p.Purpose["nl"] != "Inloggen bij de vertrouwensdienst om een verklaring aan te vragen" ||
		len(p.Authorized[PIDType]) != 2*len(QTSPClaims) || bsn {
		t.Errorf("QTSP reader authorisation = %+v; want the QTSP's origin, organisation and purpose, and the four attributes without the BSN", p)
	}
}

func TestProvisionRequiresItsInputs(t *testing.T) {
	if err := Provision([]string{"--public-url", "https://verifier.test"}); err == nil {
		t.Fatal("provisioning without CAs succeeded")
	}
}
