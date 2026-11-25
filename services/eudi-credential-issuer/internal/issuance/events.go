package issuance

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// Kinds of events.
const (
	// EventSessionStarted: a session was made for an offer, a disclosure or
	// a login, with its documents.
	EventSessionStarted = "session_started"
	// EventTokenIssued: a wallet redeemed a code or a refresh token.
	EventTokenIssued = "token_issued"
	// EventTokenRefused: a refresh token of a known session was refused.
	EventTokenRefused = "token_refused"
	// EventCredentialIssued, EventCredentialDeferred and
	// EventCredentialRefused: the outcome of a credential request.
	EventCredentialIssued   = "credential_issued"
	EventCredentialDeferred = "credential_deferred"
	EventCredentialRefused  = "credential_refused"
	// EventNotificationReceived: a wallet's notification (section 11).
	EventNotificationReceived = "notification_received"
)

// Flows a session starts with.
const (
	FlowPreAuthorizedCode = "pre-authorized_code"
	FlowAuthorizationCode = "authorization_code"
)

// Proof types of a credential request (Appendix F). ProofTypeJWT with a
// key attestation in the proofs is ProofTypeJWTKeyAttestation.
const (
	ProofTypeJWT               = "jwt"
	ProofTypeJWTKeyAttestation = "jwt+key_attestation"
	ProofTypeAttestation       = "attestation"
)

// Where the batch size in force comes from.
const (
	BatchSizeFromReusePolicy = "reuse_policy"
	BatchSizeFromProvider    = "provider"
)

// Event is something that happened in a wallet session. It holds no
// attribute values, personal data, codes, tokens, session ids or holder
// keys: a session is named by a label derived from its id, a holder key by
// a shortened thumbprint.
type Event struct {
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	Provider string    `json:"provider"`
	Profile  string    `json:"profile"`
	// Session labels the session; PreviousSession the session a refresh
	// continues.
	Session         string `json:"session"`
	PreviousSession string `json:"previous_session,omitempty"`
	// Flow and Configurations: session_started.
	Flow           string   `json:"flow,omitempty"`
	Configurations []string `json:"configurations,omitempty"`
	// GrantType and WalletProvider: token_issued; WalletProvider names the
	// trust anchor set that accepted the wallet's attestation.
	GrantType      string `json:"grant_type,omitempty"`
	WalletProvider string `json:"wallet_provider,omitempty"`
	// Configuration to KeyFingerprints: credential requests.
	Configuration   string   `json:"configuration,omitempty"`
	ProofType       string   `json:"proof_type,omitempty"`
	KeysOffered     int      `json:"keys_offered,omitempty"`
	BatchSize       int      `json:"batch_size,omitempty"`
	BatchSizeFrom   string   `json:"batch_size_from,omitempty"`
	Credentials     int      `json:"credentials,omitempty"`
	KeyFingerprints []string `json:"key_fingerprints,omitempty"`
	// Error and ErrorDescription: credential_refused and token_refused.
	Error            string `json:"error,omitempty"`
	ErrorDescription string `json:"error_description,omitempty"`
	// Notification is the event a wallet notified.
	Notification string `json:"notification,omitempty"`
}

// Events takes the events of the wallet sessions. Recording must not fail
// an issuance, so Record returns nothing; an implementation deals with its
// own errors.
type Events interface {
	Record(ctx context.Context, e Event)
}

// noEvents records nothing: the default.
type noEvents struct{}

func (noEvents) Record(context.Context, Event) {}

// sessionLabel names a session in events without giving away its id.
func sessionLabel(id string) string {
	sum := sha256.Sum256([]byte("session label " + id))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:12]
}

// KeyFingerprint is the shortened JWK thumbprint (RFC 7638) of a holder
// key: enough to tell the copies of a batch apart in a timeline.
func KeyFingerprint(k jose.JSONWebKey) string {
	tp, err := thumbprint(k)
	if err != nil {
		return ""
	}
	return tp[:16]
}

func (s *Service) record(ctx context.Context, e Event) {
	e.At = s.now()
	s.cfg.Events.Record(ctx, e)
}

// recordCredential records the outcome of a credential request.
func (s *Service) recordCredential(ctx context.Context, e Event, res CredentialResult, err error) {
	var refusal *Error
	switch {
	case err == nil && res.Deferred:
		e.Kind = EventCredentialDeferred
	case err == nil:
		e.Kind = EventCredentialIssued
		e.Credentials = len(e.KeyFingerprints)
	case errors.As(err, &refusal):
		e.Kind, e.Error, e.ErrorDescription = EventCredentialRefused, refusal.Code, refusal.Description
		e.Credentials, e.KeyFingerprints = 0, nil
	default:
		e.Kind, e.Error = EventCredentialRefused, "server_error"
		e.Credentials, e.KeyFingerprints = 0, nil
	}
	s.record(ctx, e)
}
