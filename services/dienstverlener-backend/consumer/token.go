package consumer

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// consentClaims is what the consumer reads from the consent token: which
// consent it is and what it covers. The token names the citizen only in
// values encrypted for the sources, which the consumer cannot read and does
// not need: it asks about "the subject of this consent".
type consentClaims struct {
	ConsentID string   `json:"consent_id"`
	Scopes    []string `json:"scopes"`
}

// SubjectPlaceholder is what a query names its subject with. The consumer
// holds no identifier of the citizen; the source puts the citizen of the
// consent in this place once the request is authorized.
const SubjectPlaceholder = "consent:subject"

// readConsentToken reads only enough to construct the question. This is
// intentionally not an authorization decision: the PDP verifies signature,
// issuer, audience, time, status and FSC actor before granting access.
func readConsentToken(token string) (consentClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return consentClaims{}, errors.New("consent token is not a compact JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return consentClaims{}, fmt.Errorf("decode consent token payload: %w", err)
	}
	var claims consentClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return consentClaims{}, fmt.Errorf("decode consent token claims: %w", err)
	}
	if claims.ConsentID == "" {
		return consentClaims{}, errors.New("consent token misses consent_id")
	}
	return claims, nil
}
