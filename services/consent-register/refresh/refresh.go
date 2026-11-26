// Package refresh is the core of the refresh grant: a service provider that
// holds a refresh token gets a new consent proof without the citizen.
//
// The register keeps, with each consent that was given through the pushed
// authorization flow, the SHA-256 of one refresh token, the FSC peer of the
// client it was issued to, and the encrypted values per source that the first
// proof carried. A refresh checks the token, the peer, the status and the end
// of the consent, then signs a new proof over the same values and records the
// processing. The token is not rotated: one token per consent, reusable until
// the consent ends.
//
// The package imports no transport and no storage. The store, the signer and
// the logbook are ports; the register's main package adapts them.
package refresh

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"time"
)

// ScopeEntry is one source's part of a consent: the fields of one scope the
// citizen consented to.
type ScopeEntry struct {
	Bronhouder      string   `json:"bronhouder"`
	ScopeID         string   `json:"scope_id"`
	ConsentedFields []string `json:"consented_fields"`
}

// Consent is a consent as far as a refresh needs it.
type Consent struct {
	ID                string
	Status            string
	SubjectRef        string
	DienstverlenerOIN string
	// ClientPeer is the FSC peer the refresh token was issued to.
	ClientPeer   string
	Scopes       []string
	ScopeEntries []ScopeEntry
	ValidUntil   time.Time
	// EncryptedSubject is the encrypted values per source, as the first
	// proof carried them. The register cannot read them; it hands them on.
	EncryptedSubject []byte
}

// StatusActive is the status of a consent that may still be used.
const StatusActive = "ACTIVE"

// Store is the register's storage, seen from a refresh.
type Store interface {
	// ByRefreshToken returns the consent whose refresh token has this
	// SHA-256, and whether there is one.
	ByRefreshToken(ctx context.Context, tokenHash []byte) (Consent, bool, error)
	// Forget wipes the encrypted values and the refresh token hash of a
	// consent. Its token is refused from then on.
	Forget(ctx context.Context, consentID string) error
}

// Signer signs a new proof for a consent, over the values kept with it.
type Signer interface {
	Sign(ctx context.Context, consent Consent) (Proof, error)
}

// Logbook records a refresh as a processing of the register, for the client
// peer. An error means the record was not stored.
type Logbook interface {
	RecordRefresh(ctx context.Context, consent Consent) error
}

// Proof is a newly signed consent proof.
type Proof struct {
	Token     string
	ExpiresAt time.Time
}

// Request is one refresh, as the client asked it.
type Request struct {
	RefreshToken string
	// ClientPeer is the FSC peer the request came from.
	ClientPeer string
	// Scopes is what the client asked for; empty means the whole consent.
	Scopes []string
}

// Result is a refreshed proof.
type Result struct {
	ConsentID string
	Scopes    []string
	Proof     Proof
}

// The refusals, in the terms of RFC 6749 §5.2.
var (
	ErrInvalidRequest = errors.New("invalid_request")
	ErrInvalidGrant   = errors.New("invalid_grant")
	ErrInvalidScope   = errors.New("invalid_scope")
)

// Service is the refresh use case.
type Service struct {
	Store   Store
	Signer  Signer
	Logbook Logbook
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Refresh signs a new proof for the consent the refresh token belongs to.
// It returns an error wrapping ErrInvalidRequest, ErrInvalidGrant or
// ErrInvalidScope for a refusal (the wrapped text is the reason, for the
// service log, not for the client); any other error is the register's own
// failure. A refusal stores nothing and writes no record.
func (s Service) Refresh(ctx context.Context, req Request) (Result, error) {
	if req.RefreshToken == "" {
		return Result{}, fmt.Errorf("%w: no refresh token", ErrInvalidRequest)
	}
	if req.ClientPeer == "" {
		return Result{}, fmt.Errorf("%w: no client peer", ErrInvalidRequest)
	}
	consent, found, err := s.Store.ByRefreshToken(ctx, HashToken(req.RefreshToken))
	if err != nil {
		return Result{}, fmt.Errorf("look up refresh token: %w", err)
	}
	if !found {
		// Also a token whose consent was revoked: revoking wipes the hash.
		return Result{}, fmt.Errorf("%w: unknown refresh token", ErrInvalidGrant)
	}
	if consent.ClientPeer != req.ClientPeer {
		return Result{}, fmt.Errorf("%w: refresh token of consent %s was issued to another client", ErrInvalidGrant, consent.ID)
	}
	if !s.now().Before(consent.ValidUntil) {
		// The values and the token are of no use once the consent has
		// ended. Wiping them here does not hang on a revocation, which a
		// register may refuse for an ended consent.
		if err := s.Store.Forget(ctx, consent.ID); err != nil {
			return Result{}, fmt.Errorf("forget the values of ended consent %s: %w", consent.ID, err)
		}
		return Result{}, fmt.Errorf("%w: consent %s ended at %s", ErrInvalidGrant, consent.ID, consent.ValidUntil.UTC().Format(time.RFC3339))
	}
	if consent.Status != StatusActive {
		return Result{}, fmt.Errorf("%w: consent %s is %s", ErrInvalidGrant, consent.ID, consent.Status)
	}
	if len(consent.EncryptedSubject) == 0 {
		return Result{}, fmt.Errorf("%w: consent %s keeps no values to sign", ErrInvalidGrant, consent.ID)
	}
	if len(req.Scopes) > 0 && !sameSet(req.Scopes, consent.Scopes) {
		// RFC 6749 §6 allows a narrower scope, but a proof always carries
		// the whole consent, so anything but the whole consent is refused.
		return Result{}, fmt.Errorf("%w: asked %v, consent %s has %v", ErrInvalidScope, req.Scopes, consent.ID, consent.Scopes)
	}

	proof, err := s.Signer.Sign(ctx, consent)
	if err != nil {
		return Result{}, fmt.Errorf("sign refreshed proof for consent %s: %w", consent.ID, err)
	}
	// Fail closed: a proof nobody recorded is not handed out.
	if err := s.Logbook.RecordRefresh(ctx, consent); err != nil {
		return Result{}, fmt.Errorf("record refresh of consent %s: %w", consent.ID, err)
	}
	return Result{ConsentID: consent.ID, Scopes: consent.Scopes, Proof: proof}, nil
}

func (s Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func sameSet(a, b []string) bool {
	for _, value := range a {
		if !slices.Contains(b, value) {
			return false
		}
	}
	for _, value := range b {
		if !slices.Contains(a, value) {
			return false
		}
	}
	return true
}

// NewToken returns a new opaque refresh token, 32 random bytes in base64url,
// and the SHA-256 the register keeps instead of the token.
func NewToken() (token string, hash []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate refresh token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token), nil
}

// HashToken is what the register keeps of a refresh token.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
