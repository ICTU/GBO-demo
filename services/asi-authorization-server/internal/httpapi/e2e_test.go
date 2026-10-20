package httpapi

// Case 24 through /verify and case 37 of #503: the whole flow across both
// services. The test builds services/asi-provider and starts it as a process;
// this test plays the QTSP, and a fake Outway the BRP behind it.

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

func TestEndToEndVerify(t *testing.T) { // case 24 (through /verify), case 37, case D2; S12, S13 (through the chooser)
	if testing.Short() {
		t.Skip("builds and starts services/asi-provider")
	}
	asipDir := filepath.Join("..", "..", "..", "asi-provider")
	dir := t.TempDir()
	bin := filepath.Join(dir, "asi-provider")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = asipDir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build asi-provider: %v\n%s", err, out)
	}
	keyFile, certFile := writeSealMaterial(t, dir)

	asipURL := "http://" + freeAddr(t)
	var asLogs syncBuffer
	log.SetOutput(&asLogs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	// The BRP data service of the ASIP.
	brp := asipURL + "/brp/authsrc-api"
	as := newTestASFor(t, brp)

	outway := newFakeBRPOutway(t)

	var asipLogs syncBuffer
	asip := exec.Command(bin)
	asip.Dir = asipDir
	asip.Env = append(os.Environ(),
		"ASIP_ADDR="+strings.TrimPrefix(asipURL, "http://"),
		"ASIP_PUBLIC_URL="+asipURL,
		"ASIP_AUTHORIZATION_SERVER="+as.srv.URL,
		"ASIP_SEAL_KEY_FILE="+keyFile,
		"ASIP_SEAL_CERT_FILE="+certFile,
		"ASIP_OUTWAY_URL="+outway.URL,
	)
	asip.Stdout, asip.Stderr = &asipLogs, &asipLogs
	if err := asip.Start(); err != nil {
		t.Fatalf("start asi-provider: %v", err)
	}
	t.Cleanup(func() {
		_ = asip.Process.Kill()
		_ = asip.Wait()
	})
	waitHealthy(t, asipURL+"/healthz", &asipLogs)

	p := newPKCE()
	params := authorizeParams(p)
	params.Set("resource", brp)
	setAuthorizationDetails(params, "https://gbo.example/attributes/family_name/1")
	token := as.accessToken(t, params, p)

	body := `{"attributes":[{"attributeIdentifier":"https://gbo.example/attributes/family_name/1","attributeValue":{"family_name":"Jansen"}}]}`
	resp := as.callASIP(t, brp+"/verify", token, body)
	if resp.status != http.StatusOK {
		t.Fatalf("/verify = %d %s, want 200\nasi-provider log:\n%s", resp.status, resp.body, asipLogs.String())
	}
	var result struct {
		AttributeVerificationResults []struct {
			AttributeVerificationResult string `json:"attributeVerificationResult"`
		} `json:"attributeVerificationResults"`
	}
	if err := json.Unmarshal(resp.body, &result); err != nil || len(result.AttributeVerificationResults) != 1 ||
		result.AttributeVerificationResults[0].AttributeVerificationResult != "http://uri.etsi.org/19478/VerificationResult/Match" {
		t.Fatalf("/verify body = %s, want one Match", resp.body)
	}

	// An ETSI client that sends no RFC 9396 extension gets an operation-only
	// token, which can use every attribute in this data service's catalogue.
	p = newPKCE()
	params = authorizeParams(p)
	params.Set("resource", brp)
	params.Del(authorization.AuthorizationDetailsClaim)
	operationOnlyToken := as.accessToken(t, params, p)
	if r := as.callASIP(t, brp+"/verify", operationOnlyToken, body); r.status != http.StatusOK {
		t.Fatalf("/verify with an operation-only token = %d %s, want 200", r.status, r.body)
	}
	if got := outway.asked(); len(got) != 2 || got[0] != bsnFrouke || got[1] != bsnFrouke {
		t.Fatalf("the BRP was asked for %v, want the BSN of the chosen person twice", got)
	}
	if resp.header.Get("X-JWS-Signature") == "" {
		t.Fatalf("/verify response is not sealed")
	}
	// D2: a client registered with a sealed software statement runs the same
	// flow with the key of its certificate.
	id := as.registerQTSP(t)
	p = newPKCE()
	params = authorizeParams(p)
	params.Set("resource", brp)
	setAuthorizationDetails(params, "https://gbo.example/attributes/family_name/1")
	params.Set("client_id", id)
	form := as.tokenForm(t, as.code(t, params), p)
	form.Set("client_assertion", as.assertion(t, id, nil))
	tr := as.postToken(t, form, as.proof(t, nil))
	if tr.status != http.StatusOK {
		t.Fatalf("token response for the registered client = %d %v, want 200", tr.status, tr.body)
	}
	registeredToken, _ := tr.body["access_token"].(string)
	if r := as.callASIP(t, brp+"/verify", registeredToken, body); r.status != http.StatusOK {
		t.Fatalf("/verify with the token of the registered client = %d %s, want 200", r.status, r.body)
	}

	// The token is for verify only.
	if r := as.callASIP(t, brp+"/retrieve", token, `{"attributeIdentifiers":[{"attributeIdentifier":"https://gbo.example/attributes/family_name/1"}]}`); r.status != http.StatusUnauthorized {
		t.Fatalf("/retrieve with a verify token = %d, want 401", r.status)
	}

	// Case 37, S13: no log line of either service contains the BSN or a PID
	// value of the test person.
	_ = asip.Process.Kill()
	_ = asip.Wait()
	for name, logs := range map[string]string{"asi-authorization-server": asLogs.String(), "asi-provider": asipLogs.String()} {
		for _, value := range []string{bsnFrouke, "Jansen", "Frouke", "2000-03-24", "Leeuwarden"} {
			if strings.Contains(logs, value) {
				t.Errorf("%s log contains %q:\n%s", name, value, logs)
			}
		}
	}
}

// fakeBRPOutway answers the ASIP's queries on the grant-link /brp/graphql:
// the surname of the chosen test person, no one else.
type fakeBRPOutway struct {
	*httptest.Server
	mu   sync.Mutex
	bsns []string
}

func newFakeBRPOutway(t *testing.T) *fakeBRPOutway {
	t.Helper()
	o := &fakeBRPOutway{}
	o.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables struct {
				BSN string `json:"bsn"`
			} `json:"variables"`
		}
		if r.URL.Path != "/brp/graphql" || json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "not the grant-link", http.StatusNotFound)
			return
		}
		o.mu.Lock()
		o.bsns = append(o.bsns, req.Variables.BSN)
		o.mu.Unlock()
		if req.Variables.BSN != bsnFrouke {
			_, _ = w.Write([]byte(`{"data":{"ingeschrevenPersoon":null}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ingeschrevenPersoon":{"geslachtsnaam":"Jansen","voorvoegsel":null}}}`))
	}))
	t.Cleanup(o.Close)
	return o
}

func (o *fakeBRPOutway) asked() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.bsns...)
}

type asipResponse struct {
	status int
	header http.Header
	body   []byte
}

// callASIP sends a request to the ASIP with the access token and a DPoP proof
// for that request (RFC 9449 section 7).
func (as *testAS) callASIP(t *testing.T, endpoint, token, body string) asipResponse {
	t.Helper()
	digest := sha256.Sum256([]byte(token))
	proof := signJWT(t, as.dpopKey, "", "dpop+jwt", map[string]any{
		"jti": authorization.RandomID(), "htm": http.MethodPost, "htu": endpoint, "iat": time.Now().Unix(),
		"ath": base64.RawURLEncoding.EncodeToString(digest[:]),
	}, true)
	req, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "DPoP "+token)
	req.Header.Set("DPoP", proof)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	return asipResponse{status: resp.StatusCode, header: resp.Header, body: b.Bytes()}
}

func waitHealthy(t *testing.T, url string, logs *syncBuffer) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(url); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("asi-provider did not become healthy\n%s", logs.String())
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

// writeSealMaterial writes a seal key and a certificate chain from a throwaway
// CA, which asi-provider needs to start.
func writeSealMaterial(t *testing.T, dir string) (string, string) {
	t.Helper()
	caKey := newECKey(t)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "e2e test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	sealKey := newECKey(t)
	sealTmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "e2e test seal"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	sealDER, err := x509.CreateCertificate(rand.Reader, sealTmpl, ca, &sealKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyFile, certFile := filepath.Join(dir, "seal-key.pem"), filepath.Join(dir, "seal-cert.pem")
	writePEM(t, keyFile, "PRIVATE KEY", pkcs8(t, sealKey))
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: sealDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...)
	if err := os.WriteFile(certFile, chain, 0o600); err != nil {
		t.Fatal(err)
	}
	return keyFile, certFile
}

func pkcs8(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent writers.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
