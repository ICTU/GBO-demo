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
}

// checkRegistrationCertificate reads the provider's WRPRC without verifying
// its signature (the wallet does that) and refuses one that a wallet would
// refuse for this provider: its subject must be the organizationIdentifier
// of the access certificate, and it must name every vct the provider
// issues. Only the JWT form is read.
func checkRegistrationCertificate(raw []byte, access *x509.Certificate, vcts []string) error {
	parts := bytes.Split(raw, []byte("."))
	if len(parts) != 3 {
		return errors.New("the registration certificate is not a compact JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(string(parts[1]))
	if err != nil {
		return fmt.Errorf("registration certificate payload: %w", err)
	}
	var rc registrationCertificate
	if err := json.Unmarshal(payload, &rc); err != nil {
		return fmt.Errorf("registration certificate payload: %w", err)
	}
	org := organizationIdentifier(access)
	if org == "" {
		return errors.New("the access certificate has no organizationIdentifier to bind the registration certificate to")
	}
	if rc.Sub != org {
		return fmt.Errorf("the registration certificate subject %q is not the access certificate organizationIdentifier %q", rc.Sub, org)
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
