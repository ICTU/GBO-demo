package consentrequest

import (
	"context"
	"errors"
	"fmt"
)

// PushedRequest is the pushed authorization request. It carries no
// client_name or purpose: the portal shows those from the provider's
// registration.
type PushedRequest struct {
	ClientID      string
	Service       string
	Scopes        []string
	RedirectURI   string
	State         string
	CodeChallenge string
	ValidUntil    string
}

// CodeExchange is the token request for a code.
type CodeExchange struct {
	ClientID     string
	Code         string
	RedirectURI  string
	CodeVerifier string
}

// Consent is what the exchange gives: the consent token and what it covers.
type Consent struct {
	Token     string
	ConsentID string
	Scopes    []string
}

// Refusal is an OAuth error the portal answered with: Code is the error,
// Reason its reason code when it gave one.
type Refusal struct {
	Code        string
	Reason      string
	Description string
}

func (r *Refusal) Error() string {
	return fmt.Sprintf("refused by the consent portal: %s %s %s", r.Code, r.Reason, r.Description)
}

// Portal is the consent portal, reached over FSC. Push returns the
// request_uri. A refusal is a *Refusal; any other error means the portal did
// not answer.
type Portal interface {
	Push(ctx context.Context, req PushedRequest) (string, error)
	Exchange(ctx context.Context, req CodeExchange) (Consent, error)
}

// Session is the server-side state of one browser.
type Session struct {
	Pending *Pending
	Consent *Consent
}

// Pending is a pushed request waiting for the citizen.
type Pending struct {
	State    string
	Verifier string
}

// ErrUnknownSession: no such session, or it expired.
var ErrUnknownSession = errors.New("unknown session")

// Sessions keeps sessions by an id that is hard to guess. Update applies
// change atomically and keeps nothing of it when change returns an error.
type Sessions interface {
	New(ctx context.Context, s Session) (string, error)
	Get(ctx context.Context, id string) (Session, error)
	Update(ctx context.Context, id string, change func(*Session) error) error
}
