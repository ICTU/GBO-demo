package authorization

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"sync"
	"time"
)

// The user is identified and authorizes the request during the
// authorization request (REQ-AZSP-6.1.3.1-02; clause 6.1.3.3, steps 7-11).
// The authorization endpoint answers a valid request with a transaction. The
// user denies it, or logs in with an identification means; the grant is
// established only when the login succeeds (RFC 6749 section 3.1).

// TransactionLifetime is how long the user has to decide.
const TransactionLifetime = 5 * time.Minute

// Identity is the identification data of the user, as it goes into the
// access token: the BSN, the means that identified the user, and the
// mandatory PID attributes (CIR (EU) 2024/2977) when the means provides them.
// DigiD provides the BSN only; the wallet (slice 7 of #336) the PID.
type Identity struct {
	BSN string
	// Means names the identification means; the token carries it as acr
	// and amr.
	Means         string
	FamilyName    string
	GivenName     string
	Birthdate     string // YYYY-MM-DD
	PlaceOfBirth  PlaceOfBirth
	Nationalities []string
}

// PlaceOfBirth holds at least one of country (ISO 3166-1 alpha-2), region
// and locality.
type PlaceOfBirth struct {
	Country  string `json:"country,omitempty"`
	Region   string `json:"region,omitempty"`
	Locality string `json:"locality,omitempty"`
}

// Identifier is an identification means (REQ-AZSP-6.1.3.1-02): it turns what
// the user presented into the identity of the user. The DigiD mock is one;
// the wallet (slice 7 of #336) another. The core names the means in the
// identity itself, so a means cannot claim to be another.
type Identifier interface {
	Identify(ctx context.Context, credential string) (Identity, error)
}

// ErrNotIdentified: the means did not identify the user with what was
// presented. The transaction stays, so the user can try again.
var ErrNotIdentified = errors.New("the user was not identified")

// Transaction is a valid authorization request waiting for the user. Binding
// is a secret the browser keeps (a cookie); the decision must present it.
type Transaction struct {
	ID       string
	Binding  string
	ClientID string
	// ClientName is the legal name of the client from its registration,
	// or its client_id when it has none.
	ClientName string
	Scope      string
	// Source is the name of the data service the authorization is for.
	Source string
}

// Login is the user logging in with a means, which approves the request
// once the means identifies the user: Credential is what the user presented
// to it.
type Login struct {
	TransactionID string
	Binding       string
	Means         string
	Credential    string
}

type pendingAuthorization struct {
	binding     string
	redirectURI string
	state       string
	code        authorizationCode
	expires     time.Time
}

type transactionStore struct {
	mu sync.Mutex
	m  map[string]pendingAuthorization
}

func (s *transactionStore) put(p pendingAuthorization) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, old := range s.m {
		if time.Now().After(old.expires) {
			delete(s.m, k)
		}
	}
	id := RandomID()
	s.m[id] = p
	return id
}

// live reports whether transaction id is waiting for the browser with
// binding, without completing it.
func (s *transactionStore) live(id, binding string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[id]
	return ok && subtle.ConstantTimeCompare([]byte(p.binding), []byte(binding)) == 1 && !now.After(p.expires)
}

// take removes and returns the transaction when binding matches. A wrong
// binding leaves the transaction in place, so a party that only knows the
// identifier cannot spoil it for the user.
func (s *transactionStore) take(id, binding string) (pendingAuthorization, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[id]
	if !ok || subtle.ConstantTimeCompare([]byte(p.binding), []byte(binding)) != 1 {
		return pendingAuthorization{}, false
	}
	delete(s.m, id)
	return p, true
}

// Pending reports whether a transaction is still waiting for the browser that
// holds binding, so the user can be shown a means to log in with.
func (s *Service) Pending(transactionID, binding string) bool {
	return s.transactions.live(transactionID, binding, s.cfg.Now())
}

// Offers reports whether means is an identification means of this server.
func (s *Service) Offers(means string) bool {
	_, ok := s.cfg.Identifiers[means]
	return ok
}

// Deny completes a transaction with the user's refusal: access_denied to
// the client. An unknown, used or expired transaction, or a wrong browser
// binding, cannot be redirected.
func (s *Service) Deny(transactionID, binding string) (AuthorizationResponse, error) {
	p, err := s.takeTransaction(transactionID, binding)
	if err != nil {
		return AuthorizationResponse{}, err
	}
	return AuthorizationResponse{RedirectURI: p.redirectURI, State: p.state, Err: &Error{Code: "access_denied", Description: "the user denied the request"}}, nil
}

// LogIn identifies the user with the means of l and, when that succeeds,
// approves the transaction: the identity, with the means named by the core,
// goes with the code into the token. When the means does not identify the
// user the error is ErrNotIdentified and the transaction stays. An unknown
// means, transaction or browser binding cannot be redirected.
func (s *Service) LogIn(ctx context.Context, l Login) (AuthorizationResponse, error) {
	identifier, ok := s.cfg.Identifiers[l.Means]
	if !ok {
		return AuthorizationResponse{}, &NotRedirectableError{Description: "the identification means is not available"}
	}
	if !s.Pending(l.TransactionID, l.Binding) {
		return AuthorizationResponse{}, notPending
	}
	identity, err := identifier.Identify(ctx, l.Credential)
	if err != nil {
		return AuthorizationResponse{}, fmt.Errorf("%w: %v", ErrNotIdentified, err)
	}
	if identity.BSN == "" {
		return AuthorizationResponse{}, fmt.Errorf("%w: the means gave no BSN", ErrNotIdentified)
	}
	identity.Means = l.Means
	p, err := s.takeTransaction(l.TransactionID, l.Binding)
	if err != nil {
		return AuthorizationResponse{}, err
	}
	code := p.code
	code.identity = identity
	code.expires = s.cfg.Now().Add(AuthorizationCodeLifetime)
	return AuthorizationResponse{RedirectURI: p.redirectURI, State: p.state, Code: s.codes.put(code)}, nil
}

var notPending = &NotRedirectableError{Description: "the authorization request is unknown, used, expired or was started in another browser"}

// takeTransaction completes a transaction of the browser with binding.
func (s *Service) takeTransaction(id, binding string) (pendingAuthorization, error) {
	p, ok := s.transactions.take(id, binding)
	if !ok || s.cfg.Now().After(p.expires) {
		return pendingAuthorization{}, notPending
	}
	return p, nil
}
