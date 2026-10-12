package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// The ASIP is the resource server of ETSI TS 119 478 clause 6.1.3. Every
// request carries a DPoP-bound JWT access token from the authorization
// server. The ASIP validates it as RFC 9068 section 4 and RFC 9449 section 7
// require, and checks that its scope covers the operation. Every failure is
// 401, as ETSI TS 119 478 lists for "an authentication or authorization
// error" (REQ-ASIP-6.1.1.2-12), also insufficient_scope, for which RFC 6750
// would use 403.

const (
	// bsnClaim carries the identification data of the user: the BSN in plain
	// text until slice 9 of #336 (D3). Provisional namespace (D8).
	bsnClaim = "https://gbo.example/claims/bsn"

	// tokenLeeway allows for clock skew on exp, nbf and iat.
	tokenLeeway = 30 * time.Second

	// keyRefetchInterval limits how often an unknown kid makes the ASIP fetch
	// the keys of the authorization server again.
	keyRefetchInterval = 10 * time.Second
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

// keySource returns the verification keys of the authorization server.
type keySource interface {
	keys(ctx context.Context, kid string) ([]jose.JSONWebKey, error)
}

// errKeysUnavailable means the ASIP could not obtain the keys of the
// authorization server at all, so it cannot validate any token.
var errKeysUnavailable = errors.New("the keys of the authorization server are unavailable")

type tokenValidator struct {
	// issuer is the issuer identifier of the authorization server.
	issuer string
	// audience is the ASIP base URL, the resource indicator of its tokens.
	audience string
	// publicURL is the scheme and host QTSPs use to reach the ASIP; DPoP
	// proofs are made for that URL.
	publicURL string
	keys      keySource
	replay    *replayCache
	now       func() time.Time
}

func newTokenValidator(issuer, publicURL string, keys keySource) *tokenValidator {
	return &tokenValidator{
		issuer:    issuer,
		audience:  publicURL + basePath,
		publicURL: publicURL,
		keys:      keys,
		replay:    newReplayCache(),
		now:       time.Now,
	}
}

// authFailure is a 401 with the error code of RFC 6750 section 3.1 or
// RFC 9449 section 7.1; code is empty when the request carries no token.
type authFailure struct {
	code, description, scope string
	unavailable              bool
}

// authenticate validates the token and the DPoP proof of r for operation
// (verify or retrieve) and returns the BSN of the user.
func (v *tokenValidator) authenticate(r *http.Request, operation string) (string, *authFailure) {
	invalid := func(description string) *authFailure {
		return &authFailure{code: "invalid_token", description: description}
	}
	authorization := r.Header.Get("Authorization")
	if authorization == "" {
		return "", &authFailure{description: "a DPoP-bound access token is required"}
	}
	scheme, token, _ := strings.Cut(authorization, " ")
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
	keys, err := v.keys.keys(r.Context(), tok.Headers[0].KeyID)
	if errors.Is(err, errKeysUnavailable) {
		return "", &authFailure{unavailable: true, description: err.Error()}
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
	proofs := r.Header.Values("DPoP")
	if len(proofs) == 0 {
		return "", &authFailure{code: "invalid_request", description: "a DPoP proof is required"}
	}
	jkt, err := verifyDPoPProof(proofs, r.Method, v.publicURL+r.URL.Path, token, now, v.replay)
	if err != nil {
		return "", &authFailure{code: "invalid_dpop_proof", description: err.Error()}
	}
	if jkt != claims.Cnf.JKT {
		return "", &authFailure{code: "invalid_dpop_proof", description: "the DPoP proof key is not the key the access token is bound to"}
	}
	if !slices.Contains(strings.Fields(claims.Scope), operation) {
		return "", &authFailure{code: "insufficient_scope", description: "the access token does not authorize " + operation, scope: operation}
	}
	return claims.BSN, nil
}

// writeAuthFailure answers with 401, the WWW-Authenticate challenge of
// RFC 9449 section 7.1 and a problem body.
func writeAuthFailure(w http.ResponseWriter, f *authFailure) {
	if f.unavailable {
		writeProblem(w, http.StatusServiceUnavailable, "Service Unavailable", f.description)
		return
	}
	challenge := `DPoP algs="ES256"`
	if f.code != "" {
		challenge = fmt.Sprintf(`DPoP error="%s", error_description="%s", algs="ES256"`, f.code, f.description)
		if f.scope != "" {
			challenge += fmt.Sprintf(`, scope="%s"`, f.scope)
		}
	}
	w.Header().Set("WWW-Authenticate", challenge)
	writeProblem(w, http.StatusUnauthorized, "Unauthorized", f.description)
}

// --- keys of the authorization server ------------------------------------------

// remoteKeys fetches the keys from the jwks_uri in the metadata of the
// authorization server (RFC 8414; RFC 9068 section 4), and again when a token
// names a kid it does not know.
type remoteKeys struct {
	issuer  string
	client  *http.Client
	mu      sync.Mutex
	set     jose.JSONWebKeySet
	fetched time.Time
}

func newRemoteKeys(issuer string) *remoteKeys {
	return &remoteKeys{issuer: issuer, client: &http.Client{Timeout: 5 * time.Second}}
}

func (k *remoteKeys) keys(ctx context.Context, kid string) ([]jose.JSONWebKey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if found := lookupKeys(k.set, kid); len(found) > 0 || time.Since(k.fetched) < keyRefetchInterval {
		return found, nil
	}
	set, err := k.fetch(ctx)
	if err != nil {
		if len(k.set.Keys) == 0 {
			return nil, fmt.Errorf("%w: %v", errKeysUnavailable, err)
		}
		return nil, nil
	}
	k.set, k.fetched = set, time.Now()
	return lookupKeys(k.set, kid), nil
}

func (k *remoteKeys) fetch(ctx context.Context) (jose.JSONWebKeySet, error) {
	var meta struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := k.getJSON(ctx, k.issuer+"/.well-known/oauth-authorization-server", &meta); err != nil {
		return jose.JSONWebKeySet{}, err
	}
	if meta.Issuer != k.issuer || meta.JWKSURI == "" {
		return jose.JSONWebKeySet{}, errors.New("authorization server metadata has another issuer or no jwks_uri")
	}
	var set jose.JSONWebKeySet
	if err := k.getJSON(ctx, meta.JWKSURI, &set); err != nil {
		return jose.JSONWebKeySet{}, err
	}
	public := jose.JSONWebKeySet{}
	for _, key := range set.Keys {
		if key.IsPublic() {
			public.Keys = append(public.Keys, key)
		}
	}
	return public, nil
}

func (k *remoteKeys) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// staticKeys is a fixed key set, for tests.
type staticKeys jose.JSONWebKeySet

func (s staticKeys) keys(_ context.Context, kid string) ([]jose.JSONWebKey, error) {
	return lookupKeys(jose.JSONWebKeySet(s), kid), nil
}

func lookupKeys(set jose.JSONWebKeySet, kid string) []jose.JSONWebKey {
	if kid == "" {
		return set.Keys
	}
	return set.Key(kid)
}
