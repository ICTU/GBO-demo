package authorization

import (
	"crypto/subtle"
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

// Decision is the user's answer to a transaction: a denial, or the identity
// a means established for the user, which approves the request.
type Decision struct {
	TransactionID string
	Binding       string
	Approve       bool
	Identity      Identity
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
// holds binding, so a means can log the user in before Decide completes it.
func (s *Service) Pending(transactionID, binding string) bool {
	return s.transactions.live(transactionID, binding, s.cfg.Now())
}

// Decide completes a transaction. An unknown, used or expired transaction, a
// wrong browser binding, or an approval without an identity cannot be
// redirected.
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
	if d.Identity.BSN == "" || d.Identity.Means == "" {
		return AuthorizationResponse{}, &NotRedirectableError{Description: "the user was not identified"}
	}
	code := p.code
	code.identity = d.Identity
	code.expires = s.cfg.Now().Add(AuthorizationCodeLifetime)
	resp.Code = s.codes.put(code)
	return resp, nil
}
