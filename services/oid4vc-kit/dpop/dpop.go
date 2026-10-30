// Package dpop verifies DPoP proofs (RFC 9449). Remembering proof
// identifiers against replay is left to the caller, so that the state can
// live outside the process.
package dpop

import (
	"crypto"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Window is how far the iat of a proof may lie from now.
const Window = 60 * time.Second

// Algorithms are the proof algorithms accepted, published as
// dpop_signing_alg_values_supported.
var Algorithms = []jose.SignatureAlgorithm{jose.ES256}

// Proof is a verified DPoP proof.
type Proof struct {
	// JKT is the base64url JWK SHA-256 thumbprint (RFC 7638) of the key.
	JKT string
	JTI string
	IAT time.Time
}

// Expected describes the request a proof must belong to.
type Expected struct {
	Method string
	URL    string
	// AccessToken, when set, must be hashed into the proof's ath claim
	// (RFC 9449 section 4.3, check 10).
	AccessToken string
}

type claims struct {
	JTI   string           `json:"jti"`
	HTM   string           `json:"htm"`
	HTU   string           `json:"htu"`
	IAT   *jwt.NumericDate `json:"iat"`
	ATH   string           `json:"ath"`
	Nonce string           `json:"nonce"`
}

// Verify checks the DPoP header values of a request as RFC 9449 section
// 4.3 requires. Server-provided DPoP nonces are not used.
func Verify(headers []string, want Expected, now time.Time) (Proof, error) {
	if len(headers) != 1 {
		return Proof{}, errors.New("exactly one DPoP proof is required")
	}
	tok, err := jwt.ParseSigned(headers[0], Algorithms)
	if err != nil {
		return Proof{}, errors.New("the DPoP proof is not a JWT signed with ES256")
	}
	h := tok.Headers[0]
	if typ, _ := h.ExtraHeaders[jose.HeaderType].(string); typ != "dpop+jwt" {
		return Proof{}, errors.New("the DPoP proof must have typ dpop+jwt")
	}
	if h.JSONWebKey == nil || !h.JSONWebKey.Valid() || !h.JSONWebKey.IsPublic() {
		return Proof{}, errors.New("the DPoP proof must carry a public jwk")
	}
	var c claims
	if err := tok.Claims(h.JSONWebKey.Key, &c); err != nil {
		return Proof{}, errors.New("the DPoP proof signature does not verify with its jwk")
	}
	if c.JTI == "" || c.HTM == "" || c.HTU == "" || c.IAT == nil {
		return Proof{}, errors.New("the DPoP proof must contain jti, htm, htu and iat")
	}
	if c.HTM != want.Method || !sameHTU(c.HTU, want.URL) {
		return Proof{}, errors.New("the DPoP proof is for another method or URL")
	}
	iat := c.IAT.Time()
	if iat.Before(now.Add(-Window)) || iat.After(now.Add(Window)) {
		return Proof{}, errors.New("the DPoP proof is too old or from the future")
	}
	if want.AccessToken != "" && c.ATH != AccessTokenHash(want.AccessToken) {
		return Proof{}, errors.New("the DPoP proof is not bound to this access token")
	}
	thumbprint, err := h.JSONWebKey.Thumbprint(crypto.SHA256)
	if err != nil {
		return Proof{}, errors.New("the DPoP proof key has no thumbprint")
	}
	return Proof{JKT: base64.RawURLEncoding.EncodeToString(thumbprint), JTI: c.JTI, IAT: iat}, nil
}

// AccessTokenHash is the ath value for an access token: base64url of its
// SHA-256 hash.
func AccessTokenHash(accessToken string) string {
	sum := sha256.Sum256([]byte(accessToken))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// sameHTU compares htu with the request URI, ignoring query and fragment
// (RFC 9449 section 4.3, check 9). Scheme and host are case-insensitive.
func sameHTU(claimed, expected string) bool {
	a, err := url.Parse(claimed)
	if err != nil {
		return false
	}
	b, err := url.Parse(expected)
	if err != nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host) && a.EscapedPath() == b.EscapedPath()
}
