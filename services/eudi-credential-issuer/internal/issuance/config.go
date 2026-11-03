package issuance

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/x509"
	"time"

	"gbo-demo/oid4vc-kit/x5c"
)

// Kind selects the rules of a wallet profile. Each provider is published
// under one credential issuer identifier per profile, so the metadata of
// each is conformant on its own (see the design choice on proof types).
type Kind string

const (
	// KindNL serves the NL Wallet: jwt proofs without key attestation, the
	// NL credential preview endpoint, no tx_code.
	KindNL Kind = "nl"
	// KindEUDI serves wallets that follow HAIP and the ARF: key attestation
	// is required, no preview endpoint, encrypted requests and responses
	// are supported.
	KindEUDI Kind = "eudi"
)

// Provider is an Attestation Provider: the party a wallet sees as the
// issuer, with its own access and registration certificates, its own
// signing keys and its own credential configurations. The issuer service
// hosts several.
type Provider struct {
	// Name is the first path segment of its issuer identifiers.
	Name string
	// AccessKey and AccessChain are the access certificate (WRPAC) that
	// signs the issuer metadata.
	AccessKey   crypto.Signer
	AccessChain []*x509.Certificate
	// RegistrationCertificate is the WRPRC published in issuer_info;
	// required when a KindNL profile exists, as the NL Wallet refuses
	// metadata without it.
	RegistrationCertificate []byte
	// IssuerKey and IssuerChain sign the credentials.
	IssuerKey   crypto.Signer
	IssuerChain []*x509.Certificate
	// StatusKey and StatusChain sign the status lists; the certificate has
	// the same subject as the issuer certificate.
	StatusKey   crypto.Signer
	StatusChain []*x509.Certificate
	// RequestEncryptionKey is the P-256 key wallets encrypt their credential
	// requests to (OpenID4VCI 1.0 section 10); required when a KindEUDI
	// profile exists, and published only there.
	RequestEncryptionKey *ecdsa.PrivateKey
}

// Profile is a set of wallet rules; every provider is published once per
// profile.
type Profile struct {
	// Name is the second path segment of the issuer identifiers.
	Name string
	Kind Kind
	// WalletProviders are the trust anchors for Wallet Instance
	// Attestations. A WIA from another provider is refused on this profile.
	WalletProviders []x5c.Anchors
	// KeyAttestationProviders are the trust anchors for key attestations;
	// required for KindEUDI.
	KeyAttestationProviders []x5c.Anchors
	// KeyStorage and UserAuthentication are the attack potential
	// resistance values (OpenID4VCI Appendix D.2) a key attestation must
	// assert; published in key_attestations_required (ARF ISSU_27d).
	// Required for KindEUDI.
	KeyStorage         []string
	UserAuthentication []string
	// UniversalLinkBase is the wallet's universal link base (KindNL).
	UniversalLinkBase string
}

// CredentialConfiguration is one credential a provider offers.
type CredentialConfiguration struct {
	ID string
	// Provider is the name of the provider that issues it.
	Provider string
	VCT      string
	Scope    string
	// Display and Claims become credential_metadata. Every attribute a
	// source returns must have a claim here.
	Display []Display
	Claims  []Claim
	// ValidDays is the validity of an issued credential.
	ValidDays int
	// StatusList names the status list its credentials are listed in.
	StatusList string
}

// Display is a localised name.
type Display struct {
	Name   string `json:"name"`
	Locale string `json:"locale"`
}

// Claim describes one attribute (credential_metadata.claims).
type Claim struct {
	Path      []string  `json:"path"`
	Mandatory bool      `json:"mandatory,omitempty"`
	Display   []Display `json:"display,omitempty"`
}

// Config is everything the issuer needs besides its ports.
type Config struct {
	// PublicURL is the external base URL; issuer identifiers are
	// PublicURL + "/" + provider + "/" + profile.
	PublicURL      string
	Providers      []Provider
	Profiles       []Profile
	Configurations []CredentialConfiguration
	StatusListSize int
	BatchSize      int
	OfferTTL       time.Duration
	TokenTTL       time.Duration
	NonceTTL       time.Duration
}

// Defaults for the durations and sizes a deployment does not set.
const (
	DefaultBatchSize      = 4
	DefaultOfferTTL       = 5 * time.Minute
	DefaultTokenTTL       = 10 * time.Minute
	DefaultNonceTTL       = 5 * time.Minute
	DefaultStatusListSize = 1 << 16
)
