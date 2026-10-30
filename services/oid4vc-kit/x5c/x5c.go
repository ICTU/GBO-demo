// Package x5c verifies the certificate chain that a JOSE header carries in
// x5c (RFC 7515 section 4.1.6) against a set of trust anchors, and builds
// that header for signing.
package x5c

import (
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"errors"
	"slices"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// Anchors is a named set of root certificates. The name says which anchor
// accepted a chain, so a caller can tell trust domains apart (for example
// the wallet providers of different wallet profiles).
type Anchors struct {
	Name  string
	Roots *x509.CertPool
}

// NewAnchors returns anchors with the given root certificates.
func NewAnchors(name string, roots ...*x509.Certificate) Anchors {
	pool := x509.NewCertPool()
	for _, r := range roots {
		pool.AddCert(r)
	}
	return Anchors{Name: name, Roots: pool}
}

// Verify checks the x5c chain in h against the anchors at time now and
// returns the leaf certificate and the name of the anchor set that accepted
// it. When eku is non-empty the leaf must carry that extended key usage;
// unknown OIDs are matched against the leaf's UnknownExtKeyUsage.
func Verify(h jose.Header, anchors []Anchors, now time.Time, eku asn1.ObjectIdentifier) (*x509.Certificate, string, error) {
	for _, a := range anchors {
		chains, err := h.Certificates(x509.VerifyOptions{
			Roots:       a.Roots,
			CurrentTime: now,
			KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		})
		if err != nil || len(chains) == 0 {
			continue
		}
		leaf := chains[0][0]
		if SelfSigned(leaf) {
			return nil, "", errors.New("x5c: the signing certificate must not be self-signed")
		}
		if len(eku) > 0 && !hasEKU(leaf, eku) {
			return nil, "", errors.New("x5c: leaf certificate lacks the required extended key usage")
		}
		return leaf, a.Name, nil
	}
	return nil, "", errors.New("x5c: chain does not verify against any trust anchor")
}

// SelfSigned reports whether cert is signed by its own key, as a root is.
// HAIP 1.0 forbids self-signed signing certificates and trust anchors in
// x5c.
func SelfSigned(cert *x509.Certificate) bool {
	return cert.Subject.String() == cert.Issuer.String() && cert.CheckSignatureFrom(cert) == nil
}

func hasEKU(cert *x509.Certificate, oid asn1.ObjectIdentifier) bool {
	return slices.ContainsFunc(cert.UnknownExtKeyUsage, oid.Equal)
}

// Header returns the x5c header value for a chain, leaf first, as base64
// (not base64url) DER per RFC 7515 section 4.1.6.
func Header(chain []*x509.Certificate) []string {
	out := make([]string, len(chain))
	for i, c := range chain {
		out[i] = base64.StdEncoding.EncodeToString(c.Raw)
	}
	return out
}
