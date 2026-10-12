package main

// Token cases of slice 2 (#503): the ASIP as resource server of ETSI
// TS 119 478 clause 6.1.3. The numbers refer to the case table of #503.

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	testIssuer    = "https://as.test"
	testPublicURL = "https://asip.test"
	testKeyID     = "test-as-key"
)

// --- case 1-2: the token identifies the person --------------------------------

func TestTokenIdentifiesThePerson(t *testing.T) { // case 1, 2
	env := newTestServer(t)
	res := verifyAttribute(t, env, env.person(bsnFrouke), idFamilyName, `{"family_name":"Jansen"}`)
	assertResult(t, res, idFamilyName, resultMatch)
	res = verifyAttribute(t, env, env.person(bsnTom), idFamilyName, `{"family_name":"Jansen"}`)
	assertResult(t, res, idFamilyName, resultNoMatch)
}

// --- case 3-10, 18: the access token -------------------------------------------

func TestRequestWithoutTokenIs401(t *testing.T) { // case 3
	env := newTestServer(t)
	for _, path := range []string{"/verify", "/retrieve"} {
		rec := post(t, env, path, nil, verifyBody(idFamilyName, `{"family_name":"Jansen"}`))
		assertProblem(t, rec, http.StatusUnauthorized)
		// RFC 6750 section 3.1: no error code when the request has no token.
		if got := rec.Header().Get("WWW-Authenticate"); got != `DPoP algs="ES256"` {
			t.Fatalf("%s: WWW-Authenticate = %q, want a DPoP challenge without error", path, got)
		}
	}
}

func TestInvalidAccessTokenIs401(t *testing.T) { // case 4-10, 18
	env := newTestServer(t)
	otherKey := newECKey(t)
	valid := func() map[string]any { return env.authz.claims(bsnFrouke, "verify retrieve", env.qtsp.jkt) }
	with := func(edit func(map[string]any)) map[string]any {
		c := valid()
		edit(c)
		return c
	}
	for name, auth := range map[string]authFunc{
		"4: signed with a key not of the authorization server": env.presentingToken(func(t *testing.T) string {
			return signJWT(t, otherKey, jose.ES256, testKeyID, "at+jwt", valid())
		}, nil),
		"5: alg none": env.presentingToken(func(t *testing.T) string {
			return unsignedJWT(t, map[string]any{"alg": "none", "typ": "at+jwt"}, valid())
		}, nil),
		"6: typ is not at+jwt": env.presentingToken(func(t *testing.T) string {
			return signJWT(t, env.authz.key, jose.ES256, testKeyID, "JWT", valid())
		}, nil),
		"7: another issuer":     env.presenting(with(func(c map[string]any) { c["iss"] = "https://other-as.test" }), nil),
		"8: another audience":   env.presenting(with(func(c map[string]any) { c["aud"] = "https://other-asip.test/authsrc-api" }), nil),
		"9: expired":            env.presenting(with(func(c map[string]any) { c["iat"], c["exp"] = ago(11*time.Minute), ago(time.Minute) }), nil),
		"9b: no exp":            env.presenting(with(func(c map[string]any) { delete(c, "exp") }), nil),
		"10: Bearer scheme":     env.withScheme("Bearer"),
		"10b: not DPoP-bound":   env.presenting(with(func(c map[string]any) { delete(c, "cnf") }), nil),
		"18: no BSN claim":      env.presenting(with(func(c map[string]any) { delete(c, bsnClaim) }), nil),
		"18b: empty BSN claim":  env.presenting(with(func(c map[string]any) { c[bsnClaim] = "" }), nil),
		"not a JWT at all":      env.presentingToken(func(*testing.T) string { return "not-a-jwt" }, nil),
		"DPoP scheme, no token": func(_ *testing.T, req *http.Request) { req.Header.Set("Authorization", "DPoP ") },
	} {
		t.Run(name, func(t *testing.T) {
			rec := post(t, env, "/verify", auth, verifyBody(idFamilyName, `{"family_name":"Jansen"}`))
			assertAuthError(t, rec, "invalid_token")
		})
	}
}

// RFC 9068 section 2.1: resource servers support RS256. The authorization
// server of this demo signs with ES256; an RS256 token with a key the
// authorization server publishes is accepted too.
func TestRS256AccessTokenIsAccepted(t *testing.T) {
	env := newTestServer(t)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	env = env.withAuthorizationServerKey(t, jose.JSONWebKey{Key: &rsaKey.PublicKey, KeyID: "rsa-key", Algorithm: "RS256", Use: "sig"})
	auth := env.presentingToken(func(t *testing.T) string {
		return signJWT(t, rsaKey, jose.RS256, "rsa-key", "at+jwt", env.authz.claims(bsnFrouke, "verify", env.qtsp.jkt))
	}, nil)
	res := verifyAttribute(t, env, auth, idFamilyName, `{"family_name":"Jansen"}`)
	assertResult(t, res, idFamilyName, resultMatch)
}

// --- case 11-16: the DPoP proof --------------------------------------------------

func TestMissingDPoPProofIs401(t *testing.T) { // case 11
	env := newTestServer(t)
	auth := func(t *testing.T, req *http.Request) {
		req.Header.Set("Authorization", "DPoP "+env.authz.sign(t, env.authz.claims(bsnFrouke, "verify", env.qtsp.jkt)))
	}
	rec := post(t, env, "/verify", auth, verifyBody(idFamilyName, `{"family_name":"Jansen"}`))
	assertAuthError(t, rec, "invalid_request")
}

func TestInvalidDPoPProofIs401(t *testing.T) { // case 12-15
	env := newTestServer(t)
	otherQTSP := newTestQTSP(t)
	claims := env.authz.claims(bsnFrouke, "verify retrieve", env.qtsp.jkt)
	for name, auth := range map[string]authFunc{
		"12: proof key is not cnf.jkt": func(t *testing.T, req *http.Request) {
			token := env.authz.sign(t, claims)
			req.Header.Set("Authorization", "DPoP "+token)
			req.Header.Set("DPoP", otherQTSP.proof(t, req.Method, testPublicURL+req.URL.Path, token, nil))
		},
		"13: proof for another URL":     env.presenting(claims, func(p map[string]any) { p["htu"] = testPublicURL + basePath + "/retrieve" }),
		"13b: proof for another method": env.presenting(claims, func(p map[string]any) { p["htm"] = http.MethodGet }),
		"14: ath of another token":      env.presenting(claims, func(p map[string]any) { p["ath"] = accessTokenHash("another-token") }),
		"15: proof too old":             env.presenting(claims, func(p map[string]any) { p["iat"] = ago(2 * time.Minute) }),
		"15b: proof from the future":    env.presenting(claims, func(p map[string]any) { p["iat"] = ago(-2 * time.Minute) }),
		"proof without jti":             env.presenting(claims, func(p map[string]any) { delete(p, "jti") }),
		"proof typ is not dpop+jwt": func(t *testing.T, req *http.Request) {
			token := env.authz.sign(t, claims)
			req.Header.Set("Authorization", "DPoP "+token)
			req.Header.Set("DPoP", env.qtsp.proofWithType(t, "JWT", req.Method, testPublicURL+req.URL.Path, token))
		},
		"two proofs": func(t *testing.T, req *http.Request) {
			env.presenting(claims, nil)(t, req)
			req.Header.Add("DPoP", req.Header.Get("DPoP"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec := post(t, env, "/verify", auth, verifyBody(idFamilyName, `{"family_name":"Jansen"}`))
			assertAuthError(t, rec, "invalid_dpop_proof")
		})
	}
}

func TestReplayedDPoPProofIs401(t *testing.T) { // case 16
	env := newTestServer(t)
	token := env.authz.sign(t, env.authz.claims(bsnFrouke, "verify", env.qtsp.jkt))
	proof := env.qtsp.proof(t, http.MethodPost, testPublicURL+basePath+"/verify", token, nil)
	auth := func(_ *testing.T, req *http.Request) {
		req.Header.Set("Authorization", "DPoP "+token)
		req.Header.Set("DPoP", proof)
	}
	body := verifyBody(idFamilyName, `{"family_name":"Jansen"}`)
	if rec := post(t, env, "/verify", auth, body); rec.Code != http.StatusOK {
		t.Fatalf("first use: status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	assertAuthError(t, post(t, env, "/verify", auth, body), "invalid_dpop_proof")
}

// --- case 17: the scope covers the operation ----------------------------------

func TestInsufficientScopeIs401(t *testing.T) { // case 17
	env := newTestServer(t)
	for _, tc := range []struct{ scope, path, body string }{
		{"verify", "/retrieve", `{"attributeIdentifiers":[{"attributeIdentifier":"` + idFamilyName + `"}]}`},
		{"retrieve", "/verify", verifyBody(idFamilyName, `{"family_name":"Jansen"}`)},
	} {
		auth := env.presenting(env.authz.claims(bsnFrouke, tc.scope, env.qtsp.jkt), nil)
		rec := post(t, env, tc.path, auth, tc.body)
		assertAuthError(t, rec, "insufficient_scope")
		want := `scope="` + strings.TrimPrefix(tc.path, "/") + `"`
		if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, want) {
			t.Fatalf("%s: WWW-Authenticate = %q, want %s", tc.path, got, want)
		}
	}
}

// --- case 19: a BSN the source does not know ------------------------------------

func TestBSNUnknownToTheSourceIsUnknown(t *testing.T) { // case 19
	env := newTestServer(t)
	res := verifyAttribute(t, env, env.person("111222333"), idFamilyName, `{"family_name":"Jansen"}`)
	assertResult(t, res, idFamilyName, resultUnknown)
}

// --- case 20: order of the checks ----------------------------------------------

func TestOrderOfChecks(t *testing.T) { // case 20
	env := newTestServer(t)
	malformed := `{"attributes":`
	verifyOnly := env.presenting(env.authz.claims(bsnFrouke, "verify", env.qtsp.jkt), nil)
	expired := env.authz.claims(bsnFrouke, "verify retrieve", env.qtsp.jkt)
	expired["exp"] = ago(time.Minute)
	for _, tc := range []struct {
		name, path string
		auth       authFunc
		body       string
		status     int
	}{
		{"invalid token, malformed body", "/verify", env.presenting(expired, nil), malformed, http.StatusUnauthorized},
		{"verify-only token on /retrieve, malformed body", "/retrieve", verifyOnly, malformed, http.StatusUnauthorized},
		{"valid token, malformed body", "/verify", env.person(bsnFrouke), malformed, http.StatusBadRequest},
		{"valid token, mandate", "/verify", env.person(bsnFrouke),
			`{"attributes":[{"attributeIdentifier":"` + idNotInCat + `","attributeValue":{"size":43}}],"mandate":{"type":"guardian"}}`, http.StatusNotImplemented},
		{"valid token, unknown attribute", "/verify", env.person(bsnFrouke), verifyBody(idNotInCat, `{"size":43}`), http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertProblem(t, post(t, env, tc.path, tc.auth, tc.body), tc.status)
		})
	}
}

// --- keys of the authorization server ------------------------------------------

// The ASIP takes the keys from the jwks_uri in the metadata of the
// authorization server (RFC 8414, RFC 9068 section 4).
func TestKeysFromAuthorizationServerMetadata(t *testing.T) {
	authz := newTestAuthorizationServer(t)
	var as *httptest.Server
	as = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/.well-known/oauth-authorization-server"):
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": as.URL, "jwks_uri": as.URL + "/jwks.json"})
		case r.URL.Path == "/jwks.json":
			_ = json.NewEncoder(w).Encode(authz.jwks)
		default:
			http.NotFound(w, r)
		}
	}))
	defer as.Close()
	keys, err := newRemoteKeys(as.URL).keys(t.Context(), testKeyID)
	if err != nil || len(keys) != 1 || keys[0].KeyID != testKeyID {
		t.Fatalf("keys = %v, %v; want the key of the authorization server", keys, err)
	}
	// RFC 8414 section 3.3: the issuer in the metadata must be the issuer the
	// ASIP is configured with.
	if _, err := newRemoteKeys(as.URL+"/other").keys(t.Context(), testKeyID); !errors.Is(err, errKeysUnavailable) {
		t.Fatalf("metadata of another issuer was accepted: %v", err)
	}
}

// When the keys of the authorization server cannot be obtained, no token can
// be validated. That is not the client's fault: 503, an addition to Annex B.
func TestUnavailableAuthorizationServerIs503(t *testing.T) {
	authz, qtsp := newTestAuthorizationServer(t), newTestQTSP(t)
	env := newTestServerWithKeys(t, authz, qtsp, newRemoteKeys("http://127.0.0.1:1"))
	for _, path := range []string{"/verify", "/retrieve"} {
		rec := post(t, env, path, env.person(bsnFrouke), verifyBody(idFamilyName, `{"family_name":"Jansen"}`))
		assertProblem(t, rec, http.StatusServiceUnavailable)
	}
}

// --- helpers -------------------------------------------------------------------

// authFunc adds credentials to a request.
type authFunc func(t *testing.T, req *http.Request)

// testAuthorizationServer stands in for services/asi-authorization-server:
// it signs access tokens the way that service does.
type testAuthorizationServer struct {
	key  *ecdsa.PrivateKey
	jwks jose.JSONWebKeySet
}

func newTestAuthorizationServer(t *testing.T) *testAuthorizationServer {
	t.Helper()
	key := newECKey(t)
	return &testAuthorizationServer{
		key:  key,
		jwks: jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: testKeyID, Algorithm: "ES256", Use: "sig"}}},
	}
}

// claims are the claims of an access token for bsn, bound to the key with
// thumbprint jkt.
func (a *testAuthorizationServer) claims(bsn, scope, jkt string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":       testIssuer,
		"sub":       "opaque-per-token",
		"aud":       testPublicURL + basePath,
		"client_id": "test-qtsp",
		"iat":       now.Unix(),
		"exp":       now.Add(10 * time.Minute).Unix(),
		"jti":       randomTestID(),
		"scope":     scope,
		"cnf":       map[string]string{"jkt": jkt},
		bsnClaim:    bsn,
	}
}

func (a *testAuthorizationServer) sign(t *testing.T, claims map[string]any) string {
	return signJWT(t, a.key, jose.ES256, testKeyID, "at+jwt", claims)
}

// testQTSP is the client: it holds the DPoP key.
type testQTSP struct {
	key *ecdsa.PrivateKey
	jkt string
}

func newTestQTSP(t *testing.T) *testQTSP {
	t.Helper()
	key := newECKey(t)
	jwk := jose.JSONWebKey{Key: &key.PublicKey}
	thumbprint, err := jwk.Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	return &testQTSP{key: key, jkt: base64.RawURLEncoding.EncodeToString(thumbprint)}
}

// proof returns a DPoP proof (RFC 9449 section 4.2) for a request with
// token; edit may change its claims.
func (q *testQTSP) proof(t *testing.T, method, htu, token string, edit func(map[string]any)) string {
	t.Helper()
	claims := map[string]any{
		"jti": randomTestID(),
		"htm": method,
		"htu": htu,
		"iat": time.Now().Unix(),
		"ath": accessTokenHash(token),
	}
	if edit != nil {
		edit(claims)
	}
	return q.signProof(t, "dpop+jwt", claims)
}

func (q *testQTSP) proofWithType(t *testing.T, typ, method, htu, token string) string {
	t.Helper()
	return q.signProof(t, typ, map[string]any{"jti": randomTestID(), "htm": method, "htu": htu, "iat": time.Now().Unix(), "ath": accessTokenHash(token)})
}

func (q *testQTSP) signProof(t *testing.T, typ string, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: q.key},
		(&jose.SignerOptions{EmbedJWK: true}).WithType(jose.ContentType(typ)))
	if err != nil {
		t.Fatal(err)
	}
	proof, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

// person returns the credentials of a QTSP that went through the flow for
// the test person with bsn: a token for verify and retrieve, and a fresh
// DPoP proof per request.
func (env testEnv) person(bsn string) authFunc {
	return env.presenting(env.authz.claims(bsn, "verify retrieve", env.qtsp.jkt), nil)
}

// presenting signs claims as an access token and presents it with a fresh
// DPoP proof; editProof may change the proof claims.
func (env testEnv) presenting(claims map[string]any, editProof func(map[string]any)) authFunc {
	return env.presentingToken(func(t *testing.T) string { return env.authz.sign(t, claims) }, editProof)
}

func (env testEnv) presentingToken(token func(t *testing.T) string, editProof func(map[string]any)) authFunc {
	return func(t *testing.T, req *http.Request) {
		tok := token(t)
		req.Header.Set("Authorization", "DPoP "+tok)
		req.Header.Set("DPoP", env.qtsp.proof(t, req.Method, testPublicURL+req.URL.Path, tok, editProof))
	}
}

// withScheme presents a valid token and proof under another authorization
// scheme.
func (env testEnv) withScheme(scheme string) authFunc {
	return func(t *testing.T, req *http.Request) {
		env.person(bsnFrouke)(t, req)
		req.Header.Set("Authorization", scheme+" "+strings.TrimPrefix(req.Header.Get("Authorization"), "DPoP "))
	}
}

// withAuthorizationServerKey returns an environment whose authorization
// server also publishes key.
func (env testEnv) withAuthorizationServerKey(t *testing.T, key jose.JSONWebKey) testEnv {
	t.Helper()
	env.authz.jwks.Keys = append(env.authz.jwks.Keys, key)
	return newTestServerWith(t, env.authz, env.qtsp)
}

func assertAuthError(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	assertProblem(t, rec, http.StatusUnauthorized)
	if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "DPoP ") || !strings.Contains(got, `error="`+code+`"`) {
		t.Fatalf("WWW-Authenticate = %q, want a DPoP challenge with error %q", got, code)
	}
}

func signJWT(t *testing.T, key crypto.Signer, alg jose.SignatureAlgorithm, kid, typ string, claims map[string]any) string {
	t.Helper()
	opts := (&jose.SignerOptions{}).WithType(jose.ContentType(typ))
	if kid != "" {
		opts = opts.WithHeader("kid", kid)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key}, opts)
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// unsignedJWT builds header.payload. with an empty signature, as for
// alg none.
func unsignedJWT(t *testing.T, header, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c) + "."
}

func accessTokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func newECKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func ago(d time.Duration) int64 { return time.Now().Add(-d).Unix() }

func randomTestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
