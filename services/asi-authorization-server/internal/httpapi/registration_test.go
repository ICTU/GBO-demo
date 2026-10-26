package httpapi

// Dynamic client registration of #503 (cases D1-D23): ETSI TS 119 478
// clause 6.1.3.2 and RFC 7591. Case D2 through /verify is in e2e_test.go.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/asi-authorization-server/internal/authorization"
	"gbo-demo/asi-authorization-server/internal/stubregister"
)

const (
	qtspRegistryURI     = "https://register.test/wrp/qtsp"
	providerRegistryURI = "https://register.test/wrp/service-provider"
	qtspOrganizationID  = "NTRNL-90000001"
	serviceProvider     = "https://uri.etsi.org/19475/Entitlement/Service_Provider"
)

// --- D1-D3, D18: registration --------------------------------------------------

func TestRegistration(t *testing.T) { // D1
	as := newTestAS(t)
	q := as.ca.issue(t, qtspOrganizationID, x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour))
	r := as.postRegistration(t, registrationBody(as.statement(t, q, nil)))
	if r.status != http.StatusCreated {
		t.Fatalf("registration = %d %v, want 201", r.status, r.body)
	}
	if id, _ := r.body["client_id"].(string); id == "" || r.body["client_id_issued_at"] == nil {
		t.Fatalf("response = %v, want client_id and client_id_issued_at", r.body)
	}
	if r.body["token_endpoint_auth_method"] != "private_key_jwt" || r.body["scope"] != "verify" || r.body["client_secret"] != nil {
		t.Fatalf("response = %v, want private_key_jwt, scope verify and no client_secret", r.body)
	}
	if uris, _ := r.body["redirect_uris"].([]any); len(uris) != 1 || uris[0] != testRedirectURI {
		t.Fatalf("redirect_uris = %v", r.body["redirect_uris"])
	}
	var jwks jose.JSONWebKeySet
	raw, _ := json.Marshal(r.body["jwks"])
	if err := json.Unmarshal(raw, &jwks); err != nil || len(jwks.Keys) != 1 || jwks.Keys[0].KeyID == "" {
		t.Fatalf("jwks = %s, want the certificate key with a kid", raw)
	}
	if r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", r.header.Get("Cache-Control"))
	}
}

// A registered client authenticates with the key of its certificate.
func TestRegisteredClientGetsAToken(t *testing.T) { // D2 (to the token)
	as := newTestAS(t)
	id := as.registerQTSP(t)
	p := newPKCE()
	params := authorizeParams(p)
	params.Set("client_id", id)
	// The user sees the legal name of the register entry (#544).
	if tx := as.startAuthorization(t, params); !strings.Contains(tx.page, "<h1>Toestemming voor Test QTSP</h1>") || strings.Contains(tx.page, id) {
		t.Errorf("request page does not name the registered QTSP by its legal name:\n%s", tx.page)
	}
	form := as.tokenForm(t, as.code(t, params), p)
	form.Set("client_assertion", as.assertion(t, id, nil))
	if r := as.postToken(t, form, as.proof(t, nil)); r.status != http.StatusOK {
		t.Fatalf("token response = %d %v, want 200", r.status, r.body)
	}
}

// RFC 7591 section 3.1.1: metadata in the statement take precedence.
func TestStatementMetadataTakePrecedence(t *testing.T) { // D3
	as := newTestAS(t)
	q := as.ca.issue(t, qtspOrganizationID, x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour))
	body := registrationBody(as.statement(t, q, func(c map[string]any) { c["scope"] = "verify" }))
	body["scope"] = "verify retrieve"
	if r := as.postRegistration(t, body); r.status != http.StatusCreated || r.body["scope"] != "verify" {
		t.Fatalf("registration = %d %v, want 201 with scope verify", r.status, r.body)
	}
}

func TestTwoRegistrationsAreTwoClients(t *testing.T) { // D18
	as := newTestAS(t)
	if a, b := as.registerQTSP(t), as.registerQTSP(t); a == b {
		t.Fatalf("both registrations got client_id %s", a)
	}
}

// --- D4-D13: the software statement ---------------------------------------------

func TestInvalidSoftwareStatement(t *testing.T) { // D4-D10
	as := newTestAS(t)
	valid := as.ca.issue(t, qtspOrganizationID, x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour))
	expired := as.ca.issue(t, qtspOrganizationID, x509.KeyUsageDigitalSignature, time.Now().Add(-time.Minute))
	noOrgID := as.ca.issue(t, "", x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour))
	noSignature := as.ca.issue(t, qtspOrganizationID, x509.KeyUsageKeyEncipherment, time.Now().Add(time.Hour))
	selfSigned := newTestCA(t).issue(t, qtspOrganizationID, x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour))
	t.Run("D4: no software statement", func(t *testing.T) {
		r := as.postRegistration(t, registrationBody(""))
		assertRegistrationError(t, r, http.StatusBadRequest, "invalid_software_statement")
		if d, _ := r.body["error_description"].(string); !strings.Contains(d, "required") {
			t.Fatalf("error_description = %q, want it to say the statement is required", d)
		}
	})
	for name, statement := range map[string]func(t *testing.T) string{
		"D5: alg none": func(t *testing.T) string {
			return unsignedStatement(t, valid, as.statementClaims(nil))
		},
		"D6: signed with a key other than the certificate's": func(t *testing.T) string {
			return signStatement(t, newECKey(t), valid.chain(), as.statementClaims(nil))
		},
		"D6b: no x5c": func(t *testing.T) string {
			return signStatement(t, valid.key, nil, as.statementClaims(nil))
		},
		"D7: chain does not reach the trust anchor":  func(t *testing.T) string { return as.statement(t, selfSigned, nil) },
		"D8: certificate expired":                    func(t *testing.T) string { return as.statement(t, expired, nil) },
		"certificate without organizationIdentifier": func(t *testing.T) string { return as.statement(t, noOrgID, nil) },
		"certificate without digitalSignature":       func(t *testing.T) string { return as.statement(t, noSignature, nil) },
		"D9: statement expired": func(t *testing.T) string {
			return as.statement(t, valid, func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() })
		},
		"D9b: no exp": func(t *testing.T) string { return as.statement(t, valid, func(c map[string]any) { delete(c, "exp") }) },
		"no iat":      func(t *testing.T) string { return as.statement(t, valid, func(c map[string]any) { delete(c, "iat") }) },
		"iat in the future": func(t *testing.T) string {
			return as.statement(t, valid, func(c map[string]any) { c["iat"] = time.Now().Add(time.Hour).Unix() })
		},
		"no iss": func(t *testing.T) string { return as.statement(t, valid, func(c map[string]any) { delete(c, "iss") }) },
		"D10: no registryURI": func(t *testing.T) string {
			return as.statement(t, valid, func(c map[string]any) { delete(c, "registryURI") })
		},
	} {
		t.Run(name, func(t *testing.T) {
			assertRegistrationError(t, as.postRegistration(t, registrationBody(statement(t))), http.StatusBadRequest, "invalid_software_statement")
		})
	}
}

func TestUnapprovedSoftwareStatement(t *testing.T) { // D11-D13
	as := newTestAS(t)
	valid := as.ca.issue(t, qtspOrganizationID, x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour))
	other := as.ca.issue(t, "NTRNL-99999999", x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour))
	for name, statement := range map[string]func(t *testing.T) string{
		"D11: registryURI unknown": func(t *testing.T) string {
			return as.statement(t, valid, func(c map[string]any) { c["registryURI"] = "https://register.test/wrp/unknown" })
		},
		"D12: no QEAA_Provider entitlement": func(t *testing.T) string {
			return as.statement(t, valid, func(c map[string]any) { c["registryURI"] = providerRegistryURI })
		},
		"D13: organizationIdentifier is not of the entry": func(t *testing.T) string { return as.statement(t, other, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			assertRegistrationError(t, as.postRegistration(t, registrationBody(statement(t))), http.StatusBadRequest, "unapproved_software_statement")
		})
	}
}

// --- D14-D17, D21: client metadata -----------------------------------------------

func TestInvalidClientMetadata(t *testing.T) { // D14-D17, D21
	as := newTestAS(t)
	q := as.ca.issue(t, qtspOrganizationID, x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour))
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		code string
	}{
		{"D14: scope outside verify and retrieve", func(b map[string]any) { b["scope"] = "verify admin" }, "invalid_client_metadata"},
		{"D14b: no scope", func(b map[string]any) { delete(b, "scope") }, "invalid_client_metadata"},
		{"D15: another authentication method", func(b map[string]any) { b["token_endpoint_auth_method"] = "client_secret_basic" }, "invalid_client_metadata"},
		{"D16: jwks", func(b map[string]any) { b["jwks"] = map[string]any{"keys": []any{}} }, "invalid_client_metadata"},
		{"D16b: jwks_uri", func(b map[string]any) { b["jwks_uri"] = "https://qtsp.test/jwks" }, "invalid_client_metadata"},
		{"D17: no redirect_uris", func(b map[string]any) { delete(b, "redirect_uris") }, "invalid_redirect_uri"},
		{"D17b: http redirect_uri", func(b map[string]any) { b["redirect_uris"] = []string{"http://qtsp.test/callback"} }, "invalid_redirect_uri"},
		{"D17d: http on a LAN address", func(b map[string]any) { b["redirect_uris"] = []string{"http://192.168.1.20:9427/callback"} }, "invalid_redirect_uri"},
		{"D17e: http on a name that starts like localhost", func(b map[string]any) { b["redirect_uris"] = []string{"http://localhost.qtsp.test/callback"} }, "invalid_redirect_uri"},
		{"D17f: another scheme", func(b map[string]any) { b["redirect_uris"] = []string{"ftp://localhost/callback"} }, "invalid_redirect_uri"},
		{"D17c: redirect_uri with a fragment", func(b map[string]any) { b["redirect_uris"] = []string{"https://qtsp.test/callback#x"} }, "invalid_redirect_uri"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := registrationBody(as.statement(t, q, nil))
			tc.edit(body)
			assertRegistrationError(t, as.postRegistration(t, body), http.StatusBadRequest, tc.code)
		})
	}
	// http is allowed on a loopback address only: browser and client on the
	// same machine (RFC 8252 section 7.3).
	for _, u := range []string{"http://localhost:9427/callback", "http://127.0.0.1:4023/callback", "http://[::1]:4023/callback"} {
		t.Run("D17g: loopback "+u, func(t *testing.T) {
			body := registrationBody(as.statement(t, q, nil))
			body["redirect_uris"] = []string{u}
			if r := as.postRegistration(t, body); r.status != http.StatusCreated {
				t.Fatalf("status = %d %v, want 201", r.status, r.body)
			}
		})
	}
	t.Run("D21: body is not JSON", func(t *testing.T) {
		resp, err := http.Post(as.srv.URL+authorization.RegisterPath, "application/json", bytes.NewBufferString("not json"))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		assertRegistrationError(t, tokenResponse{status: resp.StatusCode, body: body}, http.StatusBadRequest, "invalid_client_metadata")
	})
}

// --- D22-D23: periodic re-verification ---------------------------------------------

func TestRecheckDeactivatesAClientWithoutEntitlement(t *testing.T) { // D22
	as := newTestAS(t)
	id := as.registerQTSP(t)
	p := newPKCE()
	params := authorizeParams(p)
	params.Set("client_id", id)
	code := as.code(t, params)

	as.register.set(qtspRegistryURI, authorization.RegisterEntry{Identifiers: []string{qtspOrganizationID}, Entitlements: []string{serviceProvider}})
	if res := as.service.RecheckEntitlements(context.Background()); res.Deactivated != 1 {
		t.Fatalf("recheck = %+v, want one client deactivated", res)
	}
	form := as.tokenForm(t, code, p)
	form.Set("client_assertion", as.assertion(t, id, nil))
	assertTokenError(t, as.postToken(t, form, as.proof(t, nil)), http.StatusUnauthorized, "invalid_client")
	if resp := as.authorize(t, params); resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
		t.Fatalf("authorization request of a deactivated client = %d, Location %q; want 400 without redirect", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// The user sees the legal name the register holds now: a re-verification
// takes a changed name, while authorizations run alongside it.
func TestRecheckTakesAChangedLegalName(t *testing.T) {
	as := newTestAS(t)
	id := as.registerQTSP(t)
	params := authorizeParams(newPKCE())
	params.Set("client_id", id)
	as.register.set(qtspRegistryURI, authorization.RegisterEntry{Identifiers: []string{qtspOrganizationID}, LegalName: "Test QTSP B.V.", Entitlements: []string{authorization.QEAAProviderEntitlement}})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 20 {
			as.service.RecheckEntitlements(context.Background())
		}
	}()
	valid := authorization.AuthorizationRequest{ResponseType: "code", ClientID: id, RedirectURI: testRedirectURI, Scope: "verify",
		CodeChallenge: newPKCE().challenge, CodeChallengeMethod: "S256", Resources: []string{testResource},
		AuthorizationDetails: authorizeParams(newPKCE()).Get(authorization.AuthorizationDetailsClaim)}
	for range 20 {
		if resp, err := as.service.Authorize(valid); err != nil || resp.Pending == nil || resp.Pending.ClientName == "" {
			t.Fatalf("Authorize = %+v, %v; want a transaction naming the client", resp, err)
		}
	}
	<-done
	if tx := as.startAuthorization(t, params); !strings.Contains(tx.page, "<h1>Toestemming voor Test QTSP B.V.</h1>") {
		t.Fatalf("request page after the re-verification does not show the new legal name:\n%s", tx.page)
	}
}

// D22 through the stub register the demo runs: withdrawing the entitlement
// from the register file deactivates the client at the next re-verification,
// without a restart.
func TestRecheckSeesAWithdrawalInTheRegisterFile(t *testing.T) { // D22, real adapter
	path := filepath.Join(t.TempDir(), "register.json")
	writeRegister := func(entitlement string) {
		t.Helper()
		doc := `{"entries":[{"registryURI":"` + qtspRegistryURI + `","identifiers":["` + qtspOrganizationID + `"],"entitlements":["` + entitlement + `"]}]}`
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeRegister(authorization.QEAAProviderEntitlement)
	register, err := stubregister.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	as := newTestASWithRegister(t, testResource, register)
	id := as.registerQTSP(t)
	if res := as.service.RecheckEntitlements(context.Background()); res.Deactivated != 0 {
		t.Fatalf("recheck before the withdrawal = %+v, want nothing deactivated", res)
	}
	writeRegister(serviceProvider)
	if res := as.service.RecheckEntitlements(context.Background()); res.Deactivated != 1 {
		t.Fatalf("recheck after the withdrawal = %+v, want the client deactivated", res)
	}
	params := authorizeParams(newPKCE())
	params.Set("client_id", id)
	if resp := as.authorize(t, params); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("authorization request of the deactivated client = %d, want 400", resp.StatusCode)
	}
}

func TestRecheckKeepsTheClientWhenTheRegisterIsUnavailable(t *testing.T) { // D23
	as := newTestAS(t)
	id := as.registerQTSP(t)
	as.register.setUnavailable(true)
	if res := as.service.RecheckEntitlements(context.Background()); res.Unavailable != 1 || res.Deactivated != 0 {
		t.Fatalf("recheck = %+v, want the register unavailable and no client deactivated", res)
	}
	p := newPKCE()
	params := authorizeParams(p)
	params.Set("client_id", id)
	form := as.tokenForm(t, as.code(t, params), p)
	form.Set("client_assertion", as.assertion(t, id, nil))
	if r := as.postToken(t, form, as.proof(t, nil)); r.status != http.StatusOK {
		t.Fatalf("token response = %d %v, want 200", r.status, r.body)
	}
}

// When the register cannot be consulted, a registration cannot be approved.
func TestRegistrationWhileTheRegisterIsUnavailable(t *testing.T) {
	as := newTestAS(t)
	as.register.setUnavailable(true)
	q := as.ca.issue(t, qtspOrganizationID, x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour))
	assertRegistrationError(t, as.postRegistration(t, registrationBody(as.statement(t, q, nil))), http.StatusServiceUnavailable, "temporarily_unavailable")
}

// --- helpers -------------------------------------------------------------------

// fakeRegister is a register whose entries and availability a test controls.
type fakeRegister struct {
	mu          sync.Mutex
	entries     map[string]authorization.RegisterEntry
	unavailable bool
}

func newFakeRegister() *fakeRegister {
	return &fakeRegister{entries: map[string]authorization.RegisterEntry{
		qtspRegistryURI:     {Identifiers: []string{qtspOrganizationID}, LegalName: "Test QTSP", Entitlements: []string{authorization.QEAAProviderEntitlement}},
		providerRegistryURI: {Identifiers: []string{qtspOrganizationID}, LegalName: "Test service provider", Entitlements: []string{serviceProvider}},
	}}
}

func (f *fakeRegister) Lookup(_ context.Context, uri string) (authorization.RegisterEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable {
		return authorization.RegisterEntry{}, authorization.ErrRegisterUnavailable
	}
	e, ok := f.entries[uri]
	if !ok {
		return authorization.RegisterEntry{}, authorization.ErrNotRegistered
	}
	return e, nil
}

func (f *fakeRegister) set(uri string, e authorization.RegisterEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[uri] = e
}

func (f *fakeRegister) setUnavailable(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unavailable = v
}

// testCA stands in for the development reader CA.
type testCA struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key := newECKey(t)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test reader CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &testCA{key: key, cert: cert}
}

func (ca *testCA) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

// qtspCert is a QTSP certificate with its key and chain.
type qtspCert struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
	ca   *x509.Certificate
}

func (q qtspCert) chain() []*x509.Certificate { return []*x509.Certificate{q.cert, q.ca} }

// issue issues a QTSP certificate with organizationIdentifier orgID (none
// when empty), the key usage given, valid until notAfter.
func (ca *testCA) issue(t *testing.T, orgID string, usage x509.KeyUsage, notAfter time.Time) qtspCert {
	t.Helper()
	key := newECKey(t)
	subject := pkix.Name{CommonName: "test QTSP", Organization: []string{"Test QTSP"}}
	if orgID != "" {
		subject.ExtraNames = []pkix.AttributeTypeAndValue{{Type: asn1.ObjectIdentifier{2, 5, 4, 97}, Value: orgID}}
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: subject, NotBefore: time.Now().Add(-2 * time.Hour), NotAfter: notAfter, KeyUsage: usage}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return qtspCert{key: key, cert: cert, ca: ca.cert}
}

// statementClaims are the claims of a software statement for the test QTSP;
// edit may change them.
func (as *testAS) statementClaims(edit func(map[string]any)) map[string]any {
	now := time.Now()
	c := map[string]any{"iss": "test-qtsp-software", "registryURI": qtspRegistryURI, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
	if edit != nil {
		edit(c)
	}
	return c
}

// statement is a software statement signed with the key of q, x5c the chain.
func (as *testAS) statement(t *testing.T, q qtspCert, edit func(map[string]any)) string {
	t.Helper()
	return signStatement(t, q.key, q.chain(), as.statementClaims(edit))
}

func signStatement(t *testing.T, key *ecdsa.PrivateKey, chain []*x509.Certificate, claims map[string]any) string {
	t.Helper()
	opts := &jose.SignerOptions{}
	if len(chain) > 0 {
		opts = opts.WithHeader("x5c", x5c(chain))
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, opts)
	if err != nil {
		t.Fatal(err)
	}
	s, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func unsignedStatement(t *testing.T, q qtspCert, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(map[string]any{"alg": "none", "x5c": x5c(q.chain())})
	c, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c) + "."
}

func x5c(chain []*x509.Certificate) []string {
	out := make([]string, len(chain))
	for i, c := range chain {
		out[i] = base64.StdEncoding.EncodeToString(c.Raw)
	}
	return out
}

func registrationBody(statement string) map[string]any {
	body := map[string]any{"redirect_uris": []string{testRedirectURI}, "scope": "verify"}
	if statement != "" {
		body["software_statement"] = statement
	}
	return body
}

func (as *testAS) postRegistration(t *testing.T, body map[string]any) tokenResponse {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, as.srv.URL+authorization.RegisterPath, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	return as.send(t, req)
}

// registerQTSP registers the test QTSP and returns its client_id; the
// client key and kid are remembered for client assertions.
func (as *testAS) registerQTSP(t *testing.T) string {
	t.Helper()
	q := as.ca.issue(t, qtspOrganizationID, x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour))
	r := as.postRegistration(t, registrationBody(as.statement(t, q, nil)))
	if r.status != http.StatusCreated {
		t.Fatalf("registration = %d %v, want 201", r.status, r.body)
	}
	id, _ := r.body["client_id"].(string)
	var jwks jose.JSONWebKeySet
	raw, _ := json.Marshal(r.body["jwks"])
	_ = json.Unmarshal(raw, &jwks)
	as.keys[id] = q.key
	as.kids[id] = jwks.Keys[0].KeyID
	return id
}

func assertRegistrationError(t *testing.T, r tokenResponse, status int, code string) {
	t.Helper()
	if r.status != status || r.body["error"] != code {
		t.Fatalf("registration response = %d %v, want %d with error %q", r.status, r.body, status, code)
	}
	if r.body["client_id"] != nil {
		t.Fatalf("an error response carries a client_id")
	}
}
