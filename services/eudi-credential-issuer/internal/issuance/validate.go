package issuance

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"slices"

	"gbo-demo/oid4vc-kit/x5c"
)

func validate(cfg Config) error {
	if err := validateProcess(cfg); err != nil {
		return err
	}
	if len(cfg.Providers) == 0 || len(cfg.Profiles) == 0 || len(cfg.Configurations) == 0 {
		return errors.New("at least one provider, one profile and one credential configuration are required")
	}
	kinds := map[Kind]bool{}
	names := map[string]bool{}
	for _, p := range cfg.Profiles {
		if !validName(p.Name) || names[p.Name] {
			return fmt.Errorf("profile name %q must be unique and only lower-case letters, digits and hyphens", p.Name)
		}
		names[p.Name] = true
		kinds[p.Kind] = true
		if err := validateProfile(p); err != nil {
			return fmt.Errorf("profile %q: %w", p.Name, err)
		}
	}
	vcts := map[string][]string{}
	for _, c := range cfg.Configurations {
		vcts[c.Provider] = append(vcts[c.Provider], c.VCT)
	}
	providers := map[string]bool{}
	for _, p := range cfg.Providers {
		if !validName(p.Name) || providers[p.Name] {
			return fmt.Errorf("provider name %q must be unique and only lower-case letters, digits and hyphens", p.Name)
		}
		providers[p.Name] = true
		if len(vcts[p.Name]) == 0 {
			return fmt.Errorf("provider %q has no credential configurations", p.Name)
		}
		if err := validateProvider(p, kinds, vcts[p.Name]); err != nil {
			return fmt.Errorf("provider %q: %w", p.Name, err)
		}
	}
	ids := map[string]bool{}
	scopes := map[string]bool{}
	for _, c := range cfg.Configurations {
		if c.ID == "" || ids[c.ID] {
			return fmt.Errorf("credential configuration id %q is empty or used twice", c.ID)
		}
		ids[c.ID] = true
		if !providers[c.Provider] {
			return fmt.Errorf("credential configuration %q: unknown provider %q", c.ID, c.Provider)
		}
		// HAIP 1.0 section 4.3: a scope maps to one credential type.
		if scopes[c.Scope] {
			return fmt.Errorf("credential configuration %q: scope %q is used twice", c.ID, c.Scope)
		}
		scopes[c.Scope] = true
		if err := validateConfiguration(c); err != nil {
			return fmt.Errorf("credential configuration %q: %w", c.ID, err)
		}
	}
	return validateDisclosure(cfg)
}

// validName allows lower-case letters, digits and hyphens, so that a name is
// one path segment, cannot collide with the fixed routes and keeps the
// status list key "provider/list" unambiguous.
func validName(name string) bool {
	if name == "" || name == "status" {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// validateProcess checks the public URL and the sizes and durations.
func validateProcess(cfg Config) error {
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		// The well-known URLs are served at the root, so the public URL
		// cannot have a path.
		return errors.New("public URL must be an origin without path, query or trailing slash")
	}
	if u.Path == "/" {
		return errors.New("public URL must not end with a slash")
	}
	if cfg.BatchSize < 0 || cfg.StatusListSize < 0 || cfg.OfferTTL < 0 || cfg.TokenTTL < 0 || cfg.NonceTTL < 0 || cfg.DisclosureTTL < 0 {
		return errors.New("sizes and durations must not be negative")
	}
	return nil
}

// validateProvider checks a provider's signing material, its request
// encryption key, which a KindEUDI profile requires, and its registration
// certificate, which a KindNL profile requires.
func validateProvider(p Provider, kinds map[Kind]bool, vcts []string) error {
	for name, key := range map[string]crypto.Signer{"access": p.AccessKey, "issuer": p.IssuerKey, "status": p.StatusKey} {
		if err := checkKey(name, key); err != nil {
			return err
		}
	}
	for name, chain := range map[string][]*x509.Certificate{"access": p.AccessChain, "issuer": p.IssuerChain, "status": p.StatusChain} {
		if err := checkChain(name, chain); err != nil {
			return err
		}
	}
	if p.IssuerChain[0].Subject.String() != p.StatusChain[0].Subject.String() {
		return errors.New("the status list certificate must have the subject of the issuer certificate")
	}
	if kinds[KindEUDI] && p.RequestEncryptionKey == nil {
		return errors.New("a request encryption key is required for the eudi profile")
	}
	if p.RequestEncryptionKey != nil && p.RequestEncryptionKey.Curve != elliptic.P256() {
		return errors.New("the request encryption key must be a P-256 key (ECDH-ES)")
	}
	if kinds[KindNL] && len(p.RegistrationCertificate) == 0 {
		return errors.New("the NL Wallet refuses metadata without a registration certificate")
	}
	if len(p.VerifierRegistrationCertificate) > 0 {
		if err := checkVerifierRegistrationCertificate(p.VerifierRegistrationCertificate, p.AccessChain[0]); err != nil {
			return err
		}
	}
	if len(p.RegistrationCertificate) == 0 {
		return nil
	}
	return checkRegistrationCertificate(p.RegistrationCertificate, p.AccessChain[0], vcts)
}

// validateDisclosure checks the disclosure offers: unique keys, a provider
// that can ask for the PID, and configurations of that provider. With any
// offer, PIDs need trust anchors with unique names and a status fetcher.
func validateDisclosure(cfg Config) error {
	providers := map[string]Provider{}
	for _, p := range cfg.Providers {
		providers[p.Name] = p
	}
	configs := map[string]string{}
	for _, c := range cfg.Configurations {
		configs[c.ID] = c.Provider
	}
	keys := map[string]bool{}
	for _, o := range cfg.DisclosureOffers {
		if !validName(o.Key) || keys[o.Key] {
			return fmt.Errorf("disclosure offer key %q must be unique and only lower-case letters, digits and hyphens", o.Key)
		}
		keys[o.Key] = true
		p, ok := providers[o.Provider]
		if !ok || len(p.VerifierRegistrationCertificate) == 0 {
			return fmt.Errorf("disclosure offer %q: provider %q is unknown or has no verifier registration certificate", o.Key, o.Provider)
		}
		if len(o.ConfigurationIDs) == 0 || len(uniq(o.ConfigurationIDs)) != len(o.ConfigurationIDs) {
			return fmt.Errorf("disclosure offer %q needs credential configurations, each once", o.Key)
		}
		for _, id := range o.ConfigurationIDs {
			if configs[id] != o.Provider {
				return fmt.Errorf("disclosure offer %q: credential configuration %q is not one of provider %q", o.Key, id, o.Provider)
			}
		}
	}
	if len(cfg.DisclosureOffers) == 0 {
		return nil
	}
	if len(cfg.PIDAnchors) == 0 || cfg.FetchPIDStatus == nil {
		return errors.New("disclosure offers need PID trust anchors and a status list fetcher")
	}
	names := map[string]bool{}
	for _, a := range cfg.PIDAnchors {
		if a.Name == "" || names[a.Name] || a.Roots == nil {
			return fmt.Errorf("PID trust anchor set %q must have a unique name and roots", a.Name)
		}
		names[a.Name] = true
	}
	return nil
}

func validateProfile(p Profile) error {
	switch p.Kind {
	case KindNL:
	case KindEUDI:
		if len(p.KeyAttestationProviders) == 0 {
			return errors.New("key attestation providers are required")
		}
		if len(p.KeyStorage) == 0 || len(p.UserAuthentication) == 0 {
			return errors.New("the required key storage and user authentication levels must be set (ARF ISSU_27d)")
		}
	default:
		return fmt.Errorf("unknown kind %q", p.Kind)
	}
	if len(p.WalletProviders) == 0 {
		return errors.New("wallet providers are required")
	}
	return nil
}

func validateConfiguration(c CredentialConfiguration) error {
	if c.VCT == "" || c.ValidDays <= 0 {
		return errors.New("vct and valid days are required")
	}
	if !validName(c.StatusList) {
		return fmt.Errorf("status list name %q is empty or not lower-case letters, digits and hyphens", c.StatusList)
	}
	// HAIP 1.0 section 4.1: a scope for every credential configuration.
	if c.Scope == "" {
		return errors.New("a scope is required")
	}
	if len(c.Display) == 0 || len(c.Claims) == 0 {
		return errors.New("display and claims are required")
	}
	for _, cl := range c.Claims {
		if len(cl.Path) == 0 {
			return errors.New("a claim path must not be empty")
		}
		for _, seg := range cl.Path {
			if seg == "" {
				return errors.New("a claim path must not have empty segments")
			}
		}
		// A wallet checks mandatory claims against leaf attributes (the NL
		// Wallet does), so a mandatory claim must not be an object that
		// other claims describe the inside of.
		if cl.Mandatory {
			for _, other := range c.Claims {
				if len(other.Path) > len(cl.Path) && slices.Equal(other.Path[:len(cl.Path)], cl.Path) {
					return fmt.Errorf("mandatory claim %v is an object; make its leaves mandatory instead", cl.Path)
				}
			}
		}
	}
	return nil
}

// checkKey requires a P-256 ECDSA key: everything is signed with ES256.
func checkKey(name string, key crypto.Signer) error {
	if key == nil {
		return fmt.Errorf("%s signing key is required", name)
	}
	pub, ok := key.Public().(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return fmt.Errorf("%s signing key must be a P-256 ECDSA key (ES256)", name)
	}
	return nil
}

// checkChain refuses an empty chain and a chain with a self-signed
// certificate: HAIP 1.0 forbids the trust anchor in x5c and self-signed
// signing certificates.
func checkChain(name string, chain []*x509.Certificate) error {
	if len(chain) == 0 {
		return fmt.Errorf("%s certificate chain is required", name)
	}
	for _, c := range chain {
		if x5c.SelfSigned(c) {
			return fmt.Errorf("%s certificate chain must not contain a self-signed certificate", name)
		}
	}
	return nil
}
