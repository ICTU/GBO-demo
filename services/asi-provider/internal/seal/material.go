package seal

import (
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// LoadMaterial reads a signing key and its certificate chain (leaf first)
// from PEM files.
func LoadMaterial(keyPath, certPath string) (crypto.Signer, []*x509.Certificate, error) {
	if keyPath == "" || certPath == "" {
		return nil, nil, errors.New("ASIP_SEAL_KEY_FILE and ASIP_SEAL_CERT_FILE are required; run `make provision-asip-seal-certificate`")
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read seal key: %w", err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, nil, errors.New("seal key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if k, ecErr := x509.ParseECPrivateKey(block.Bytes); ecErr == nil {
			parsed = k
		} else {
			return nil, nil, fmt.Errorf("parse seal key: %w", err)
		}
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, nil, errors.New("seal key cannot sign")
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read seal certificate: %w", err)
	}
	var chain []*x509.Certificate
	for rest := certPEM; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("parse seal certificate: %w", err)
		}
		chain = append(chain, c)
	}
	if len(chain) == 0 {
		return nil, nil, errors.New("seal certificate file contains no certificate")
	}
	return signer, chain, nil
}
