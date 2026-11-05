package httpapi_test

// A pre-authorized offer with a transaction code (OpenID4VCI 1.0 section
// 4.1.1, #614 phase 4, F3; Q-81 option b). The issuer makes the code and
// hands it to the offer caller, who delivers it through another channel.
// Cases X-1 to X-7.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"gbo-demo/oid4vc-kit/oid4vci"
)

// txOffer makes an offer with a transaction code on profile and returns the
// response.
func (f *fixture) txOffer(profile string) (int, map[string]any) {
	f.t.Helper()
	return f.offer(map[string]any{
		"profile": profile, "credential_configuration_ids": []string{incomeConfig},
		"subject": map[string]any{"bsn": "999990019"}, "parameters": map[string]string{"jaar": "2025"}, "tx_code": true,
	})
}

func preAuthorized(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	grants := body["credential_offer"].(map[string]any)["grants"].(map[string]any)
	return grants[oid4vci.GrantPreAuthorizedCode].(map[string]any)
}

func TestAnOfferWithATransactionCode(t *testing.T) { // X-1, X-2
	f := newFixture(t)
	status, body := f.txOffer("eudi")
	if status != http.StatusCreated {
		t.Fatalf("offer = %d %v", status, body)
	}
	grant := preAuthorized(t, body)
	tx, _ := grant["tx_code"].(map[string]any)
	if tx["input_mode"] != "numeric" || tx["length"] != float64(6) || tx["description"] != "Vul de code in die u apart hebt ontvangen." {
		t.Errorf("tx_code = %v", tx)
	}
	code, _ := body["tx_code_value"].(string)
	if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		t.Errorf("tx_code_value = %q, want six digits", code)
	}
	if strings.Contains(body["links"].(map[string]any)["openid-credential-offer"].(string), code) {
		t.Error("the code is in the offer link")
	}
	for _, profile := range []string{"nl", "wallet"} {
		if status, body := f.txOffer(profile); status != http.StatusBadRequest || body["error"] != "tx_code_unsupported" {
			t.Errorf("X-2: offer with tx_code on %s = %d %v", profile, status, body)
		}
	}
}

func TestRedeemingWithATransactionCode(t *testing.T) { // X-3 .. X-7
	f := newFixture(t)
	offer := func() (string, string) {
		_, body := f.txOffer("eudi")
		return preAuthorized(t, body)["pre-authorized_code"].(string), body["tx_code_value"].(string)
	}
	wrong := func(code string) string {
		if code == "000000" {
			return "111111"
		}
		return "000000"
	}
	w := f.wallet("eudi")

	t.Run("X-3: the right code", func(t *testing.T) {
		c, code := offer()
		if status, body := w.tokenRequestForm(c, url.Values{"tx_code": {code}}); status != http.StatusOK {
			t.Fatalf("token = %d %v", status, body)
		}
	})
	t.Run("X-4: no or a wrong code, then the right one", func(t *testing.T) {
		c, code := offer()
		// T-1 (OpenID4VCI 1.0 section 6.3): a missing code is invalid_request
		// and no failed attempt.
		for i := 0; i < 3; i++ {
			if status, body := w.tokenRequestForm(c, nil); status != http.StatusBadRequest || body["error"] != "invalid_request" {
				t.Fatalf("token without code = %d %v, want invalid_request", status, body)
			}
		}
		if status, body := w.tokenRequestForm(c, url.Values{"tx_code": {wrong(code)}}); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
			t.Fatalf("T-2: token with a wrong code = %d %v, want invalid_grant", status, body)
		}
		if status, body := w.tokenRequestForm(c, url.Values{"tx_code": {code}}); status != http.StatusOK {
			t.Fatalf("token with the right code after missing codes and a failure = %d %v", status, body)
		}
	})
	t.Run("X-5: three failures use the offer up", func(t *testing.T) {
		c, code := offer()
		for i := 0; i < 3; i++ {
			if status, body := w.tokenRequestForm(c, url.Values{"tx_code": {wrong(code)}}); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
				t.Fatalf("failure %d = %d %v", i+1, status, body)
			}
		}
		if status, body := w.tokenRequestForm(c, url.Values{"tx_code": {code}}); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
			t.Fatalf("the right code after three failures = %d %v, want invalid_grant", status, body)
		}
	})
	t.Run("X-6: a code for an offer without one", func(t *testing.T) {
		c := f.preAuthCode("eudi")
		if status, body := w.tokenRequestForm(c, url.Values{"tx_code": {"123456"}}); status != http.StatusBadRequest || body["error"] != "invalid_request" {
			t.Fatalf("token = %d %v, want invalid_request", status, body)
		}
		if status, body := w.tokenRequestForm(c, nil); status != http.StatusOK {
			t.Fatalf("the offer is still usable without a code: %d %v", status, body)
		}
	})
	t.Run("X-7: the code is not logged", func(t *testing.T) {
		_, code := offer()
		// Timestamps hold digit runs too: look for the code as a value.
		if logs := f.logs.String(); strings.Contains(logs, "="+code) || strings.Contains(logs, `"`+code+`"`) {
			t.Fatal("the log holds the transaction code")
		}
	})
}
