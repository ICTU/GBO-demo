package seal

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// sealer signs bodies as the ASIP does: a detached ES256 JWS with x5c.
type sealer struct {
	key   *ecdsa.PrivateKey
	chain []*x509.Certificate
	root  *x509.Certificate
}

func newSealer(t *testing.T) sealer {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test seal CA"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "GBO demo ASIP seal"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	leaf, _ := x509.ParseCertificate(der)
	return sealer{key: key, chain: []*x509.Certificate{leaf, ca}, root: ca}
}

func (s sealer) seal(t *testing.T, body []byte) string {
	t.Helper()
	x5c := []string{}
	for _, c := range s.chain {
		x5c = append(x5c, base64.StdEncoding.EncodeToString(c.Raw))
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: s.key}, (&jose.SignerOptions{}).WithHeader("x5c", x5c))
	if err != nil {
		t.Fatal(err)
	}
	jws, err := signer.Sign(body)
	if err != nil {
		t.Fatal(err)
	}
	detached, err := jws.DetachedCompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return detached
}

func pool(certs ...*x509.Certificate) *x509.CertPool {
	p := x509.NewCertPool()
	for _, c := range certs {
		p.AddCert(c)
	}
	return p
}

func TestValidate(t *testing.T) {
	s := newSealer(t)
	body := []byte(`{"attributeVerificationResults":[]}`)
	sig := s.seal(t, body)
	check, err := Validate(sig, body, pool(s.root))
	if err != nil || !strings.Contains(check.Signer, "GBO demo ASIP seal") {
		t.Fatalf("Validate = %+v, %v", check, err)
	}
}

// A result is not accepted when its seal is missing, does not cover the
// body, or does not chain to a trust anchor (Q-C5).
func TestRefused(t *testing.T) {
	s, other := newSealer(t), newSealer(t)
	body := []byte(`{"attributeVerificationResults":[]}`)
	sig := s.seal(t, body)
	for name, tc := range map[string]struct {
		sig  string
		body []byte
		pool *x509.CertPool
	}{
		"no seal":           {"", body, pool(s.root)},
		"tampered body":     {sig, []byte(`{"attributeVerificationResults":[{}]}`), pool(s.root)},
		"other anchor":      {sig, body, pool(other.root)},
		"not a JWS":         {"abc", body, pool(s.root)},
		"signed by another": {other.seal(t, body), body, pool(s.root)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Validate(tc.sig, tc.body, tc.pool); err == nil {
				t.Fatal("err = nil, want the result refused")
			}
		})
	}
}
