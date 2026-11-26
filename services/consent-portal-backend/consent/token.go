package consent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sync"
	"time"
)

// A granted decision gives the provider a one-time code through the
// citizen's browser. The provider exchanges it over FSC, with the PKCE
// verifier that belongs to the challenge it pushed, for the consent token
// (RFC 6749 §4.1.3, RFC 7636 §4.6).

// codeVerifier is a PKCE code verifier (RFC 7636 §4.1).
var codeVerifier = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

// ErrInvalidGrant is returned when a code cannot be exchanged: it is
// unknown, used, expired, issued to another provider or for another
// redirect URI, or the code verifier does not match its challenge. A
// refresh is refused with it too: the refresh token is unknown, was issued
// to another client, or its consent was revoked or has ended.
var ErrInvalidGrant = errors.New("invalid grant")

// ErrInvalidScope is returned when a refresh asks for a scope that is not
// the whole consent.
var ErrInvalidScope = errors.New("invalid scope")

// IssuedCode is what a code stands for until the provider exchanges it.
type IssuedCode struct {
	Provider      PeerID
	RedirectURI   string
	CodeChallenge string
	Consent       Granted
	Scopes        []string
	ExpiresAt     time.Time
}

// TokenRequest is a provider's request to exchange a code.
type TokenRequest struct {
	Code         string
	RedirectURI  string
	CodeVerifier string
}

// Token is what the provider gets for a code or a refresh: the consent
// token, how long it holds, the consent's ID and the data sets the citizen
// consented to. RefreshToken is set only by a code exchange, and only for a
// consent the register gave one for.
type Token struct {
	ConsentToken string
	ConsentID    string
	Scopes       []string
	// ExpiresIn is the time from now to the token's exp, never negative.
	ExpiresIn    time.Duration
	RefreshToken string
}

// ExchangeCode gives provider, the authenticated peer, the consent token
// for a code. A code works once, for the provider it was issued to, with
// the redirect URI of the request, before it expires and with the code
// verifier of the pushed challenge; otherwise the error wraps
// ErrInvalidGrant. A failed attempt leaves the code as it was.
//
// Two deliberate choices, both open in the standards:
//   - A code presented again after its exchange is refused, but the consent
//     it gave is not revoked. RFC 6749 §4.1.2 says the server SHOULD revoke
//     the tokens of a reused code. Risk: whoever has both the code and the
//     verifier (in practice the provider's backend) keeps a token that holds
//     until the citizen revokes the consent. Revoking would mean keeping
//     exchanged codes for a lifetime and calling the register; the demo does
//     not.
//   - A failed attempt does not burn the code. A verifier has at least 43
//     characters and the code lives a minute, so guessing is out of reach,
//     while burning would let anyone who sees the code (in the browser's
//     address bar) stop the provider from exchanging it.
func (p *Portal) ExchangeCode(_ context.Context, provider PeerID, request TokenRequest) (Token, error) {
	if p.Codes == nil {
		return Token{}, errors.New("no code store is configured")
	}
	issued, err := p.Codes.Redeem(request.Code, p.now(), func(issued IssuedCode) error {
		switch {
		case issued.Provider != provider:
			return fmt.Errorf("%w: the code was issued to another client", ErrInvalidGrant)
		case issued.RedirectURI != request.RedirectURI:
			return fmt.Errorf("%w: redirect_uri is not the one of the request", ErrInvalidGrant)
		case !verifies(request.CodeVerifier, issued.CodeChallenge):
			return fmt.Errorf("%w: the code verifier does not match the code challenge", ErrInvalidGrant)
		}
		return nil
	})
	if err != nil {
		return Token{}, err
	}
	return Token{
		ConsentToken: issued.Consent.ConsentToken,
		ConsentID:    issued.Consent.ConsentID,
		Scopes:       slices.Clone(issued.Scopes),
		ExpiresIn:    p.until(issued.Consent.TokenExpiresAt),
		RefreshToken: issued.Consent.RefreshToken,
	}, nil
}

// RefreshRequest is a client's refresh grant (RFC 6749 §6).
type RefreshRequest struct {
	RefreshToken string
	// Scopes is what the client asked for; empty means the whole consent.
	Scopes []string
}

// RefreshProof gives client, the authenticated FSC peer, a new consent token
// for the consent its refresh token belongs to (RFC 6749 §6). The register
// decides: the token must be one it issued to this client, for a consent that
// is active and has not ended, and a scope, when sent, must be the whole
// consent. A refusal returns an error wrapping ErrInvalidGrant or
// ErrInvalidScope.
//
// The refresh token is not rotated: the answer has no new one, and the
// client keeps using the token it got with the code. One token per consent,
// bound to the client's FSC peer, until the consent ends. RFC 6749 §6 allows
// it, and RFC 9700 §4.14.2 asks for rotation or sender-constraining only for
// public clients; a client here authenticates with its FSC identity.
func (p *Portal) RefreshProof(ctx context.Context, client PeerID, request RefreshRequest) (Token, error) {
	if request.RefreshToken == "" {
		return Token{}, fmt.Errorf("%w: no refresh token", ErrInvalidGrant)
	}
	if p.Consents == nil {
		return Token{}, errors.New("no consent register is configured")
	}
	proof, err := p.Consents.Refresh(ctx, request.RefreshToken, client, slices.Clone(request.Scopes))
	if err != nil {
		return Token{}, err
	}
	return Token{
		ConsentToken: proof.ConsentToken,
		ConsentID:    proof.ConsentID,
		Scopes:       slices.Clone(proof.Scopes),
		ExpiresIn:    p.until(proof.ExpiresAt),
	}, nil
}

// until is the time from now to at, or zero once at has passed.
func (p *Portal) until(at time.Time) time.Duration {
	return max(0, at.Sub(p.now()))
}

// verifies reports whether verifier is a code verifier whose S256
// challenge is challenge.
func verifies(verifier, challenge string) bool {
	if !codeVerifier.MatchString(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}

// issueCode keeps what a granted decision gave under a new code.
func (p *Portal) issueCode(issued IssuedCode) (string, error) {
	if p.Codes == nil {
		return "", errors.New("no code store is configured")
	}
	return p.Codes.Issue(issued, p.now())
}

// CodeStore keeps issued codes until they are exchanged or expire.
//
// Demo shortcut: the store is in memory, like the request store, so a
// restart of the backend drops every code that was not yet exchanged. A
// real deployment keeps them in a shared store.
type CodeStore struct {
	lifetime time.Duration
	mu       sync.Mutex
	codes    map[string]IssuedCode
}

// NewCodeStore returns an empty store whose codes expire after lifetime.
func NewCodeStore(lifetime time.Duration) *CodeStore {
	return &CodeStore{lifetime: lifetime, codes: map[string]IssuedCode{}}
}

// Issue keeps issued under a new, unguessable code and returns the code.
// Expired codes are dropped on the way.
func (s *CodeStore) Issue(issued IssuedCode, now time.Time) (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	code := base64.RawURLEncoding.EncodeToString(random)
	issued.ExpiresAt = now.Add(s.lifetime)

	s.mu.Lock()
	defer s.mu.Unlock()
	for key, kept := range s.codes {
		if !now.Before(kept.ExpiresAt) {
			delete(s.codes, key)
		}
	}
	s.codes[code] = issued
	return code, nil
}

// Redeem returns what code stands for and forgets the code, once accept
// returns nil for it. An unknown or expired code, or one accept returns an
// error for, is not redeemed; the error wraps ErrInvalidGrant or is
// accept's.
func (s *CodeStore) Redeem(code string, now time.Time, accept func(IssuedCode) error) (IssuedCode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	issued, found := s.codes[code]
	switch {
	case !found:
		return IssuedCode{}, fmt.Errorf("%w: the code is unknown or was already used", ErrInvalidGrant)
	case !now.Before(issued.ExpiresAt):
		delete(s.codes, code)
		return IssuedCode{}, fmt.Errorf("%w: the code has expired", ErrInvalidGrant)
	}
	if err := accept(issued); err != nil {
		return IssuedCode{}, err
	}
	delete(s.codes, code)
	return issued, nil
}
