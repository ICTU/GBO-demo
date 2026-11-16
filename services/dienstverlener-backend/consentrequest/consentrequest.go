// Package consentrequest is the use case "ask the citizen for consent". The
// provider pushes its request to the consent portal over FSC (RFC 9126),
// sends the browser to the portal with only its client_id and the
// request_uri, and exchanges the code the browser brings back, with its PKCE
// verifier (RFC 7636), for the consent token. The token stays in the
// provider's session; it is never part of an answer to the browser.
//
// Like package consumer, this package imports no transport library.
package consentrequest

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Requester asks consent on behalf of one provider. ClientID is the
// provider's own FSC Peer ID and RedirectURI its registered return location;
// PortalURL is the public address of the consent portal.
type Requester struct {
	ClientID    string
	RedirectURI string
	PortalURL   string
	Portal      Portal
	Sessions    Sessions
}

// Request is what the provider asks consent for. ValidUntil is optional.
type Request struct {
	Service    string
	Scopes     []string
	ValidUntil string
}

// Started is a pushed request: the session it belongs to, and where the
// browser goes next.
type Started struct {
	SessionID  string
	RedirectTo string
}

// Return is what the browser brings back from the portal: a code, or an
// error, each with the state of the request.
type Return struct {
	Code  string
	State string
	Error string
}

// Outcome is how the citizen's decision ended.
type Outcome string

const (
	Granted Outcome = "granted"
	Denied  Outcome = "denied"
	// Failed: the portal could not record the consent (server_error).
	Failed Outcome = "failed"
)

// Completed is the end of a request. It names the consent but never holds
// its token. SessionID is set when the consent went into a new session: the
// browser's session id from then on.
type Completed struct {
	Outcome   Outcome
	ConsentID string
	Scopes    []string
	SessionID string
	// Error is the error the portal returned, for logs.
	Error string
}

var (
	// ErrInvalidRequest: the request cannot be pushed as it is.
	ErrInvalidRequest = errors.New("invalid consent request")
	// ErrInvalidReturn: a return with neither a code nor an error, or both.
	ErrInvalidReturn = errors.New("a return carries either a code or an error, with its state")
	// ErrNoPendingRequest: the session has no request waiting for a return.
	ErrNoPendingRequest = errors.New("no consent request is pending in this session")
	// ErrStateMismatch: the return belongs to another request.
	ErrStateMismatch = errors.New("the state does not match the pending request")
)

// Start pushes the request and records it in the session, a new one when
// sessionID is empty or unknown. A request already pending in the session is
// replaced; a consent the session holds is kept.
func (r *Requester) Start(ctx context.Context, sessionID string, req Request) (Started, error) {
	if err := validScopes(req.Scopes); err != nil {
		return Started{}, err
	}
	state, err := randomString()
	if err != nil {
		return Started{}, err
	}
	verifier, err := randomString()
	if err != nil {
		return Started{}, err
	}
	requestURI, err := r.Portal.Push(ctx, PushedRequest{
		ClientID:      r.ClientID,
		Service:       req.Service,
		Scopes:        req.Scopes,
		RedirectURI:   r.RedirectURI,
		State:         state,
		CodeChallenge: CodeChallenge(verifier),
		ValidUntil:    req.ValidUntil,
	})
	if err != nil {
		return Started{}, err
	}

	pending := &Pending{State: state, Verifier: verifier}
	err = ErrUnknownSession
	if sessionID != "" {
		err = r.Sessions.Update(ctx, sessionID, func(s *Session) error {
			s.Pending = pending
			return nil
		})
	}
	if errors.Is(err, ErrUnknownSession) {
		sessionID, err = r.Sessions.New(ctx, Session{Pending: pending})
	}
	if err != nil {
		return Started{}, err
	}
	return Started{SessionID: sessionID, RedirectTo: r.startURL(requestURI)}, nil
}

// Complete ends the pending request of the session with what the browser
// brought back. A code is exchanged for the consent token. The token goes into
// a new session, whose id Completed carries, and the old session is deleted:
// an id someone else knew before the grant (a session fixation, for example a
// cookie planted by another page on the same host) never gets the token. A
// return whose state does not match leaves the pending request as it is.
func (r *Requester) Complete(ctx context.Context, sessionID string, ret Return) (Completed, error) {
	if ret.State == "" || (ret.Code == "") == (ret.Error == "") {
		return Completed{}, ErrInvalidReturn
	}
	var pending Pending
	err := r.Sessions.Update(ctx, sessionID, func(s *Session) error {
		if s.Pending == nil {
			return ErrNoPendingRequest
		}
		if subtle.ConstantTimeCompare([]byte(s.Pending.State), []byte(ret.State)) != 1 {
			return ErrStateMismatch
		}
		pending = *s.Pending
		s.Pending = nil
		return nil
	})
	if errors.Is(err, ErrUnknownSession) {
		return Completed{}, ErrNoPendingRequest
	}
	if err != nil {
		return Completed{}, err
	}

	switch ret.Error {
	case "":
	case "access_denied":
		return Completed{Outcome: Denied, Error: ret.Error}, nil
	default:
		return Completed{Outcome: Failed, Error: ret.Error}, nil
	}

	consent, err := r.Portal.Exchange(ctx, CodeExchange{
		ClientID:     r.ClientID,
		Code:         ret.Code,
		RedirectURI:  r.RedirectURI,
		CodeVerifier: pending.Verifier,
	})
	if err != nil {
		return Completed{}, err
	}
	// The old session holds nothing more than the new one: its pending
	// request is gone and its consent, if any, is replaced. A new session
	// also keeps the token when the old one expired during the exchange.
	newID, err := r.Sessions.New(ctx, Session{Consent: &consent})
	if err != nil {
		return Completed{}, err
	}
	if err := r.Sessions.Delete(ctx, sessionID); err != nil {
		return Completed{}, err
	}
	return Completed{Outcome: Granted, ConsentID: consent.ConsentID, Scopes: consent.Scopes, SessionID: newID}, nil
}

// ConsentToken is the consent token the session holds, if any.
func (r *Requester) ConsentToken(ctx context.Context, sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	s, err := r.Sessions.Get(ctx, sessionID)
	if err != nil || s.Consent == nil {
		return "", false
	}
	return s.Consent.Token, true
}

// startURL is the portal's start route with only the client_id and the
// request_uri (RFC 9126 §4).
func (r *Requester) startURL(requestURI string) string {
	query := url.Values{"client_id": {r.ClientID}, "request_uri": {requestURI}}
	return strings.TrimRight(r.PortalURL, "/") + "/toestemming/start?" + query.Encode()
}

// CodeChallenge is the S256 challenge of a PKCE verifier (RFC 7636 §4.2).
func CodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// randomString is 32 random bytes, base64url-encoded: 43 characters, as RFC
// 7636 §4.1 recommends for a verifier.
func randomString() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// validScopes refuses a list the scope parameter cannot carry: data sets
// travel space-separated (RFC 6749 §3.3).
func validScopes(scopes []string) error {
	for _, s := range scopes {
		if s == "" || strings.ContainsAny(s, " \t\r\n") {
			return fmt.Errorf("%w: a data set is empty or contains white space", ErrInvalidRequest)
		}
	}
	return nil
}
