package login

// The issuer as client of GBO's authorization server (#614, cases C1-C7):
// registration per provider with its access certificate, the authorization
// request, the code exchange with private_key_jwt and DPoP, and the checks on
// the access token before its BSN is used.

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	testProvider = "belastingdienst"
	testResource = "https://issuer.test/belastingdienst/eudi"
	testRedirect = "https://issuer.test/belastingdienst/eudi/callback"
	testRegistry = "https://register.test/wrp/belastingdienst"
	testBSN      = "999991772"
)

// fakeAS is an authorization server: metadata, JWKS, registration and a
// token endpoint whose token the test may edit.
type fakeAS struct {
	t   *testing.T
	srv *httptest.Server
	key *ecdsa.PrivateKey
	kid string

	mu            sync.Mutex
	registrations []map[string]any
	statements    []*jwt.JSONWebToken
	tokenForms    []url.Values
	tokenDPoP     []string
	tokenStatus   int
	tokenError    string
	editClaims    func(map[string]any)
	clientID      string
	// signWith signs tokens with another key than the published one.
	signWith *ecdsa.PrivateKey
}

func newFakeAS(t *testing.T) *fakeAS {
	t.Helper()
	as := &fakeAS{t: t, key: newKey(t), kid: "as-key-1", tokenStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 as.srv.URL,
			"authorization_endpoint": as.srv.URL + "/authorize",
			"token_endpoint":         as.srv.URL + "/token",
			"jwks_uri":               as.srv.URL + "/jwks.json",
			"registration_endpoint":  as.srv.URL + "/register",
		})
	})
	mux.HandleFunc("GET /jwks.json", func(w http.ResponseWriter, r *http.Request) {
		as.mu.Lock()
		defer as.mu.Unlock()
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &as.key.PublicKey, KeyID: as.kid, Algorithm: "ES256", Use: "sig"}}})
	})
	mux.HandleFunc("POST /register", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		statement, _ := jwt.ParseSigned(body["software_statement"].(string), []jose.SignatureAlgorithm{jose.ES256})
		as.mu.Lock()
		as.registrations = append(as.registrations, body)
		as.statements = append(as.statements, statement)
		as.clientID = "client-" + string(rune('0'+len(as.registrations)))
		id := as.clientID
		as.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": id, "jwks": map[string]any{"keys": []map[string]any{{"kid": "client-key"}}}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		as.mu.Lock()
		as.tokenForms = append(as.tokenForms, r.PostForm)
		as.tokenDPoP = append(as.tokenDPoP, r.Header.Get("DPoP"))
		status, code, edit, id := as.tokenStatus, as.tokenError, as.editClaims, as.clientID
		as.mu.Unlock()
		if status != http.StatusOK {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": code})
			return
		}
		jkt := dpopThumbprint(t, r.Header.Get("DPoP"))
		claims := map[string]any{
			"iss": as.srv.URL, "aud": testResource, "exp": time.Now().Add(10 * time.Minute).Unix(), "iat": time.Now().Unix(),
			"jti": "token-1", "client_id": id, "scope": "issuance", "sub": "opaque",
			"cnf": map[string]any{"jkt": jkt}, "https://gbo.example/claims/bsn": testBSN,
		}
		if edit != nil {
			edit(claims)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": as.sign(claims, "at+jwt"), "token_type": "DPoP", "expires_in": 600, "scope": "issuance"})
	})
	as.srv = httptest.NewServer(mux)
	t.Cleanup(as.srv.Close)
	return as
}

func (as *fakeAS) sign(claims map[string]any, typ string) string {
	as.mu.Lock()
	key, kid := as.key, as.kid
	if as.signWith != nil {
		key = as.signWith
	}
	as.mu.Unlock()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, (&jose.SignerOptions{}).WithType(jose.ContentType(typ)).WithHeader("kid", kid))
	if err != nil {
		as.t.Fatal(err)
	}
	s, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		as.t.Fatal(err)
	}
	return s
}

// restart gives the server a new signing key and forgets its clients, as a
// restart of the demo's authorization server does.
func (as *fakeAS) restart() {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.key = newKey(as.t)
	as.kid = "as-key-2"
}

func dpopThumbprint(t *testing.T, proof string) string {
	tok, err := jwt.ParseSigned(proof, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return ""
	}
	jwk := tok.Headers[0].JSONWebKey
	if jwk == nil {
		return ""
	}
	tp, _ := jwk.Thumbprint(crypto.SHA256)
	return base64.RawURLEncoding.EncodeToString(tp)
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// accessCertificate is a WRPAC of the provider, self-signed for the test.
func accessCertificate(t *testing.T) (*ecdsa.PrivateKey, []*x509.Certificate) {
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		Subject:  pkix.Name{CommonName: "Belastingdienst", ExtraNames: []pkix.AttributeTypeAndValue{{Type: asn1.ObjectIdentifier{2, 5, 4, 97}, Value: "NTRNL-99990200"}}},
		KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return key, []*x509.Certificate{cert}
}

func newClient(t *testing.T, as *fakeAS) (*Client, *ecdsa.PrivateKey) {
	t.Helper()
	key, chain := accessCertificate(t)
	c, err := New(as.srv.URL, as.srv.Client(), []Provider{{Name: testProvider, Key: key, Chain: chain, RegistryURI: testRegistry, RedirectURI: testRedirect}})
	if err != nil {
		t.Fatal(err)
	}
	return c, key
}

// --- C1, C2, C3 ---------------------------------------------------------------

func TestTheFirstLoginRegistersTheProvider(t *testing.T) { // C1, C3
	as := newFakeAS(t)
	c, key := newClient(t, as)
	target, err := c.AuthorizationURL(context.Background(), testProvider, testResource, []string{"tax-income"}, "state-1", "challenge-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(as.registrations) != 1 {
		t.Fatalf("registrations = %d, want 1", len(as.registrations))
	}
	// A-1: the request names the credential configuration in
	// authorization_details (RFC 9396, OpenID4VCI 1.0 section 5.1.1).
	if u, err := url.Parse(target); err != nil || u.Query().Get("authorization_details") != `[{"type":"openid_credential","credential_configuration_id":"tax-income"}]` {
		t.Errorf("authorization request %s, want authorization_details for tax-income", target)
	}
	reg := as.registrations[0]
	if reg["scope"] != "issuance" || reg["token_endpoint_auth_method"] != "private_key_jwt" {
		t.Errorf("registration = %v, want scope issuance and private_key_jwt", reg)
	}
	if uris, _ := reg["redirect_uris"].([]any); len(uris) != 1 || uris[0] != testRedirect {
		t.Errorf("redirect_uris = %v, want the callback", reg["redirect_uris"])
	}
	statement := as.statements[0]
	var claims map[string]any
	if err := statement.Claims(&key.PublicKey, &claims); err != nil {
		t.Fatalf("software statement is not signed with the access certificate key: %v", err)
	}
	if claims["registryURI"] != testRegistry {
		t.Errorf("software statement = %v, want registryURI %s", claims, testRegistry)
	}
	rawHeader, _ := base64.RawURLEncoding.DecodeString(strings.Split(as.registrations[0]["software_statement"].(string), ".")[0])
	var header map[string]any
	_ = json.Unmarshal(rawHeader, &header)
	if x5c, _ := header["x5c"].([]any); len(x5c) != 1 {
		t.Errorf("software statement header = %v, want the certificate in x5c", header)
	}

	u, err := url.Parse(target)
	if err != nil || !strings.HasPrefix(target, as.srv.URL+"/authorize?") {
		t.Fatalf("authorization URL = %q", target)
	}
	q := u.Query()
	for name, want := range map[string]string{
		"response_type": "code", "client_id": "client-1", "redirect_uri": testRedirect, "scope": "issuance",
		"state": "state-1", "code_challenge": "challenge-1", "code_challenge_method": "S256", "resource": testResource,
	} {
		if q.Get(name) != want {
			t.Errorf("%s = %q, want %q", name, q.Get(name), want)
		}
	}

	// The registration is kept.
	if _, err := c.AuthorizationURL(context.Background(), testProvider, testResource, []string{"tax-income"}, "state-2", "challenge-2"); err != nil {
		t.Fatal(err)
	}
	if len(as.registrations) != 1 {
		t.Fatalf("registrations after a second login = %d, want still 1", len(as.registrations))
	}
}

// The demo's authorization server keeps clients in memory and generates a
// signing key at start: when its keys changed, it restarted and forgot the
// client, so the provider registers again before the browser is sent there.
func TestARestartedServerGetsANewRegistration(t *testing.T) { // C2
	as := newFakeAS(t)
	c, _ := newClient(t, as)
	if _, err := c.AuthorizationURL(context.Background(), testProvider, testResource, []string{"tax-income"}, "s", "c"); err != nil {
		t.Fatal(err)
	}
	as.restart()
	target, err := c.AuthorizationURL(context.Background(), testProvider, testResource, []string{"tax-income"}, "s", "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(as.registrations) != 2 || !strings.Contains(target, "client_id=client-2") {
		t.Fatalf("registrations = %d, URL %s; want a second registration and its client_id", len(as.registrations), target)
	}
}

// A token request refused with invalid_client drops the registration: the
// next login registers again.
func TestInvalidClientAtTheTokenEndpointDropsTheRegistration(t *testing.T) { // C2
	as := newFakeAS(t)
	c, _ := newClient(t, as)
	if _, err := c.AuthorizationURL(context.Background(), testProvider, testResource, []string{"tax-income"}, "s", "c"); err != nil {
		t.Fatal(err)
	}
	as.tokenStatus, as.tokenError = http.StatusUnauthorized, "invalid_client"
	if _, err := c.Exchange(context.Background(), testProvider, testResource, "code", "verifier"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Exchange = %v, want ErrUnavailable", err)
	}
	if _, err := c.AuthorizationURL(context.Background(), testProvider, testResource, []string{"tax-income"}, "s", "c"); err != nil {
		t.Fatal(err)
	}
	if len(as.registrations) != 2 {
		t.Fatalf("registrations = %d, want a new one after invalid_client", len(as.registrations))
	}
}

// --- C4, C6, C7 ---------------------------------------------------------------

func TestTheCodeIsExchangedWithAssertionDPoPAndVerifier(t *testing.T) { // C4, C6
	as := newFakeAS(t)
	c, key := newClient(t, as)
	if _, err := c.AuthorizationURL(context.Background(), testProvider, testResource, []string{"tax-income"}, "s", "c"); err != nil {
		t.Fatal(err)
	}
	bsn, err := c.Exchange(context.Background(), testProvider, testResource, "the-code", "the-verifier")
	if err != nil || bsn != testBSN {
		t.Fatalf("Exchange = %q, %v; want the BSN", bsn, err)
	}
	form := as.tokenForms[0]
	for name, want := range map[string]string{
		"grant_type": "authorization_code", "code": "the-code", "code_verifier": "the-verifier", "redirect_uri": testRedirect,
		"client_id": "client-1", "client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:jwt-bearer", "resource": testResource,
	} {
		if form.Get(name) != want {
			t.Errorf("%s = %q, want %q", name, form.Get(name), want)
		}
	}
	assertion, err := jwt.ParseSigned(form.Get("client_assertion"), []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatal(err)
	}
	var claims jwt.Claims
	if err := assertion.Claims(&key.PublicKey, &claims); err != nil || claims.Issuer != "client-1" || claims.Subject != "client-1" || !claims.Audience.Contains(as.srv.URL) {
		t.Fatalf("client assertion = %+v, %v; want iss and sub the client, aud the server", claims, err)
	}
	proof, err := jwt.ParseSigned(as.tokenDPoP[0], []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || proof.Headers[0].ExtraHeaders["typ"] != "dpop+jwt" {
		t.Fatalf("DPoP proof = %v", err)
	}
	var p map[string]any
	_ = proof.UnsafeClaimsWithoutVerification(&p)
	if p["htm"] != "POST" || p["htu"] != as.srv.URL+"/token" {
		t.Errorf("DPoP claims = %v, want POST to the token endpoint", p)
	}
}

// Every check on the token: a token that fails one is not used.
func TestATokenThatFailsACheckIsRefused(t *testing.T) { // C6, C7
	for name, edit := range map[string]func(map[string]any){
		"another issuer":          func(c map[string]any) { c["iss"] = "https://other.test" },
		"another audience":        func(c map[string]any) { c["aud"] = "https://issuer.test/rvig/eudi" },
		"expired":                 func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() },
		"another DPoP key":        func(c map[string]any) { c["cnf"] = map[string]any{"jkt": "someone-else"} },
		"not DPoP-bound":          func(c map[string]any) { delete(c, "cnf") },
		"another client":          func(c map[string]any) { c["client_id"] = "client-9" },
		"another scope":           func(c map[string]any) { c["scope"] = "retrieve" },
		"C7: no BSN":              func(c map[string]any) { delete(c, "https://gbo.example/claims/bsn") },
		"C7: BSN is not a string": func(c map[string]any) { c["https://gbo.example/claims/bsn"] = 999991772 },
	} {
		t.Run(name, func(t *testing.T) {
			as := newFakeAS(t)
			c, _ := newClient(t, as)
			if _, err := c.AuthorizationURL(context.Background(), testProvider, testResource, []string{"tax-income"}, "s", "c"); err != nil {
				t.Fatal(err)
			}
			as.editClaims = edit
			if bsn, err := c.Exchange(context.Background(), testProvider, testResource, "code", "verifier"); err == nil || bsn != "" || errors.Is(err, ErrUnavailable) {
				t.Fatalf("Exchange = %q, %v; want a refusal that is not ErrUnavailable", bsn, err)
			}
		})
	}
}

func TestATokenSignedWithAnotherKeyIsRefused(t *testing.T) { // C6
	as := newFakeAS(t)
	c, _ := newClient(t, as)
	if _, err := c.AuthorizationURL(context.Background(), testProvider, testResource, []string{"tax-income"}, "s", "c"); err != nil {
		t.Fatal(err)
	}
	as.signWith = newKey(t) // not the key the JWKS publishes under this kid
	if _, err := c.Exchange(context.Background(), testProvider, testResource, "code", "verifier"); err == nil {
		t.Fatal("a token whose signature does not verify was accepted")
	}
}

// --- C10 ---------------------------------------------------------------------

func TestAnUnreachableServerIsUnavailable(t *testing.T) { // C10
	as := newFakeAS(t)
	c, _ := newClient(t, as)
	if _, err := c.AuthorizationURL(context.Background(), testProvider, testResource, []string{"tax-income"}, "s", "c"); err != nil {
		t.Fatal(err)
	}
	as.tokenStatus, as.tokenError = http.StatusServiceUnavailable, "temporarily_unavailable"
	if _, err := c.Exchange(context.Background(), testProvider, testResource, "code", "verifier"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Exchange with the server failing = %v, want ErrUnavailable", err)
	}
	as.srv.Close()
	if _, err := c.AuthorizationURL(context.Background(), testProvider, testResource, []string{"tax-income"}, "s", "c"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("AuthorizationURL with the server down = %v, want ErrUnavailable", err)
	}
}

func TestAnUnknownProviderIsRefused(t *testing.T) {
	as := newFakeAS(t)
	c, _ := newClient(t, as)
	if _, err := c.AuthorizationURL(context.Background(), "rvig", testResource, nil, "s", "c"); err == nil {
		t.Fatal("an unknown provider got an authorization URL")
	}
}
