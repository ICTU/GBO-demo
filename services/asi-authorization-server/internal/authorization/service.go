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

// Error is an OAuth error response: RFC 6749 sections 4.1.2.1 and 5.2,
// RFC 8707 and RFC 9449.
type Error struct{ Code, Description string }

func (e *Error) Error() string { return e.Code + ": " + e.Description }

// Resource is a data service of the ASIP. Name is what the user is shown:
// the authentic source the authorization is for.
type Resource struct {
	URL  string
	Name string
}

// NotRedirectableError is an authorization request that cannot be answered
// with a redirect: the client or its redirect_uri is unknown
// (RFC 6749 section 4.1.2.1).
type NotRedirectableError struct{ Description string }

func (e *NotRedirectableError) Error() string { return e.Description }

type Config struct {
	// Issuer is the public base URL of this server and the iss of its tokens.
	Issuer string
	// Resources are the ASIP's data services, one per authentic source: their
	// base URLs are the accepted resource indicators (RFC 8707). An
	// authorization names exactly one, and its tokens have it as aud.
	Resources  []Resource
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
	cfg          Config
	registered   *registeredClients
	transactions *transactionStore
	codes        *codeStore
	assertions   *replayCache
	proofReplay  *replayCache
}

func NewService(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{
		cfg:          cfg,
		registered:   &registeredClients{clients: map[string]*registeredClient{}},
		transactions: &transactionStore{m: map[string]pendingAuthorization{}},
		codes:        &codeStore{codes: map[string]authorizationCode{}},
		assertions:   newReplayCache(),
		proofReplay:  newReplayCache(),
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

// Issuer is the issuer identifier: the public base URL of this server.
func (s *Service) Issuer() string { return s.cfg.Issuer }

// --- authorization request (RFC 6749 section 4.1.1) ---------------------------

type AuthorizationRequest struct {
	ResponseType, ClientID, RedirectURI, Scope, State string
	CodeChallenge, CodeChallengeMethod                string
	Resources                                         []string
	// LoginHint may name a test person to preselect; it never identifies.
	LoginHint string
}

// AuthorizationResponse goes back to the client at RedirectURI: a Code, or
// an Err (RFC 6749 sections 4.1.2 and 4.1.2.1), with the State. A valid
// authorization request is Pending: the user decides first.
type AuthorizationResponse struct {
	RedirectURI string
	Code        string
	State       string
	Err         *Error
	Pending     *Transaction
}

// codeChallengePattern is a base64url SHA-256 digest: 43 characters.
var codeChallengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// Authorize handles an authorization request. An unknown client or
// redirect_uri is a NotRedirectableError; an invalid request goes back to the
// client; a valid one becomes a transaction for the user to decide.
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
		return fail("invalid_target", "resource must be one data service of the ASIP: "+strings.Join(s.resourceURLs(), ", "))
	}
	binding := RandomID()
	id := s.transactions.put(pendingAuthorization{
		binding:     binding,
		redirectURI: resp.RedirectURI,
		state:       req.State,
		code: authorizationCode{
			clientID:    c.ID,
			redirectURI: resp.RedirectURI,
			scope:       scope,
			challenge:   req.CodeChallenge,
			audience:    audience,
		},
		expires: s.cfg.Now().Add(TransactionLifetime),
	})
	resp.Pending = &Transaction{ID: id, Binding: binding, ClientID: c.ID, Scope: scope, Source: s.resourceName(audience), Persons: s.cfg.Identifier.Persons(), Preselected: req.LoginHint}
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

// resource resolves the resource indicators of a request (RFC 8707). bound
// is the audience an authorization code was issued for, empty for an
// authorization request. An authorization request names exactly one data
// service: with several there is no default, and a token is for one data
// service only. A token request may leave it out; then the token is for the
// data service of the code.
func (s *Service) resource(indicators []string, bound string) (string, bool) {
	if bound != "" {
		switch {
		case len(indicators) == 0:
			return bound, true
		case len(indicators) == 1 && indicators[0] == bound:
			return bound, true
		default:
			return "", false
		}
	}
	if len(indicators) != 1 || !slices.Contains(s.resourceURLs(), indicators[0]) {
		return "", false
	}
	return indicators[0], true
}

func (s *Service) resourceURLs() []string {
	urls := make([]string, 0, len(s.cfg.Resources))
	for _, r := range s.cfg.Resources {
		urls = append(urls, r.URL)
	}
	return urls
}

func (s *Service) resourceName(url string) string {
	for _, r := range s.cfg.Resources {
		if r.URL == url {
			return r.Name
		}
	}
	return ""
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
	token, err := s.cfg.Tokens.issue(c.ID, code.audience, code.scope, code.identity, jkt, now)
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
	clientID, redirectURI, scope, challenge, audience string
	identity                                          Identity
	expires                                           time.Time
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
