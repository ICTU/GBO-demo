package mapping

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/eam/models"
)

func TestHasConsentEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{name: "consent token present", headers: map[string]string{"x-gbo-consent-token": "ey.some.token"}, want: true},
		{name: "no headers falls through to the PID regime", headers: map[string]string{}, want: false},
		{name: "empty value is not evidence", headers: map[string]string{"x-gbo-consent-token": ""}, want: false},
		{
			// The regime is no longer named by the caller. X-GBO-Flow was
			// removed for letting a caller pick what it was judged under,
			// and the FSC grant property that replaced it is gone too
			// (#334); neither may come back through this door.
			name: "a named flow does not select the regime",
			headers: map[string]string{
				"x-gbo-flow":        "dvtp:query",
				"fsc-authorization": "Bearer ey.prp.flow",
			},
			want: false,
		},
		{
			// A subject variable cannot discriminate — both regimes carry
			// one — so nothing about the query changes this answer.
			name:    "scope header alone is not consent evidence",
			headers: map[string]string{"x-gbo-scope": "bd:ib:2025"},
			want:    false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := hasConsentEvidence(tt.headers); got != tt.want {
				t.Fatalf("hasConsentEvidence(%v) = %v, want %v", tt.headers, got, tt.want)
			}
		})
	}
}

func consentTestToken(t *testing.T, key *ecdsa.PrivateKey, kid, audience string, expires time.Time) string {
	return consentTestTokenAt(t, key, kid, audience, time.Now().Add(-time.Minute), expires)
}

func consentTestTokenAt(t *testing.T, key *ecdsa.PrivateKey, kid, audience string, notBefore, expires time.Time) string {
	return consentTestTokenWithTimes(t, key, kid, audience, time.Now().Add(-time.Minute), notBefore, expires)
}

func consentTestTokenWithTimes(
	t *testing.T,
	key *ecdsa.PrivateKey,
	kid, audience string,
	issuedAt, notBefore, expires time.Time,
) string {
	t.Helper()
	claims := consentClaims{
		ConsentID:        "c-signed",
		PI:               "PI-abc123",
		Scopes:           []string{"bd:ib:2025"},
		DienstverlenrOIN: "99999999900000000300",
		ValidUntil:       expires.UTC().Format(time.RFC3339),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "test-issuer",
			Audience:  jwt.ClaimStrings{audience},
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			NotBefore: jwt.NewNumericDate(notBefore),
			ExpiresAt: jwt.NewNumericDate(expires),
			ID:        "jti-1",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = kid
	token.Header["typ"] = "gbo-consent+jwt"
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func resetConsentKeyCache(t *testing.T) {
	t.Helper()
	cachedConsentKeys.mu.Lock()
	cachedConsentKeys.source = ""
	cachedConsentKeys.keys = nil
	cachedConsentKeys.freshUntil = time.Time{}
	cachedConsentKeys.staleUntil = time.Time{}
	cachedConsentKeys.now = time.Now
	cachedConsentKeys.mu.Unlock()
	t.Cleanup(func() {
		cachedConsentKeys.mu.Lock()
		cachedConsentKeys.source = ""
		cachedConsentKeys.keys = nil
		cachedConsentKeys.freshUntil = time.Time{}
		cachedConsentKeys.staleUntil = time.Time{}
		cachedConsentKeys.now = time.Now
		cachedConsentKeys.mu.Unlock()
	})
}

func jwksHandler(key *ecdsa.PrivateKey, hits *atomic.Int32, fail *atomic.Bool) http.Handler {
	encode := base64.RawURLEncoding.EncodeToString
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/jwks.json" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		if fail != nil && fail.Load() {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "EC", "crv": "P-256", "alg": "ES256", "kid": "test-key",
			"x": encode(key.X.FillBytes(make([]byte, 32))),
			"y": encode(key.Y.FillBytes(make([]byte, 32))),
		}}})
	})
}

func TestConsentSigningKeyCachesAndRefreshesByKid(t *testing.T) {
	resetConsentKeyCache(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	server := httptest.NewServer(jwksHandler(key, &hits, nil))
	defer server.Close()
	t.Setenv("GBO_CONSENT_URL", server.URL)

	if _, err := consentSigningKey("test-key"); err != nil {
		t.Fatalf("initial key fetch: %v", err)
	}
	if _, err := consentSigningKey("test-key"); err != nil {
		t.Fatalf("cached key fetch: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("JWKS fetches = %d, want 1 for cached key", got)
	}
	if _, err := consentSigningKey("unknown-key"); err == nil {
		t.Fatal("unknown kid accepted")
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("JWKS fetches = %d, want refresh for unknown kid", got)
	}
}

func TestConsentSigningKeyUsesBoundedStaleKeyOnJWKSFailure(t *testing.T) {
	resetConsentKeyCache(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	var fail atomic.Bool
	server := httptest.NewServer(jwksHandler(key, &hits, &fail))
	defer server.Close()
	t.Setenv("GBO_CONSENT_URL", server.URL)

	now := time.Now()
	cachedConsentKeys.mu.Lock()
	cachedConsentKeys.now = func() time.Time { return now }
	cachedConsentKeys.mu.Unlock()
	if _, err := consentSigningKey("test-key"); err != nil {
		t.Fatalf("initial key fetch: %v", err)
	}

	fail.Store(true)
	now = now.Add(consentJWKSCacheTTL + time.Second)
	if _, err := consentSigningKey("test-key"); err != nil {
		t.Fatalf("stale key rejected during brief outage: %v", err)
	}

	now = now.Add(consentJWKSMaxStale)
	if _, err := consentSigningKey("test-key"); err == nil {
		t.Fatal("key remained usable beyond maximum stale period")
	}
}

func consentTestServer(t *testing.T, key *ecdsa.PrivateKey, status string) *httptest.Server {
	t.Helper()
	encode := base64.RawURLEncoding.EncodeToString
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/jwks.json":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "EC", "crv": "P-256", "alg": "ES256", "kid": "test-key",
				"x": encode(key.X.FillBytes(make([]byte, 32))),
				"y": encode(key.Y.FillBytes(make([]byte, 32))),
			}}})
		case "/consents/c-signed/status":
			if status == "NOT_FOUND" {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"consent_id": "c-signed", "status": status})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestFetchConsentVerifiesTokenAndExactStatus(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := consentTestServer(t, key, "ACTIVE")
	defer server.Close()
	t.Setenv("GBO_CONSENT_URL", server.URL)
	t.Setenv("GBO_CONSENT_ISSUER", "test-issuer")
	t.Setenv("GBO_CONSENT_AUDIENCE", "test-audience")

	token := consentTestToken(t, key, "test-key", "test-audience", time.Now().Add(time.Hour))
	got := fetchConsent(map[string]string{"x-gbo-consent-token": token})
	if got["context_valid"] != true || got["status_available"] != true || got["exists"] != true {
		t.Fatalf("verified consent = %#v", got)
	}
	if got["pi"] != "PI-abc123" || got["dienstverlener_oin"] != "99999999900000000300" {
		t.Fatalf("signed bindings missing: %#v", got)
	}
}

func TestFetchConsentAllowsOnlyBoundedClockSkew(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := consentTestServer(t, key, "ACTIVE")
	defer server.Close()
	t.Setenv("GBO_CONSENT_URL", server.URL)
	t.Setenv("GBO_CONSENT_ISSUER", "test-issuer")
	t.Setenv("GBO_CONSENT_AUDIENCE", "test-audience")

	now := time.Now().UTC()
	withinLeeway := consentTestTokenWithTimes(
		t, key, "test-key", "test-audience",
		now.Add(20*time.Second), now.Add(20*time.Second), now.Add(time.Hour),
	)
	got := fetchConsent(map[string]string{"x-gbo-consent-token": withinLeeway})
	if got["context_valid"] != true {
		t.Fatalf("clock skew within leeway rejected: %#v", got)
	}

	beyondLeeway := consentTestTokenWithTimes(
		t, key, "test-key", "test-audience",
		now.Add(time.Minute), now.Add(time.Minute), now.Add(time.Hour),
	)
	got = fetchConsent(map[string]string{"x-gbo-consent-token": beyondLeeway})
	if got["context_valid"] != false {
		t.Fatalf("clock skew beyond leeway accepted: %#v", got)
	}
}

func TestFetchConsentFailsClosedForInvalidContext(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := consentTestServer(t, key, "ACTIVE")
	defer server.Close()
	t.Setenv("GBO_CONSENT_URL", server.URL)
	t.Setenv("GBO_CONSENT_ISSUER", "test-issuer")
	t.Setenv("GBO_CONSENT_AUDIENCE", "test-audience")

	tests := map[string]string{
		"missing":        "",
		"wrong audience": consentTestToken(t, key, "test-key", "other-audience", time.Now().Add(time.Hour)),
		"expired":        consentTestToken(t, key, "test-key", "test-audience", time.Now().Add(-time.Hour)),
		"not yet valid":  consentTestTokenAt(t, key, "test-key", "test-audience", time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)),
		"unknown kid":    consentTestToken(t, key, "unknown-key", "test-audience", time.Now().Add(time.Hour)),
	}
	valid := consentTestToken(t, key, "test-key", "test-audience", time.Now().Add(time.Hour))
	parts := strings.Split(valid, ".")
	signature := []byte(parts[2])
	replacement := byte('A')
	if signature[0] == replacement {
		replacement = 'B'
	}
	signature[0] = replacement
	parts[2] = string(signature)
	tests["tampered signature"] = strings.Join(parts, ".")
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			got := fetchConsent(map[string]string{"x-gbo-consent-token": token})
			if got["context_valid"] != false {
				t.Fatalf("invalid token accepted: %#v", got)
			}
		})
	}
}

func TestFetchConsentReportsRevocation(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := consentTestServer(t, key, "REVOKED")
	defer server.Close()
	t.Setenv("GBO_CONSENT_URL", server.URL)
	t.Setenv("GBO_CONSENT_ISSUER", "test-issuer")
	t.Setenv("GBO_CONSENT_AUDIENCE", "test-audience")
	token := consentTestToken(t, key, "test-key", "test-audience", time.Now().Add(time.Hour))
	got := fetchConsent(map[string]string{"x-gbo-consent-token": token})
	if got["withdrawn"] != true {
		t.Fatalf("revoked status not propagated: %#v", got)
	}
}

func TestFetchConsentFailsClosedForMissingOrUnknownStatus(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GBO_CONSENT_ISSUER", "test-issuer")
	t.Setenv("GBO_CONSENT_AUDIENCE", "test-audience")
	token := consentTestToken(t, key, "test-key", "test-audience", time.Now().Add(time.Hour))

	for _, status := range []string{"NOT_FOUND", "UNKNOWN"} {
		t.Run(status, func(t *testing.T) {
			server := consentTestServer(t, key, status)
			defer server.Close()
			t.Setenv("GBO_CONSENT_URL", server.URL)
			got := fetchConsent(map[string]string{"x-gbo-consent-token": token})
			if got["exists"] != false {
				t.Fatalf("status %s did not fail closed: %#v", status, got)
			}
		})
	}
}

// ── Identifier scrubbing ──────────────────────────────────────────────────
// No raw BSN may reach OPA's input, and with it the decision log, whichever
// regime a request claims. The consent header decides HOW the subject
// identifier is made safe — kept only when it is the verified consent's own
// PI, pseudonymised in the PID regime — never WHETHER.

const demoBSN = "123456789"

// graphQLRequest builds the PARC the PEP hands the mapper: the GraphQL body
// on the action, the request headers on the context.
func graphQLRequest(t *testing.T, headers map[string]string, variables map[string]any) *models.PARC {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"query":     `query($bsn: BSN!) { ingeschrevenPersoon(bsn: $bsn) { heeftBelastingjaarAangifte(belastingjaren: [2025]) { belastingjaar } } }`,
		"variables": variables,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &models.PARC{
		Principal: models.NewEntity("org", "99999999900000000300", models.NewAttributeSet()),
		Action:    models.NewEntity("name", "POST", models.NewAttributeSet(models.NewAttribute(models.AttrBody, string(body)))),
		Resource:  models.NewEntity("service", "bri", models.NewAttributeSet()),
		Context:   models.NewAttributeSet(models.NewAttribute(models.AttrHeaders, headers)),
	}
}

// decisionInput serialises everything the mapper hands on to OPA, so a test
// can assert what never appears in it.
func decisionInput(t *testing.T, parc *models.PARC) string {
	t.Helper()
	out, err := json.Marshal(parc)
	if err != nil {
		t.Fatal(err)
	}
	// Guard against a vacuous negative check: the serialisation must carry
	// the request itself, or "BSN absent" would prove nothing.
	if !strings.Contains(string(out), "ingeschrevenPersoon") {
		t.Fatalf("decision input does not carry the request: %s", out)
	}
	return string(out)
}

func subjectVariable(parc *models.PARC) any {
	resource, _ := parc.Context.GetAttributeValue("resource").(map[string]any)
	variables, _ := resource["variables"].(map[string]any)
	return variables["bsn"]
}

func pidPI(parc *models.PARC) any {
	pip, _ := parc.Context.GetAttributeValue("pip").(map[string]any)
	pid, _ := pip["pid"].(map[string]any)
	return pid["pi"]
}

func verifiedConsentToken(t *testing.T) string {
	t.Helper()
	resetConsentKeyCache(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := consentTestServer(t, key, "ACTIVE")
	t.Cleanup(server.Close)
	t.Setenv("GBO_CONSENT_URL", server.URL)
	t.Setenv("GBO_CONSENT_ISSUER", "test-issuer")
	t.Setenv("GBO_CONSENT_AUDIENCE", "test-audience")
	return consentTestToken(t, key, "test-key", "test-audience", time.Now().Add(time.Hour))
}

func bsnkTestServer(t *testing.T, status int, pi string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pseudonymize" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"pi": pi})
	}))
	t.Cleanup(server.Close)
	t.Setenv("GBO_BSNK_URL", server.URL)
}

// Review of #363: an unverifiable consent header switched scrubbing off, so
// an EUDI request carrying one kept its raw BSN in the action body, the
// resource variables and the resolved arguments.
func TestConsentHeaderDoesNotBypassBSNScrubbing(t *testing.T) {
	out := GraphQLToContext(graphQLRequest(t,
		map[string]string{"X-GBO-Consent-Token": "invalid"},
		map[string]any{"bsn": demoBSN},
	))
	if in := decisionInput(t, out); strings.Contains(in, demoBSN) {
		t.Fatalf("raw BSN reached the decision input: %s", in)
	}
}

// The verified consent's own PI is the one subject value the consent regime
// expects, and it is kept: DVT0001's constraint binding compares against it.
func TestVerifiedConsentKeepsItsOwnPI(t *testing.T) {
	token := verifiedConsentToken(t)
	out := GraphQLToContext(graphQLRequest(t,
		map[string]string{"X-GBO-Consent-Token": token},
		map[string]any{"bsn": "PI-abc123"},
	))
	if got := subjectVariable(out); got != "PI-abc123" {
		t.Fatalf("subject variable = %#v, want the consent's own PI kept", got)
	}
}

// Any other subject value under a verified consent is blanked, not
// pseudonymised: pseudonymising a BSN yields that citizen's PI, which would
// then satisfy DVT0001's constraint binding for a consumer that was never
// meant to hold the BSN. Blanked, the binding fails as it does on main.
func TestVerifiedConsentRedactsAnyOtherSubject(t *testing.T) {
	token := verifiedConsentToken(t)
	out := GraphQLToContext(graphQLRequest(t,
		map[string]string{"X-GBO-Consent-Token": token},
		map[string]any{"bsn": demoBSN},
	))
	if in := decisionInput(t, out); strings.Contains(in, demoBSN) {
		t.Fatalf("raw BSN reached the decision input: %s", in)
	}
	if got := subjectVariable(out); got != "" {
		t.Fatalf("subject variable = %#v, want it blanked", got)
	}
}

func TestPIDRegimePseudonymisesTheBSN(t *testing.T) {
	bsnkTestServer(t, http.StatusOK, "PI-2f1a7c9b40e6d853")
	out := GraphQLToContext(graphQLRequest(t, map[string]string{}, map[string]any{"bsn": demoBSN}))
	if in := decisionInput(t, out); strings.Contains(in, demoBSN) {
		t.Fatalf("raw BSN reached the decision input: %s", in)
	}
	if got := pidPI(out); got != "PI-2f1a7c9b40e6d853" {
		t.Fatalf("pip.pid.pi = %#v", got)
	}
	if got := subjectVariable(out); got != "PI-2f1a7c9b40e6d853" {
		t.Fatalf("subject variable = %#v, want the PI substituted", got)
	}
}

// A BSNk failure scrubs the identifier rather than passing it through. The
// policy still sees that PID enrichment was attempted (pip.pid is present),
// which is what keeps the deny reason PID_NOT_PRESENT.
func TestFailedPseudonymisationScrubsRatherThanPassesThrough(t *testing.T) {
	bsnkTestServer(t, http.StatusInternalServerError, "")
	out := GraphQLToContext(graphQLRequest(t, map[string]string{}, map[string]any{"bsn": demoBSN}))
	if in := decisionInput(t, out); strings.Contains(in, demoBSN) {
		t.Fatalf("raw BSN reached the decision input: %s", in)
	}
	if got := pidPI(out); got != "" {
		t.Fatalf("pip.pid.pi = %#v, want empty", got)
	}
}
