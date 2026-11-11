package issuance

import (
	"bytes"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"unicode"

	"gbo-demo/oid4vc-kit/sdjwtvc"
)

// oidOrganizationIdentifier is the X.520 organizationIdentifier attribute
// (2.5.4.97) that a legal person's access certificate carries.
var oidOrganizationIdentifier = asn1.ObjectIdentifier{2, 5, 4, 97}

// registrationCertificate is the part of a WRPRC payload (ETSI TS 119 475)
// the issuer checks at startup.
type registrationCertificate struct {
	Sub                  string `json:"sub"`
	ProvidesAttestations []struct {
		Meta struct {
			VCTValues []string `json:"vct_values"`
		} `json:"meta"`
	} `json:"provides_attestations"`
	// Credentials are what a relying party may ask for; NL Wallet v0.6
	// names the claims of each "claim".
	Credentials []struct {
		Format string `json:"format"`
		Meta   struct {
			VCTValues []string `json:"vct_values"`
		} `json:"meta"`
		// Path elements may be numbers or null (DCQL); values restrict
		// what may be asked.
		Claim []struct {
			Path   []any `json:"path"`
			Values []any `json:"values"`
		} `json:"claim"`
	} `json:"credentials"`
}

// checkRegistrationCertificate reads the provider's WRPRC without verifying
// its signature (the wallet does that) and refuses one that a wallet would
// refuse for this provider: its subject must be the organizationIdentifier
// of the access certificate, and it must name every vct the provider
// issues. Only the JWT form is read.
func checkRegistrationCertificate(raw []byte, access *x509.Certificate, vcts []string) error {
	rc, err := readRegistrationCertificate(raw, access)
	if err != nil {
		return err
	}
	var provided []string
	for _, a := range rc.ProvidesAttestations {
		provided = append(provided, a.Meta.VCTValues...)
	}
	for _, vct := range vcts {
		if !slices.Contains(provided, vct) {
			return fmt.Errorf("the registration certificate does not list vct %q in provides_attestations", vct)
		}
	}
	return nil
}

// checkVerifierRegistrationCertificate refuses a WRPRC for the verifier
// role that the NL Wallet would refuse for the PID query: bound to another
// organization, or not authorizing the bsn of the PID in dc+sd-jwt.
func checkVerifierRegistrationCertificate(raw []byte, access *x509.Certificate) error {
	rc, err := readRegistrationCertificate(raw, access)
	if err != nil {
		return err
	}
	for _, c := range rc.Credentials {
		if c.Format != sdjwtvc.Type || !slices.Contains(c.Meta.VCTValues, PIDType) {
			continue
		}
		for _, cl := range c.Claim {
			// With values, the NL Wallet refuses a request without them.
			if len(cl.Path) == 1 && cl.Path[0] == "bsn" && len(cl.Values) == 0 {
				return nil
			}
		}
	}
	return fmt.Errorf("the verifier registration certificate does not authorize the bsn of %s", PIDType)
}

// readRegistrationCertificate reads a WRPRC in JWT form without verifying
// its signature and requires its subject to be the organizationIdentifier
// of the access certificate.
func readRegistrationCertificate(raw []byte, access *x509.Certificate) (registrationCertificate, error) {
	if bytes.ContainsFunc(raw, unicode.IsSpace) {
		return registrationCertificate{}, errors.New("the registration certificate must not contain whitespace")
	}
	parts := bytes.Split(raw, []byte("."))
	if len(parts) != 3 {
		return registrationCertificate{}, errors.New("the registration certificate is not a compact JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(string(parts[1]))
	if err != nil {
		return registrationCertificate{}, fmt.Errorf("registration certificate payload: %w", err)
	}
	var rc registrationCertificate
	if err := json.Unmarshal(payload, &rc); err != nil {
		return registrationCertificate{}, fmt.Errorf("registration certificate payload: %w", err)
	}
	org := organizationIdentifier(access)
	if org == "" {
		return registrationCertificate{}, errors.New("the access certificate has no organizationIdentifier to bind the registration certificate to")
	}
	if rc.Sub != org {
		return registrationCertificate{}, fmt.Errorf("the registration certificate subject %q is not the access certificate organizationIdentifier %q", rc.Sub, org)
	}
	return rc, nil
}

func organizationIdentifier(c *x509.Certificate) string {
	for _, n := range c.Subject.Names {
		if n.Type.Equal(oidOrganizationIdentifier) {
			if s, ok := n.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}
