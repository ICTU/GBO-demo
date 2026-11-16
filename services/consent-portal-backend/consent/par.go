package consent

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"regexp"
	"sync"
	"time"
)

// A service provider starts a consent flow by pushing its request to the
// portal over FSC (a pushed authorization request, RFC 9126). The portal
// checks it against the provider's registration and keeps it under a
// one-time reference, the request_uri, which is all the citizen's browser
// carries to the portal.

// requestURIPrefix is the URN namespace RFC 9126 §2.2 gives request URIs.
const requestURIPrefix = "urn:ietf:params:oauth:request_uri:"

// s256Challenge is a PKCE S256 code challenge: the unpadded base64url
// encoding of a SHA-256 hash (RFC 7636 §4.2).
var s256Challenge = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// PushedRequest is a pushed authorization request as the provider sent it.
// ValidUntil, ClientName and Purpose are optional.
type PushedRequest struct {
	ResponseType        string
	ClientID            string
	Service             string
	Scopes              []string
	RedirectURI         string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	ValidUntil          string // RFC 3339
	ClientName          string
	Purpose             string
}

// PendingRequest is a pushed request that passed every check, kept until
// the citizen's browser presents its reference or it expires.
type PendingRequest struct {
	Request       VerifiedRequest
	State         string
	CodeChallenge string
	ValidUntil    time.Time // zero when the provider asked for no end date
	ExpiresAt     time.Time
	// Citizen is the citizen the request is bound to: the first one to
	// resolve its reference. Empty until then.
	Citizen SubjectRef
	// Spent is set once the citizen's decision used the reference.
	Spent bool
}

// PushedReference is what the provider gets back for an accepted request:
// the reference to send the citizen's browser with, and how long it holds.
type PushedReference struct {
	RequestURI string
	ExpiresIn  time.Duration
}

// PushRequest checks a pushed request from provider, the authenticated
// peer, and keeps it under a new request URI. A refusal is a *Refusal: a
// missing or unusable OAuth parameter, or a valid_until that is malformed or
// not later than the expiry of the new request URI, is
// ReasonInitiationIncomplete, checked before anything else; the rest are
// VerifyRequest's checks.
func (p *Portal) PushRequest(ctx context.Context, provider PeerID, pushed PushedRequest) (PushedReference, error) {
	if problem := pushed.incomplete(); problem != "" {
		return PushedReference{}, refuse(ReasonInitiationIncomplete, "%s", problem)
	}
	if p.Requests == nil {
		return PushedReference{}, errors.New("no request store is configured")
	}
	now := p.now()
	var validUntil time.Time
	if pushed.ValidUntil != "" {
		parsed, err := time.Parse(time.RFC3339, pushed.ValidUntil)
		if err != nil {
			return PushedReference{}, refuse(ReasonInitiationIncomplete, "valid_until is not an RFC 3339 time")
		}
		// A consent that ends before the citizen can decide is no consent:
		// valid_until has to outlast the request URI, so that it is still
		// in the future whenever the decision is made.
		if !parsed.After(p.Requests.expiry(now)) {
			return PushedReference{}, refuse(ReasonInitiationIncomplete, "valid_until is not later than the expiry of the request_uri")
		}
		validUntil = parsed
	}

	verified, err := p.VerifyRequest(ctx, provider, ClaimedRequest{
		ClientID:  pushed.ClientID,
		Service:   pushed.Service,
		Scopes:    pushed.Scopes,
		ReturnURL: pushed.RedirectURI,
		Name:      pushed.ClientName,
		Purpose:   pushed.Purpose,
	})
	if err != nil {
		return PushedReference{}, err
	}

	uri, expiresAt, err := p.Requests.Put(PendingRequest{
		Request:       verified,
		State:         pushed.State,
		CodeChallenge: pushed.CodeChallenge,
		ValidUntil:    validUntil,
	}, now)
	if err != nil {
		return PushedReference{}, err
	}
	return PushedReference{RequestURI: uri, ExpiresIn: expiresAt.Sub(now)}, nil
}

// incomplete names what makes the OAuth part of the request unusable, or "".
// The consent part (client ID, service, data sets, return URL) is checked by
// VerifyRequest.
func (r PushedRequest) incomplete() string {
	switch {
	case r.ResponseType != "code":
		return "the request has no response_type=code"
	case r.State == "":
		return "the request has no state"
	case r.CodeChallenge == "":
		return "the request has no code_challenge"
	case r.CodeChallengeMethod != "S256":
		return "the code challenge method is not S256"
	case !s256Challenge.MatchString(r.CodeChallenge):
		return "the code_challenge is not an S256 challenge"
	}
	return ""
}

// RequestStore keeps pending requests by request URI until they expire.
// An expired or spent request is remembered for one more lifetime, so that
// a citizen who comes back with its reference is told it expired or was
// used; after that its reference is unknown.
//
// Demo shortcut: the store is in memory, so a restart of the backend drops
// every open request and the backend runs as a single replica. A real
// deployment keeps them in a shared store.
type RequestStore struct {
	lifetime time.Duration
	mu       sync.Mutex
	requests map[string]PendingRequest
}

// NewRequestStore returns an empty store whose requests expire after
// lifetime.
func NewRequestStore(lifetime time.Duration) *RequestStore {
	return &RequestStore{lifetime: lifetime, requests: map[string]PendingRequest{}}
}

// Put keeps a request under a new, unguessable request URI and returns the
// URI and when the request expires. Requests past their retention are
// dropped on the way.
func (s *RequestStore) Put(request PendingRequest, now time.Time) (string, time.Time, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", time.Time{}, err
	}
	uri := requestURIPrefix + base64.RawURLEncoding.EncodeToString(random)
	request.ExpiresAt = s.expiry(now)

	s.mu.Lock()
	defer s.mu.Unlock()
	for key, pending := range s.requests {
		if !now.Before(pending.ExpiresAt.Add(s.lifetime)) {
			delete(s.requests, key)
		}
	}
	s.requests[uri] = request
	return uri, request.ExpiresAt, nil
}

// expiry is when a request put at now expires.
func (s *RequestStore) expiry(now time.Time) time.Time {
	return now.Add(s.lifetime)
}

// Get returns the request kept under uri. found is false for an unknown
// URI and for a request that has expired.
func (s *RequestStore) Get(uri string, now time.Time) (request PendingRequest, found bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, found = s.requests[uri]
	if !found || !now.Before(request.ExpiresAt) {
		return PendingRequest{}, false
	}
	return request, true
}

// Bind returns the request under uri for citizen, after checking that its
// reference is usable, that clientID is the provider that pushed it and
// that no other citizen has it. The first citizen to resolve the reference
// is bound to it; the same citizen may resolve it again.
func (s *RequestStore) Bind(uri, clientID string, citizen SubjectRef, now time.Time) (PendingRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, err := s.usable(uri, now)
	if err != nil {
		return PendingRequest{}, err
	}
	if clientID != string(request.Request.Provider) {
		return PendingRequest{}, refuse(ReasonClientIDMismatch, "the reference was not pushed by client_id %q", clientID)
	}
	if request.Citizen != "" && request.Citizen != citizen {
		return PendingRequest{}, refuse(ReasonRequestURIOtherCitizen, "the reference belongs to another citizen")
	}
	request.Citizen = citizen
	s.requests[uri] = request
	return request, nil
}

// Spend uses the reference under uri for citizen's decision: the reference
// must be usable and already bound to citizen by Bind, which checked the
// client ID; then accept runs on the request. When accept returns nil the
// reference is spent; when it returns an error nothing changes. Only one
// decision can spend a reference.
func (s *RequestStore) Spend(uri string, citizen SubjectRef, now time.Time, accept func(PendingRequest) error) (PendingRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, err := s.usable(uri, now)
	if err != nil {
		return PendingRequest{}, err
	}
	switch request.Citizen {
	case citizen:
	case "":
		return PendingRequest{}, refuse(ReasonRequestURINotResolved, "the reference was not resolved before the decision")
	default:
		return PendingRequest{}, refuse(ReasonRequestURIOtherCitizen, "the reference belongs to another citizen")
	}
	if err := accept(request); err != nil {
		return PendingRequest{}, err
	}
	request.Spent = true
	s.requests[uri] = request
	return request, nil
}

// usable returns the request under uri, or a refusal when its reference is
// unknown, expired or spent. The caller holds s.mu.
func (s *RequestStore) usable(uri string, now time.Time) (PendingRequest, error) {
	request, found := s.requests[uri]
	switch {
	case !found || !now.Before(request.ExpiresAt.Add(s.lifetime)):
		return PendingRequest{}, refuse(ReasonRequestURIUnknown, "the reference is unknown")
	case !now.Before(request.ExpiresAt):
		return PendingRequest{}, refuse(ReasonRequestURIExpired, "the reference has expired")
	case request.Spent:
		return PendingRequest{}, refuse(ReasonRequestURISpent, "the reference has been used")
	}
	return request, nil
}
