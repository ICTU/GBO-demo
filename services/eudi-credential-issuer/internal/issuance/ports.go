package issuance

import (
	"context"
	"errors"
	"time"
)

// Source is the bronpoort: the backend that knows the data for a subject.
// Its contract is the IssuableDocument contract of the attestation server
// that nl-wallet's issuance_server calls, so existing backends work as is.
type Source interface {
	// Fetch returns the documents a backend issues for subject for one
	// credential configuration; parameters are the offer parameters (for
	// example a tax year). Which backend serves a configuration is the
	// adapter's concern.
	Fetch(ctx context.Context, configurationID string, subject map[string]any, parameters map[string]string) ([]SourceDocument, error)
}

// SourceDocument is one IssuableDocument as the backend returns it.
type SourceDocument struct {
	AttestationType string
	Format          string
	Attributes      map[string]any
}

// Errors a Source returns.
var (
	// ErrNoData means the backend has nothing to issue for this subject.
	ErrNoData = errors.New("the source has no data for this subject")
	// ErrSourceUnavailable means the backend could not be reached or failed.
	ErrSourceUnavailable = errors.New("the source is unavailable")
)

// Sessions stores issuance sessions and the access tokens bound to them.
type Sessions interface {
	CreateSession(ctx context.Context, s Session) error
	// RedeemCode marks the pre-authorized code of a session of profile as
	// used and returns the session, atomically: a code works once. It
	// returns ErrNotFound for an unknown, used or expired code, or a code of
	// another profile, and then leaves the code as it was.
	RedeemCode(ctx context.Context, profile, codeHash string, now time.Time) (Session, error)
	SaveToken(ctx context.Context, t AccessToken) error
	// Token returns an unexpired access token and its session, or
	// ErrNotFound.
	Token(ctx context.Context, tokenHash string, now time.Time) (AccessToken, Session, error)
	// ReserveIssue reserves a credential of the session for issuance until
	// the given time, atomically, and reports whether it got the
	// reservation. It does not when the credential has been issued, or is
	// reserved by another request whose reservation has not expired.
	ReserveIssue(ctx context.Context, sessionID, credentialID string, now, until time.Time) (bool, error)
	// CompleteIssue marks a reserved credential as issued, for good.
	CompleteIssue(ctx context.Context, sessionID, credentialID string) error
	// ReleaseIssue removes a reservation that did not lead to issuance.
	ReleaseIssue(ctx context.Context, sessionID, credentialID string) error
}

// OneTime holds single-use values: nonces, attestation challenges and the
// identifiers of proofs seen before (replay protection).
type OneTime interface {
	// Put stores value of kind until expiry.
	Put(ctx context.Context, kind, value string, expiry time.Time) error
	// Take removes an unexpired value and reports whether it was there.
	Take(ctx context.Context, kind, value string, now time.Time) (bool, error)
	// FirstUse records id of kind until expiry and reports whether it was
	// not seen before.
	FirstUse(ctx context.Context, kind, id string, expiry, now time.Time) (bool, error)
}

// StatusLists hands out and revokes entries of the Token Status Lists.
type StatusLists interface {
	// Allocate reserves a random free index in list, linked to reference
	// when it is not empty.
	Allocate(ctx context.Context, list string, size int, reference string) (int, error)
	Revoke(ctx context.Context, list string, index int) error
	// RevokeReference revokes every entry linked to reference, records the
	// reference as revoked for good, and returns how many entries it
	// revoked.
	RevokeReference(ctx context.Context, reference string) (int, error)
	// ReferenceRevoked reports whether RevokeReference was called for it.
	ReferenceRevoked(ctx context.Context, reference string) (bool, error)
	// Revoked returns the revoked indices of list.
	Revoked(ctx context.Context, list string) ([]int, error)
}

// ErrNotFound is returned by stores for an unknown or expired item.
var ErrNotFound = errors.New("not found")

// Session is one issuance: the documents fetched for a pre-authorized
// code, to be issued to the wallet that redeems it.
type Session struct {
	ID        string
	Profile   string
	CodeHash  string
	ExpiresAt time.Time
	// Reference is the offer caller's reference for revocation, if any.
	Reference string
	Documents []Document
}

// Document is a credential to issue within a session.
type Document struct {
	// CredentialID is the credential_identifier the wallet asks for.
	CredentialID    string
	ConfigurationID string
	Claims          map[string]any
	NotBefore       time.Time
	Expires         time.Time
}

// AccessToken binds a token to a session, a DPoP key and a client.
type AccessToken struct {
	Hash      string
	SessionID string
	// JKT is the DPoP key thumbprint the token is bound to.
	JKT       string
	ClientID  string
	ExpiresAt time.Time
}
