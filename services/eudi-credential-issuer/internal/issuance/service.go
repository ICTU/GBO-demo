// Package issuance is the core of the credential issuer: the use cases of
// OpenID4VCI 1.0 as profiled by HAIP 1.0, independent of HTTP and storage.
// It knows nothing about the backends behind the bronpoort.
package issuance

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"time"

	"gbo-demo/oid4vc-kit/sdjwtvc"
	"gbo-demo/oid4vc-kit/x5c"
)

// Service implements the issuer's use cases.
type Service struct {
	cfg      Config
	profiles map[string]Profile
	configs  map[string]CredentialConfiguration
	signer   *sdjwtvc.Signer
	source   Source
	sessions Sessions
	onetime  OneTime
	status   StatusLists
	now      func() time.Time
}

// New validates cfg and returns the service. A configuration the wallets of
// a profile would refuse is refused here, at startup.
func New(cfg Config, source Source, sessions Sessions, onetime OneTime, status StatusLists, now func() time.Time) (*Service, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}
	setDefaults(&cfg)
	s := &Service{cfg: cfg, profiles: map[string]Profile{}, configs: map[string]CredentialConfiguration{},
		source: source, sessions: sessions, onetime: onetime, status: status, now: now}
	for _, p := range cfg.Profiles {
		s.profiles[p.Name] = p
	}
	for _, c := range cfg.Configurations {
		s.configs[c.ID] = c
	}
	signer, err := sdjwtvc.NewSigner(cfg.IssuerKey, x5c.Header(cfg.IssuerChain))
	if err != nil {
		return nil, err
	}
	s.signer = signer
	return s, nil
}

func setDefaults(cfg *Config) {
	if cfg.BatchSize == 0 {
		cfg.BatchSize = DefaultBatchSize
	}
	if cfg.OfferTTL == 0 {
		cfg.OfferTTL = DefaultOfferTTL
	}
	if cfg.TokenTTL == 0 {
		cfg.TokenTTL = DefaultTokenTTL
	}
	if cfg.NonceTTL == 0 {
		cfg.NonceTTL = DefaultNonceTTL
	}
	if cfg.StatusListSize == 0 {
		cfg.StatusListSize = DefaultStatusListSize
	}
}

// Identifier is the credential issuer identifier of a profile; it is also
// the authorization server's issuer identifier.
func (s *Service) Identifier(profile string) string {
	return s.cfg.PublicURL + "/" + profile
}

// Profile returns a known profile.
func (s *Service) Profile(name string) (Profile, bool) {
	p, ok := s.profiles[name]
	return p, ok
}

// Error is a refusal with an OAuth/OpenID4VCI error code.
type Error struct {
	Code        string
	Description string
}

func (e *Error) Error() string { return e.Code + ": " + e.Description }

func refuse(code, description string) error {
	return &Error{Code: code, Description: description}
}

// Kinds of single-use values.
const (
	kindNonce     = "c_nonce"
	kindChallenge = "attestation_challenge"
	kindDPoP      = "dpop_jti"
	kindPoP       = "attestation_pop_jti"
	kindPreview   = "preview"
)

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hash is how codes and tokens are stored: only their SHA-256.
func hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
