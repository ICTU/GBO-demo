// Package keyattest verifies key attestations in JWT format (OpenID4VCI 1.0
// Appendix D.1): a statement by a wallet provider or key storage component
// about one or more holder keys.
package keyattest

import (
	"errors"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/oid4vc-kit/jwttyp"
	"gbo-demo/oid4vc-kit/x5c"
)

// Type is the typ header of a key attestation.
const Type = "key-attestation+jwt"

// Verified is an accepted key attestation.
type Verified struct {
	Keys    []jose.JSONWebKey
	Nonce   string
	Expires time.Time
	Anchor  string
	// KeyStorage and UserAuthentication are the attack potential
	// resistance the attestation asserts (Appendix D.2).
	KeyStorage         []string
	UserAuthentication []string
}

type claims struct {
	IAT                *jwt.NumericDate  `json:"iat"`
	Exp                *jwt.NumericDate  `json:"exp"`
	AttestedKeys       []jose.JSONWebKey `json:"attested_keys"`
	Nonce              string            `json:"nonce"`
	KeyStorage         []string          `json:"key_storage"`
	UserAuthentication []string          `json:"user_authentication"`
}

// Verify checks a key attestation against anchors. requireExp is set when
// the attestation comes with a jwt proof, where exp is mandatory
// (Appendix D.1); with the attestation proof type it is optional.
func Verify(attestation string, anchors []x5c.Anchors, now time.Time, requireExp bool) (Verified, error) {
	tok, err := jwt.ParseSigned(attestation, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return Verified{}, errors.New("the key attestation is not a JWT signed with ES256")
	}
	h := tok.Headers[0]
	if !jwttyp.Is(h, Type) {
		return Verified{}, errors.New("the key attestation must have typ key-attestation+jwt")
	}
	leaf, anchor, err := x5c.Verify(h, anchors, now, nil)
	if err != nil {
		return Verified{}, errors.New("the key attestation is not signed by a trusted wallet provider")
	}
	var c claims
	if err := tok.Claims(leaf.PublicKey, &c); err != nil {
		return Verified{}, errors.New("the key attestation signature does not verify")
	}
	if c.IAT == nil || len(c.AttestedKeys) == 0 || (requireExp && c.Exp == nil) {
		return Verified{}, errors.New("the key attestation must contain iat and attested_keys, and exp with a jwt proof")
	}
	var exp time.Time
	if c.Exp != nil {
		exp = c.Exp.Time()
		if !now.Before(exp) {
			return Verified{}, errors.New("the key attestation has expired")
		}
	}
	for _, k := range c.AttestedKeys {
		if !k.Valid() || !k.IsPublic() {
			return Verified{}, errors.New("the key attestation contains an invalid key")
		}
	}
	return Verified{Keys: c.AttestedKeys, Nonce: c.Nonce, Expires: exp, Anchor: anchor,
		KeyStorage: c.KeyStorage, UserAuthentication: c.UserAuthentication}, nil
}
