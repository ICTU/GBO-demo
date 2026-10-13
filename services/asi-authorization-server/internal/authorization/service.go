// Package authorization is the core of the authorization server of ETSI
// TS 119 478 V1.1.1 clause 6.1.3 (I4 Authorize): the authorization code flow
// of RFC 6749 section 4.1 with PKCE (REQ-AZSP-6.1.3.1-04), private_key_jwt
// client authentication (REQ-AZSP-6.1.3.1-05), DPoP-bound access tokens
// (REQ-AZSP-6.1.3.1-06) and JWT access tokens (REQ-AZSP-6.1.3.1-08). It knows
// nothing of HTTP; httpapi translates requests and responses.
package authorization

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// The endpoints, relative to the issuer. They are part of the metadata, and
// the token endpoint is the audience of client assertions and the htu of
// DPoP proofs.
const (
	MetadataPath  = "/.well-known/oauth-authorization-server"
	JWKSPath      = "/jwks.json"
	AuthorizePath = "/authorize"
	TokenPath     = "/token"
	// RegisterPath is the client registration endpoint (RFC 7591).
	RegisterPath = "/register"

	// AuthorizationCodeLifetime: codes are single-use and short-lived.
	AuthorizationCodeLifetime = 60 * time.Second
)

// OperationScopes are the scopes of ETSI TS 119 478 clause 6.1.3.2: one per
// operation of the ASIP.
var OperationScopes = []string{"verify", "retrieve"}

// Client is a registered client.
type Client struct {
	ID           string
	RedirectURIs []string
	Scopes       map[string]bool
	Keys         jose.JSONWebKeySet
}

// Clients is the client registry.
type Clients interface {
	Client(id string) (*Client, bool)
}

// Identifier identifies the user during the authorization request
// (REQ-AZSP-6.1.3.1-02) and returns the BSN. It is the seam where slice 3
// puts the test-person chooser and slice 7 the OpenID4VP PID disclosure.
type Identifier interface {
	Identify(loginHint string) (bsn string, ok bool)
}

// Error is an OAuth error response: RFC 6749 sections 4.1.2.1 and 5.2,
// RFC 8707 and RFC 9449.
type Error struct{ Code, Description string }

func (e *Error) Error() string { return e.Code + ": " + e.Description }

// NotRedirectableError is an authorization request that cannot be answered
// with a redirect: the client or its redirect_uri is unknown
// (RFC 6749 section 4.1.2.1).
type NotRedirectableError struct{ Description string }

func (e *NotRedirectableError) Error() string { return e.Description }

type Config struct {
	// Issuer is the public base URL of this server and the iss of its tokens.
	Issuer string
	// Resource is the ASIP base URL: the default and only accepted resource
	// indicator (RFC 8707), and the aud of every token.
	Resource   string
	Clients    Clients
	Identifier Identifier
	Tokens     *TokenIssuer
	// Register is the register of wallet-relying parties; TrustAnchors are
	// the roots the certificates of registering clients must chain to.
	Register     Register
	TrustAnchors *x509.CertPool
	Now          func() time.Time
}

type Service struct {
	cfg         Config
	registered  *registeredClients
	codes       *codeStore
	assertions  *replayCache
	proofReplay *replayCache
}

func NewService(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{
		cfg:         cfg,
		registered:  &registeredClients{clients: map[string]*registeredClient{}},
		codes:       &codeStore{codes: map[string]authorizationCode{}},
		assertions:  newReplayCache(),
		proofReplay: newReplayCache(),
	}
}

// --- metadata (RFC 8414) -----------------------------------------------------

func (s *Service) Metadata() map[string]any {
	return map[string]any{
		"issuer":                                s.cfg.Issuer,
		"authorization_endpoint":                s.cfg.Issuer + AuthorizePath,
		"token_endpoint":                        s.cfg.Issuer + TokenPath,
		"jwks_uri":                              s.cfg.Issuer + JWKSPath,
		"registration_endpoint":                 s.cfg.Issuer + RegisterPath,
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"scopes_supported":                      OperationScopes,
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"private_key_jwt"},
		"token_endpoint_auth_signing_alg_values_supported": clientAssertionAlgorithms,
		"dpop_signing_alg_values_supported":                dpopAlgorithms,
	}
}

func (s *Service) JWKS() jose.JSONWebKeySet { return s.cfg.Tokens.jwks }

// --- authorization request (RFC 6749 section 4.1.1) ---------------------------

type AuthorizationRequest struct {
	ResponseType, ClientID, RedirectURI, Scope, State string
	CodeChallenge, CodeChallengeMethod                string
	Resources                                         []string
	// LoginHint names a test person for the identification stub.
	LoginHint string
}

// AuthorizationResponse goes back to the client at RedirectURI: a Code, or
// an Err (RFC 6749 sections 4.1.2 and 4.1.2.1), with the State.
type AuthorizationResponse struct {
	RedirectURI string
	Code        string
	State       string
	Err         *Error
}

// codeChallengePattern is a base64url SHA-256 digest: 43 characters.
var codeChallengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// Authorize handles an authorization request. An unknown client or
// redirect_uri is a NotRedirectableError; every other outcome goes back to
// the client.
func (s *Service) Authorize(req AuthorizationRequest) (AuthorizationResponse, error) {
	c, ok := s.client(req.ClientID)
	if !ok {
		return AuthorizationResponse{}, &NotRedirectableError{Description: "unknown client_id"}
	}
	// Redirect only to the registered value, never to the one in the request.
	i := slices.Index(c.RedirectURIs, req.RedirectURI)
	if i < 0 {
		return AuthorizationResponse{}, &NotRedirectableError{Description: "redirect_uri is not registered for this client"}
	}
	resp := AuthorizationResponse{RedirectURI: c.RedirectURIs[i], State: req.State}
	fail := func(code, description string) (AuthorizationResponse, error) {
		resp.Err = &Error{Code: code, Description: description}
		return resp, nil
	}
	if req.ResponseType != "code" {
		return fail("unsupported_response_type", "response_type must be code")
	}
	scope, reason := c.grantScope(req.Scope)
	if reason != "" {
		return fail("invalid_scope", reason)
	}
	if req.CodeChallengeMethod != "S256" || !codeChallengePattern.MatchString(req.CodeChallenge) {
		return fail("invalid_request", "PKCE is required: code_challenge_method S256 with a SHA-256 code_challenge")
	}
	audience, ok := s.resource(req.Resources, "")
	if !ok {
		return fail("invalid_target", "resource must be "+s.cfg.Resource)
	}
	bsn, ok := s.cfg.Identifier.Identify(req.LoginHint)
	if !ok {
		return fail("access_denied", "the user could not be identified")
	}
	resp.Code = s.codes.put(authorizationCode{
		clientID:    c.ID,
		redirectURI: resp.RedirectURI,
		scope:       scope,
		challenge:   req.CodeChallenge,
		audience:    audience,
		bsn:         bsn,
		expires:     s.cfg.Now().Add(AuthorizationCodeLifetime),
	})
	return resp, nil
}

// grantScope returns the requested scope in canonical order, or the reason it
// cannot be granted.
func (c *Client) grantScope(requested string) (string, string) {
	want := map[string]bool{}
	for _, sc := range strings.Fields(requested) {
		if !c.Scopes[sc] {
			return "", "scope " + sc + " is not available to this client"
		}
		want[sc] = true
	}
	var granted []string
	for _, sc := range OperationScopes {
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
func (s *Service) resource(indicators []string, bound string) (string, bool) {
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

// --- token request (RFC 6749 section 4.1.3) ------------------------------------

type TokenRequest struct {
	GrantType, Code, RedirectURI, CodeVerifier string
	Resources                                  []string
	// Client authentication: private_key_jwt only. ClientSecret and
	// BasicAuth record an attempt at another method.
	ClientID, ClientAssertionType, ClientAssertion string
	ClientSecret                                   string
	BasicAuth                                      bool
	// DPoP are the DPoP proof headers of the request.
	DPoP []string
}

type TokenResponse struct {
	AccessToken string
	TokenType   string
	ExpiresIn   int
	Scope       string
}

// Token handles a token request.
func (s *Service) Token(req TokenRequest) (TokenResponse, *Error) {
	now := s.cfg.Now()
	c, err := s.authenticateClient(req, now)
	if err != nil {
		return TokenResponse{}, &Error{Code: "invalid_client", Description: err.Error()}
	}
	if req.GrantType != "authorization_code" {
		return TokenResponse{}, &Error{Code: "unsupported_grant_type", Description: "grant_type must be authorization_code"}
	}
	jkt, err := verifyDPoPProof(req.DPoP, "POST", s.cfg.Issuer+TokenPath, now, s.proofReplay)
	if err != nil {
		return TokenResponse{}, &Error{Code: "invalid_dpop_proof", Description: err.Error()}
	}
	// take removes the code: a code is redeemed at most once, also when the
	// attempt fails.
	code, ok := s.codes.take(req.Code)
	switch {
	case !ok || now.After(code.expires):
		return TokenResponse{}, &Error{Code: "invalid_grant", Description: "the authorization code is unknown, used or expired"}
	case code.clientID != c.ID || code.redirectURI != req.RedirectURI:
		return TokenResponse{}, &Error{Code: "invalid_grant", Description: "the authorization code was issued to another client or redirect_uri"}
	case !pkceMatches(code.challenge, req.CodeVerifier):
		return TokenResponse{}, &Error{Code: "invalid_grant", Description: "code_verifier does not match the code_challenge"}
	}
	if _, ok := s.resource(req.Resources, code.audience); !ok {
		return TokenResponse{}, &Error{Code: "invalid_target", Description: "resource must be " + code.audience}
	}
	token, err := s.cfg.Tokens.issue(c.ID, code.audience, code.scope, code.bsn, jkt, now)
	if err != nil {
		return TokenResponse{}, &Error{Code: "server_error", Description: "could not issue the access token"}
	}
	return TokenResponse{AccessToken: token, TokenType: "DPoP", ExpiresIn: int(AccessTokenLifetime.Seconds()), Scope: code.scope}, nil
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
	code := RandomID()
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
