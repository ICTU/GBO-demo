package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gbo-demo/eudi-adapter/internal/trustlists"
)

// I1, I7, I8: consume actual publisher output over TLS, including the NL
// and GBO test-PID roots, while excluding every other role. Both runtime
// configs carry the same URL and public signing root, without PEM fallback.
func TestInternalPIDLoTEProvisioning(t *testing.T) {
	f := newLoTEFixture(t)
	if _, err := f.publish(nil, false, f.now); err != nil {
		t.Fatal(err)
	}
	files := f.files(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(files["PIDProviders.jwt"])) }))
	defer srv.Close()
	saved := http.DefaultTransport
	http.DefaultTransport = srv.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = saved })
	root := filepath.Join(f.options.publicDir, "development-ca", "lote-ca-cert.pem")
	exported := filepath.Join(t.TempDir(), "anchors.txt")
	args := []string{"export-pid-trust", "--url", srv.URL + "/lote/PIDProviders.jwt", "--signer-root", root, "--out-file", exported}
	if handled, err := runExportPIDTrust(context.Background(), args, io.Discard); !handled || err != nil {
		t.Fatalf("export: %v %v", handled, err)
	}
	certs, err := readBase64Certificates(exported)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, cert := range certs {
		got[base64.StdEncoding.EncodeToString(cert.Raw)] = true
	}
	want := map[string]bool{base64.StdEncoding.EncodeToString(f.nlPID): true, base64.StdEncoding.EncodeToString(f.snapshot.GBO[trustlists.PID]): true}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("exported roots differ from the central PID role")
	}
	before, _ := os.ReadFile(exported)
	files["PIDProviders.jwt"] = files["PubEAAProviders.jwt"]
	if _, err := runExportPIDTrust(context.Background(), args, io.Discard); err == nil {
		t.Fatal("wrong role exported")
	}
	after, _ := os.ReadFile(exported)
	if !bytes.Equal(before, after) {
		t.Fatal("failed export replaced previous snapshot")
	}

	issuer := newCredentialIssuerFixture(t)
	issuer.options.pidTrustListURL = srv.URL + "/lote/PIDProviders.jwt"
	issuer.options.loteSignerRootFile = root
	issuer.options.pidTrustAnchorsFile = "/no/legacy/file"
	out := issuer.materialized(t)
	if out.PIDTrustList == nil || out.PIDTrustList.URL != issuer.options.pidTrustListURL || len(out.PIDTrustAnchors) != 0 {
		t.Fatal("issuer is not LoTE-only")
	}
	pin, _ := os.ReadFile(filepath.Join(issuer.options.outputDir, out.PIDTrustList.SignerRootFile))
	original, _ := os.ReadFile(root)
	if !bytes.Equal(pin, original) {
		t.Fatal("issuer lost signer pin")
	}
	issuer.options.loteSignerRootFile = "/missing/root"
	if err := materializeCredentialIssuer(issuer.options, issuer.release); err == nil {
		t.Fatal("missing pin fell back to legacy trust")
	}

	_, verifier := asiVerifierFixture(t, "https://as.example")
	verifier.pidTrustListURL = srv.URL + "/lote/PIDProviders.jwt"
	verifier.loteSignerRootFile = root
	verifier.pidTrustAnchorsFile = "/no/legacy/file"
	if err := provisionASIWalletVerifier(verifier); err != nil {
		t.Fatal(err)
	}
	raw := readFile(t, filepath.Join(verifier.secretsDir, asiVerifierDir, asiVerifierConfig))
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if config["pid_anchors"] != nil || config["pid_trusted_authorities"] != nil || config["pid_trust_list"].(map[string]any)["url"] != verifier.pidTrustListURL {
		t.Fatal("verifier has stale/static trust")
	}
	if strings.Contains(string(raw), "PRIVATE KEY") {
		t.Fatal("unexpected private key in JSON")
	}
}
