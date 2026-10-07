package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
)

// sealer seals a response body as required by CIR (EU) 2025/1569
// Article 9(4), as amended by CIR (EU) 2026/1735: every verification result is
// signed or sealed by the responsible body or the designated intermediary.
//
// ETSI TS 119 478 clause 6.1.1 defines no signature on the response, so the
// form is a GBO choice: a detached JWS (RFC 7515, appendix F) over the exact
// response body, carried in the X-JWS-Signature header. The protected header
// carries the certificate chain in x5c, leaf first, so a QTSP can validate the
// seal against the trusted lists. The body itself stays plain JSON as the
// OpenAPI specifies.
type sealer struct {
	key  crypto.Signer
	x5c  []string
	prot string
}

func newSealer(key crypto.Signer, chain []*x509.Certificate) *sealer {
	x5c := make([]string, len(chain))
	for i, c := range chain {
		x5c[i] = base64.StdEncoding.EncodeToString(c.Raw)
	}
	header, _ := json.Marshal(map[string]any{"alg": "ES256", "x5c": x5c})
	return &sealer{key: key, x5c: x5c, prot: base64.RawURLEncoding.EncodeToString(header)}
}

// seal returns the detached JWS "<protected>..<signature>" over body.
func (s *sealer) seal(body []byte) (string, error) {
	signingInput := s.prot + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(signingInput))
	der, err := s.key.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return "", fmt.Errorf("seal: %w", err)
	}
	raw, err := ecdsaDERToRaw(der)
	if err != nil {
		return "", err
	}
	return s.prot + ".." + base64.RawURLEncoding.EncodeToString(raw), nil
}

// ecdsaDERToRaw converts an ASN.1 ECDSA signature to the fixed 64-byte r||s
// form JWS uses for ES256.
func ecdsaDERToRaw(der []byte) ([]byte, error) {
	var sig struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(der, &sig); err != nil {
		return nil, fmt.Errorf("seal: parse signature: %w", err)
	}
	out := make([]byte, 64)
	sig.R.FillBytes(out[:32])
	sig.S.FillBytes(out[32:])
	return out, nil
}

var _ crypto.Signer = (*ecdsa.PrivateKey)(nil)
