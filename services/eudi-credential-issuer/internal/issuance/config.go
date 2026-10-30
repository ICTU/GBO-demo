package issuance

import (
	"crypto"
	"crypto/x509"
	"time"

	"gbo-demo/oid4vc-kit/x5c"
)

// Kind selects the rules of a wallet profile. Each profile is published
// under its own credential issuer identifier, so the metadata of each is
// conformant on its own (see the design choice on proof types).
type Kind string

const (
	// KindNL serves the NL Wallet: jwt proofs without key attestation, the
	// NL credential preview endpoint, no tx_code.
	KindNL Kind = "nl"
	// KindEUDI serves wallets that follow HAIP and the ARF: key attestation
	// is required, no preview endpoint.
	KindEUDI Kind = "eudi"
)

// Profile is one credential issuer identifier with its wallet rules.
type Profile struct {
	// Name is the path segment of the issuer identifier.
	Name string
	Kind Kind
	// WalletProviders are the trust anchors for Wallet Instance
	// Attestations. A WIA from another provider is refused on this profile.
	WalletProviders []x5c.Anchors
	// KeyAttestationProviders are the trust anchors for key attestations;
	// required for KindEUDI.
	KeyAttestationProviders []x5c.Anchors
	// AccessKey and AccessChain are the access certificate (WRPAC) that
	// signs the issuer metadata.
	AccessKey   crypto.Signer
	AccessChain []*x509.Certificate
	// RegistrationCertificate is the WRPRC published in issuer_info;
	// required for KindNL, which refuses metadata without it.
	RegistrationCertificate []byte
	// KeyStorage and UserAuthentication are the attack potential
	// resistance values (OpenID4VCI Appendix D.2) a key attestation must
	// assert; published in key_attestations_required (ARF ISSU_27d).
	// Required for KindEUDI.
	KeyStorage         []string
	UserAuthentication []string
	// UniversalLinkBase is the wallet's universal link base (KindNL).
	UniversalLinkBase string
}

// CredentialConfiguration is one credential the issuer offers.
type CredentialConfiguration struct {
	ID    string
	VCT   string
	Scope string
	// Display and Claims become credential_metadata. Every attribute a
	// source returns must have a claim here.
	Display []Display
	Claims  []Claim
	// ValidDays is the validity of an issued credential.
	ValidDays int
	// SourceURL is the bronpoort endpoint for this configuration.
	SourceURL string
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
	// PublicURL + "/" + profile name.
	PublicURL      string
	Profiles       []Profile
	Configurations []CredentialConfiguration
	// IssuerKey and IssuerChain sign the credentials.
	IssuerKey   crypto.Signer
	IssuerChain []*x509.Certificate
	// StatusKey and StatusChain sign the status lists; the certificate has
	// the same subject as the issuer certificate.
	StatusKey      crypto.Signer
	StatusChain    []*x509.Certificate
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
