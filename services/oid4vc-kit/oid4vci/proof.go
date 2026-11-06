package oid4vci

import (
	"crypto/x509"
	"errors"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/oid4vc-kit/jwttyp"
)

// ProofType is the typ header of a jwt key proof (Appendix F.1).
const ProofType = "openid4vci-proof+jwt"

// ProofWindow is how far the iat of a key proof may lie in the past; a
// proof is made right before the credential request.
const ProofWindow = 5 * time.Minute

// ProofAlgorithms are the accepted proof signing algorithms, published as
// proof_signing_alg_values_supported.
var ProofAlgorithms = []jose.SignatureAlgorithm{jose.ES256}

// JWTProof is a verified jwt key proof.
type JWTProof struct {
	// Key is the key the proof is signed with (the jwk header).
	Key   jose.JSONWebKey
	Nonce string
	// KeyAttestation is the raw key_attestation header, if present; the
	// caller verifies it and that it attests Key.
	KeyAttestation string
}

type proofClaims struct {
	Aud   jwt.Audience     `json:"aud"`
	IAT   *jwt.NumericDate `json:"iat"`
	Nonce string           `json:"nonce"`
}

// VerifyJWTProof checks a jwt key proof for the credential issuer
// identified by audience (Appendix F.1 and F.4). Only the jwk header is
// supported to identify the key; kid and x5c are refused.
func VerifyJWTProof(proof, audience string, now time.Time) (JWTProof, error) {
	tok, err := jwt.ParseSigned(proof, ProofAlgorithms)
	if err != nil {
		return JWTProof{}, errors.New("the key proof is not a JWT signed with ES256")
	}
	h := tok.Headers[0]
	if !jwttyp.Is(h, ProofType) {
		return JWTProof{}, errors.New("the key proof must have typ openid4vci-proof+jwt")
	}
	if _, err := h.Certificates(x509.VerifyOptions{}); h.KeyID != "" || !errors.Is(err, jose.ErrMissingX5cHeader) {
		return JWTProof{}, errors.New("the key proof must identify its key with jwk")
	}
	if h.JSONWebKey == nil || !h.JSONWebKey.Valid() || !h.JSONWebKey.IsPublic() {
		return JWTProof{}, errors.New("the key proof must carry a public jwk")
	}
	var c proofClaims
	if err := tok.Claims(h.JSONWebKey.Key, &c); err != nil {
		return JWTProof{}, errors.New("the key proof signature does not verify with its jwk")
	}
	if len(c.Aud) != 1 || c.Aud[0] != audience {
		return JWTProof{}, errors.New("the key proof is for another credential issuer")
	}
	if c.IAT == nil {
		return JWTProof{}, errors.New("the key proof must contain iat")
	}
	iat := c.IAT.Time()
	if iat.Before(now.Add(-ProofWindow)) || iat.After(now.Add(time.Minute)) {
		return JWTProof{}, errors.New("the key proof is too old or from the future")
	}
	ka, _ := h.ExtraHeaders["key_attestation"].(string)
	return JWTProof{Key: *h.JSONWebKey, Nonce: c.Nonce, KeyAttestation: ka}, nil
}
