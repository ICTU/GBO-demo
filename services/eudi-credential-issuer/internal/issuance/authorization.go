package issuance

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"gbo-demo/oid4vc-kit/dpop"
	"gbo-demo/oid4vc-kit/oid4vci"
)

// The authorization code flow of OpenID4VCI 1.0 with a login at GBO's
// authorization server (#614, phase 3, B15-B25). The issuer is the wallet's
// authorization server: a pushed authorization request (RFC 9126, required
// by HAIP 1.0 through FAPI 2.0), the authorization request, which sends the
// browser to GBO's authorization server, the login callback, which asks the
// source and hands the wallet a code, and the token request with that code.
// It is offered on a KindEUDI profile with wallet redirect URIs and a Login.

const (
	// GrantAuthorizationCode is the grant of the token request with a code.
	GrantAuthorizationCode = "authorization_code"
	// RequestURIPrefix starts every request_uri (RFC 9126 section 2.2).
	RequestURIPrefix = "urn:ietf:params:oauth:request_uri:"

	// PushedRequestLifetime is the lifetime of a request_uri.
	PushedRequestLifetime = 60 * time.Second
	// LoginLifetime bounds the citizen's login at the authorization server.
	LoginLifetime = 10 * time.Minute
	// CodeLifetime is the lifetime of an authorization code.
	CodeLifetime = 60 * time.Second

	pendingPushed = "par"
	pendingLogin  = "login"
)

// ErrInvalidAuthorization is an authorization request or login return that
// cannot be answered to the wallet: no pushed request, an unknown, used or
// expired one, another client's, or an unknown login state. The browser gets
// an error page, never a redirect.
var ErrInvalidAuthorization = errors.New("the authorization request is unknown, used or expired")

// offersLogin says whether profile p offers the authorization code flow.
func (s *Service) offersLogin(p Profile) bool {
	return s.cfg.Login != nil && p.Kind == KindEUDI && len(p.WalletRedirectURIs) > 0
}

// resolveLogin resolves t for the authorization code flow: a target whose
// profile does not offer it is unknown.
func (s *Service) resolveLogin(t Target) (*provider, Profile, error) {
	prov, p, err := s.resolve(t)
	if err != nil {
		return nil, Profile{}, err
	}
	if !s.offersLogin(p) {
		return nil, Profile{}, ErrUnknownProfile
	}
	return prov, p, nil
}

// PushedAuthorizationRequest is a wallet's pushed authorization request with
// the headers that authenticate it.
type PushedAuthorizationRequest struct {
	Target                              Target
	ResponseType, ClientID, RedirectURI string
	State                               string
	CodeChallenge, CodeChallengeMethod  string
	// IssuerState is the issuer_state of the credential offer: the key of
	// the source offer (F4).
	IssuerState          string
	Scope                string
	AuthorizationDetails string
	ClientAttestation    string
	ClientAttestationPoP string
	DPoP                 []string
}

// PushedAuthorizationResponse is the answer to a pushed request.
type PushedAuthorizationResponse struct {
	RequestURI string `json:"request_uri"`
	ExpiresIn  int64  `json:"expires_in"`
}

// pushedRequest is what the issuer keeps of a pushed request until the
// wallet opens it, and of the login that follows.
type pushedRequest struct {
	Provider      string `json:"provider"`
	Profile       string `json:"profile"`
	ClientID      string `json:"client_id"`
	RedirectURI   string `json:"redirect_uri"`
	State         string `json:"state"`
	CodeChallenge string `json:"code_challenge"`
	DPoPJKT       string `json:"dpop_jkt,omitempty"`
	Offer         string `json:"offer"`
}

// login is a login in progress: the pushed request and the issuer's own PKCE
// verifier towards the authorization server.
type login struct {
	Request  pushedRequest `json:"request"`
	Verifier string        `json:"verifier"`
}

// PushAuthorization handles a pushed authorization request. The wallet
// authenticates as at the token endpoint; a DPoP proof binds the code to
// its key.
func (s *Service) PushAuthorization(ctx context.Context, req PushedAuthorizationRequest) (PushedAuthorizationResponse, error) {
	_, p, err := s.resolveLogin(req.Target)
	if err != nil {
		return PushedAuthorizationResponse{}, err
	}
	now := s.now()
	id := s.Identifier(req.Target)
	att, err := s.authenticateWallet(ctx, req.Target, p, req.ClientAttestation, req.ClientAttestationPoP, req.ClientID, now)
	if err != nil {
		return PushedAuthorizationResponse{}, err
	}
	switch {
	case req.ClientID == "":
		return PushedAuthorizationResponse{}, refuse(oid4vci.ErrInvalidRequest, "client_id is required")
	case req.ResponseType != "code":
		return PushedAuthorizationResponse{}, refuse(oid4vci.ErrInvalidRequest, "response_type must be code")
	case req.CodeChallengeMethod != "S256" || !codeChallengePattern(req.CodeChallenge):
		return PushedAuthorizationResponse{}, refuse(oid4vci.ErrInvalidRequest, "PKCE is required: code_challenge_method S256 with a SHA-256 code_challenge")
	case !slices.Contains(p.WalletRedirectURIs, req.RedirectURI):
		return PushedAuthorizationResponse{}, refuse(oid4vci.ErrInvalidRequest, "redirect_uri is not a redirect URI of a wallet this issuer trusts")
	}
	offer, err := s.requestedOffer(req.Target, req)
	if err != nil {
		return PushedAuthorizationResponse{}, err
	}
	pushed := pushedRequest{Provider: req.Target.Provider, Profile: p.Name, ClientID: att.ClientID, RedirectURI: req.RedirectURI,
		State: req.State, CodeChallenge: req.CodeChallenge, Offer: offer.Key}
	if len(req.DPoP) > 0 {
		proof, err := dpop.Verify(req.DPoP, dpop.Expected{Method: "POST", URL: id + "/par"}, now)
		if err != nil {
			return PushedAuthorizationResponse{}, refuse(oid4vci.ErrInvalidDPoPProof, err.Error())
		}
		if ok, err := s.onetime.FirstUse(ctx, kindDPoP, proof.JTI, proof.IAT.Add(dpop.Window), now); err != nil {
			return PushedAuthorizationResponse{}, err
		} else if !ok {
			return PushedAuthorizationResponse{}, refuse(oid4vci.ErrInvalidDPoPProof, "the DPoP proof has been used before")
		}
		pushed.DPoPJKT = proof.JKT
	}
	token, err := randomToken()
	if err != nil {
		return PushedAuthorizationResponse{}, err
	}
	if err := s.putPending(ctx, pendingPushed, token, pushed, now.Add(PushedRequestLifetime)); err != nil {
		return PushedAuthorizationResponse{}, err
	}
	return PushedAuthorizationResponse{RequestURI: RequestURIPrefix + token, ExpiresIn: int64(PushedRequestLifetime.Seconds())}, nil
}

// ErrInvalidScope refuses a wallet-initiated request whose credential type
// names no single offer of this issuer (RFC 6749 section 4.1.2.1).
const ErrInvalidScope = "invalid_scope"

// requestedOffer returns the source offer a pushed request is for: the one
// its issuer_state names (F4), or, when the wallet started at the issuer
// (F5), the only offer of the one credential configuration its scope or
// authorization_details name (HAIP 1.0 section 4.3).
func (s *Service) requestedOffer(t Target, req PushedAuthorizationRequest) (DisclosureOffer, error) {
	if req.IssuerState != "" {
		o, ok := s.disclosureOffers[t.Provider+"/"+req.IssuerState]
		if !ok {
			return DisclosureOffer{}, refuse(oid4vci.ErrInvalidRequest, "issuer_state names no offer of this issuer")
		}
		return o, nil
	}
	id, err := s.requestedConfiguration(t, req)
	if err != nil {
		return DisclosureOffer{}, err
	}
	var found []DisclosureOffer
	for _, o := range s.disclosureOffers {
		if o.Provider == t.Provider && len(o.ConfigurationIDs) == 1 && o.ConfigurationIDs[0] == id {
			found = append(found, o)
		}
	}
	if len(found) != 1 {
		return DisclosureOffer{}, refuse(ErrInvalidScope, "the credential type does not name a single offer of this issuer; start from its credential offer")
	}
	return found[0], nil
}

// requestedConfiguration returns the one credential configuration of this
// provider that scope and authorization_details name (OpenID4VCI 1.0 section
// 5.1.2): unknown scope values are ignored, the two are read each on their
// own, and details that do not conform are refused (RFC 9396 section 5).
// One login fetches one source offer, so they must name one configuration.
func (s *Service) requestedConfiguration(t Target, req PushedAuthorizationRequest) (string, error) {
	var ids []string
	for _, sc := range strings.Fields(req.Scope) {
		for _, c := range s.cfg.Configurations {
			if c.Provider == t.Provider && c.Scope == sc {
				ids = append(ids, c.ID)
			}
		}
	}
	if req.AuthorizationDetails != "" {
		var details []oid4vci.AuthorizationDetail
		if err := json.Unmarshal([]byte(req.AuthorizationDetails), &details); err != nil {
			return "", refuse(oid4vci.ErrInvalidAuthorizationDetails, "authorization_details is not a JSON array of authorization details")
		}
		for _, d := range details {
			if d.Type != oid4vci.AuthorizationDetailsType {
				return "", refuse(oid4vci.ErrInvalidAuthorizationDetails, "authorization details of type "+d.Type+" are not supported")
			}
			c, ok := s.configs[d.CredentialConfigurationID]
			if !ok || c.Provider != t.Provider {
				return "", refuse(oid4vci.ErrInvalidAuthorizationDetails, "authorization_details names a credential configuration this issuer does not have")
			}
			ids = append(ids, c.ID)
		}
	}
	switch ids = uniq(ids); len(ids) {
	case 0:
		return "", refuse(ErrInvalidScope, "the request names no credential type of this issuer")
	case 1:
		return ids[0], nil
	}
	return "", refuse(ErrInvalidScope, "the request must name exactly one credential type")
}

// Authorize handles the authorization request the wallet opens in the
// browser: it takes the pushed request and returns where the browser goes to
// log in. A request without a valid request_uri of this client is
// ErrInvalidAuthorization. When the login cannot be started, the citizen
// gets a WalletReturn page.
func (s *Service) Authorize(ctx context.Context, t Target, clientID, requestURI string) (string, *WalletReturn, error) {
	if _, _, err := s.resolveLogin(t); err != nil {
		return "", nil, err
	}
	now := s.now()
	token, ok := cutPrefix(requestURI, RequestURIPrefix)
	if !ok {
		return "", nil, ErrInvalidAuthorization
	}
	var pushed pushedRequest
	if err := s.takePending(ctx, pendingPushed, token, now, &pushed); err != nil {
		return "", nil, err
	}
	if pushed.Provider != t.Provider || pushed.Profile != t.Profile || pushed.ClientID != clientID {
		return "", nil, ErrInvalidAuthorization
	}
	state, err := randomToken()
	if err != nil {
		return "", nil, err
	}
	verifier, err := randomToken()
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if err := s.putPending(ctx, pendingLogin, state, login{Request: pushed, Verifier: verifier}, now.Add(LoginLifetime)); err != nil {
		return "", nil, err
	}
	target, err := s.cfg.Login.AuthorizationURL(ctx, t.Provider, s.Identifier(t), state, challenge)
	if errors.Is(err, ErrLoginUnavailable) {
		return "", s.walletReturn(t, pushed, ReturnUnavailable, "temporarily_unavailable"), nil
	}
	if err != nil {
		return "", nil, err
	}
	return target, nil, nil
}

// LoginReturn is the browser's return from the login: the code and state, or
// the error the authorization server sent.
type LoginReturn struct {
	Code, State, Error string
}

// The pages a citizen may see instead of being sent back to the wallet at
// once.
const (
	// ReturnRedirect sends the browser to the wallet now.
	ReturnRedirect = ""
	// ReturnNoData: the source has no data for the citizen (K6).
	ReturnNoData = "no_data"
	// ReturnUnavailable: the source or the login is unavailable (K7).
	ReturnUnavailable = "unavailable"
)

// WalletReturn is the authorization response to the wallet (RFC 6749
// section 4.1.2, with iss of RFC 9207): Location carries the code or the
// error. With a Page the citizen first sees that page, whose button follows
// Location.
type WalletReturn struct {
	Location     string
	Page         string
	Organization string
	Attestation  string
}

// Callback handles the browser's return from the login: it redeems the code
// at the authorization server, asks the source with the BSN from the login
// and returns the code for the wallet. An unknown or used state, or a login
// whose token does not hold, is ErrInvalidAuthorization.
func (s *Service) Callback(ctx context.Context, t Target, ret LoginReturn) (*WalletReturn, error) {
	_, p, err := s.resolveLogin(t)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var l login
	if err := s.takePending(ctx, pendingLogin, ret.State, now, &l); err != nil {
		return nil, err
	}
	pushed := l.Request
	if pushed.Provider != t.Provider || pushed.Profile != t.Profile {
		return nil, ErrInvalidAuthorization
	}
	switch {
	case ret.Error == "access_denied":
		return s.walletReturn(t, pushed, ReturnRedirect, "access_denied"), nil
	case ret.Error != "":
		return s.walletReturn(t, pushed, ReturnUnavailable, "temporarily_unavailable"), nil
	}
	bsn, err := s.cfg.Login.Exchange(ctx, t.Provider, s.Identifier(t), ret.Code, l.Verifier)
	if errors.Is(err, ErrLoginUnavailable) {
		return s.walletReturn(t, pushed, ReturnUnavailable, "temporarily_unavailable"), nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidAuthorization, err)
	}
	o, ok := s.disclosureOffers[t.Provider+"/"+pushed.Offer]
	if !ok {
		return nil, fmt.Errorf("offer %q of the request is not configured here", pushed.Offer)
	}
	binding := &CodeBinding{ClientID: pushed.ClientID, RedirectURI: pushed.RedirectURI, CodeChallenge: pushed.CodeChallenge, DPoPJKT: pushed.DPoPJKT}
	code, err := s.createSession(ctx, t, p, o.ConfigurationIDs, "", CodeLifetime, sessionOptions{binding: binding}, func(id string) ([]SourceDocument, error) {
		return s.source.FetchLogin(ctx, id, bsn, o.Parameters)
	})
	switch {
	case errors.Is(err, ErrNoData):
		r := s.walletReturn(t, pushed, ReturnNoData, "access_denied")
		r.Organization, r.Attestation = s.offerNames(t, o)
		return r, nil
	case errors.Is(err, ErrSourceUnavailable):
		// Deferred issuance (V2): the wallet gets its code; the credential
		// follows when the source answers.
		if code, err = s.createPendingSession(ctx, t, p, o, binding, bsn); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	}
	q := url.Values{"code": {code}, "iss": {s.Identifier(t)}}
	if pushed.State != "" {
		q.Set("state", pushed.State)
	}
	return &WalletReturn{Location: walletLocation(pushed.RedirectURI, q)}, nil
}

// walletReturn is an error response to the wallet, shown first on page.
func (s *Service) walletReturn(t Target, pushed pushedRequest, page, code string) *WalletReturn {
	q := url.Values{"error": {code}, "iss": {s.Identifier(t)}}
	if pushed.State != "" {
		q.Set("state", pushed.State)
	}
	return &WalletReturn{Location: walletLocation(pushed.RedirectURI, q), Page: page}
}

// offerNames are the organisation and the attestation of an offer, as a page
// names them.
func (s *Service) offerNames(t Target, o DisclosureOffer) (string, string) {
	var organization, attestation string
	if prov, ok := s.providers[t.Provider]; ok && len(prov.AccessChain) > 0 {
		organization = issuerName(prov.AccessChain[0])
	}
	if len(o.ConfigurationIDs) > 0 {
		attestation = displayName(s.configs[o.ConfigurationIDs[0]].Display)
	}
	return organization, attestation
}

// walletLocation adds q to the wallet's redirect URI.
func walletLocation(redirectURI string, q url.Values) string {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return redirectURI
	}
	merged := u.Query()
	for k, v := range q {
		merged[k] = v
	}
	u.RawQuery = merged.Encode()
	return u.String()
}

func (s *Service) putPending(ctx context.Context, kind, secret string, v any, expires time.Time) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.sessions.PutPending(ctx, PendingAuthorization{Kind: kind, ID: hash(secret), Payload: payload, ExpiresAt: expires})
}

// takePending takes the pending authorization of kind for secret into v; an
// unknown, used or expired one is ErrInvalidAuthorization.
func (s *Service) takePending(ctx context.Context, kind, secret string, now time.Time, v any) error {
	if secret == "" {
		return ErrInvalidAuthorization
	}
	p, err := s.sessions.TakePending(ctx, kind, hash(secret), now)
	if errors.Is(err, ErrNotFound) {
		return ErrInvalidAuthorization
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(p.Payload, v)
}

// codeChallengePattern is a base64url SHA-256 digest: 43 characters.
func codeChallengePattern(c string) bool {
	if len(c) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(c)
	return err == nil
}

// pkceMatches checks the S256 transformation of RFC 7636 section 4.6.
func pkceMatches(challenge, verifier string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) <= len(prefix) || s[:len(prefix)] != prefix {
		return "", false
	}
	return s[len(prefix):], true
}

// LoginLink is the credential offer of a source offer for the authorization
// code flow, as a link: issuer_state names the offer. Empty when the profile
// does not offer the flow.
func (s *Service) LoginLink(t Target, offerKey string) string {
	if _, _, err := s.resolveLogin(t); err != nil {
		return ""
	}
	o, ok := s.disclosureOffers[t.Provider+"/"+offerKey]
	if !ok {
		return ""
	}
	raw, err := json.Marshal(oid4vci.CredentialOffer{
		CredentialIssuer:           s.Identifier(t),
		CredentialConfigurationIDs: o.ConfigurationIDs,
		Grants:                     map[string]any{GrantAuthorizationCode: map[string]string{"issuer_state": o.Key}},
	})
	if err != nil {
		return ""
	}
	return "openid-credential-offer://?" + url.Values{"credential_offer": {string(raw)}}.Encode()
}
