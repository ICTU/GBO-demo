package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// The authorization server of ETSI TS 119 478 V1.1.1 clause 6.1.3 (I4
// Authorize): the authorization code flow of RFC 6749 section 4.1 with PKCE
// (REQ-AZSP-6.1.3.1-04), private_key_jwt client authentication
// (REQ-AZSP-6.1.3.1-05), DPoP-bound access tokens (REQ-AZSP-6.1.3.1-06) and
// JWT access tokens (REQ-AZSP-6.1.3.1-08).

const (
	metadataPath  = "/.well-known/oauth-authorization-server"
	jwksPath      = "/jwks.json"
	authorizePath = "/authorize"
	tokenPath     = "/token"

	// authorizationCodeLifetime: codes are single-use and short-lived.
	authorizationCodeLifetime = 60 * time.Second
)

// operationScopes are the scopes of ETSI TS 119 478 clause 6.1.3.2: one per
// operation of the ASIP.
var operationScopes = []string{"verify", "retrieve"}

type serverConfig struct {
	// Issuer is the public base URL of this server and the iss of its tokens.
	Issuer string
	// Resource is the ASIP base URL: the default and only accepted resource
	// indicator (RFC 8707), and the aud of every token.
	Resource   string
	Clients    map[string]*client
	Identifier identifier
	Tokens     *tokenIssuer
	Now        func() time.Time
}

type server struct {
	cfg         serverConfig
	codes       *codeStore
	assertions  *replayCache
	proofReplay *replayCache
}

func newServer(cfg serverConfig) http.Handler {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &server{cfg: cfg, codes: &codeStore{codes: map[string]authorizationCode{}}, assertions: newReplayCache(), proofReplay: newReplayCache()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+metadataPath, s.metadata)
	mux.HandleFunc("GET "+jwksPath, s.jwks)
	mux.HandleFunc("GET "+authorizePath, s.authorize)
	mux.HandleFunc("POST "+tokenPath, s.token)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

// --- metadata (RFC 8414) -----------------------------------------------------

func (s *server) metadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                s.cfg.Issuer,
		"authorization_endpoint":                s.cfg.Issuer + authorizePath,
		"token_endpoint":                        s.cfg.Issuer + tokenPath,
		"jwks_uri":                              s.cfg.Issuer + jwksPath,
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"scopes_supported":                      operationScopes,
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"private_key_jwt"},
		"token_endpoint_auth_signing_alg_values_supported": clientAssertionAlgorithms,
		"dpop_signing_alg_values_supported":                dpopAlgorithms,
	})
}

func (s *server) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.cfg.Tokens.jwks)
}

// --- authorization endpoint (RFC 6749 section 4.1.1) --------------------------

// codeChallengePattern is a base64url SHA-256 digest: 43 characters.
var codeChallengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func (s *server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, ok := s.cfg.Clients[q.Get("client_id")]
	if !ok {
		authorizeErrorPage(w, "unknown client_id")
		return
	}
	// Redirect only to the registered value, never to the one in the request.
	i := slices.Index(c.RedirectURIs, q.Get("redirect_uri"))
	if i < 0 {
		authorizeErrorPage(w, "redirect_uri is not registered for this client")
		return
	}
	redirectURI := c.RedirectURIs[i]
	// From here on errors go back to the client (RFC 6749 section 4.1.2.1).
	fail := func(code, description string) {
		redirectWith(w, r, redirectURI, url.Values{"error": {code}, "error_description": {description}, "state": {q.Get("state")}})
	}
	if q.Get("response_type") != "code" {
		fail("unsupported_response_type", "response_type must be code")
		return
	}
	scope, err := c.grantScope(q.Get("scope"))
	if err != "" {
		fail("invalid_scope", err)
		return
	}
	if q.Get("code_challenge_method") != "S256" || !codeChallengePattern.MatchString(q.Get("code_challenge")) {
		fail("invalid_request", "PKCE is required: code_challenge_method S256 with a SHA-256 code_challenge")
		return
	}
	audience, ok := s.resource(q["resource"], "")
	if !ok {
		fail("invalid_target", "resource must be "+s.cfg.Resource)
		return
	}
	bsn, ok := s.cfg.Identifier.identify(r)
	if !ok {
		fail("access_denied", "the user could not be identified")
		return
	}
	code := s.codes.put(authorizationCode{
		clientID:    c.ID,
		redirectURI: redirectURI,
		scope:       scope,
		challenge:   q.Get("code_challenge"),
		audience:    audience,
		bsn:         bsn,
		expires:     s.cfg.Now().Add(authorizationCodeLifetime),
	})
	redirectWith(w, r, redirectURI, url.Values{"code": {code}, "state": {q.Get("state")}})
}

// grantScope returns the requested scope in canonical order, or the reason it
// cannot be granted.
func (c *client) grantScope(requested string) (string, string) {
	want := map[string]bool{}
	for _, sc := range strings.Fields(requested) {
		if !c.Scopes[sc] {
			return "", "scope " + sc + " is not available to this client"
		}
		want[sc] = true
	}
	var granted []string
	for _, sc := range operationScopes {
		if want[sc] {
			granted = append(granted, sc)
		}
	}
	if len(granted) == 0 {
		return "", "scope must contain verify, retrieve or both"
	}
	return strings.Join(granted, " "), ""
}

// resource resolves the resource indicators of a request (RFC 8707). Without
// one, the token is for the ASIP (RFC 9068 section 3). bound is the audience
// an authorization code was issued for, if any.
func (s *server) resource(indicators []string, bound string) (string, bool) {
	want := s.cfg.Resource
	if bound != "" {
		want = bound
	}
	switch {
	case len(indicators) == 0:
		return want, true
	case len(indicators) == 1 && indicators[0] == want:
		return want, true
	default:
		return "", false
	}
}

// --- token endpoint (RFC 6749 section 4.1.3) ----------------------------------

func (s *server) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request", "the request body is not a form")
		return
	}
	now := s.cfg.Now()
	c, err := s.authenticateClient(r, now)
	if err != nil {
		tokenError(w, http.StatusUnauthorized, "invalid_client", err.Error())
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code")
		return
	}
	jkt, err := verifyDPoPProof(r.Header.Values("DPoP"), http.MethodPost, s.cfg.Issuer+tokenPath, now, s.proofReplay)
	if err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_dpop_proof", err.Error())
		return
	}
	// take removes the code: a code is redeemed at most once, also when the
	// attempt fails.
	code, ok := s.codes.take(r.PostForm.Get("code"))
	switch {
	case !ok || now.After(code.expires):
		tokenError(w, http.StatusBadRequest, "invalid_grant", "the authorization code is unknown, used or expired")
		return
	case code.clientID != c.ID || code.redirectURI != r.PostForm.Get("redirect_uri"):
		tokenError(w, http.StatusBadRequest, "invalid_grant", "the authorization code was issued to another client or redirect_uri")
		return
	case !pkceMatches(code.challenge, r.PostForm.Get("code_verifier")):
		tokenError(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match the code_challenge")
		return
	}
	if _, ok := s.resource(r.PostForm["resource"], code.audience); !ok {
		tokenError(w, http.StatusBadRequest, "invalid_target", "resource must be "+code.audience)
		return
	}
	token, err := s.cfg.Tokens.issue(c.ID, code.audience, code.scope, code.bsn, jkt, now)
	if err != nil {
		tokenError(w, http.StatusInternalServerError, "server_error", "could not issue the access token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "DPoP",
		"expires_in":   int(accessTokenLifetime.Seconds()),
		"scope":        code.scope,
	})
}

// codeVerifierPattern is RFC 7636 section 4.1: 43 to 128 unreserved characters.
var codeVerifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

// pkceMatches checks the S256 transformation of RFC 7636 section 4.6.
func pkceMatches(challenge, verifier string) bool {
	if !codeVerifierPattern.MatchString(verifier) {
		return false
	}
	digest := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(digest[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}

// --- authorization codes -------------------------------------------------------

type authorizationCode struct {
	clientID, redirectURI, scope, challenge, audience, bsn string
	expires                                                time.Time
}

type codeStore struct {
	mu    sync.Mutex
	codes map[string]authorizationCode
}

func (s *codeStore) put(c authorizationCode) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, old := range s.codes {
		if time.Now().After(old.expires) {
			delete(s.codes, k)
		}
	}
	code := randomID()
	s.codes[code] = c
	return code
}

func (s *codeStore) take(code string) (authorizationCode, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.codes[code]
	delete(s.codes, code)
	return c, ok
}

// --- helpers -------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// tokenError writes an error response of RFC 6749 section 5.2.
func tokenError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

// authorizeErrorPage answers an authorization request that cannot be
// redirected safely: the client or its redirect_uri is unknown
// (RFC 6749 section 4.1.2.1).
func authorizeErrorPage(w http.ResponseWriter, description string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte("invalid_request: " + description + "\n"))
}

func redirectWith(w http.ResponseWriter, r *http.Request, redirectURI string, params url.Values) {
	u, _ := url.Parse(redirectURI)
	q := u.Query()
	for k, v := range params {
		if len(v) > 0 && v[0] != "" {
			q[k] = v
		}
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}
