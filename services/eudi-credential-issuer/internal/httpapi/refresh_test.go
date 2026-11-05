package httpapi_test

// Re-issuance with a refresh token (RFC 6749 section 6, OpenID4VCI 1.0
// section 6; HAIP 1.0 section 4.4 recommends it), #614 phase 4, F8. The
// re-issued credential has the claims of the first issuance (R2): the issuer
// keeps no BSN and does not ask the source again. Cases F-1 to F-7.

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
)

// refresh redeems refreshToken with the wallet's attestation and dpopKey.
func (w *wallet) refresh(refreshToken string, dpopKey *ecdsa.PrivateKey) (int, map[string]any) {
	w.f.t.Helper()
	if dpopKey == nil {
		dpopKey = w.dpopKey
	}
	id := w.f.idOf(w.issuer, w.profile)
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}
	return w.post("/token", strings.NewReader(form.Encode()), map[string]string{
		"OAuth-Client-Attestation":     w.wia,
		"OAuth-Client-Attestation-PoP": testwallet.PoP(w.f.t, w.wiaKey, w.clientID, id, w.challenge()),
		"DPoP":                         testwallet.DPoP(w.f.t, dpopKey, "POST", id+"/token", ""),
	}, "Content-Type", "application/x-www-form-urlencoded")
}

// issueWith asks for the first credential of tok with a fresh key.
func (w *wallet) issueWith(t *testing.T, tok oid4vci.TokenResponse) (int, map[string]any) {
	t.Helper()
	ka := w.f.eudiWallet.KeyAttestation(t, []*ecdsa.PrivateKey{testpki.NewKey(t)}, w.nonce(), time.Now().Add(time.Hour))
	return w.credentialRequest(tok.AccessToken, w.dpopKey, oid4vci.CredentialRequest{
		CredentialIdentifier: tok.AuthorizationDetails[0].CredentialIdentifiers[0], Proofs: &oid4vci.Proofs{Attestation: []string{ka}},
	})
}

func TestRefreshTokens(t *testing.T) { // F-1, F-2
	f := newFixture(t)
	if grants, _ := f.asMetadata(t, "eudi")["grant_types_supported"].([]any); !containsAny(grants, "refresh_token") {
		t.Errorf("F-1: grant_types_supported = %v, want refresh_token", grants)
	}
	w := f.wallet("eudi")
	status, body := w.tokenRequest(f.preAuthCode("eudi"), nil)
	rt, _ := body["refresh_token"].(string)
	if status != http.StatusOK || rt == "" {
		t.Fatalf("F-1: token = %d %v, want a refresh_token", status, body)
	}
	var first oid4vci.TokenResponse
	remarshal(t, body, &first)
	if status, cred := w.issueWith(t, first); status != http.StatusOK {
		t.Fatalf("first credential = %d %v", status, cred)
	}
	calls := f.sourceCallCount()

	status, body = w.refresh(rt, nil)
	next, _ := body["refresh_token"].(string)
	if status != http.StatusOK || body["access_token"] == nil || next == "" || next == rt {
		t.Fatalf("F-2: refresh = %d %v, want a new access and refresh token", status, body)
	}
	var again oid4vci.TokenResponse
	remarshal(t, body, &again)
	status, cred := w.issueWith(t, again)
	if status != http.StatusOK || cred["credentials"] == nil {
		t.Fatalf("F-2: re-issued credential = %d %v", status, cred)
	}
	if f.sourceCallCount() != calls {
		t.Error("F-2: the source was asked again; a re-issuance keeps the claims")
	}
	if status, body := w.refresh(rt, nil); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Errorf("F-4: the used refresh token = %d %v, want invalid_grant", status, body)
	}
	if status, _ := w.refresh(next, nil); status != http.StatusOK {
		t.Errorf("the rotated refresh token works: %d", status)
	}
}

func TestRefreshTokenRefusals(t *testing.T) { // F-3, F-4, F-5, F-6
	f := newFixture(t)
	refreshOf := func(t *testing.T, w *wallet, code string) string {
		t.Helper()
		_, body := w.tokenRequest(code, nil)
		rt, _ := body["refresh_token"].(string)
		if rt == "" {
			t.Fatalf("no refresh token: %v", body)
		}
		return rt
	}
	t.Run("F-3: another DPoP key", func(t *testing.T) {
		w := f.wallet("eudi")
		rt := refreshOf(t, w, f.preAuthCode("eudi"))
		if status, body := w.refresh(rt, testpki.NewKey(t)); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
			t.Fatalf("refresh = %d %v, want invalid_grant", status, body)
		}
	})
	t.Run("F-3: another wallet", func(t *testing.T) {
		w := f.wallet("eudi")
		rt := refreshOf(t, w, f.preAuthCode("eudi"))
		other := f.wallet("eudi")
		other.clientID = "another-wallet"
		other.wia = f.eudiWallet.WIA(t, other.clientID, other.wiaKey, time.Now().Add(time.Hour))
		other.dpopKey = w.dpopKey
		if status, body := other.refresh(rt, nil); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
			t.Fatalf("refresh = %d %v, want invalid_grant", status, body)
		}
	})
	t.Run("F-5: the offer was revoked", func(t *testing.T) {
		w := f.wallet("eudi")
		rt := refreshOf(t, w, f.preAuthCodeRef("eudi", "case-refresh"))
		if status, _ := f.revoke(map[string]any{"reference": "case-refresh"}); status != http.StatusOK {
			t.Fatal("revocation failed")
		}
		if status, body := w.refresh(rt, nil); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
			t.Fatalf("refresh = %d %v, want invalid_grant", status, body)
		}
	})
	t.Run("F-6: no refresh token on nl", func(t *testing.T) {
		w := f.wallet("nl")
		_, body := w.tokenRequest(f.preAuthCode("nl"), nil)
		if body["refresh_token"] != nil {
			t.Fatalf("nl token response %v carries a refresh token", body)
		}
		if grants, _ := f.asMetadata(t, "nl")["grant_types_supported"].([]any); containsAny(grants, "refresh_token") {
			t.Fatalf("nl advertises refresh_token")
		}
	})
}

// A refresh token expires (R3: 30 days; here the lifetime is one minute).
// It is made 59 seconds in the issuer's past and used 2 seconds after it
// expired: the wallet's proofs use the real clock.
func TestAnExpiredRefreshToken(t *testing.T) { // F-4
	f := newFixtureWith(t, func(_ *fixture, cfg *issuance.Config) { cfg.RefreshTTL = time.Minute })
	w := f.wallet("eudi")
	code := f.preAuthCode("eudi")
	f.clock.Advance(-59 * time.Second)
	_, body := w.tokenRequest(code, nil)
	rt, _ := body["refresh_token"].(string)
	f.clock.Advance(61 * time.Second)
	if status, body := w.refresh(rt, nil); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("refresh = %d %v, want invalid_grant", status, body)
	}
}

// asMetadata returns the authorization server metadata of tax on profile.
func (f *fixture) asMetadata(t *testing.T, profile string) map[string]any {
	t.Helper()
	_, body, _ := get(t, f.public.URL+"/.well-known/oauth-authorization-server/tax/"+profile, "")
	var md map[string]any
	if err := json.Unmarshal([]byte(body), &md); err != nil {
		t.Fatal(err)
	}
	return md
}

// revoke posts a revocation to the internal API.
func (f *fixture) revoke(body map[string]any) (int, map[string]any) {
	f.t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(f.internal.URL+"/revocations", "application/json", bytes.NewReader(raw))
	if err != nil {
		f.t.Fatal(err)
	}
	return decode(f.t, resp)
}

func containsAny(list []any, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
