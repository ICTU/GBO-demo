package authorization

import (
	"crypto/subtle"
	"sync"
	"time"
)

// The user is identified and authorizes the request during the
// authorization request (REQ-AZSP-6.1.3.1-02; clause 6.1.3.3, step 7). The
// authorization endpoint answers a valid request with a transaction; the
// user's decision completes it.

// TransactionLifetime is how long the user has to decide.
const TransactionLifetime = 5 * time.Minute

// Identity is the identification data of the user: the BSN and the mandatory
// PID attributes (CIR (EU) 2024/2977), as they go into the access token.
type Identity struct {
	BSN           string
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

// TestPerson is a person the chooser offers.
type TestPerson struct {
	ID   string
	Name string
}

// Identifier identifies the user. It is the seam where slice 7 puts the
// OpenID4VP PID disclosure; now it is the test-person chooser.
type Identifier interface {
	Persons() []TestPerson
	Identify(personID string) (Identity, bool)
}

// Transaction is a valid authorization request waiting for the user. Binding
// is a secret the browser keeps (a cookie); the decision must present it.
type Transaction struct {
	ID       string
	Binding  string
	ClientID string
	Scope    string
	// Source is the name of the data service the authorization is for.
	Source      string
	Persons     []TestPerson
	Preselected string
}

// Decision is the user's answer to a transaction.
type Decision struct {
	TransactionID string
	Binding       string
	PersonID      string
	Approve       bool
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

// Decide completes a transaction. An unknown, used or expired transaction, a
// wrong browser binding or an unknown test person cannot be redirected.
func (s *Service) Decide(d Decision) (AuthorizationResponse, error) {
	p, ok := s.transactions.take(d.TransactionID, d.Binding)
	if !ok || s.cfg.Now().After(p.expires) {
		return AuthorizationResponse{}, &NotRedirectableError{Description: "the authorization request is unknown, used, expired or was started in another browser"}
	}
	resp := AuthorizationResponse{RedirectURI: p.redirectURI, State: p.state}
	if !d.Approve {
		resp.Err = &Error{Code: "access_denied", Description: "the user denied the request"}
		return resp, nil
	}
	identity, ok := s.cfg.Identifier.Identify(d.PersonID)
	if !ok {
		return AuthorizationResponse{}, &NotRedirectableError{Description: "unknown test person"}
	}
	code := p.code
	code.identity = identity
	code.expires = s.cfg.Now().Add(AuthorizationCodeLifetime)
	resp.Code = s.codes.put(code)
	return resp, nil
}
