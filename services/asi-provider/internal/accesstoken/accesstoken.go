// Package accesstoken is the ASIP in its role of resource server of ETSI
// TS 119 478 clause 6.1.3. Every request carries a DPoP-bound JWT access
// token from the authorization server. The package validates it as RFC 9068
// section 4 and RFC 9449 section 7 require, and checks that its scope covers
// the operation. The BSN of the user comes from the token.
package accesstoken

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	// BSNClaim carries the identification data of the user: the BSN in plain
	// text until slice 9 of #336 (D3). Provisional namespace (D8).
	BSNClaim = "https://gbo.example/claims/bsn"

	// tokenLeeway allows for clock skew on exp, nbf and iat.
	tokenLeeway = 30 * time.Second
)

// accessTokenAlgorithms: RFC 9068 section 2.1 requires RS256 support. The
// authorization server signs with ES256 and publishes EC keys only.
var accessTokenAlgorithms = []jose.SignatureAlgorithm{jose.ES256, jose.RS256}

type accessTokenClaims struct {
	jwt.Claims
	ClientID string `json:"client_id"`
	Scope    string `json:"scope"`
	Cnf      struct {
		JKT string `json:"jkt"`
	} `json:"cnf"`
	BSN string `json:"https://gbo.example/claims/bsn"`
}

// KeySource returns the verification keys of the authorization server; with
// a kid, the keys with that kid.
type KeySource interface {
	Keys(ctx context.Context, kid string) ([]jose.JSONWebKey, error)
}

// ErrKeysUnavailable means the keys of the authorization server could not be
// obtained at all, so no token can be validated.
var ErrKeysUnavailable = errors.New("the keys of the authorization server are unavailable")

// Request is what the validator needs from a request.
type Request struct {
	Method        string
	Path          string
	Authorization string
	DPoP          []string
}

// Failure is a request that is not authorized. Code is the error code of
// RFC 6750 section 3.1 or RFC 9449 section 7.1, empty when the request
// carries no token. Unavailable means no token could be validated at all.
type Failure struct {
	Code, Description, Scope string
	Unavailable              bool
}

type Validator struct {
	// issuer is the issuer identifier of the authorization server.
	issuer string
	// audience is the ASIP base URL, the resource indicator of its tokens.
	audience string
	// publicURL is the scheme and host QTSPs use to reach the ASIP; DPoP
	// proofs are made for that URL.
	publicURL string
	keys      KeySource
	replay    *replayCache
	now       func() time.Time
}

func NewValidator(issuer, audience, publicURL string, keys KeySource) *Validator {
	return &Validator{
		issuer:    issuer,
		audience:  audience,
		publicURL: publicURL,
		keys:      keys,
		replay:    newReplayCache(),
		now:       time.Now,
	}
}

// Authenticate validates the token and the DPoP proof of r for operation
// (verify or retrieve) and returns the BSN of the user.
func (v *Validator) Authenticate(ctx context.Context, r Request, operation string) (string, *Failure) {
	invalid := func(description string) *Failure {
		return &Failure{Code: "invalid_token", Description: description}
	}
	if r.Authorization == "" {
		return "", &Failure{Description: "a DPoP-bound access token is required"}
	}
	scheme, token, _ := strings.Cut(r.Authorization, " ")
	if !strings.EqualFold(scheme, "DPoP") || token == "" {
		return "", invalid("the access token is DPoP-bound: use the DPoP authorization scheme")
	}
	tok, err := jwt.ParseSigned(token, accessTokenAlgorithms)
	if err != nil {
		return "", invalid("the access token is not a JWT signed with ES256 or RS256")
	}
	if typ, _ := tok.Headers[0].ExtraHeaders[jose.HeaderType].(string); !strings.EqualFold(typ, "at+jwt") && !strings.EqualFold(typ, "application/at+jwt") {
		return "", invalid(`the access token must have typ "at+jwt"`)
	}
	keys, err := v.keys.Keys(ctx, tok.Headers[0].KeyID)
	if errors.Is(err, ErrKeysUnavailable) {
		return "", &Failure{Unavailable: true, Description: err.Error()}
	}
	var claims accessTokenClaims
	verified := false
	for _, k := range keys {
		if tok.Claims(k.Key, &claims) == nil {
			verified = true
			break
		}
	}
	if !verified {
		return "", invalid("the access token signature does not verify with a key of the authorization server")
	}
	now := v.now()
	if err := claims.ValidateWithLeeway(jwt.Expected{Issuer: v.issuer, AnyAudience: jwt.Audience{v.audience}, Time: now}, tokenLeeway); err != nil || claims.Expiry == nil {
		return "", invalid("the access token has another issuer or audience, or is expired")
	}
	if claims.Cnf.JKT == "" {
		return "", invalid("the access token is not DPoP-bound")
	}
	if claims.BSN == "" {
		return "", invalid("the access token carries no identification data of the user")
	}
	if len(r.DPoP) == 0 {
		return "", &Failure{Code: "invalid_request", Description: "a DPoP proof is required"}
	}
	jkt, err := verifyDPoPProof(r.DPoP, r.Method, v.publicURL+r.Path, token, now, v.replay)
	if err != nil {
		return "", &Failure{Code: "invalid_dpop_proof", Description: err.Error()}
	}
	if jkt != claims.Cnf.JKT {
		return "", &Failure{Code: "invalid_dpop_proof", Description: "the DPoP proof key is not the key the access token is bound to"}
	}
	if !slices.Contains(strings.Fields(claims.Scope), operation) {
		return "", &Failure{Code: "insufficient_scope", Description: "the access token does not authorize " + operation, Scope: operation}
	}
	return claims.BSN, nil
}

// StaticKeys is a fixed key set.
func StaticKeys(set jose.JSONWebKeySet) KeySource { return staticKeys{set: set} }

type staticKeys struct{ set jose.JSONWebKeySet }

func (s staticKeys) Keys(_ context.Context, kid string) ([]jose.JSONWebKey, error) {
	return Lookup(s.set, kid), nil
}

// Lookup returns the keys of set with kid, or all keys without one.
func Lookup(set jose.JSONWebKeySet, kid string) []jose.JSONWebKey {
	if kid == "" {
		return set.Keys
	}
	return set.Key(kid)
}
