package authorization

import (
	"errors"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Client authentication is private_key_jwt only (OpenID Connect Core 1.0
// section 9; RFC 7523). REQ-AZSP-6.1.3.1-05 lets the authorization server
// mandate it; mTLS is not offered.

// ClientAssertionType is the client_assertion_type of private_key_jwt.
const ClientAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

// clientAssertionLeeway allows for clock skew on exp, nbf and iat.
const clientAssertionLeeway = 30 * time.Second

// clientAssertionAlgorithms are published as
// token_endpoint_auth_signing_alg_values_supported.
var clientAssertionAlgorithms = []jose.SignatureAlgorithm{jose.ES256, jose.PS256}

// authenticateClient checks the client assertion of a token request and
// returns the authenticated client.
func (s *Service) authenticateClient(r TokenRequest, now time.Time) (*Client, error) {
	if r.BasicAuth || r.ClientSecret != "" {
		return nil, errors.New("only private_key_jwt client authentication is accepted")
	}
	if r.ClientAssertionType != ClientAssertionType {
		return nil, errors.New("client_assertion_type must be " + ClientAssertionType)
	}
	tok, err := jwt.ParseSigned(r.ClientAssertion, clientAssertionAlgorithms)
	if err != nil {
		return nil, errors.New("client_assertion is not a JWT signed with ES256 or PS256")
	}
	var unverified jwt.Claims
	if err := tok.UnsafeClaimsWithoutVerification(&unverified); err != nil {
		return nil, errors.New("client_assertion has no readable claims")
	}
	c, ok := s.client(unverified.Issuer)
	if !ok {
		return nil, errors.New("unknown client")
	}
	if id := r.ClientID; id != "" && id != c.ID {
		return nil, errors.New("client_id does not match the client assertion")
	}
	var claims jwt.Claims
	if !verifyWithAny(tok, c.Keys, &claims) {
		return nil, errors.New("client_assertion signature does not verify with a key of the client")
	}
	if claims.Issuer != c.ID || claims.Subject != c.ID {
		return nil, errors.New("client_assertion iss and sub must be the client_id")
	}
	if claims.Expiry == nil || claims.ID == "" {
		return nil, errors.New("client_assertion must contain exp and jti")
	}
	if !claims.Audience.Contains(s.cfg.Issuer) && !claims.Audience.Contains(s.cfg.Issuer+TokenPath) {
		return nil, errors.New("client_assertion aud must be the issuer or the token endpoint")
	}
	if err := claims.ValidateWithLeeway(jwt.Expected{Time: now}, clientAssertionLeeway); err != nil {
		return nil, errors.New("client_assertion is expired or not yet valid")
	}
	if !s.assertions.firstUse(c.ID+" "+claims.ID, claims.Expiry.Time().Add(clientAssertionLeeway), now) {
		return nil, errors.New("client_assertion has been used before")
	}
	return c, nil
}

// verifyWithAny verifies tok with the key its kid names, or with any key of
// the set when it names none.
func verifyWithAny(tok *jwt.JSONWebToken, set jose.JSONWebKeySet, dest any) bool {
	keys := set.Keys
	if kid := tok.Headers[0].KeyID; kid != "" {
		keys = set.Key(kid)
	}
	for _, k := range keys {
		if tok.Claims(k.Key, dest) == nil {
			return true
		}
	}
	return false
}
