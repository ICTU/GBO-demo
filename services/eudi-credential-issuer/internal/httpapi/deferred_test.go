package httpapi_test

// Deferred issuance (OpenID4VCI 1.0 section 9, #614 phase 4, F6): after a
// login the source is temporarily unavailable; the wallet gets its code
// anyway, the credential request answers with a transaction_id, and the
// deferred request asks the source again (V1-V5). Cases D-1 to D-9.

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
)

// deferredStart logs in while the source is unavailable and asks for the
// credential; it returns the wallet, its token and the credential response.
func deferredStart(t *testing.T, f *fixture) (*acfWallet, oid4vci.TokenResponse, int, map[string]any) {
	t.Helper()
	f.setSource(http.StatusServiceUnavailable, nil)
	w := f.acfWallet()
	status, body := w.tokenForCode(w.code(nil), nil, nil)
	if status != http.StatusOK {
		t.Fatalf("token = %d %v", status, body)
	}
	var tok oid4vci.TokenResponse
	remarshal(t, body, &tok)
	status, cred := w.issueWith(t, tok)
	return w, tok, status, cred
}

// deferred posts a deferred credential request with token and dpopKey (the
// wallet's when nil).
func (w *wallet) deferred(accessToken string, dpopKey *ecdsa.PrivateKey, body map[string]any) (int, map[string]any) {
	w.f.t.Helper()
	if dpopKey == nil {
		dpopKey = w.dpopKey
	}
	raw, _ := json.Marshal(body)
	return w.post("/deferred_credential", bytes.NewReader(raw), map[string]string{
		"Authorization": "DPoP " + accessToken,
		"DPoP":          testwallet.DPoP(w.f.t, dpopKey, "POST", w.f.idOf(w.issuer, w.profile)+"/deferred_credential", accessToken),
	}, "Content-Type", "application/json")
}

func TestDeferredMetadata(t *testing.T) { // D-1
	f := newFixture(t)
	if md := f.metadata(t, "tax", "eudi"); md["deferred_credential_endpoint"] != f.id("eudi")+"/deferred_credential" {
		t.Errorf("eudi deferred_credential_endpoint = %v", md["deferred_credential_endpoint"])
	}
	for _, profile := range []string{"nl", "wallet"} {
		if md := f.metadata(t, "tax", profile); md["deferred_credential_endpoint"] != nil {
			t.Errorf("%s publishes a deferred_credential_endpoint", profile)
		}
	}
}

func TestDeferredIssuance(t *testing.T) { // D-2, D-3, D-4, D-8
	f := newFixture(t)
	w, tok, status, cred := deferredStart(t, f)
	txID, _ := cred["transaction_id"].(string)
	if status != http.StatusAccepted || txID == "" || cred["interval"] != float64(5) || cred["credentials"] != nil {
		t.Fatalf("D-2: credential = %d %v, want 202 with transaction_id and interval 5", status, cred)
	}
	calls := f.sourceCallCount()

	// D-4: still unavailable.
	status, body := w.deferred(tok.AccessToken, nil, map[string]any{"transaction_id": txID})
	if status != http.StatusAccepted || body["transaction_id"] != txID || body["interval"] != float64(5) {
		t.Fatalf("D-4: deferred = %d %v, want 202 with the same transaction_id", status, body)
	}
	// D-3: available again.
	f.setSource(http.StatusOK, f.sourceDocs)
	status, body = w.deferred(tok.AccessToken, nil, map[string]any{"transaction_id": txID})
	if status != http.StatusOK || body["credentials"] == nil {
		t.Fatalf("D-3: deferred = %d %v, want the credential", status, body)
	}
	if status, body := w.deferred(tok.AccessToken, nil, map[string]any{"transaction_id": txID}); status != http.StatusBadRequest || body["error"] != "invalid_transaction_id" {
		t.Errorf("D-3: the used transaction_id = %d %v, want invalid_transaction_id", status, body)
	}
	// D-8: every attempt asked the source with the BSN from the login.
	if got := f.sourceCallCount() - calls; got != 2 {
		t.Errorf("D-8: the source was asked %d times by the deferred requests, want 2", got)
	}
	if b := string(f.lastSourceBody()); !strings.Contains(b, loginBSN) || !strings.Contains(b, `"origin":"login"`) {
		t.Errorf("D-8: source request = %s", b)
	}
	if strings.Contains(f.logs.String(), loginBSN) {
		t.Error("the log holds the BSN")
	}
}

func TestDeferredRefusals(t *testing.T) { // D-5, D-7
	t.Run("D-5: the source has no data", func(t *testing.T) {
		f := newFixture(t)
		w, tok, _, cred := deferredStart(t, f)
		f.setSource(http.StatusNotFound, nil)
		if status, body := w.deferred(tok.AccessToken, nil, map[string]any{"transaction_id": cred["transaction_id"]}); status != http.StatusBadRequest || body["error"] != "credential_request_denied" {
			t.Fatalf("deferred = %d %v, want credential_request_denied", status, body)
		}
		if status, body := w.deferred(tok.AccessToken, nil, map[string]any{"transaction_id": cred["transaction_id"]}); status != http.StatusBadRequest || body["error"] != "invalid_transaction_id" {
			t.Fatalf("after the refusal = %d %v, want invalid_transaction_id", status, body)
		}
	})
	t.Run("D-7: another session's token, another DPoP key, unknown id", func(t *testing.T) {
		f := newFixture(t)
		w, tok, _, cred := deferredStart(t, f)
		// The source answers again: the refusals come before it is asked.
		f.setSource(http.StatusOK, f.sourceDocs)
		other := f.wallet("eudi")
		otherTok := other.token(f.preAuthCode("eudi"))
		if status, body := other.deferred(otherTok.AccessToken, nil, map[string]any{"transaction_id": cred["transaction_id"]}); status != http.StatusBadRequest || body["error"] != "invalid_transaction_id" {
			t.Errorf("another session's token = %d %v, want invalid_transaction_id", status, body)
		}
		if status, body := w.deferred(tok.AccessToken, testpki.NewKey(t), map[string]any{"transaction_id": cred["transaction_id"]}); status != http.StatusUnauthorized || body["error"] != "invalid_dpop_proof" {
			t.Errorf("another DPoP key = %d %v, want 401 invalid_dpop_proof", status, body)
		}
		if status, body := w.deferred(tok.AccessToken, nil, map[string]any{"transaction_id": "nope"}); status != http.StatusBadRequest || body["error"] != "invalid_transaction_id" {
			t.Errorf("unknown id = %d %v, want invalid_transaction_id", status, body)
		}
		// D-10 (OpenID4VCI 1.0 section 9.1): transaction_id is required.
		if status, body := w.deferred(tok.AccessToken, nil, map[string]any{}); status != http.StatusBadRequest || body["error"] != "invalid_credential_request" {
			t.Errorf("no transaction_id = %d %v, want invalid_credential_request", status, body)
		}
		// The refusals did not use the transaction up.
		if status, body := w.deferred(tok.AccessToken, nil, map[string]any{"transaction_id": cred["transaction_id"]}); status != http.StatusOK {
			t.Errorf("the transaction after the refusals = %d %v, want the credential", status, body)
		}
	})
	t.Run("no refresh while the source has not answered", func(t *testing.T) {
		f := newFixture(t)
		w, tok, _, _ := deferredStart(t, f)
		if status, body := w.refresh(tok.RefreshToken, nil); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
			t.Errorf("refresh = %d %v, want invalid_grant", status, body)
		}
	})
	t.Run("a pending credential is deferred once", func(t *testing.T) {
		f := newFixture(t)
		w, tok, _, _ := deferredStart(t, f)
		if status, body := w.issueWith(t, tok); status != http.StatusBadRequest || body["error"] != "invalid_credential_request" {
			t.Errorf("second credential request = %d %v, want invalid_credential_request", status, body)
		}
	})
}

// D-6: the transaction, and the BSN with it, last 24 hours (here one
// minute); afterwards the id is unknown (OpenID4VCI 1.0 has no error for an
// expired one).
func TestAnExpiredDeferral(t *testing.T) { // D-6
	f := newFixtureWith(t, func(_ *fixture, cfg *issuance.Config) { cfg.DeferralTTL = time.Minute })
	f.setSource(http.StatusServiceUnavailable, nil)
	w := f.acfWallet()
	requestURI := w.requestURI(nil)
	f.clock.Advance(-59 * time.Second)
	_, location, _ := w.login(w.authorize(requestURI))
	u, _ := url.Parse(location)
	_, body := w.tokenForCode(u.Query().Get("code"), nil, nil)
	var tok oid4vci.TokenResponse
	remarshal(t, body, &tok)
	_, cred := w.issueWith(t, tok)
	f.clock.Advance(61 * time.Second)
	f.setSource(http.StatusOK, f.sourceDocs)
	if status, body := w.deferred(tok.AccessToken, nil, map[string]any{"transaction_id": cred["transaction_id"]}); status != http.StatusBadRequest || body["error"] != "invalid_transaction_id" {
		t.Fatalf("deferred = %d %v, want invalid_transaction_id", status, body)
	}
}

// D-9: an encrypted deferred request may ask for an encrypted response; a
// plain one may not (section 9.1).
func TestEncryptedDeferredRequest(t *testing.T) { // D-9
	f := newFixture(t)
	w, tok, _, cred := deferredStart(t, f)
	f.setSource(http.StatusOK, f.sourceDocs)
	walletKey, params := testwallet.ResponseEncryption(t, "A256GCM")
	if status, body := w.deferred(tok.AccessToken, nil, map[string]any{"transaction_id": cred["transaction_id"], "credential_response_encryption": params}); status != http.StatusBadRequest || body["error"] != "invalid_encryption_parameters" {
		t.Fatalf("plain request with response encryption = %d %v", status, body)
	}
	raw, _ := json.Marshal(map[string]any{"transaction_id": cred["transaction_id"], "credential_response_encryption": params})
	jwe := testwallet.EncryptRequest(t, raw, f.requestKey(t, "tax"), jose.A256GCM, nil)
	status, ctype, body := w.postJWTTo(t, "/deferred_credential", tok.AccessToken, jwe)
	if status != http.StatusOK || ctype != "application/jwt" {
		t.Fatalf("encrypted deferred = %d %s %s", status, ctype, body)
	}
	_, plain := testwallet.DecryptResponse(t, body, walletKey)
	if !strings.Contains(string(plain), `"credentials"`) {
		t.Fatalf("decrypted response %s", plain)
	}
}

// postJWTTo posts an encrypted request to path with the wallet's DPoP key.
func (w *wallet) postJWTTo(t *testing.T, path, accessToken, body string) (int, string, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, w.f.idOf(w.issuer, w.profile)+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/jwt")
	req.Header.Set("Authorization", "DPoP "+accessToken)
	req.Header.Set("DPoP", testwallet.DPoP(t, w.dpopKey, "POST", w.f.idOf(w.issuer, w.profile)+path, accessToken))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(out)
}
