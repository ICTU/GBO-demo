package issuance

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"

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

// offerKey is a disclosure offer key: one path segment, as the offer ids
// of the sources are (dots inside only, so never "." or "..").
var offerKey = regexp.MustCompile(`^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$`)

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
	// OpenID4VCI 1.0 requires TLS for every endpoint; http only on the local
	// machine, for development. TLS itself ends at the proxy in front.
	if u.Scheme != "https" && (u.Scheme != "http" || !localHost(u.Hostname())) {
		return errors.New("public URL must use https (http only for localhost)")
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
	if issuerName(p.AccessChain[0]) == "" {
		return errors.New("the access certificate has no organization name (O), which wallets show as the issuer")
	}
	if p.IssuerChain[0].Subject.String() != p.StatusChain[0].Subject.String() {
		return errors.New("the status list certificate must have the subject of the issuer certificate")
	}
	if (kinds[KindEUDI] || kinds[KindCombined]) && p.RequestEncryptionKey == nil {
		return errors.New("a request encryption key is required for the eudi profile")
	}
	if p.RequestEncryptionKey != nil && p.RequestEncryptionKey.Curve != elliptic.P256() {
		return errors.New("the request encryption key must be a P-256 key (ECDH-ES)")
	}
	if (kinds[KindNL] || kinds[KindCombined]) && len(p.RegistrationCertificate) == 0 {
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
	allOffers := append(append([]DisclosureOffer(nil), cfg.DisclosureOffers...), cfg.LoginOffers...)
	for i, o := range allOffers {
		if !offerKey.MatchString(o.Key) || keys[o.Provider+"/"+o.Key] {
			return fmt.Errorf("disclosure offer key %q must be unique per provider and only letters, digits, hyphens, underscores and inner dots", o.Key)
		}
		keys[o.Provider+"/"+o.Key] = true
		p, ok := providers[o.Provider]
		if !ok || (i < len(cfg.DisclosureOffers) && len(p.VerifierRegistrationCertificate) == 0) {
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
	if cfg.PIDTrust != nil && len(cfg.PIDAnchors) > 0 {
		return errors.New("configure either dynamic PID trust or static anchors, never both")
	}
	if (len(cfg.PIDAnchors) == 0 && cfg.PIDTrust == nil) || cfg.FetchPIDStatus == nil {
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
	case KindNL, KindEUDI, KindCombined:
	default:
		return fmt.Errorf("unknown kind %q", p.Kind)
	}
	for _, aki := range p.PIDTrustedAuthorities {
		if raw, err := base64.RawURLEncoding.DecodeString(aki); err != nil || len(raw) == 0 {
			return fmt.Errorf("PID trusted authority %q is not a base64url KeyIdentifier", aki)
		}
	}
	if p.servesEUDI() {
		if len(p.KeyAttestationProviders) == 0 {
			return errors.New("key attestation providers are required")
		}
		if len(p.KeyStorage) == 0 || len(p.UserAuthentication) == 0 {
			return errors.New("the required key storage and user authentication levels must be set (ARF ISSU_27d)")
		}
	}
	if len(p.WalletProviders) == 0 {
		return errors.New("wallet providers are required")
	}
	// Without them a combined profile serves the EUDI wallets only, until
	// the NL Wallet's provider is trusted.
	if p.Kind != KindCombined && len(p.PlainJWTWalletProviders) > 0 {
		return errors.New("the wallet providers whose wallets may skip key attestation are set on a combined profile only")
	}
	for _, name := range p.PlainJWTWalletProviders {
		if !slices.ContainsFunc(p.WalletProviders, func(a x5c.Anchors) bool { return a.Name == name }) {
			return fmt.Errorf("wallet provider %q may skip key attestation but is not a trusted wallet provider", name)
		}
	}
	names := map[string]bool{}
	for _, a := range p.WalletProviders {
		if a.Name == "" {
			return errors.New("every wallet provider anchor set needs a name")
		}
		if names[a.Name] {
			// The name tells which wallets may skip key attestation.
			return fmt.Errorf("wallet provider anchor set %q occurs twice", a.Name)
		}
		names[a.Name] = true
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
	if err := validateDisplays(c.Display); err != nil {
		return err
	}
	// Appendix B.3: a repeated claim description aborts processing.
	paths := map[string]bool{}
	for _, cl := range c.Claims {
		if len(cl.Path) == 0 {
			return errors.New("a claim path must not be empty")
		}
		key := strings.Join(cl.Path, "\x00")
		if paths[key] {
			return fmt.Errorf("claim %v is described twice", cl.Path)
		}
		paths[key] = true
		if err := validateDisplays(cl.Display); err != nil {
			return fmt.Errorf("claim %v: %w", cl.Path, err)
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

// validateDisplays holds displays to OpenID4VCI 1.0 section 12.2.4 and
// Appendix B.2: each has a name, and one object per language.
func validateDisplays(ds []Display) error {
	locales := map[string]bool{}
	for _, d := range ds {
		if d.Name == "" {
			return errors.New("a display needs a name")
		}
		if locales[d.Locale] {
			return fmt.Errorf("two displays for locale %q", d.Locale)
		}
		locales[d.Locale] = true
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

// localHost tells whether host is the local machine.
func localHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
