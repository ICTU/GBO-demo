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
	// FetchDisclosed is Fetch for a subject that a wallet disclosed with
	// its PID; the backend receives the disclosure, not an offer caller's
	// subject, so it can tell the two apart.
	FetchDisclosed(ctx context.Context, configurationID string, pid DisclosedPID, parameters map[string]string) ([]SourceDocument, error)
	// FetchLogin is Fetch for a citizen who logged in at GBO's
	// authorization server: the backend learns that the BSN comes from that
	// login, so it logs the right Dataverwerking.
	FetchLogin(ctx context.Context, configurationID, bsn string, parameters map[string]string) ([]SourceDocument, error)
}

// Login is GBO's authorization server, where the citizen logs in during the
// authorization code flow (B15). The issuer is its client per provider and
// the resource server of its tokens.
type Login interface {
	// AuthorizationURL returns where the browser goes to log in for
	// provider: an authorization request for resource with state and the
	// S256 challenge.
	AuthorizationURL(ctx context.Context, provider, resource, state, challenge string) (string, error)
	// Exchange redeems the code for a token for resource, checks it and
	// returns the BSN it carries.
	Exchange(ctx context.Context, provider, resource, code, verifier string) (string, error)
}

// ErrLoginUnavailable means the authorization server could not be reached
// or failed: the citizen can try again later.
var ErrLoginUnavailable = errors.New("the login is unavailable")

// DisclosedPID is what a wallet disclosed of its PID, verified: the
// requested attributes and where the PID came from.
type DisclosedPID struct {
	// QueryID is the id of the DCQL credential query it answers.
	QueryID         string
	AttestationType string
	Attributes      map[string]any
	// CA is the common name of the CA that issued the PID signer.
	CA         string
	Signed     time.Time
	ValidFrom  time.Time
	ValidUntil time.Time
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
	// RedeemCode marks the code of a session of the provider and profile
	// as used and returns the session, atomically: a code works once. It
	// returns ErrNotFound for an unknown, used or expired code, or a code of
	// another provider or profile, and then leaves the code as it was.
	// txCodeHash is the hash of the transaction code the wallet sent, empty
	// for none. For a session with a transaction code, a missing one returns
	// ErrTxCodeMissing and leaves the code as it was; a wrong one returns
	// ErrTxCodeMismatch and counts as a failed attempt; the
	// MaxTxCodeAttempts-th failure uses the code up. For a session without
	// one, a code sent returns ErrTxCodeUnexpected and leaves the code as
	// it was.
	RedeemCode(ctx context.Context, provider, profile, codeHash, txCodeHash string, now time.Time) (Session, error)
	SaveToken(ctx context.Context, t AccessToken) error
	// Session returns a session by id, or ErrNotFound.
	Session(ctx context.Context, id string) (Session, error)
	// ExtendSession keeps a session, and its claims, at least until until.
	ExtendSession(ctx context.Context, id string, until time.Time) error
	// CompleteDocument replaces the document of a pending session that has
	// doc's credential id; once every document has its claims, the session
	// is no longer pending. ErrNotFound for an unknown session or document.
	CompleteDocument(ctx context.Context, id string, doc Document) error
	// Token returns an unexpired access token and its session, or
	// ErrNotFound.
	Token(ctx context.Context, tokenHash string, now time.Time) (AccessToken, Session, error)
	// ReserveIssue reserves a credential of the session for owner until the
	// given time, atomically, and reports whether it got the reservation.
	// It does not when the credential has been issued, or is reserved by
	// another owner whose reservation has not expired.
	ReserveIssue(ctx context.Context, sessionID, credentialID, owner string, now, until time.Time) (bool, error)
	// CompleteIssue marks the credential as issued, for good, if owner still
	// holds the reservation, and reports whether it did. An owner whose
	// reservation was taken over has lost it, also after it expired.
	CompleteIssue(ctx context.Context, sessionID, credentialID, owner string) (bool, error)
	// ReleaseIssue removes the reservation if owner holds it.
	ReleaseIssue(ctx context.Context, sessionID, credentialID, owner string) error
	// PutPending stores a pending authorization: a pushed authorization
	// request, or a login in progress.
	PutPending(ctx context.Context, p PendingAuthorization) error
	// TakePending removes an unexpired pending authorization of kind and
	// returns it, atomically: it is used once. It returns ErrNotFound for an
	// unknown, used or expired one.
	TakePending(ctx context.Context, kind, id string, now time.Time) (PendingAuthorization, error)
	CreateDisclosure(ctx context.Context, d DisclosureSession) error
	// TakeDisclosure removes an unexpired disclosure session and returns
	// it, atomically: a session answers one response. It returns
	// ErrNotFound for an unknown, used or expired session.
	TakeDisclosure(ctx context.Context, id string, now time.Time) (DisclosureSession, error)
}

// PendingAuthorization is a step of the authorization code flow that waits
// for the next: its Payload is the core's, the store keeps it until it is
// taken or expires.
type PendingAuthorization struct {
	Kind      string
	ID        string
	Payload   []byte
	ExpiresAt time.Time
}

// DisclosureSession is one OpenID4VP request for a PID, from the moment a
// wallet fetches it until its response arrives.
type DisclosureSession struct {
	ID       string
	Provider string
	Profile  string
	// Offer is the key of the disclosure offer the wallet started.
	Offer string
	Nonce string
	State string
	// ResponseKeyID and ResponseKey (PKCS #8 DER) are the session's
	// ephemeral key the wallet encrypts its response to.
	ResponseKeyID string
	ResponseKey   []byte
	ExpiresAt     time.Time
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

// Errors RedeemCode returns for a transaction code.
var (
	ErrTxCodeMissing    = errors.New("the transaction code is missing")
	ErrTxCodeMismatch   = errors.New("the transaction code is wrong")
	ErrTxCodeUnexpected = errors.New("the offer has no transaction code")
)

// MaxTxCodeAttempts is how many wrong transaction codes an offer takes; the
// last one uses it up.
const MaxTxCodeAttempts = 3

// Session is one issuance: the documents fetched for a pre-authorized
// code, to be issued to the wallet that redeems it.
type Session struct {
	ID        string
	Provider  string
	Profile   string
	CodeHash  string
	ExpiresAt time.Time
	// Reference is the offer caller's reference for revocation, if any.
	Reference string
	Documents []Document
	// Binding is set for an authorization code: what the token request
	// must match. A session without it has a pre-authorized code.
	Binding *CodeBinding
	// TxCodeHash is the hash of the offer's transaction code, if it has one.
	TxCodeHash string
	// Pending is set while the source has not answered yet: the documents
	// then have no claims, and the credential request is deferred.
	Pending *PendingSource
}

// PendingSource is the source offer a pending session waits for. It holds
// no personal data: the BSN is kept apart, sealed, for at most DeferralTTL.
type PendingSource struct {
	Offer string `json:"offer"`
}

// CodeBinding binds an authorization code to the pushed authorization
// request it was issued for.
type CodeBinding struct {
	ClientID      string `json:"client_id"`
	RedirectURI   string `json:"redirect_uri"`
	CodeChallenge string `json:"code_challenge"`
	// DPoPJKT is the thumbprint of the DPoP key of the pushed request, if
	// it had one (RFC 9449 section 10).
	DPoPJKT string `json:"dpop_jkt,omitempty"`
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
	JKT      string
	ClientID string
	// WalletProvider names the anchor set that accepted the client's WIA.
	WalletProvider string
	ExpiresAt      time.Time
}
