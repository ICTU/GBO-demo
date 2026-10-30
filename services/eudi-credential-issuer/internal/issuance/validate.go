package issuance

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"unicode"

	"gbo-demo/oid4vc-kit/x5c"
)

func validate(cfg Config) error {
	if err := validateProcess(cfg); err != nil {
		return err
	}
	if len(cfg.Profiles) == 0 || len(cfg.Configurations) == 0 {
		return errors.New("at least one profile and one credential configuration are required")
	}
	names := map[string]bool{}
	for _, p := range cfg.Profiles {
		if p.Name == "" || names[p.Name] || p.Name == "status" || p.Name == ".well-known" {
			return fmt.Errorf("profile name %q is empty, reserved or used twice", p.Name)
		}
		names[p.Name] = true
		if err := validateProfile(p); err != nil {
			return fmt.Errorf("profile %q: %w", p.Name, err)
		}
	}
	ids := map[string]bool{}
	scopes := map[string]bool{}
	for _, c := range cfg.Configurations {
		if c.ID == "" || ids[c.ID] {
			return fmt.Errorf("credential configuration id %q is empty or used twice", c.ID)
		}
		ids[c.ID] = true
		// HAIP 1.0 section 4.3: a scope maps to one credential type.
		if scopes[c.Scope] {
			return fmt.Errorf("credential configuration %q: scope %q is used twice", c.ID, c.Scope)
		}
		scopes[c.Scope] = true
		if err := validateConfiguration(c); err != nil {
			return fmt.Errorf("credential configuration %q: %w", c.ID, err)
		}
	}
	return nil
}

// validateProcess checks the public URL, the sizes and durations and the
// issuer and status signing material.
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
	if cfg.BatchSize < 0 || cfg.StatusListSize < 0 || cfg.OfferTTL < 0 || cfg.TokenTTL < 0 || cfg.NonceTTL < 0 {
		return errors.New("sizes and durations must not be negative")
	}
	for name, key := range map[string]crypto.Signer{"issuer": cfg.IssuerKey, "status": cfg.StatusKey} {
		if err := checkKey(name, key); err != nil {
			return err
		}
	}
	if err := checkChain("issuer", cfg.IssuerChain); err != nil {
		return err
	}
	if err := checkChain("status", cfg.StatusChain); err != nil {
		return err
	}
	if cfg.IssuerChain[0].Subject.String() != cfg.StatusChain[0].Subject.String() {
		return errors.New("the status list certificate must have the subject of the issuer certificate")
	}
	return nil
}

func validateProfile(p Profile) error {
	switch p.Kind {
	case KindNL:
		if len(p.RegistrationCertificate) == 0 {
			return errors.New("the NL Wallet refuses metadata without a registration certificate")
		}
		if bytes.ContainsFunc(p.RegistrationCertificate, unicode.IsSpace) {
			return errors.New("the registration certificate must not contain whitespace")
		}
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
	if err := checkKey("access", p.AccessKey); err != nil {
		return err
	}
	return checkChain("access", p.AccessChain)
}

func validateConfiguration(c CredentialConfiguration) error {
	if c.VCT == "" || c.StatusList == "" || c.ValidDays <= 0 {
		return errors.New("vct, status list and valid days are required")
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
