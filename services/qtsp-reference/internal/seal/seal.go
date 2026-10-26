// Package seal validates the seal on an ASI result before the QTSP accepts
// it: a detached JWS (RFC 7515 appendix F) over the exact response body in
// X-JWS-Signature, ES256, with the certificate chain in x5c. The form is the
// GBO demo's choice; ETSI TS 119 478 defines none (CIR 2025/1569 art. 9(4),
// as amended by 2026/1735, only requires the seal).
package seal

import (
	"crypto/x509"
	"errors"
	"fmt"
	"strings"

	"github.com/go-jose/go-jose/v4"
)

// Header is the response header that carries the seal.
const Header = "X-JWS-Signature"

// Check is a valid seal: who sealed it.
type Check struct {
	// Signer is the subject of the seal certificate.
	Signer string
	// Organization is the organisation in that subject, a readable name of
	// who sealed it; empty when the certificate names none.
	Organization string
}

// Validate checks the detached JWS over body: its certificate chain must lead
// to one of anchors, and the signature must verify with the leaf key. An
// error says why the result must not be accepted.
func Validate(signature string, body []byte, anchors *x509.CertPool) (Check, error) {
	if signature == "" {
		return Check{}, errors.New("the response carries no seal (" + Header + ")")
	}
	jws, err := jose.ParseDetached(signature, body, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return Check{}, fmt.Errorf("the seal is not a detached ES256 JWS: %w", err)
	}
	if len(jws.Signatures) != 1 {
		return Check{}, errors.New("the seal must carry exactly one signature")
	}
	chains, err := jws.Signatures[0].Protected.Certificates(x509.VerifyOptions{
		Roots: anchors, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	if err != nil || len(chains) == 0 {
		return Check{}, fmt.Errorf("the seal certificate does not chain to a trust anchor: %v", err)
	}
	leaf := chains[0][0]
	if _, err := jws.Verify(leaf.PublicKey); err != nil {
		return Check{}, errors.New("the seal does not verify over the response body")
	}
	return Check{Signer: leaf.Subject.String(), Organization: strings.Join(leaf.Subject.Organization, ", ")}, nil
}
