package portalhttp

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// jwtSecret signs mock-DigiD tokens. In this demo the portal is both the
// issuer and the verifier (inline JWT); a future slice can split that out
// into a dedicated mock-DigiD service with a JWKS endpoint.
const jwtSecret = "gbo-demo-portal-secret-do-not-use-in-production"

// PortalClaims is the mock-DigiD JWT payload. sub carries the BSN; in a real
// system the BSN would never appear in a bearer claim - DigiD returns an
// identifier from which BSN is later resolved at the service. For the demo
// the simplification is acceptable because the portal IS the resolver.
type PortalClaims struct {
	BSN string `json:"sub"`
	jwt.RegisteredClaims
}

func signPortalToken(bsn string) (string, error) {
	now := time.Now()
	claims := PortalClaims{
		BSN: bsn,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "mock-digid",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(jwtSecret))
}

func parseBearerToken(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", fmt.Errorf("missing Authorization header")
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", fmt.Errorf("invalid Authorization header")
	}
	return parts[1], nil
}

func validatePortalToken(tokenStr string) (string, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &PortalClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(jwtSecret), nil
	})
	if err != nil {
		return "", err
	}
	claims, ok := token.Claims.(*PortalClaims)
	if !ok || !token.Valid {
		return "", fmt.Errorf("invalid token")
	}
	if claims.BSN == "" {
		return "", fmt.Errorf("token missing sub")
	}
	return claims.BSN, nil
}
