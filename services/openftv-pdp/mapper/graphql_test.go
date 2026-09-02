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
)

func TestIsEUDIFlow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		flow string
		want bool
	}{
		{flow: "eudi:attestation", want: true},
		// Bron-specific variants were retired; the policy denies them
		// (engine_test.rego), so the mapper must not treat them as
		// wallet flows either.
		{flow: "eudi:attestation:brp", want: false},
		{flow: "dvtp:query", want: false},
		{flow: "eudi:attestation-unknown", want: false},
		{flow: "", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.flow, func(t *testing.T) {
			t.Parallel()
			if got := isEUDIFlow(tt.flow); got != tt.want {
				t.Fatalf("isEUDIFlow(%q) = %v, want %v", tt.flow, got, tt.want)
			}
		})
	}
}

// fscToken builds an unsigned JWT carrying the given claims payload. The
// mapper reads the payload without verifying — FSC-Inway validated the
// signature before it called us — so the header and signature are filler.
func fscToken(payloadJSON string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(payloadJSON)) + ".sig"
}

func TestFlowFromHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{
			name:    "grant-property claim",
			headers: map[string]string{"fsc-authorization": "Bearer " + fscToken(`{"prp":{"flow":"dvtp:query"}}`)},
			want:    "dvtp:query",
		},
		{
			// `add` is the retired OpenFSC Additional Claims hook. Honouring
			// it would accept a regime only the provider signed, next to the
			// countersigned one in `prp`.
			name:    "retired add claim is not honoured",
			headers: map[string]string{"fsc-authorization": "Bearer " + fscToken(`{"add":{"flow":"eudi:attestation"}}`)},
			want:    "",
		},
		{
			name:    "no token denies rather than defaulting",
			headers: map[string]string{},
			want:    "",
		},
		{
			name:    "claim without a flow",
			headers: map[string]string{"fsc-authorization": "Bearer " + fscToken(`{"prp":{"subject_id_type":"pseudonym"}}`)},
			want:    "",
		},
		{
			name:    "undecodable token",
			headers: map[string]string{"fsc-authorization": "Bearer not-a-jwt"},
			want:    "",
		},
		{
			// The X-GBO-Flow header let a caller name the regime it wanted
			// to be judged under. It is no longer sent and no longer read;
			// this guards against it coming back.
			name:    "X-GBO-Flow header is not honoured",
			headers: map[string]string{"x-gbo-flow": "dvtp:query"},
			want:    "",
		},
		{
			name: "header does not override the claim",
			headers: map[string]string{
				"fsc-authorization": "Bearer " + fscToken(`{"prp":{"flow":"eudi:attestation"}}`),
				"x-gbo-flow":        "dvtp:query",
			},
			want: "eudi:attestation",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := flowFromHeaders(tt.headers); got != tt.want {
				t.Fatalf("flowFromHeaders() = %q, want %q", got, tt.want)
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
