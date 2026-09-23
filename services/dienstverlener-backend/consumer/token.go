package consumer

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// consentClaims is what the consumer reads from the consent token: whose
// consent it is and what it covers.
type consentClaims struct {
	ConsentID string   `json:"consent_id"`
	PI        string   `json:"pi"`
	Scopes    []string `json:"scopes"`
}

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
	if claims.ConsentID == "" || claims.PI == "" {
		return consentClaims{}, errors.New("consent token misses consent_id or pi")
	}
	return claims, nil
}
