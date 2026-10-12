package httpapi

// Cases of slice 2 (#503) for the authorization server of ETSI TS 119 478
// clause 6.1.3. The numbers refer to the case table of #503; case 24
// through /verify and case 37 are in e2e_test.go.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

// --- case 23: metadata -----------------------------------------------------------

func TestMetadata(t *testing.T) { // case 23
	as := newTestAS(t)
	resp, err := http.Get(as.srv.URL + authorization.MetadataPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var meta map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]any{
		"issuer":                                as.srv.URL,
		"authorization_endpoint":                as.srv.URL + authorization.AuthorizePath,
		"token_endpoint":                        as.srv.URL + authorization.TokenPath,
		"jwks_uri":                              as.srv.URL + authorization.JWKSPath,
		"code_challenge_methods_supported":      []any{"S256"},
		"token_endpoint_auth_methods_supported": []any{"private_key_jwt"},
		"dpop_signing_alg_values_supported":     []any{"ES256"},
		"scopes_supported":                      []any{"verify", "retrieve"},
		"response_types_supported":              []any{"code"},
		"grant_types_supported":                 []any{"authorization_code"},
	} {
		got, _ := json.Marshal(meta[field])
		w, _ := json.Marshal(want)
		if string(got) != string(w) {
			t.Errorf("%s = %s, want %s", field, got, w)
		}
	}
	keys := fetchJWKS(t, as)
	if len(keys.Keys) != 1 || keys.Keys[0].KeyID == "" || !keys.Keys[0].IsPublic() {
		t.Fatalf("jwks = %+v, want one public key with a kid", keys)
	}
}

// --- case 24-26: the access token ----------------------------------------------

func TestFullFlowIssuesDPoPBoundAccessToken(t *testing.T) { // case 24
	as := newTestAS(t)
	p := newPKCE()
	r := as.postToken(t, as.tokenForm(t, as.code(t, authorizeParams(p)), p), as.proof(t, nil))
	if r.status != http.StatusOK {
		t.Fatalf("token response = %d %v, want 200", r.status, r.body)
	}
	if r.body["token_type"] != "DPoP" || r.body["scope"] != "verify" || r.body["expires_in"] != float64(600) {
		t.Fatalf("token response = %v, want token_type DPoP, scope verify, expires_in 600", r.body)
	}
	if r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", r.header.Get("Cache-Control"))
	}
	token, _ := r.body["access_token"].(string)
	header, claims := verifyAccessToken(t, as, token)
	if typ, _ := header.ExtraHeaders[jose.HeaderType].(string); typ != "at+jwt" || header.KeyID == "" || header.Algorithm != "ES256" {
		t.Fatalf("header = %+v, want typ at+jwt, alg ES256 and a kid", header)
	}
	for field, want := range map[string]any{
		"iss":                  as.srv.URL,
		"aud":                  testResource,
		"client_id":            testClientID,
		"scope":                "verify",
		authorization.BSNClaim: bsnFrouke,
	} {
		if claims[field] != want {
			t.Errorf("%s = %v, want %v", field, claims[field], want)
		}
	}
	if cnf, _ := claims["cnf"].(map[string]any); cnf["jkt"] != as.jkt {
		t.Errorf("cnf = %v, want jkt %s", claims["cnf"], as.jkt)
	}
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if exp-iat <= 0 || exp-iat > 600 {
		t.Errorf("exp - iat = %v, want at most 600", exp-iat)
	}
	if claims["sub"] == "" || claims["sub"] == nil || claims["jti"] == nil {
		t.Errorf("sub or jti missing: %v", claims)
	}
}

func TestSubIsOpaquePerToken(t *testing.T) { // case 25
	as := newTestAS(t)
	var subs []any
	for range 2 {
		p := newPKCE()
		_, claims := verifyAccessToken(t, as, as.accessToken(t, authorizeParams(p), p))
		subs = append(subs, claims["sub"])
	}
	if subs[0] == subs[1] || subs[0] == bsnFrouke {
		t.Fatalf("sub = %v, want a different opaque value per token", subs)
	}
}

func TestResourceIndicator(t *testing.T) { // case 26
	as := newTestAS(t)

	p := newPKCE()
	params := authorizeParams(p)
	params.Set("resource", testResource)
	_, claims := verifyAccessToken(t, as, as.accessToken(t, params, p))
	if claims["aud"] != testResource {
		t.Fatalf("aud = %v, want %s", claims["aud"], testResource)
	}

	params = authorizeParams(newPKCE())
	params.Set("resource", "https://other-asip.test/authsrc-api")
	assertAuthorizeError(t, as.authorize(t, params), "invalid_target")

	p = newPKCE()
	form := as.tokenForm(t, as.code(t, authorizeParams(p)), p)
	form.Set("resource", "https://other-asip.test/authsrc-api")
	assertTokenError(t, as.postToken(t, form, as.proof(t, nil)), http.StatusBadRequest, "invalid_target")
}

// --- case 27-30, 36: the authorization request ---------------------------------

func TestAuthorizationRequestErrors(t *testing.T) { // case 27, 28, 30, 36
	as := newTestAS(t)
	for _, tc := range []struct {
		name string
		edit func(url.Values)
		code string
	}{
		{"27: no code_challenge", func(q url.Values) { q.Del("code_challenge") }, "invalid_request"},
		{"28: code_challenge_method plain", func(q url.Values) { q.Set("code_challenge_method", "plain") }, "invalid_request"},
		{"28b: no code_challenge_method", func(q url.Values) { q.Del("code_challenge_method") }, "invalid_request"},
		{"28c: code_challenge is not a SHA-256 digest", func(q url.Values) { q.Set("code_challenge", "short") }, "invalid_request"},
		{"30: scope outside the registration", func(q url.Values) { q.Set("client_id", verifyOnlyID); q.Set("scope", "retrieve") }, "invalid_scope"},
		{"30b: unknown scope", func(q url.Values) { q.Set("scope", "verify admin") }, "invalid_scope"},
		{"30c: no scope", func(q url.Values) { q.Del("scope") }, "invalid_scope"},
		{"36: unknown test person", func(q url.Values) { q.Set("login_hint", "nobody") }, "access_denied"},
		{"36b: no login_hint", func(q url.Values) { q.Del("login_hint") }, "access_denied"},
		{"response_type is not code", func(q url.Values) { q.Set("response_type", "token") }, "unsupported_response_type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := authorizeParams(newPKCE())
			tc.edit(params)
			assertAuthorizeError(t, as.authorize(t, params), tc.code)
		})
	}
}

func TestUnknownClientOrRedirectURIIsNotRedirected(t *testing.T) { // case 29
	as := newTestAS(t)
	for name, edit := range map[string]func(url.Values){
		"unknown client":            func(q url.Values) { q.Set("client_id", "unknown") },
		"unregistered redirect_uri": func(q url.Values) { q.Set("redirect_uri", "https://attacker.test/callback") },
		"no redirect_uri":           func(q url.Values) { q.Del("redirect_uri") },
	} {
		t.Run(name, func(t *testing.T) {
			params := authorizeParams(newPKCE())
			edit(params)
			resp := as.authorize(t, params)
			if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
				t.Fatalf("status = %d, Location = %q; want 400 without redirect", resp.StatusCode, resp.Header.Get("Location"))
			}
		})
	}
}

func TestVerifyOnlyClientGetsVerify(t *testing.T) { // case 30, positive
	as := newTestAS(t)
	params := authorizeParams(newPKCE())
	params.Set("client_id", verifyOnlyID)
	params.Set("scope", "verify")
	as.code(t, params)
}

// --- case 31-32: the authorization code ----------------------------------------

func TestWrongCodeVerifierIsInvalidGrant(t *testing.T) { // case 31
	as := newTestAS(t)
	p := newPKCE()
	assertTokenError(t, as.postToken(t, as.tokenForm(t, as.code(t, authorizeParams(p)), newPKCE()), as.proof(t, nil)),
		http.StatusBadRequest, "invalid_grant")
}

func TestCodeIsSingleUseShortLivedAndBoundToTheClient(t *testing.T) { // case 32
	t.Run("used twice", func(t *testing.T) {
		as := newTestAS(t)
		p := newPKCE()
		code := as.code(t, authorizeParams(p))
		if r := as.postToken(t, as.tokenForm(t, code, p), as.proof(t, nil)); r.status != http.StatusOK {
			t.Fatalf("first use = %d %v, want 200", r.status, r.body)
		}
		assertTokenError(t, as.postToken(t, as.tokenForm(t, code, p), as.proof(t, nil)), http.StatusBadRequest, "invalid_grant")
	})
	t.Run("expired", func(t *testing.T) {
		as := newTestAS(t)
		p := newPKCE()
		code := as.code(t, authorizeParams(p))
		as.offset = authorization.AuthorizationCodeLifetime + time.Second
		assertTokenError(t, as.postToken(t, as.tokenForm(t, code, p), as.proof(t, nil)), http.StatusBadRequest, "invalid_grant")
	})
	t.Run("redeemed by another client", func(t *testing.T) {
		as := newTestAS(t)
		p := newPKCE()
		form := as.tokenForm(t, as.code(t, authorizeParams(p)), p)
		form.Set("client_assertion", as.assertion(t, otherClientID, nil))
		assertTokenError(t, as.postToken(t, form, as.proof(t, nil)), http.StatusBadRequest, "invalid_grant")
	})
	t.Run("another redirect_uri", func(t *testing.T) {
		as := newTestAS(t)
		p := newPKCE()
		form := as.tokenForm(t, as.code(t, authorizeParams(p)), p)
		form.Set("redirect_uri", "https://qtsp.test/other")
		assertTokenError(t, as.postToken(t, form, as.proof(t, nil)), http.StatusBadRequest, "invalid_grant")
	})
	t.Run("unknown code", func(t *testing.T) {
		as := newTestAS(t)
		assertTokenError(t, as.postToken(t, as.tokenForm(t, "no-such-code", newPKCE()), as.proof(t, nil)), http.StatusBadRequest, "invalid_grant")
	})
}

func TestGrantTypeMustBeAuthorizationCode(t *testing.T) {
	as := newTestAS(t)
	p := newPKCE()
	form := as.tokenForm(t, as.code(t, authorizeParams(p)), p)
	form.Set("grant_type", "client_credentials")
	assertTokenError(t, as.postToken(t, form, as.proof(t, nil)), http.StatusBadRequest, "unsupported_grant_type")
}

// --- case 33-34: client authentication -----------------------------------------

func TestClientAuthenticationIsPrivateKeyJWT(t *testing.T) { // case 33, 34
	as := newTestAS(t)
	for _, tc := range []struct {
		name string
		edit func(t *testing.T, form url.Values, req *http.Request)
	}{
		{"33: no client_assertion", func(_ *testing.T, f url.Values, _ *http.Request) {
			f.Del("client_assertion")
			f.Del("client_assertion_type")
		}},
		{"33b: client_secret_basic", func(_ *testing.T, f url.Values, r *http.Request) {
			f.Del("client_assertion")
			f.Del("client_assertion_type")
			r.SetBasicAuth(testClientID, "secret")
		}},
		{"33c: client_secret_basic next to the assertion", func(_ *testing.T, _ url.Values, r *http.Request) {
			r.SetBasicAuth(testClientID, "secret") // RFC 6749 section 2.3: one method per request
		}},
		{"33d: client_secret_post", func(_ *testing.T, f url.Values, _ *http.Request) { f.Set("client_secret", "secret") }},
		{"33e: another client_assertion_type", func(_ *testing.T, f url.Values, _ *http.Request) { f.Set("client_assertion_type", "urn:other") }},
		{"34: signed with another key", func(t *testing.T, f url.Values, _ *http.Request) {
			claims := map[string]any{"iss": testClientID, "sub": testClientID, "aud": as.srv.URL, "exp": as.now().Add(time.Minute).Unix(), "jti": authorization.RandomID()}
			f.Set("client_assertion", signJWT(t, newECKey(t), testClientID+"-key", "", claims, false))
		}},
		{"34b: aud is not the authorization server", func(t *testing.T, f url.Values, _ *http.Request) {
			f.Set("client_assertion", as.assertion(t, testClientID, func(c map[string]any) { c["aud"] = "https://other-as.test" }))
		}},
		{"34c: expired", func(t *testing.T, f url.Values, _ *http.Request) {
			f.Set("client_assertion", as.assertion(t, testClientID, func(c map[string]any) { c["exp"] = as.now().Add(-time.Minute).Unix() }))
		}},
		{"34d: no exp", func(t *testing.T, f url.Values, _ *http.Request) {
			f.Set("client_assertion", as.assertion(t, testClientID, func(c map[string]any) { delete(c, "exp") }))
		}},
		{"34e: no jti", func(t *testing.T, f url.Values, _ *http.Request) {
			f.Set("client_assertion", as.assertion(t, testClientID, func(c map[string]any) { delete(c, "jti") }))
		}},
		{"34f: sub is not the client", func(t *testing.T, f url.Values, _ *http.Request) {
			f.Set("client_assertion", as.assertion(t, testClientID, func(c map[string]any) { c["sub"] = otherClientID }))
		}},
		{"34g: unknown client", func(t *testing.T, f url.Values, _ *http.Request) {
			f.Set("client_assertion", as.assertion(t, testClientID, func(c map[string]any) { c["iss"], c["sub"] = "unknown", "unknown" }))
		}},
		{"34h: client_id differs from the assertion", func(_ *testing.T, f url.Values, _ *http.Request) { f.Set("client_id", otherClientID) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPKCE()
			form := as.tokenForm(t, as.code(t, authorizeParams(p)), p)
			req, _ := http.NewRequest(http.MethodPost, as.srv.URL+authorization.TokenPath, nil)
			tc.edit(t, form, req)
			assertTokenError(t, as.sendForm(t, req, form, as.proof(t, nil)), http.StatusUnauthorized, "invalid_client")
		})
	}
}

func TestReplayedClientAssertionIsInvalidClient(t *testing.T) { // case 34, jti replayed
	as := newTestAS(t)
	assertion := as.assertion(t, testClientID, nil)
	p := newPKCE()
	form := as.tokenForm(t, as.code(t, authorizeParams(p)), p)
	form.Set("client_assertion", assertion)
	if r := as.postToken(t, form, as.proof(t, nil)); r.status != http.StatusOK {
		t.Fatalf("first use = %d %v, want 200", r.status, r.body)
	}
	p = newPKCE()
	form = as.tokenForm(t, as.code(t, authorizeParams(p)), p)
	form.Set("client_assertion", assertion)
	assertTokenError(t, as.postToken(t, form, as.proof(t, nil)), http.StatusUnauthorized, "invalid_client")
}

func TestTokenEndpointAsAudienceIsAccepted(t *testing.T) {
	as := newTestAS(t)
	p := newPKCE()
	form := as.tokenForm(t, as.code(t, authorizeParams(p)), p)
	form.Set("client_assertion", as.assertion(t, testClientID, func(c map[string]any) { c["aud"] = as.srv.URL + authorization.TokenPath }))
	if r := as.postToken(t, form, as.proof(t, nil)); r.status != http.StatusOK {
		t.Fatalf("token response = %d %v, want 200", r.status, r.body)
	}
}

// --- case 35: the DPoP proof at the token endpoint --------------------------------

func TestTokenRequestNeedsAValidDPoPProof(t *testing.T) { // case 35
	as := newTestAS(t)
	for name, proofs := range map[string]func(t *testing.T) []string{
		"no proof": func(*testing.T) []string { return nil },
		"proof for another URL": func(t *testing.T) []string {
			return []string{as.proof(t, func(c map[string]any) { c["htu"] = as.srv.URL + authorization.AuthorizePath })}
		},
		"proof for another method": func(t *testing.T) []string {
			return []string{as.proof(t, func(c map[string]any) { c["htm"] = http.MethodGet })}
		},
		"proof too old": func(t *testing.T) []string {
			return []string{as.proof(t, func(c map[string]any) { c["iat"] = as.now().Add(-2 * time.Minute).Unix() })}
		},
		"proof without jti": func(t *testing.T) []string {
			return []string{as.proof(t, func(c map[string]any) { delete(c, "jti") })}
		},
		"proof typ is not dpop+jwt": func(t *testing.T) []string {
			return []string{signJWT(t, as.dpopKey, "", "JWT", map[string]any{"jti": authorization.RandomID(), "htm": "POST", "htu": as.srv.URL + authorization.TokenPath, "iat": as.now().Unix()}, true)}
		},
		"proof without jwk": func(t *testing.T) []string {
			return []string{signJWT(t, as.dpopKey, "", "dpop+jwt", map[string]any{"jti": authorization.RandomID(), "htm": "POST", "htu": as.srv.URL + authorization.TokenPath, "iat": as.now().Unix()}, false)}
		},
		"two proofs": func(t *testing.T) []string { return []string{as.proof(t, nil), as.proof(t, nil)} },
	} {
		t.Run(name, func(t *testing.T) {
			p := newPKCE()
			assertTokenError(t, as.postToken(t, as.tokenForm(t, as.code(t, authorizeParams(p)), p), proofs(t)...),
				http.StatusBadRequest, "invalid_dpop_proof")
		})
	}
	t.Run("replayed proof", func(t *testing.T) {
		proof := as.proof(t, nil)
		p := newPKCE()
		if r := as.postToken(t, as.tokenForm(t, as.code(t, authorizeParams(p)), p), proof); r.status != http.StatusOK {
			t.Fatalf("first use = %d %v, want 200", r.status, r.body)
		}
		p = newPKCE()
		assertTokenError(t, as.postToken(t, as.tokenForm(t, as.code(t, authorizeParams(p)), p), proof), http.StatusBadRequest, "invalid_dpop_proof")
	})
}

// --- helpers -------------------------------------------------------------------

// sendForm sends form to the token endpoint with the headers of req (such as
// basic authentication) and the DPoP proofs given.
func (as *testAS) sendForm(t *testing.T, req *http.Request, form url.Values, proofs ...string) tokenResponse {
	t.Helper()
	r, _ := http.NewRequest(http.MethodPost, as.srv.URL+authorization.TokenPath, strings.NewReader(form.Encode()))
	for k, v := range req.Header {
		r.Header[k] = v
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, p := range proofs {
		r.Header.Add("DPoP", p)
	}
	return as.send(t, r)
}

func fetchJWKS(t *testing.T, as *testAS) jose.JSONWebKeySet {
	t.Helper()
	resp, err := http.Get(as.srv.URL + authorization.JWKSPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var set jose.JSONWebKeySet
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		t.Fatal(err)
	}
	return set
}

// verifyAccessToken verifies token with the published JWKS, as a resource
// server does, and returns its header and claims.
func verifyAccessToken(t *testing.T, as *testAS, token string) (jose.Header, map[string]any) {
	t.Helper()
	tok, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatalf("access token is not an ES256 JWT: %v", err)
	}
	set := fetchJWKS(t, as)
	keys := set.Key(tok.Headers[0].KeyID)
	if len(keys) != 1 {
		t.Fatalf("kid %q is not in the JWKS", tok.Headers[0].KeyID)
	}
	var claims map[string]any
	if err := tok.Claims(keys[0].Key, &claims); err != nil {
		t.Fatalf("access token does not verify with the published key: %v", err)
	}
	return tok.Headers[0], claims
}
