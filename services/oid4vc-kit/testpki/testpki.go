// Package testpki creates throwaway certificate chains for tests. It is not
// meant for anything else: keys live in memory and nothing is revocable.
package testpki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"math/big"
	"testing"
	"time"
)

// Cert is a certificate with its private key and the chain up to (not
// including) the root, leaf first.
type Cert struct {
	Cert  *x509.Certificate
	Key   *ecdsa.PrivateKey
	Chain []*x509.Certificate
}

// Options shape a leaf certificate.
type Options struct {
	Subject pkix.Name
	DNS     []string
	EKU     []asn1.ObjectIdentifier
}

// NewRoot returns a self-signed P-256 root.
func NewRoot(t testing.TB, cn string) Cert {
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return Cert{Cert: cert, Key: key}
}

// Issue returns a leaf signed by parent.
func (parent Cert) Issue(t testing.TB, opts Options) Cert {
	t.Helper()
	return parent.issue(t, &x509.Certificate{
		Subject:            opts.Subject,
		DNSNames:           opts.DNS,
		KeyUsage:           x509.KeyUsageDigitalSignature,
		UnknownExtKeyUsage: opts.EKU,
	})
}

// IssueCA returns an intermediate CA signed by parent.
func (parent Cert) IssueCA(t testing.TB, cn string) Cert {
	t.Helper()
	return parent.issue(t, &x509.Certificate{
		Subject:               pkix.Name{CommonName: cn},
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	})
}

func (parent Cert) issue(t testing.TB, tmpl *x509.Certificate) Cert {
	key := newKey(t)
	tmpl.SerialNumber = serial(t)
	tmpl.NotBefore = time.Now().Add(-time.Hour)
	tmpl.NotAfter = time.Now().Add(24 * time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent.Cert, &key.PublicKey, parent.Key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return Cert{Cert: cert, Key: key, Chain: append([]*x509.Certificate{cert}, parent.Chain...)}
}

// NewKey returns a fresh P-256 key.
func NewKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	return newKey(t)
}

func newKey(t testing.TB) *ecdsa.PrivateKey {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func serial(t testing.TB) *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatal(err)
	}
	return n
}
