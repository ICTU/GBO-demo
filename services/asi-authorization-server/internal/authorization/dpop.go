package authorization

import (
	"crypto"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// DPoP (RFC 9449) binds the access token to a key of the client
// (REQ-AZSP-6.1.3.1-06). At the token endpoint the client proves possession
// of that key; the token then carries the key's thumbprint in cnf.jkt.

// dpopProofWindow is how far the iat of a proof may lie from now.
const dpopProofWindow = 60 * time.Second

// dpopAlgorithms are the proof algorithms this server accepts, published as
// dpop_signing_alg_values_supported.
var dpopAlgorithms = []jose.SignatureAlgorithm{jose.ES256}

type dpopClaims struct {
	JTI string           `json:"jti"`
	HTM string           `json:"htm"`
	HTU string           `json:"htu"`
	IAT *jwt.NumericDate `json:"iat"`
}

// verifyDPoPProof checks a DPoP proof as RFC 9449 section 4.3 requires and
// returns the JWK SHA-256 thumbprint (RFC 7638) of its key.
func verifyDPoPProof(headers []string, method, htu string, now time.Time, replay *replayCache) (string, error) {
	if len(headers) != 1 {
		return "", errors.New("exactly one DPoP proof is required")
	}
	tok, err := jwt.ParseSigned(headers[0], dpopAlgorithms)
	if err != nil {
		return "", errors.New("the DPoP proof is not a JWT signed with ES256")
	}
	h := tok.Headers[0]
	if typ, _ := h.ExtraHeaders[jose.HeaderType].(string); typ != "dpop+jwt" {
		return "", errors.New(`the DPoP proof must have typ "dpop+jwt"`)
	}
	if h.JSONWebKey == nil || !h.JSONWebKey.Valid() || !h.JSONWebKey.IsPublic() {
		return "", errors.New("the DPoP proof must carry a public jwk")
	}
	var c dpopClaims
	if err := tok.Claims(h.JSONWebKey.Key, &c); err != nil {
		return "", errors.New("the DPoP proof signature does not verify with its jwk")
	}
	if c.JTI == "" || c.HTM == "" || c.HTU == "" || c.IAT == nil {
		return "", errors.New("the DPoP proof must contain jti, htm, htu and iat")
	}
	if c.HTM != method || !sameHTU(c.HTU, htu) {
		return "", errors.New("the DPoP proof is for another method or URL")
	}
	iat := c.IAT.Time()
	if iat.Before(now.Add(-dpopProofWindow)) || iat.After(now.Add(dpopProofWindow)) {
		return "", errors.New("the DPoP proof is too old or from the future")
	}
	thumbprint, err := h.JSONWebKey.Thumbprint(crypto.SHA256)
	if err != nil {
		return "", errors.New("the DPoP proof key has no thumbprint")
	}
	if !replay.firstUse(c.JTI, iat.Add(dpopProofWindow), now) {
		return "", errors.New("the DPoP proof has been used before")
	}
	return base64.RawURLEncoding.EncodeToString(thumbprint), nil
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
