// Package verifier is an OpenID4VP verifier for the EUDI wallets on
// oid4vc-kit/oid4vp, behind the part of the API of nl-wallet's
// verification_server (v0.5) that the ASI authorization server uses: a
// relying party starts a session, follows its status, and fetches the
// disclosed attributes. The wallet fetches a signed request object and
// posts an encrypted response (HAIP 1.0 section 5.1).
package verifier

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"gbo-demo/oid4vc-kit/oid4vp"
	"gbo-demo/oid4vc-kit/sdjwtvc"
	"gbo-demo/oid4vc-kit/x5c"
)

// PIDTrust is the verifier's port for current PID trust, independent of HTTP.
type PIDTrust interface {
	Load(context.Context) (x5c.Trust, error)
}

func (s *Service) pidVerifier(ctx context.Context) (oid4vp.Verifier, []string, error) {
	v := s.verifier
	akis := s.cfg.PIDTrustedAuthorities
	if s.cfg.PIDTrust != nil {
		trust, err := s.cfg.PIDTrust.Load(ctx)
		if err != nil || len(trust.Anchors) == 0 {
			return v, nil, refuse(http.StatusServiceUnavailable, "server_error", "PID trust temporarily unavailable")
		}
		v.Anchors = trust.Anchors
		if len(akis) == 0 {
			akis = trust.Authorities
		}
	}
	return v, akis, nil
}

// Status is the state of a session, as verification_server names it.
type Status string

const (
	StatusCreated   Status = "CREATED"
	StatusWaiting   Status = "WAITING_FOR_RESPONSE"
	StatusDone      Status = "DONE"
	StatusFailed    Status = "FAILED"
	StatusCancelled Status = "CANCELLED"
	StatusExpired   Status = "EXPIRED"
)

// Session types: a cross-device session ends at the verifier; a
// same-device session may return the wallet's browser to the relying
// party.
const (
	CrossDevice = "cross_device"
	SameDevice  = "same_device"
)

// SessionTokenPlaceholder is replaced by the session token in a return URL
// template.
const SessionTokenPlaceholder = "{session_token}"

// Error is a refusal with the HTTP status and the error type the API
// answers with.
type Error struct {
	Status int
	Type   string
	Detail string
}

func (e *Error) Error() string { return e.Type + ": " + e.Detail }

func refuse(status int, typ, format string, args ...any) *Error {
	return &Error{Status: status, Type: typ, Detail: fmt.Sprintf(format, args...)}
}

// ErrStatusUnavailable: the wallet's PID could not be checked against its
// status list. The session has failed; the wallet is told it was a server
// error.
var ErrStatusUnavailable = errors.New("the PID's status list is unavailable")

// StartRequest is what a relying party sends to start a session.
type StartRequest struct {
	Usecase           string        `json:"usecase"`
	DCQLQuery         *oid4vp.Query `json:"dcql_query,omitempty"`
	ReturnURLTemplate string        `json:"return_url_template,omitempty"`
}

// StatusResponse is the status of a session, with the link for the wallet
// while it has not fetched the request.
type StatusResponse struct {
	Status Status `json:"status"`
	UL     string `json:"ul,omitempty"`
}

// ResponseForm is what a wallet posts to the response_uri: the encrypted
// response, or an error with state (OpenID4VP 1.0 section 8.5).
type ResponseForm struct {
	Response string
	Error    string
	State    string
}

// Disclosed is one credential query with what was disclosed for it, in the
// shape of verification_server's disclosed attributes.
type Disclosed struct {
	ID           string        `json:"id"`
	Attestations []Attestation `json:"attestations"`
}

// Attestation is a disclosed credential: its type, and the requested
// claims only.
type Attestation struct {
	AttestationType string         `json:"attestation_type"`
	Format          string         `json:"format"`
	Attributes      map[string]any `json:"attributes"`
	IssuerURI       string         `json:"issuer_uri,omitempty"`
	CA              string         `json:"ca"`
}

type session struct {
	usecase        string
	query          oid4vp.Query
	returnTemplate string
	expires        time.Time
	status         Status
	sessionType    string
	nonce, state   string
	key            oid4vp.ResponseKey
	returnNonce    string
	disclosed      []Disclosed
	// verifying is set while a response is checked, so a second one is
	// refused; the status stays WAITING_FOR_RESPONSE meanwhile.
	verifying bool
}

// Service holds the sessions in memory.
type Service struct {
	cfg      Config
	verifier oid4vp.Verifier
	now      func() time.Time

	mu       sync.Mutex
	sessions map[string]*session
}

// New returns a verifier for cfg that fetches status lists with fetch.
func New(cfg Config, fetch oid4vp.StatusFetcher, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{cfg: cfg, verifier: oid4vp.Verifier{Anchors: cfg.PIDAnchors, FetchStatus: fetch}, now: now,
		sessions: map[string]*session{}}
}

// Start opens a session for a usecase. The query must stay within the
// usecase's registration: its PID type and claims; without a query the
// session asks for all of them. The verifier adds its trusted authorities.
// A return URL template is required when the usecase returns same-device
// sessions, and refused when it does not, as verification_server does.
func (s *Service) Start(r StartRequest) (string, error) {
	uc, ok := s.cfg.Usecases[r.Usecase]
	if !ok {
		return "", refuse(http.StatusBadRequest, "usecase", "unknown usecase %q", r.Usecase)
	}
	q, err := s.query(uc, r.DCQLQuery)
	if err != nil {
		return "", err
	}
	switch {
	case uc.SameDeviceReturn && r.ReturnURLTemplate == "":
		return "", refuse(http.StatusBadRequest, "return_url", "usecase %q needs a return URL template", r.Usecase)
	case !uc.SameDeviceReturn && r.ReturnURLTemplate != "":
		return "", refuse(http.StatusBadRequest, "return_url", "usecase %q takes no return URL template", r.Usecase)
	case r.ReturnURLTemplate != "":
		u, err := url.Parse(strings.ReplaceAll(r.ReturnURLTemplate, SessionTokenPlaceholder, "x"))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return "", refuse(http.StatusBadRequest, "return_url", "the return URL template is not an http(s) URL")
		}
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for t, old := range s.sessions {
		// Finished sessions stay one more lifetime for the relying party.
		if now.After(old.expires.Add(s.cfg.SessionTTL)) {
			delete(s.sessions, t)
		}
	}
	s.sessions[token] = &session{usecase: r.Usecase, query: q, returnTemplate: r.ReturnURLTemplate,
		expires: now.Add(s.cfg.SessionTTL), status: StatusCreated}
	return token, nil
}

// query checks a requested query against the usecase's registration.
func (s *Service) query(uc Usecase, requested *oid4vp.Query) (oid4vp.Query, error) {
	cq := oid4vp.CredentialQuery{ID: "pid", Format: sdjwtvc.Type, Meta: oid4vp.Meta{VCTValues: []string{uc.PIDType}}}
	if requested == nil {
		for _, c := range uc.Claims {
			cq.Claims = append(cq.Claims, oid4vp.ClaimQuery{Path: []string{c}})
		}
	} else {
		if len(requested.Credentials) != 1 {
			return oid4vp.Query{}, refuse(http.StatusBadRequest, "dcql_query", "exactly one credential query is supported")
		}
		r := requested.Credentials[0]
		if !queryID.MatchString(r.ID) || len(r.Claims) == 0 {
			return oid4vp.Query{}, refuse(http.StatusBadRequest, "dcql_query", "the credential query needs an id and claims")
		}
		if r.Format != sdjwtvc.Type || !slices.Equal(r.Meta.VCTValues, []string{uc.PIDType}) {
			return oid4vp.Query{}, refuse(http.StatusBadRequest, "dcql_query", "the query must ask for %s as %s", uc.PIDType, sdjwtvc.Type)
		}
		seen := map[string]bool{}
		for _, c := range r.Claims {
			if len(c.Path) != 1 || !slices.Contains(uc.Claims, c.Path[0]) || seen[c.Path[0]] {
				return oid4vp.Query{}, refuse(http.StatusBadRequest, "dcql_query", "claim %v is not in the usecase's registration", c.Path)
			}
			seen[c.Path[0]] = true
		}
		cq.ID, cq.Claims = r.ID, r.Claims
	}
	if len(s.cfg.PIDTrustedAuthorities) > 0 {
		cq.TrustedAuthorities = []oid4vp.TrustedAuthority{{Type: oid4vp.TrustedAuthorityAKI, Values: s.cfg.PIDTrustedAuthorities}}
	}
	return oid4vp.Query{Credentials: []oid4vp.CredentialQuery{cq}}, nil
}

// queryID is the form OpenID4VP 1.0 section 6.1 gives a credential query id.
var queryID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// get returns the session of token, expired when its time is up.
// The caller holds the lock.
func (s *Service) get(token string) (*session, error) {
	ses, ok := s.sessions[token]
	if !ok {
		return nil, refuse(http.StatusNotFound, "unknown_session", "unknown session")
	}
	if (ses.status == StatusCreated || ses.status == StatusWaiting) && s.now().After(ses.expires) {
		ses.status = StatusExpired
	}
	return ses, nil
}

// Status reports the session's state; while it is CREATED, with the link
// for the wallet for sessionType.
func (s *Service) Status(token, sessionType string) (StatusResponse, error) {
	if sessionType != CrossDevice && sessionType != SameDevice {
		return StatusResponse{}, refuse(http.StatusBadRequest, "session_type", "session_type must be %s or %s", CrossDevice, SameDevice)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ses, err := s.get(token)
	if err != nil {
		return StatusResponse{}, err
	}
	out := StatusResponse{Status: ses.status}
	if ses.status == StatusCreated {
		uc := s.cfg.Usecases[ses.usecase]
		out.UL = "openid4vp://?" + url.Values{
			"client_id":   {uc.Signer.ClientID()},
			"request_uri": {s.requestURI(token, sessionType)},
		}.Encode()
	}
	return out, nil
}

func (s *Service) requestURI(token, sessionType string) string {
	return s.cfg.PublicURL + "/disclosure/sessions/" + url.PathEscape(token) + "/request?" + url.Values{"session_type": {sessionType}}.Encode()
}

func (s *Service) responseURI(token string) string {
	return s.cfg.PublicURL + "/disclosure/sessions/" + url.PathEscape(token) + "/response"
}

// Request hands the wallet the signed request object, once: the session
// then waits for the response, and its type is fixed.
func (s *Service) Request(token, sessionType string) (string, error) {
	return s.RequestContext(context.Background(), token, sessionType)
}

// RequestContext refreshes PID trust with the wallet request's deadline.
func (s *Service) RequestContext(ctx context.Context, token, sessionType string) (string, error) {
	if sessionType != CrossDevice && sessionType != SameDevice {
		return "", refuse(http.StatusBadRequest, "session_type", "session_type must be %s or %s", CrossDevice, SameDevice)
	}
	_, akis, err := s.pidVerifier(ctx)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ses, err := s.get(token)
	if err != nil {
		return "", err
	}
	if ses.status != StatusCreated {
		return "", refuse(http.StatusBadRequest, "session_state", "the session is %s", ses.status)
	}
	if len(akis) > 0 {
		ses.query.Credentials[0].TrustedAuthorities = []oid4vp.TrustedAuthority{{Type: oid4vp.TrustedAuthorityAKI, Values: akis}}
	}
	key, err := oid4vp.NewResponseKey()
	if err != nil {
		return "", err
	}
	var nonce, state string
	for _, v := range []*string{&nonce, &state} {
		if *v, err = randomToken(); err != nil {
			return "", err
		}
	}
	uc := s.cfg.Usecases[ses.usecase]
	request, err := uc.Signer.Sign(oid4vp.Request{
		Session:     oid4vp.Session{ClientID: uc.Signer.ClientID(), Nonce: nonce, State: state, Query: ses.query, Key: key},
		ResponseURI: s.responseURI(token),
		// The EUDI wallets read the WRPRC in verifier_info as a JWT.
		RegistrationCertificate: uc.RegistrationCertificate,
		Encoding:                oid4vp.RegistrationJWT,
		IssuedAt:                s.now(),
		Expires:                 ses.expires,
	})
	if err != nil {
		return "", err
	}
	ses.status, ses.sessionType, ses.nonce, ses.state, ses.key = StatusWaiting, sessionType, nonce, state, key
	return request, nil
}

// Respond ends the session with the wallet's answer. A verified
// presentation makes the session DONE; for a same-device session with a
// return URL the result is the redirect_uri for the wallet, which carries
// the nonce that releases the attributes (HAIP 1.0 section 5.1). A wallet
// error ends the session as CANCELLED (access_denied) or FAILED, with no
// redirect_uri. A presentation the kit refuses fails the session.
func (s *Service) Respond(ctx context.Context, token string, f ResponseForm) (string, error) {
	v := s.verifier
	if f.Error == "" {
		var err error
		v, _, err = s.pidVerifier(ctx)
		if err != nil {
			return "", err
		}
	}
	s.mu.Lock()
	ses, err := s.get(token)
	if err == nil && (ses.status != StatusWaiting || ses.verifying) {
		err = refuse(http.StatusBadRequest, "invalid_request", "the session is %s or has a response", ses.status)
	}
	if err != nil {
		s.mu.Unlock()
		return "", err
	}
	if f.Error != "" {
		defer s.mu.Unlock()
		if subtle.ConstantTimeCompare([]byte(f.State), []byte(ses.state)) != 1 {
			return "", refuse(http.StatusBadRequest, "invalid_request", "the error does not carry the session's state")
		}
		ses.status = StatusFailed
		if f.Error == "access_denied" {
			ses.status = StatusCancelled
		}
		return "", nil
	}
	// Verification may fetch a status list: not under the lock.
	ses.verifying = true
	snapshot := *ses
	uc := s.cfg.Usecases[ses.usecase]
	s.mu.Unlock()

	disclosure, verr := v.VerifyResponse(ctx, f.Response, oid4vp.Session{
		ClientID: uc.Signer.ClientID(), Nonce: snapshot.nonce, State: snapshot.state, Query: snapshot.query, Key: snapshot.key,
	}, s.now())

	s.mu.Lock()
	defer s.mu.Unlock()
	ses.verifying = false
	if verr != nil {
		ses.status = StatusFailed
	}
	switch {
	case errors.Is(verr, oid4vp.ErrStatusUnavailable):
		return "", fmt.Errorf("%w: %v", ErrStatusUnavailable, verr)
	case verr != nil:
		return "", refuse(http.StatusBadRequest, "invalid_request", "%v", verr)
	}
	cq := snapshot.query.Credentials[0]
	attributes := map[string]any{}
	for _, c := range cq.Claims {
		attributes[c.Path[0]] = disclosure.Claims[c.Path[0]]
	}
	issuerURI, _ := disclosure.Claims["iss"].(string)
	ca := ""
	if disclosure.Issuer != nil {
		ca = disclosure.Issuer.Issuer.CommonName
	}
	ses.disclosed = []Disclosed{{ID: cq.ID, Attestations: []Attestation{{
		AttestationType: cq.Meta.VCTValues[0], Format: sdjwtvc.Type, Attributes: attributes, IssuerURI: issuerURI, CA: ca,
	}}}}
	ses.status = StatusDone
	if ses.sessionType != SameDevice || ses.returnTemplate == "" {
		return "", nil
	}
	if ses.returnNonce, err = randomToken(); err != nil {
		return "", err
	}
	u, err := url.Parse(strings.ReplaceAll(ses.returnTemplate, SessionTokenPlaceholder, url.QueryEscape(token)))
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("nonce", ses.returnNonce)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Disclosed returns the attributes of a DONE session. A session that
// returned the wallet's browser releases them only with the nonce of that
// return.
func (s *Service) Disclosed(token, nonce string) ([]Disclosed, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ses, err := s.get(token)
	if err != nil {
		return nil, err
	}
	if ses.status != StatusDone {
		return nil, refuse(http.StatusBadRequest, "session_state", "the session is %s", ses.status)
	}
	if ses.returnNonce != "" && subtle.ConstantTimeCompare([]byte(nonce), []byte(ses.returnNonce)) != 1 {
		return nil, refuse(http.StatusUnauthorized, "nonce", "the return nonce is missing or wrong")
	}
	return ses.disclosed, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
