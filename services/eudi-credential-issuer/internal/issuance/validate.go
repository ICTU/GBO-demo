package issuance

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"

	"gbo-demo/oid4vc-kit/x5c"
)

func validate(cfg Config) error {
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		// The well-known URLs are served at the root, so the public URL
		// cannot have a path.
		return errors.New("public URL must be an origin without path, query or trailing slash")
	}
	if u.Path == "/" {
		return errors.New("public URL must not end with a slash")
	}
	if cfg.IssuerKey == nil || cfg.StatusKey == nil {
		return errors.New("issuer and status signing keys are required")
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
	for _, c := range cfg.Configurations {
		if c.ID == "" || ids[c.ID] {
			return fmt.Errorf("credential configuration id %q is empty or used twice", c.ID)
		}
		ids[c.ID] = true
		if err := validateConfiguration(c); err != nil {
			return fmt.Errorf("credential configuration %q: %w", c.ID, err)
		}
	}
	return nil
}

func validateProfile(p Profile) error {
	switch p.Kind {
	case KindNL:
		if len(p.RegistrationCertificate) == 0 {
			return errors.New("the NL Wallet refuses metadata without a registration certificate")
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
	if len(p.WalletProviders) == 0 || p.AccessKey == nil {
		return errors.New("wallet providers and an access certificate are required")
	}
	return checkChain("access", p.AccessChain)
}

func validateConfiguration(c CredentialConfiguration) error {
	if c.VCT == "" || c.SourceURL == "" || c.StatusList == "" || c.ValidDays <= 0 {
		return errors.New("vct, source URL, status list and valid days are required")
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
