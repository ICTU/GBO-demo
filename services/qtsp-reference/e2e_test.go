package main

// The integration test of slice 6b (#533): the reference QTSP against the
// real authorization server, ASIP and DIP stub, built and started as
// processes, with a fake Outway in front of the BRP. It runs the CLI flow
// through every outcome of the acceptance criteria and the web side from
// form to result.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gbo-demo/qtsp-reference/internal/cli"
	"gbo-demo/qtsp-reference/internal/harness"
	"gbo-demo/qtsp-reference/internal/qtsp"
	"gbo-demo/qtsp-reference/internal/web"
)

const (
	idFamilyName = "https://gbo.example/attributes/family_name/1"
	idAddress    = "https://gbo.example/attributes/address/1"
	registryURI  = "https://register.gbo.example/wrp/test-qtsp"
	bsnFrouke    = "999991772"
	bsnTom       = "555555555"
)

// stack is the running chain behind the QTSP.
type stack struct {
	dip        string
	qtspKey    *ecdsa.PrivateKey
	qtspChain  []*x509.Certificate
	sealAnchor *x509.CertPool
	logs       map[string]*syncBuffer
}

var brpCalls atomic.Int64

var (
	stackOnce sync.Once
	theStack  *stack
	stackErr  error
)

func startStack(t *testing.T) *stack {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and starts the authorization server, the ASIP and the DIP stub")
	}
	stackOnce.Do(func() { theStack, stackErr = buildStack() })
	if stackErr != nil {
		t.Fatal(stackErr)
	}
	return theStack
}

func buildStack() (*stack, error) {
	dir, err := os.MkdirTemp("", "qtsp-e2e-")
	if err != nil {
		return nil, err
	}
	bins := map[string]string{}
	for _, svc := range []string{"asi-authorization-server", "asi-provider", "asi-discovery-stub"} {
		bin := filepath.Join(dir, svc)
		build := exec.Command("go", "build", "-o", bin, ".")
		build.Dir = filepath.Join("..", svc)
		if out, err := build.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("build %s: %v\n%s", svc, err, out)
		}
		bins[svc] = bin
	}

	// The reader CA the authorization server trusts, the QTSP certificate it
	// issues, and the register entry that entitles the QTSP.
	readerCA, readerKey := newCA("test reader CA")
	qtspKey, qtspCert := issue(readerCA, readerKey, "test QTSP", "NTRNL-90000001")
	anchors := filepath.Join(dir, "reader-ca.pem")
	register := filepath.Join(dir, "register.json")
	writeCerts(anchors, readerCA)
	_ = os.WriteFile(register, []byte(`{"entries":[{"registryURI":"`+registryURI+`","identifiers":["NTRNL-90000001"],"legalName":"Test QTSP","entitlements":["https://uri.etsi.org/19475/Entitlement/QEAA_Provider"]}]}`), 0o600)

	// The seal CA and the ASIP's seal certificate.
	sealCA, sealCAKey := newCA("test seal CA")
	sealKey, sealCert := issue(sealCA, sealCAKey, "GBO demo ASIP seal", "")
	sealKeyFile, sealCertFile := filepath.Join(dir, "seal-key.pem"), filepath.Join(dir, "seal-cert.pem")
	writeKey(sealKeyFile, sealKey)
	writeCerts(sealCertFile, sealCert, sealCA)

	outway := httptest.NewServer(http.HandlerFunc(fakeBRP))
	asURL, asipURL, dipURL := "http://"+freeAddr(), "http://"+freeAddr(), "http://"+freeAddr()
	s := &stack{dip: dipURL, qtspKey: qtspKey, qtspChain: []*x509.Certificate{qtspCert, readerCA}, logs: map[string]*syncBuffer{}}
	s.sealAnchor = x509.NewCertPool()
	s.sealAnchor.AddCert(sealCA)
	for _, p := range []struct {
		name, url string
		env       []string
	}{
		{"asi-authorization-server", asURL, []string{
			"AZSP_ADDR=" + strings.TrimPrefix(asURL, "http://"), "AZSP_ISSUER=" + asURL,
			"AZSP_RESOURCES=" + asipURL + "/brp/authsrc-api=Basisregistratie Personen (mock)",
			"AZSP_TRUST_ANCHORS_FILE=" + anchors, "AZSP_REGISTER_FILE=" + register,
		}},
		{"asi-provider", asipURL, []string{
			"ASIP_ADDR=" + strings.TrimPrefix(asipURL, "http://"), "ASIP_PUBLIC_URL=" + asipURL,
			"ASIP_AUTHORIZATION_SERVER=" + asURL, "ASIP_OUTWAY_URL=" + outway.URL,
			"ASIP_SEAL_KEY_FILE=" + sealKeyFile, "ASIP_SEAL_CERT_FILE=" + sealCertFile,
		}},
		{"asi-discovery-stub", dipURL, []string{
			"DIP_ADDR=" + strings.TrimPrefix(dipURL, "http://"), "DIP_SOURCES=" + asipURL + "/brp/dataservices",
		}},
	} {
		logs := &syncBuffer{}
		cmd := exec.Command(bins[p.name])
		cmd.Dir = filepath.Join("..", p.name)
		cmd.Env = append(os.Environ(), p.env...)
		cmd.Stdout, cmd.Stderr = logs, logs
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start %s: %v", p.name, err)
		}
		s.logs[p.name] = logs
		if err := waitHealthy(p.url + "/healthz"); err != nil {
			return nil, fmt.Errorf("%s: %v\n%s", p.name, err, logs.String())
		}
	}
	return s, nil
}

// fakeBRP answers the ASIP's queries as the BRP behind FSC does: Frouke a
// resident, Tom a non-resident, anyone else unknown. brpCalls counts the
// queries, so a test can show the ASIP did not ask the source.
func fakeBRP(w http.ResponseWriter, r *http.Request) {
	brpCalls.Add(1)
	var req struct {
		Variables struct {
			BSN string `json:"bsn"`
		} `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	switch req.Variables.BSN {
	case bsnFrouke:
		_, _ = io.WriteString(w, `{"data":{"ingeschrevenPersoon":{"geslachtsnaam":"Jansen","voorvoegsel":null,"voornamen":"Frouke","geboortedatum":"2000-03-24","heeftNationaliteit":[{"nationaliteit":"NL"},{"nationaliteit":"BE"}],"woontOp":{"straatnaam":"Coolsingel","huisnummer":12,"huisletter":"B","huisnummertoevoeging":null,"postcode":"3011 AD","woonplaatsnaam":"Rotterdam"}}}}`)
	case bsnTom:
		_, _ = io.WriteString(w, `{"data":{"ingeschrevenPersoon":{"geslachtsnaam":"Willems","voorvoegsel":null,"voornamen":"Tom"}}}`)
	default:
		_, _ = io.WriteString(w, `{"data":{"ingeschrevenPersoon":null}}`)
	}
}

func (s *stack) client(t *testing.T, redirect string, anchors *x509.CertPool, registry string) *qtsp.Client {
	t.Helper()
	c, err := qtsp.New(qtsp.Config{DIP: s.dip, Key: s.qtspKey, Chain: s.qtspChain, RegistryURI: registry,
		RedirectURI: redirect, SealAnchors: anchors})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// run is one CLI flow with a fresh QTSP.
func (s *stack) run(t *testing.T, o cli.Options) (cli.Outcome, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return cli.Run(ctx, s.client(t, "https://qtsp.test/callback", s.sealAnchor, registryURI), http.DefaultClient, o)
}

func attr(id, value string) qtsp.Attribute {
	return qtsp.Attribute{Identifier: id, Value: json.RawMessage(value)}
}

func results(t *testing.T, o cli.Outcome) []string {
	t.Helper()
	var r struct {
		Results []struct {
			Result string `json:"attributeVerificationResult"`
		} `json:"attributeVerificationResults"`
	}
	if err := json.Unmarshal(o.Response.Body, &r); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, x := range r.Results {
		out = append(out, x.Result[strings.LastIndex(x.Result, "/")+1:])
	}
	return out
}

// Q-C1, Q-C2: every verification result, through the whole flow, sealed.
func TestVerifyThroughTheChain(t *testing.T) {
	s := startStack(t)
	for _, tc := range []struct {
		name, bsn string
		attr      qtsp.Attribute
		want      string
	}{
		{"Match", bsnFrouke, attr(idFamilyName, `{"family_name":"Jansen"}`), "Match"},
		{"MatchWithVariation", bsnFrouke, attr(idFamilyName, `{"family_name":"JANSEN"}`), "MatchWithVariation"},
		{"NoMatch", bsnFrouke, attr(idFamilyName, `{"family_name":"Bakker"}`), "NoMatch"},
		{"Unknown", bsnTom, attr(idAddress, `{"street":"Meir","house_number":"44","postal_code":"2000","city":"Antwerpen","country":"BE"}`), "Unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := s.run(t, cli.Options{Operation: "verify", BSN: tc.bsn, Attributes: []qtsp.Attribute{tc.attr}})
			if err != nil || !o.Response.Accepted() || o.Plan.EndpointURI == "" {
				t.Fatalf("outcome %+v, %v\n%s", o.Response, err, s.logs["asi-provider"])
			}
			if got := results(t, o); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("results = %v, want %s", got, tc.want)
			}
		})
	}
}

// Q-C3: the error answers of the acceptance criteria, provoked by the test
// harness.
func TestErrorAnswers(t *testing.T) {
	s := startStack(t)
	jansen := []qtsp.Attribute{attr(idFamilyName, `{"family_name":"Jansen"}`)}
	for _, tc := range []struct {
		name string
		o    cli.Options
		want int
	}{
		{"400 malformed", cli.Options{Malformed: true}, http.StatusBadRequest},
		{"401 tampered token", cli.Options{TamperToken: true}, http.StatusUnauthorized},
		{"404 unknown attribute", cli.Options{ExtraAttribute: "https://gbo.example/attributes/shoe_size/1"}, http.StatusNotFound},
		{"501 mandate", cli.Options{Mandate: true}, http.StatusNotImplemented},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.o.Operation, tc.o.BSN, tc.o.Attributes = "verify", bsnFrouke, jansen
			o, err := s.run(t, tc.o)
			if err != nil || o.Response.Status != tc.want || o.Response.Accepted() {
				t.Fatalf("status = %d, %v; want %d", o.Response.Status, err, tc.want)
			}
		})
	}
}

// Q-C4: retrieve gives the values, sealed.
func TestRetrieve(t *testing.T) {
	s := startStack(t)
	o, err := s.run(t, cli.Options{Operation: "retrieve", BSN: bsnFrouke,
		Attributes: []qtsp.Attribute{{Identifier: idFamilyName}, {Identifier: idAddress}}})
	if err != nil || !o.Response.Accepted() || !bytes.Contains(o.Response.Body, []byte(`"family_name":"Jansen"`)) ||
		!bytes.Contains(o.Response.Body, []byte(`"house_number":"12 B"`)) {
		t.Fatalf("outcome %d %s, %v", o.Response.Status, o.Response.Body, err)
	}
}

// Q-C5: a seal that does not chain to the QTSP's anchor is not accepted.
func TestASealFromAnotherAnchorIsNotAccepted(t *testing.T) {
	s := startStack(t)
	otherCA, _ := newCA("another CA")
	anchors := x509.NewCertPool()
	anchors.AddCert(otherCA)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	o, err := cli.Run(ctx, s.client(t, "https://qtsp.test/callback", anchors, registryURI), http.DefaultClient,
		cli.Options{Operation: "verify", BSN: bsnFrouke, Attributes: []qtsp.Attribute{attr(idFamilyName, `{"family_name":"Jansen"}`)}})
	if err != nil || o.Response.Status != http.StatusOK || o.Response.Accepted() || o.Response.SealError == nil {
		t.Fatalf("outcome %+v, %v; want a 200 that is not accepted", o.Response, err)
	}
}

// Q-C6, Q-C8, Q-C9: no data service, a refused registration, a denial.
func TestNoResult(t *testing.T) {
	s := startStack(t)
	var none *qtsp.NoDataServiceError
	if _, err := s.run(t, cli.Options{Operation: "verify", BSN: bsnFrouke,
		Attributes: []qtsp.Attribute{attr("https://gbo.example/attributes/shoe_size/1", `{}`)}}); !errors.As(err, &none) {
		t.Errorf("no data service: err = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var refused *qtsp.RegistrationError
	if _, err := cli.Run(ctx, s.client(t, "https://qtsp.test/callback", s.sealAnchor, "https://register.gbo.example/wrp/unknown"), http.DefaultClient,
		cli.Options{Operation: "verify", BSN: bsnFrouke, Attributes: []qtsp.Attribute{attr(idFamilyName, `{"family_name":"Jansen"}`)}}); !errors.As(err, &refused) {
		t.Errorf("registration refused: err = %v", err)
	}
	o, err := s.run(t, cli.Options{Operation: "verify", BSN: bsnFrouke, Deny: true,
		Attributes: []qtsp.Attribute{attr(idFamilyName, `{"family_name":"Jansen"}`)}})
	if err != nil || o.Denied == nil || o.Denied.Error != "access_denied" || o.Response.Status != 0 {
		t.Errorf("denied: %+v, %v", o, err)
	}
}

// Q-W1 to Q-W5: the web side from login and form to result (T1, T7, T9 of
// slice 14 with the mock login).
func TestWeb(t *testing.T) {
	s := startStack(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + l.Addr().String()
	srv := &http.Server{Handler: web.NewHandler(s.client(t, base+"/callback", s.sealAnchor, registryURI), web.DemoAttributes, base, nil)}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// T1: no form without a login.
	resp, body := fetch(t, browser, http.MethodGet, base+"/", nil)
	if resp.StatusCode != http.StatusOK || strings.Contains(body, `action="/start"`) || !strings.Contains(body, `action="/login/mock"`) {
		t.Fatalf("T1: GET / without a login = %d\n%s", resp.StatusCode, body)
	}
	if resp, _ := fetch(t, browser, http.MethodPost, base+"/start", url.Values{"operation": {"verify"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("T1: POST /start without a login = %d, want 303 to the login page", resp.StatusCode)
	}
	// T9: the mock login, then the form names the citizen.
	if resp, _ := fetch(t, browser, http.MethodPost, base+"/login/mock", mockLogin()); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("T9: mock login = %d", resp.StatusCode)
	}
	resp, body = fetch(t, browser, http.MethodGet, base+"/", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `action="/start"`) || !strings.Contains(body, "Ingelogd als <strong>Frouke Jansen</strong> (testgegevens, mock)") {
		t.Fatalf("W1: GET / after the login = %d\n%s", resp.StatusCode, body)
	}

	start := func(t *testing.T) string {
		form := url.Values{"operation": {"verify"}, "use:" + idFamilyName: {"1"}, "value:" + idFamilyName: {`{"family_name":"Jansen"}`}}
		resp, _ := fetch(t, browser, http.MethodPost, base+"/start", form)
		loc, _ := url.Parse(resp.Header.Get("Location"))
		q := loc.Query()
		if resp.StatusCode != http.StatusFound || q.Get("resource") == "" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" {
			t.Fatalf("W2: POST /start = %d to %s", resp.StatusCode, loc)
		}
		cookie := resp.Header.Get("Set-Cookie")
		if !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Lax") {
			t.Fatalf("W2: session cookie %q", cookie)
		}
		return loc.String()
	}
	decide := func(t *testing.T, authorization, decision string) string {
		return login(t, base+"/callback", authorization, bsnFrouke, decision == "approve")
	}

	// Served over https, the login and session cookies are Secure.
	t.Run("W2 Secure over https", func(t *testing.T) {
		h := web.NewHandler(s.client(t, "https://qtsp.test/callback", s.sealAnchor, registryURI), web.DemoAttributes, "https://qtsp.test", nil)
		post := func(path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			for _, c := range cookies {
				req.AddCookie(c)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec
		}
		loggedIn := post("/login/mock", mockLogin())
		if !strings.Contains(loggedIn.Header().Get("Set-Cookie"), "Secure") {
			t.Fatalf("mock login Set-Cookie %q; want a Secure login cookie", loggedIn.Header().Get("Set-Cookie"))
		}
		form := url.Values{"operation": {"verify"}, "use:" + idFamilyName: {"1"}, "value:" + idFamilyName: {`{"family_name":"Jansen"}`}}
		rec := post("/start", form, loggedIn.Result().Cookies()...)
		if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Set-Cookie"), "Secure") {
			t.Fatalf("POST /start = %d, Set-Cookie %q; want a Secure session cookie", rec.Code, rec.Header().Get("Set-Cookie"))
		}
	})
	t.Run("W3 result", func(t *testing.T) {
		resp, page := fetch(t, browser, http.MethodGet, decide(t, start(t), "approve"), nil)
		if resp.StatusCode != http.StatusOK || !strings.Contains(page, "Zegel geldig") || !strings.Contains(page, "Match") {
			t.Fatalf("W3: %d\n%s", resp.StatusCode, page)
		}
	})
	t.Run("W4 denied", func(t *testing.T) {
		resp, page := fetch(t, browser, http.MethodGet, decide(t, start(t), "deny"), nil)
		if resp.StatusCode != http.StatusOK || !strings.Contains(page, "Geweigerd") {
			t.Fatalf("W4: %d\n%s", resp.StatusCode, page)
		}
	})
	t.Run("W5 unknown state", func(t *testing.T) {
		callback := decide(t, start(t), "approve")
		u, _ := url.Parse(callback)
		q := u.Query()
		q.Set("state", "not-the-state")
		u.RawQuery = q.Encode()
		if resp, _ := fetch(t, browser, http.MethodGet, u.String(), nil); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("W5: wrong state = %d", resp.StatusCode)
		}
		stranger := &http.Client{CheckRedirect: browser.CheckRedirect}
		if resp, _ := fetch(t, stranger, http.MethodGet, callback, nil); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("W5: no session = %d", resp.StatusCode)
		}
	})
}

// Q-C10: no log line of the chain holds the BSN; the QTSP logs nothing of
// the token (it logs only errors, which carry none).
func TestNoBSNInTheLogs(t *testing.T) {
	s := startStack(t)
	if _, err := s.run(t, cli.Options{Operation: "verify", BSN: bsnFrouke, Attributes: []qtsp.Attribute{attr(idFamilyName, `{"family_name":"Jansen"}`)}}); err != nil {
		t.Fatal(err)
	}
	for name, logs := range s.logs {
		if strings.Contains(logs.String(), bsnFrouke) {
			t.Errorf("%s log contains the BSN", name)
		}
	}
}

// mockLogin is the mock login form with test person Frouke Jansen.
func mockLogin() url.Values {
	return url.Values{"family_name": {"Jansen"}, "given_name": {"Frouke"}, "birthdate": {"2000-03-24"}, "nationalities": {"NL, BE"}}
}

// login plays the citizen at the authorization server with the harness: it
// logs in with the DigiD mock with bsn, or denies, and returns the callback
// URL the browser follows back to the QTSP.
func login(t *testing.T, callback, authorization, bsn string, approve bool) string {
	t.Helper()
	cb, err := harness.Decide(context.Background(), http.DefaultClient, authorization, bsn, approve)
	if err != nil {
		t.Fatal(err)
	}
	q := url.Values{"state": {cb.State}}
	if cb.Code != "" {
		q.Set("code", cb.Code)
	} else {
		q.Set("error", cb.Error)
		q.Set("error_description", cb.ErrorDescription)
	}
	return callback + "?" + q.Encode()
}

func fetch(t *testing.T, c *http.Client, method, target string, form url.Values) (*http.Response, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

// --- certificates and processes ------------------------------------------------

func newCA(name string) (*x509.Certificate, *ecdsa.PrivateKey) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	cert, _ := x509.ParseCertificate(der)
	return cert, key
}

// issue issues a certificate from ca, with organizationIdentifier orgID
// unless empty.
func issue(ca *x509.Certificate, caKey *ecdsa.PrivateKey, name, orgID string) (*ecdsa.PrivateKey, *x509.Certificate) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	subject := pkix.Name{CommonName: name, Organization: []string{name}}
	if orgID != "" {
		subject.ExtraNames = []pkix.AttributeTypeAndValue{{Type: asn1.ObjectIdentifier{2, 5, 4, 97}, Value: orgID}}
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: subject, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	cert, _ := x509.ParseCertificate(der)
	return key, cert
}

func writeCerts(path string, certs ...*x509.Certificate) {
	var out []byte
	for _, c := range certs {
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})...)
	}
	_ = os.WriteFile(path, out, 0o600)
}

func writeKey(path string, key *ecdsa.PrivateKey) {
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	_ = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
}

func freeAddr() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

func waitHealthy(target string) error {
	for i := 0; i < 100; i++ {
		if resp, err := http.Get(target); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("not healthy")
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
