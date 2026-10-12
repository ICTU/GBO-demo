package accesstoken

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

// The access token is bound to a key of the QTSP (RFC 9449, DPoP): with every
// request the QTSP proves possession of that key with a fresh proof for this
// method, URL and token. A stolen token is useless without the key.

// dpopProofWindow is how far the iat of a proof may lie from now.
const dpopProofWindow = 60 * time.Second

// dpopAlgorithms are the proof algorithms the authorization server accepts;
// the ASIP accepts the same.
var dpopAlgorithms = []jose.SignatureAlgorithm{jose.ES256}

type dpopClaims struct {
	JTI string           `json:"jti"`
	HTM string           `json:"htm"`
	HTU string           `json:"htu"`
	IAT *jwt.NumericDate `json:"iat"`
	ATH string           `json:"ath"`
}

// verifyDPoPProof checks a DPoP proof presented with accessToken as RFC 9449
// section 4.3 requires, including the access token hash (ath), and returns
// the JWK SHA-256 thumbprint (RFC 7638) of the proof key. Binding that key to
// the token (cnf.jkt) is the caller's check.
func verifyDPoPProof(headers []string, method, htu, accessToken string, now time.Time, replay *replayCache) (string, error) {
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
	digest := sha256.Sum256([]byte(accessToken))
	if c.ATH != base64.RawURLEncoding.EncodeToString(digest[:]) {
		return "", errors.New("the DPoP proof is for another access token (ath)")
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
