// Package clientattest verifies OAuth 2.0 Attestation-Based Client
// Authentication (draft-ietf-oauth-attestation-based-client-auth): a client
// attestation (for a wallet: the Wallet Instance Attestation) plus a proof
// of possession of the key it attests. Remembering PoP identifiers and
// challenges is left to the caller.
package clientattest

import (
	"errors"
	"slices"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/oid4vc-kit/x5c"
)

// Header names carrying the attestation and its proof of possession.
const (
	AttestationHeader = "OAuth-Client-Attestation"
	PoPHeader         = "OAuth-Client-Attestation-PoP"
	// ChallengeHeader carries a fresh challenge with the error
	// use_attestation_challenge.
	ChallengeHeader = "OAuth-Client-Attestation-Challenge"
	// Method is the token_endpoint_auth_methods_supported value.
	Method = "attest_jwt_client_auth"
)

const (
	attestationType = "oauth-client-attestation+jwt"
	popType         = "oauth-client-attestation-pop+jwt"
	// popWindow is how far the iat of a PoP may lie from now.
	popWindow = 60 * time.Second
)

// Algorithms are the accepted signature algorithms for both JWTs.
var Algorithms = []jose.SignatureAlgorithm{jose.ES256}

// ErrExpired means the attestation itself has expired; the client should
// fetch a fresh one (error use_fresh_attestation).
var ErrExpired = errors.New("the client attestation has expired")

// Verified is an accepted client attestation.
type Verified struct {
	// ClientID is the sub of the attestation.
	ClientID string
	// Anchor names the trust anchor set that accepted the chain.
	Anchor string
	// Key is the attested key (cnf.jwk) that signed the PoP.
	Key jose.JSONWebKey
	// PoPID and PoPExpiry let the caller reject a replayed PoP.
	PoPID     string
	PoPExpiry time.Time
	// Challenge is the server challenge the PoP carries, if any.
	Challenge string
}

type attestationClaims struct {
	Sub string           `json:"sub"`
	Exp *jwt.NumericDate `json:"exp"`
	Nbf *jwt.NumericDate `json:"nbf"`
	Cnf struct {
		JWK *jose.JSONWebKey `json:"jwk"`
	} `json:"cnf"`
}

type popClaims struct {
	Iss       string           `json:"iss"`
	Aud       jwt.Audience     `json:"aud"`
	JTI       string           `json:"jti"`
	IAT       *jwt.NumericDate `json:"iat"`
	Challenge string           `json:"challenge"`
}

// Verify checks the attestation chain against anchors and the PoP against
// the attested key; audience is the authorization server's issuer
// identifier.
func Verify(attestation, pop string, anchors []x5c.Anchors, audience string, now time.Time) (Verified, error) {
	tok, err := jwt.ParseSigned(attestation, Algorithms)
	if err != nil {
		return Verified{}, errors.New("the client attestation is not a JWT signed with ES256")
	}
	h := tok.Headers[0]
	if typ, _ := h.ExtraHeaders[jose.HeaderType].(string); typ != attestationType {
		return Verified{}, errors.New("the client attestation has the wrong typ")
	}
	leaf, anchor, err := x5c.Verify(h, anchors, now, nil)
	if err != nil {
		return Verified{}, errors.New("the client attestation is not signed by a trusted wallet provider")
	}
	var a attestationClaims
	if err := tok.Claims(leaf.PublicKey, &a); err != nil {
		return Verified{}, errors.New("the client attestation signature does not verify")
	}
	if a.Sub == "" || a.Exp == nil || a.Cnf.JWK == nil || !a.Cnf.JWK.Valid() || !a.Cnf.JWK.IsPublic() {
		return Verified{}, errors.New("the client attestation must contain sub, exp and cnf.jwk")
	}
	if !now.Before(a.Exp.Time()) {
		return Verified{}, ErrExpired
	}
	if a.Nbf != nil && now.Before(a.Nbf.Time()) {
		return Verified{}, errors.New("the client attestation is not yet valid")
	}

	ptok, err := jwt.ParseSigned(pop, Algorithms)
	if err != nil {
		return Verified{}, errors.New("the attestation PoP is not a JWT signed with ES256")
	}
	if typ, _ := ptok.Headers[0].ExtraHeaders[jose.HeaderType].(string); typ != popType {
		return Verified{}, errors.New("the attestation PoP has the wrong typ")
	}
	var p popClaims
	if err := ptok.Claims(a.Cnf.JWK.Key, &p); err != nil {
		return Verified{}, errors.New("the attestation PoP is not signed by the attested key")
	}
	if p.JTI == "" || p.IAT == nil {
		return Verified{}, errors.New("the attestation PoP must contain jti and iat")
	}
	if p.Iss != "" && p.Iss != a.Sub {
		return Verified{}, errors.New("the attestation PoP is from another client")
	}
	if !slices.Contains(p.Aud, audience) {
		return Verified{}, errors.New("the attestation PoP is for another authorization server")
	}
	iat := p.IAT.Time()
	if iat.Before(now.Add(-popWindow)) || iat.After(now.Add(popWindow)) {
		return Verified{}, errors.New("the attestation PoP is too old or from the future")
	}
	return Verified{
		ClientID:  a.Sub,
		Anchor:    anchor,
		Key:       *a.Cnf.JWK,
		PoPID:     p.JTI,
		PoPExpiry: iat.Add(popWindow),
		Challenge: p.Challenge,
	}, nil
}
