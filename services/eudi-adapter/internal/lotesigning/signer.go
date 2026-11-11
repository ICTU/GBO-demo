package lotesigning

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// Signer keeps the LoTE private key on the provisioning host. The certificate
// in x5c identifies the signer; a wallet still needs an independent trust pin.
type Signer struct {
	Key               *ecdsa.PrivateKey
	Certificate, Root *x509.Certificate
}

func (s Signer) Identity() string {
	h := sha256.New()
	h.Write(s.Certificate.Raw)
	h.Write(s.Root.Raw)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func (s Signer) Validate(now, until time.Time) error {
	if s.Key == nil || s.Key.Curve != elliptic.P256() || s.Certificate == nil || s.Root == nil {
		return errors.New("ES256 signer, certificate and scheme root are required")
	}
	public, ok := s.Certificate.PublicKey.(*ecdsa.PublicKey)
	if !ok || !s.Key.PublicKey.Equal(public) || s.Certificate.IsCA || s.Certificate.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return errors.New("LoTE key must match a non-CA digital-signature certificate")
	}
	pool := x509.NewCertPool()
	pool.AddCert(s.Root)
	if _, err := s.Certificate.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return err
	}
	if until.After(s.Certificate.NotAfter) || until.After(s.Root.NotAfter) {
		return errors.New("LoTE publication would outlive its signing certificate")
	}
	return nil
}

func (s Signer) Sign(ctx context.Context, payload []byte, issue time.Time) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.Validate(issue, issue); err != nil {
		return nil, err
	}
	hash := sha256.Sum256(s.Certificate.Raw)
	thumbprint := base64.RawURLEncoding.EncodeToString(hash[:])
	options := (&jose.SignerOptions{}).WithType("JOSE+JSON").
		WithHeader("x5c", []string{base64.StdEncoding.EncodeToString(s.Certificate.Raw)}).
		WithHeader("x5t#S256", thumbprint).WithHeader("kid", thumbprint).WithHeader("iat", issue.Unix())
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: s.Key}, options)
	if err != nil {
		return nil, err
	}
	signed, err := signer.Sign(payload)
	if err != nil {
		return nil, err
	}
	raw, err := signed.CompactSerialize()
	return []byte(raw), err
}
