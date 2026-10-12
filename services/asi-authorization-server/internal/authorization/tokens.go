package authorization

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Access tokens are JWTs according to RFC 9068 (REQ-AZSP-6.1.3.1-08), bound
// to the client's DPoP key (cnf.jkt), and carry the identification data of
// the user. Until slice 9 of #336 that is the BSN in plain text (D3).

// AccessTokenLifetime is the lifetime of an access token.
const AccessTokenLifetime = 10 * time.Minute

// BSNClaim is a collision-resistant claim name (RFC 9068 section 2.2.2): the
// BSN has no entry in the IANA JWT registry. The namespace is provisional,
// like the attribute identifiers (D8).
const BSNClaim = "https://gbo.example/claims/bsn"

// TokenIssuer signs access tokens.
type TokenIssuer struct {
	issuer string
	signer jose.Signer
	jwks   jose.JSONWebKeySet
}

func NewTokenIssuer(issuer string, key *ecdsa.PrivateKey) (*TokenIssuer, error) {
	pub := jose.JSONWebKey{Key: &key.PublicKey, Algorithm: string(jose.ES256), Use: "sig"}
	thumbprint, err := pub.Thumbprint(crypto.SHA256)
	if err != nil {
		return nil, err
	}
	pub.KeyID = base64.RawURLEncoding.EncodeToString(thumbprint)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("at+jwt").WithHeader("kid", pub.KeyID))
	if err != nil {
		return nil, err
	}
	return &TokenIssuer{issuer: issuer, signer: signer, jwks: jose.JSONWebKeySet{Keys: []jose.JSONWebKey{pub}}}, nil
}

// issue returns an access token. sub is an opaque identifier per token (D3),
// so two tokens for the same user cannot be linked through it.
func (ti *TokenIssuer) issue(clientID, audience, scope, bsn, jkt string, now time.Time) (string, error) {
	claims := map[string]any{
		"iss":       ti.issuer,
		"sub":       RandomID(),
		"aud":       audience,
		"client_id": clientID,
		"iat":       now.Unix(),
		"exp":       now.Add(AccessTokenLifetime).Unix(),
		"jti":       RandomID(),
		"scope":     scope,
		"cnf":       map[string]string{"jkt": jkt},
		BSNClaim:    bsn,
	}
	token, err := jwt.Signed(ti.signer).Claims(claims).Serialize()
	if err != nil {
		return "", fmt.Errorf("sign access token: %w", err)
	}
	return token, nil
}

// RandomID returns 128 random bits, base64url-encoded.
func RandomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
