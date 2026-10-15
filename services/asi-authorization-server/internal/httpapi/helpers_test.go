package httpapi

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/asi-authorization-server/internal/authorization"
	"gbo-demo/asi-authorization-server/internal/clientconfig"
	"gbo-demo/asi-authorization-server/internal/digidmock"
	"gbo-demo/asi-authorization-server/internal/walletverifier"
)

const (
	testClientID    = "test-qtsp"
	otherClientID   = "other-qtsp"
	verifyOnlyID    = "verify-only-qtsp"
	testRedirectURI = "https://qtsp.test/callback"
	testResource    = "https://asip.test/brp/authsrc-api"
	// testResource2 is a second data service of the ASIP.
	testResource2 = "https://asip.test/duo/authsrc-api"
	bsnFrouke     = "999991772"
	bsnTom        = "555555555"
	// testClientName is the legal name of testClientID in its configuration.
	testClientName = "Test QTSP (configuratie)"
)

// testAS runs the authorization server with three clients from
// configuration, a register and a trust anchor for dynamic client
// registration; the user logs in with the DigiD mock. offset shifts its
// clock.
type testAS struct {
	srv      *httptest.Server
	service  *authorization.Service
	keys     map[string]*ecdsa.PrivateKey // client_id -> client key
	kids     map[string]string            // client_id -> kid, for registered clients
	dpopKey  *ecdsa.PrivateKey
	jkt      string
	offset   time.Duration
	register *fakeRegister
	ca       *testCA
	// wallet stands in for the NL Wallet verifier.
	wallet *fakeWallet
}

func newTestAS(t *testing.T) *testAS {
	return newTestASFor(t, testResource)
}

func newTestASFor(t *testing.T, resource string) *testAS {
	t.Helper()
	return newTestASWithRegister(t, resource, nil)
}

// newTestASWithRegister uses register instead of the fake register when it
// is not nil.
func newTestASWithRegister(t *testing.T, resource string, register authorization.Register) *testAS {
	t.Helper()
	return newTestASWith(t, resource, register, true)
}

// newTestASWithoutWallet runs the server without the NL Wallet as means.
func newTestASWithoutWallet(t *testing.T) *testAS {
	t.Helper()
	return newTestASWith(t, testResource, nil, false)
}

func newTestASWith(t *testing.T, resource string, register authorization.Register, withWallet bool) *testAS {
	t.Helper()
	as := &testAS{wallet: newFakeWallet(), keys: map[string]*ecdsa.PrivateKey{}, kids: map[string]string{}, dpopKey: newECKey(t), register: newFakeRegister(), ca: newTestCA(t)}
	var reg authorization.Register = as.register
	if register != nil {
		reg = register
	}
	jwk := jose.JSONWebKey{Key: &as.dpopKey.PublicKey}
	thumbprint, err := jwk.Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	as.jkt = base64.RawURLEncoding.EncodeToString(thumbprint)

	var handler http.Handler
	as.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(as.srv.Close)
	tokens, err := authorization.NewTokenIssuer(as.srv.URL, newECKey(t))
	if err != nil {
		t.Fatal(err)
	}
	var clients []*authorization.Client
	for id, scopes := range map[string][]string{testClientID: {"verify", "retrieve"}, otherClientID: {"verify", "retrieve"}, verifyOnlyID: {"verify"}} {
		key := newECKey(t)
		as.keys[id] = key
		name := ""
		if id == testClientID {
			name = testClientName
		}
		c := &authorization.Client{ID: id, Name: name, RedirectURIs: []string{testRedirectURI}, Scopes: map[string]bool{},
			Keys: jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: id + "-key", Algorithm: "ES256", Use: "sig"}}}}
		for _, sc := range scopes {
			c.Scopes[sc] = true
		}
		clients = append(clients, c)
	}
	identifiers := map[string]authorization.Identifier{digidmock.Means: digidmock.Identifier{}}
	if withWallet {
		identifiers[walletverifier.Means] = as.wallet
	}
	as.service = authorization.NewService(authorization.Config{
		Issuer:       as.srv.URL,
		Resources:    []authorization.Resource{{URL: resource, Name: "Basisregistratie Personen (test)"}, {URL: testResource2, Name: "DUO (test)"}},
		Clients:      clientconfig.New(clients...),
		Identifiers:  identifiers,
		Tokens:       tokens,
		Register:     reg,
		TrustAnchors: as.ca.pool(),
		Now:          as.now,
	})
	handler = NewHandler(as.service)
	return as
}

func (as *testAS) now() time.Time { return time.Now().Add(as.offset) }

// --- authorization request -------------------------------------------------------

type pkce struct{ verifier, challenge string }

func newPKCE() pkce {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	verifier := base64.RawURLEncoding.EncodeToString(b)
	digest := sha256.Sum256([]byte(verifier))
	return pkce{verifier: verifier, challenge: base64.RawURLEncoding.EncodeToString(digest[:])}
}

func authorizeParams(p pkce) url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {testClientID},
		"redirect_uri":          {testRedirectURI},
		"scope":                 {"verify"},
		"state":                 {"state-123"},
		"code_challenge":        {p.challenge},
		"code_challenge_method": {"S256"},
		"resource":              {testResource},
	}
}

// authorize sends an authorization request without following the redirect.
func (as *testAS) authorize(t *testing.T, params url.Values) *http.Response {
	t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(as.srv.URL + authorization.AuthorizePath + "?" + params.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp
}

// redirectParams returns the query of the redirect to the client.
func redirectParams(t *testing.T, resp *http.Response) url.Values {
	t.Helper()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorization response status = %d, want 302", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(loc.String(), testRedirectURI) {
		t.Fatalf("Location = %q, want the redirect_uri", resp.Header.Get("Location"))
	}
	return loc.Query()
}

func assertAuthorizeError(t *testing.T, resp *http.Response, code string) {
	t.Helper()
	q := redirectParams(t, resp)
	if q.Get("error") != code || q.Get("code") != "" {
		t.Fatalf("redirect = %v, want error %q and no code", q, code)
	}
	if q.Get("state") != "state-123" {
		t.Fatalf("state = %q, want it echoed", q.Get("state"))
	}
}

// code runs the authorization request, logs in with the DigiD mock as
// Frouke, and returns the authorization code.
func (as *testAS) code(t *testing.T, params url.Values) string {
	t.Helper()
	return as.codeFor(t, params, bsnFrouke)
}

// codeFor logs in with the DigiD mock with bsn.
func (as *testAS) codeFor(t *testing.T, params url.Values, bsn string) string {
	t.Helper()
	tx := as.startAuthorization(t, params)
	if resp := as.decide(t, tx.id, tx.cookie, url.Values{"decision": {"digid"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("choosing DigiD = %d, want 303 to its login page", resp.StatusCode)
	}
	q := redirectParams(t, as.digidLogin(t, tx.id, tx.cookie, bsn))
	if q.Get("error") != "" || q.Get("code") == "" {
		t.Fatalf("redirect = %v, want a code", q)
	}
	return q.Get("code")
}

// pendingTx is a request page: the transaction and its browser cookie.
type pendingTx struct {
	id     string
	cookie *http.Cookie
	page   string
	header http.Header
}

var transactionField = regexp.MustCompile(`name="transaction" value="([^"]+)"`)

// startAuthorization sends a valid authorization request and returns the
// request page it answers with.
func (as *testAS) startAuthorization(t *testing.T, params url.Values) pendingTx {
	t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(as.srv.URL + authorization.AuthorizePath + "?" + params.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorization request = %d, want the request page (200); Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	m := transactionField.FindStringSubmatch(string(body))
	if m == nil {
		t.Fatalf("request page has no transaction field:\n%s", body)
	}
	var cookie *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == "azsp_tx_"+m[1] {
			cookie = ck
		}
	}
	if cookie == nil {
		t.Fatalf("request page set no transaction cookie")
	}
	return pendingTx{id: m[1], cookie: cookie, page: string(body), header: resp.Header}
}

// decide posts a choice on the request page for transaction tx with cookie
// (none when nil).
func (as *testAS) decide(t *testing.T, tx string, cookie *http.Cookie, form url.Values) *http.Response {
	t.Helper()
	return as.postForm(t, DecisionPath, tx, cookie, form)
}

// digidLogin posts the DigiD mock's login form with bsn as the password.
func (as *testAS) digidLogin(t *testing.T, tx string, cookie *http.Cookie, bsn string) *http.Response {
	t.Helper()
	return as.postForm(t, digidmock.Path, tx, cookie, url.Values{"username": {"j.dejong"}, "bsn": {bsn}})
}

// get fetches path with cookie (none when nil), without following redirects.
func (as *testAS) get(t *testing.T, path string, cookie *http.Cookie) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, as.srv.URL+path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func (as *testAS) postForm(t *testing.T, path, tx string, cookie *http.Cookie, form url.Values) *http.Response {
	t.Helper()
	form.Set("transaction", tx)
	req, _ := http.NewRequest(http.MethodPost, as.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp
}

// --- token request -------------------------------------------------------------

// assertion is a private_key_jwt client assertion of clientID; edit may
// change its claims.
func (as *testAS) assertion(t *testing.T, clientID string, edit func(map[string]any)) string {
	t.Helper()
	now := as.now()
	claims := map[string]any{
		"iss": clientID,
		"sub": clientID,
		"aud": as.srv.URL,
		"iat": now.Unix(),
		"exp": now.Add(time.Minute).Unix(),
		"jti": authorization.RandomID(),
	}
	if edit != nil {
		edit(claims)
	}
	kid := clientID + "-key"
	if k, ok := as.kids[clientID]; ok {
		kid = k
	}
	return signJWT(t, as.keys[clientID], kid, "", claims, false)
}

// proof is a DPoP proof for the token endpoint; edit may change its claims.
func (as *testAS) proof(t *testing.T, edit func(map[string]any)) string {
	t.Helper()
	claims := map[string]any{"jti": authorization.RandomID(), "htm": http.MethodPost, "htu": as.srv.URL + authorization.TokenPath, "iat": as.now().Unix()}
	if edit != nil {
		edit(claims)
	}
	return signJWT(t, as.dpopKey, "", "dpop+jwt", claims, true)
}

func (as *testAS) tokenForm(t *testing.T, code string, p pkce) url.Values {
	return url.Values{
		"grant_type":            {"authorization_code"},
		"code":                  {code},
		"redirect_uri":          {testRedirectURI},
		"code_verifier":         {p.verifier},
		"client_assertion_type": {authorization.ClientAssertionType},
		"client_assertion":      {as.assertion(t, testClientID, nil)},
	}
}

type tokenResponse struct {
	status int
	header http.Header
	body   map[string]any
}

// postToken sends a token request with the DPoP proofs given.
func (as *testAS) postToken(t *testing.T, form url.Values, proofs ...string) tokenResponse {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, as.srv.URL+authorization.TokenPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, p := range proofs {
		req.Header.Add("DPoP", p)
	}
	return as.send(t, req)
}

func (as *testAS) send(t *testing.T, req *http.Request) tokenResponse {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return tokenResponse{status: resp.StatusCode, header: resp.Header, body: body}
}

func assertTokenError(t *testing.T, r tokenResponse, status int, code string) {
	t.Helper()
	if r.status != status || r.body["error"] != code {
		t.Fatalf("token response = %d %v, want %d with error %q", r.status, r.body, status, code)
	}
	if r.body["access_token"] != nil {
		t.Fatalf("an error response carries an access token")
	}
}

// accessToken runs the whole flow and returns the access token.
func (as *testAS) accessToken(t *testing.T, params url.Values, p pkce) string {
	t.Helper()
	r := as.postToken(t, as.tokenForm(t, as.code(t, params), p), as.proof(t, nil))
	if r.status != http.StatusOK {
		t.Fatalf("token response = %d %v, want 200", r.status, r.body)
	}
	token, _ := r.body["access_token"].(string)
	return token
}

// --- JOSE ------------------------------------------------------------------------

func signJWT(t *testing.T, key *ecdsa.PrivateKey, kid, typ string, claims map[string]any, embedJWK bool) string {
	t.Helper()
	opts := &jose.SignerOptions{EmbedJWK: embedJWK}
	if typ != "" {
		opts = opts.WithType(jose.ContentType(typ))
	}
	if kid != "" {
		opts = opts.WithHeader("kid", kid)
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

func newECKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// fakeWallet is the NL Wallet verifier as SessionIdentifier, in memory: a
// test sets how each session goes.
type fakeWallet struct {
	mu       sync.Mutex
	next     int
	startErr error
	sessions map[string]*fakeWalletSession
}

type fakeWalletSession struct {
	progress    authorization.Progress
	progressErr error
	identity    authorization.Identity
	identifyErr error
}

func newFakeWallet() *fakeWallet { return &fakeWallet{sessions: map[string]*fakeWalletSession{}} }

func (f *fakeWallet) Start(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return "", f.startErr
	}
	f.next++
	id := fmt.Sprintf("session-%d", f.next)
	f.sessions[id] = &fakeWalletSession{progress: authorization.Progress{State: authorization.SessionWaiting, Link: "https://app.example/deeplink/" + id}}
	return id, nil
}

func (f *fakeWallet) Progress(_ context.Context, session string) (authorization.Progress, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[session]
	return s.progress, s.progressErr
}

func (f *fakeWallet) Identify(_ context.Context, session string) (authorization.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[session]
	return s.identity, s.identifyErr
}

// set changes how session goes.
func (f *fakeWallet) set(session string, edit func(*fakeWalletSession)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	edit(f.sessions[session])
}
