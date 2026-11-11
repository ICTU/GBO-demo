// Package issuance is the core of the credential issuer: the use cases of
// OpenID4VCI 1.0 as profiled by HAIP 1.0, independent of HTTP and storage.
// It knows nothing about the backends behind the bronpoort.
package issuance

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"time"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/oid4vp"
	"gbo-demo/oid4vc-kit/sdjwtvc"
	"gbo-demo/oid4vc-kit/x5c"
)

// Service implements the issuer's use cases.
type Service struct {
	cfg       Config
	providers map[string]*provider
	profiles  map[string]Profile
	configs   map[string]CredentialConfiguration
	// disclosureOffers are keyed by provider and key; verifier checks the
	// responses to their requests.
	disclosureOffers map[string]DisclosureOffer
	loginOffers      map[string]DisclosureOffer
	verifier         oid4vp.Verifier
	source           Source
	sessions         Sessions
	onetime          OneTime
	status           StatusLists
	now              func() time.Time
}

// New validates cfg and returns the service. A configuration the wallets of
// a profile would refuse is refused here, at startup.
func New(cfg Config, source Source, sessions Sessions, onetime OneTime, status StatusLists, now func() time.Time) (*Service, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}
	setDefaults(&cfg)
	s := &Service{cfg: cfg, providers: map[string]*provider{}, profiles: map[string]Profile{},
		configs: map[string]CredentialConfiguration{}, disclosureOffers: map[string]DisclosureOffer{}, loginOffers: map[string]DisclosureOffer{},
		verifier: oid4vp.Verifier{Anchors: cfg.PIDAnchors, FetchStatus: cfg.FetchPIDStatus},
		source:   source, sessions: sessions, onetime: onetime, status: status, now: now}
	for _, p := range cfg.Providers {
		signer, err := sdjwtvc.NewSigner(p.IssuerKey, x5c.Header(p.IssuerChain))
		if err != nil {
			return nil, err
		}
		prov := &provider{Provider: p, signer: signer}
		if p.RequestEncryptionKey != nil {
			// The kid is the key's JWK thumbprint, so it changes with the key.
			kid, err := thumbprint(jose.JSONWebKey{Key: p.RequestEncryptionKey.Public()})
			if err != nil {
				return nil, err
			}
			prov.requestKey = oid4vci.RequestKey{KeyID: kid, Key: p.RequestEncryptionKey}
		}
		if len(p.VerifierRegistrationCertificate) > 0 {
			// The access certificate also signs the provider's OpenID4VP
			// requests; its x509_hash is the client_id.
			if prov.requestSigner, err = oid4vp.NewRequestSigner(p.AccessKey, p.AccessChain); err != nil {
				return nil, err
			}
		}
		s.providers[p.Name] = prov
	}
	for _, p := range cfg.Profiles {
		s.profiles[p.Name] = p
	}
	for _, c := range cfg.Configurations {
		s.configs[c.ID] = c
	}
	for _, o := range cfg.DisclosureOffers {
		s.disclosureOffers[o.Provider+"/"+o.Key] = o
		s.loginOffers[o.Provider+"/"+o.Key] = o
	}
	for _, o := range cfg.LoginOffers {
		s.loginOffers[o.Provider+"/"+o.Key] = o
	}
	return s, nil
}

// provider is a configured provider with its credential signer, its
// request encryption key and its OpenID4VP request signer, if any.
type provider struct {
	Provider
	signer        *sdjwtvc.Signer
	requestKey    oid4vci.RequestKey
	requestSigner *oid4vp.RequestSigner
}

// Target names a credential issuer identifier: a provider under a profile.
type Target struct {
	Provider string
	Profile  string
}

// resolve returns the provider and profile of t, or ErrUnknownProvider or
// ErrUnknownProfile.
func (s *Service) resolve(t Target) (*provider, Profile, error) {
	prov, ok := s.providers[t.Provider]
	if !ok {
		return nil, Profile{}, ErrUnknownProvider
	}
	p, ok := s.profiles[t.Profile]
	if !ok {
		return nil, Profile{}, ErrUnknownProfile
	}
	return prov, p, nil
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
	if cfg.DisclosureTTL == 0 {
		cfg.DisclosureTTL = DefaultDisclosureTTL
	}
	if cfg.RefreshTTL == 0 {
		cfg.RefreshTTL = DefaultRefreshTTL
	}
	if cfg.DeferralTTL == 0 {
		cfg.DeferralTTL = DefaultDeferralTTL
	}
}

// Identifier is the credential issuer identifier of a target; it is also
// the authorization server's issuer identifier.
func (s *Service) Identifier(t Target) string {
	return s.cfg.PublicURL + "/" + t.Provider + "/" + t.Profile
}

// Error is a refusal with an OAuth/OpenID4VCI error code.
type Error struct {
	Code        string
	Description string
	// Challenge is a fresh attestation challenge, sent with
	// use_attestation_challenge.
	Challenge string
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
	// kindNotification holds the notification ids handed out per session.
	kindNotification = "notification"
)

// scoped binds a single-use kind to one issuer identifier, so that a
// c_nonce or attestation challenge handed out by one provider and profile
// is not accepted by another.
func scoped(kind, issuer string) string { return kind + " " + issuer }

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
