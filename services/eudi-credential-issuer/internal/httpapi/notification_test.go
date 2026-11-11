package httpapi_test

// The notification endpoint (OpenID4VCI 1.0 section 11, #614 phase 4, F7):
// the wallet tells whether it stored an issued credential. The issuer logs
// the event and nothing else (N1). Cases N-1 to N-6.

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
)

// issuedOnEUDI issues one credential on the eudi profile and returns the
// wallet, its access token and the credential response.
func issuedOnEUDI(t *testing.T, f *fixture) (*wallet, string, map[string]any) {
	t.Helper()
	w := f.wallet("eudi")
	tok := w.token(f.preAuthCode("eudi"))
	ka := f.eudiWallet.KeyAttestation(t, []*ecdsa.PrivateKey{testpki.NewKey(t)}, w.nonce(), time.Now().Add(time.Hour))
	status, body := w.credentialRequest(tok.AccessToken, w.dpopKey, oid4vci.CredentialRequest{
		CredentialIdentifier: tok.AuthorizationDetails[0].CredentialIdentifiers[0], Proofs: &oid4vci.Proofs{Attestation: []string{ka}},
	})
	if status != http.StatusOK {
		t.Fatalf("credential = %d %v", status, body)
	}
	return w, tok.AccessToken, body
}

// notify posts a notification with the wallet's DPoP key.
func (w *wallet) notify(accessToken string, body map[string]any) (int, map[string]any) {
	w.f.t.Helper()
	raw, _ := json.Marshal(body)
	return w.post("/notification", bytes.NewReader(raw), map[string]string{
		"Authorization": "DPoP " + accessToken,
		"DPoP":          testwallet.DPoP(w.f.t, w.dpopKey, "POST", w.f.idOf(w.issuer, w.profile)+"/notification", accessToken),
	}, "Content-Type", "application/json")
}

func TestNotificationMetadataAndID(t *testing.T) { // N-1, N-6
	f := newFixture(t)
	if md := f.metadata(t, "tax", "eudi"); md["notification_endpoint"] != f.id("eudi")+"/notification" {
		t.Errorf("eudi notification_endpoint = %v", md["notification_endpoint"])
	}
	for _, profile := range []string{"nl", "wallet"} {
		if md := f.metadata(t, "tax", profile); md["notification_endpoint"] != nil {
			t.Errorf("%s publishes a notification_endpoint", profile)
		}
	}
	_, _, body := issuedOnEUDI(t, f)
	if id, _ := body["notification_id"].(string); id == "" {
		t.Fatalf("credential response %v has no notification_id", body)
	}
}

func TestNotifications(t *testing.T) { // N-2 .. N-5
	for _, event := range []string{"credential_accepted", "credential_failure", "credential_deleted"} {
		t.Run("N-2: "+event, func(t *testing.T) {
			f := newFixture(t)
			w, at, body := issuedOnEUDI(t, f)
			status, resp := w.notify(at, map[string]any{"notification_id": body["notification_id"], "event": event, "event_description": "test"})
			if status != http.StatusNoContent {
				t.Fatalf("notification = %d %v", status, resp)
			}
			if !strings.Contains(f.logs.String(), event) {
				t.Errorf("the event is not logged")
			}
			// An id is used once.
			if status, resp := w.notify(at, map[string]any{"notification_id": body["notification_id"], "event": event}); status != http.StatusBadRequest || resp["error"] != "invalid_notification_id" {
				t.Errorf("second notification = %d %v, want invalid_notification_id", status, resp)
			}
		})
	}
	f := newFixture(t)
	w, at, body := issuedOnEUDI(t, f)
	_, otherToken, otherBody := issuedOnEUDI(t, f)
	for name, tc := range map[string]struct {
		token  string
		body   map[string]any
		status int
		code   string
	}{
		"N-3: unknown id":            {at, map[string]any{"notification_id": "nope", "event": "credential_accepted"}, http.StatusBadRequest, "invalid_notification_id"},
		"N-3: id of another session": {at, map[string]any{"notification_id": otherBody["notification_id"], "event": "credential_accepted"}, http.StatusBadRequest, "invalid_notification_id"},
		"N-4: unknown event":         {at, map[string]any{"notification_id": body["notification_id"], "event": "credential_stolen"}, http.StatusBadRequest, "invalid_notification_request"},
		"N-4: no id":                 {at, map[string]any{"event": "credential_accepted"}, http.StatusBadRequest, "invalid_notification_request"},
		"N-5: unknown access token":  {"nope", map[string]any{"notification_id": body["notification_id"], "event": "credential_accepted"}, http.StatusUnauthorized, "invalid_token"},
	} {
		t.Run(name, func(t *testing.T) {
			if status, resp := w.notify(tc.token, tc.body); status != tc.status || resp["error"] != tc.code {
				t.Fatalf("notification = %d %v, want %d %s", status, resp, tc.status, tc.code)
			}
		})
	}
	_ = otherToken
}
