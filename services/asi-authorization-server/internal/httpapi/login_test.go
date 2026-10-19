package httpapi

// The request page and the DigiD mock of #544 (cases L1-L8). L4 is in
// TestFullFlowIssuesDPoPBoundAccessToken, L5 and L10 in e2e_test.go, L9 in
// the reference QTSP.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"gbo-demo/asi-authorization-server/internal/digidmock"
)

// L1: one screen names the QTSP by its legal name, what it asks and at which
// source, and offers DigiD (mock), the NL Wallet (#546) and deny.
func TestRequestPage(t *testing.T) {
	as := newTestAS(t)
	tx := as.startAuthorization(t, authorizeParams(newPKCE()))
	for _, want := range []string{
		"Testomgeving — geen echte identificatie", testClientName, "wil bij Basisregistratie Personen (test) laten controleren of gegevens over u kloppen.", "Controleren of gegevens kloppen",
		`value="digid">Inloggen met DigiD (mock)`, `value="wallet">Inloggen met NL Wallet`, `value="deny">Weigeren`,
	} {
		if !strings.Contains(tx.page, want) {
			t.Errorf("request page lacks %q:\n%s", want, tx.page)
		}
	}
	if strings.Contains(tx.page, testClientID) || strings.Contains(tx.page, "testpersoon") {
		t.Errorf("request page shows the client_id or a test person:\n%s", tx.page)
	}
	for header, want := range map[string]string{"Cache-Control": "no-store", "X-Frame-Options": "DENY"} {
		if got := tx.header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if csp := tx.header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("Content-Security-Policy = %q, want frame-ancestors 'none'", csp)
	}
	if c := tx.cookie; !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/authorize" {
		t.Errorf("transaction cookie = %+v, want HttpOnly, SameSite=Strict, Path /authorize", c)
	}
}

// A client without a legal name is shown by its client_id; the user sees
// which authentic source the authorization is for.
func TestRequestPageWithoutALegalName(t *testing.T) {
	as := newTestAS(t)
	params := authorizeParams(newPKCE())
	params.Set("client_id", otherClientID)
	params.Set("resource", testResource2)
	setAuthorizationDetails(params, "https://gbo.example/attributes/education_qualification/1")
	tx := as.startAuthorization(t, params)
	if !strings.Contains(tx.page, otherClientID) || !strings.Contains(tx.page, "bij DUO (test)") || strings.Contains(tx.page, "Basisregistratie Personen") {
		t.Errorf("request page does not name the client_id and the second data service alone:\n%s", tx.page)
	}
}

// L2: deny without logging in.
func TestDenyIsAccessDenied(t *testing.T) {
	as := newTestAS(t)
	tx := as.startAuthorization(t, authorizeParams(newPKCE()))
	assertAuthorizeError(t, as.decide(t, tx.id, tx.cookie, url.Values{"decision": {"deny"}}), "access_denied")
}

// L3: DigiD (mock) leads to its login page, and a login goes straight back
// to the client with a code.
func TestDigiDLogin(t *testing.T) {
	as := newTestAS(t)
	tx := as.startAuthorization(t, authorizeParams(newPKCE()))
	resp := as.decide(t, tx.id, tx.cookie, url.Values{"decision": {"digid"}})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, digidmock.Path+"?") {
		t.Fatalf("choosing DigiD = %d to %q, want 303 to %s", resp.StatusCode, loc, digidmock.Path)
	}
	page, body := as.get(t, loc, tx.cookie)
	if page.StatusCode != http.StatusOK || !strings.Contains(body, "Mock van DigiD") || !strings.Contains(body, `name="bsn"`) || !strings.Contains(body, tx.id) {
		t.Fatalf("DigiD page = %d:\n%s", page.StatusCode, body)
	}
	q := redirectParams(t, as.digidLogin(t, tx.id, tx.cookie, bsnFrouke))
	if q.Get("code") == "" || q.Get("state") != "state-123" {
		t.Fatalf("redirect after the DigiD login = %v, want a code and the state", q)
	}
	// Annuleren on the DigiD page posts deny to the request page's endpoint.
	if !strings.Contains(body, `formaction="`+DecisionPath+`" name="decision" value="deny"`) {
		t.Errorf("DigiD page has no Annuleren that denies:\n%s", body)
	}
}

// L7: a BSN that is not nine digits keeps the DigiD page open with an error,
// and the transaction stays usable; a nine-digit BSN the source does not know
// logs in (the ASIP answers Unknown).
func TestDigiDRefusesABSNThatIsNotNineDigits(t *testing.T) {
	as := newTestAS(t)
	tx := as.startAuthorization(t, authorizeParams(newPKCE()))
	for _, bsn := range []string{"", "12345678", "1234567890", "12345678a", "999 991 772"} {
		resp := as.digidLogin(t, tx.id, tx.cookie, bsn)
		if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
			t.Errorf("BSN %q = %d, Location %q; want 400 on the DigiD page", bsn, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if q := redirectParams(t, as.digidLogin(t, tx.id, tx.cookie, "123456782")); q.Get("code") == "" {
		t.Fatalf("a nine-digit BSN unknown to the source = %v, want a code", q)
	}
}

// L6, L8 and the protection of a transaction: none of these can be
// redirected to the client.
func TestChoiceThatCannotBeRedirected(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, as *testAS) *http.Response
	}{
		{"L6: DigiD login without the transaction cookie", func(t *testing.T, as *testAS) *http.Response {
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			return as.digidLogin(t, tx.id, nil, bsnFrouke)
		}},
		{"L6: DigiD login with the cookie of another transaction", func(t *testing.T, as *testAS) *http.Response {
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			other := as.startAuthorization(t, authorizeParams(newPKCE()))
			c := *other.cookie
			c.Name = tx.cookie.Name
			return as.digidLogin(t, tx.id, &c, bsnFrouke)
		}},
		{"L6: DigiD login for an unknown transaction", func(t *testing.T, as *testAS) *http.Response {
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			return as.digidLogin(t, "unknown", tx.cookie, bsnFrouke)
		}},
		{"L6: DigiD login for a used transaction", func(t *testing.T, as *testAS) *http.Response {
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			if resp := as.digidLogin(t, tx.id, tx.cookie, bsnFrouke); resp.StatusCode != http.StatusFound {
				t.Fatalf("first login = %d, want 302", resp.StatusCode)
			}
			return as.digidLogin(t, tx.id, tx.cookie, bsnFrouke)
		}},
		{"L6: DigiD login for an expired transaction", func(t *testing.T, as *testAS) *http.Response {
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			as.offset = 5*time.Minute + time.Second
			return as.digidLogin(t, tx.id, tx.cookie, bsnFrouke)
		}},
		{"L6: choosing DigiD without the transaction cookie", func(t *testing.T, as *testAS) *http.Response {
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			return as.decide(t, tx.id, nil, url.Values{"decision": {"digid"}})
		}},
		{"L6: the DigiD page without the transaction cookie", func(t *testing.T, as *testAS) *http.Response {
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			resp, _ := as.get(t, digidmock.Path+"?transaction="+tx.id, nil)
			return resp
		}},
		{"deny without the transaction cookie", func(t *testing.T, as *testAS) *http.Response {
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			return as.decide(t, tx.id, nil, url.Values{"decision": {"deny"}})
		}},
		{"L8: the wallet when no verifier is configured", func(t *testing.T, _ *testAS) *http.Response {
			as := newTestASWithoutWallet(t)
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			return as.decide(t, tx.id, tx.cookie, url.Values{"decision": {"wallet"}})
		}},
		{"a choice other than deny, digid or wallet", func(t *testing.T, as *testAS) *http.Response {
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			return as.decide(t, tx.id, tx.cookie, url.Values{"decision": {"approve"}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.run(t, newTestAS(t))
			if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
				t.Fatalf("= %d, Location %q; want 400 without redirect", resp.StatusCode, resp.Header.Get("Location"))
			}
		})
	}
}

// L8: without a verifier the wallet is shown but inactive, and after a
// refused wallet choice the transaction is still there for DigiD.
func TestWalletChoiceLeavesTheTransaction(t *testing.T) {
	as := newTestASWithoutWallet(t)
	tx := as.startAuthorization(t, authorizeParams(newPKCE()))
	if !strings.Contains(tx.page, `value="wallet" disabled>Inloggen met NL Wallet`) {
		t.Errorf("without a verifier the wallet is not inactive:\n%s", tx.page)
	}
	_ = as.decide(t, tx.id, tx.cookie, url.Values{"decision": {"wallet"}})
	if q := redirectParams(t, as.digidLogin(t, tx.id, tx.cookie, bsnFrouke)); q.Get("code") == "" {
		t.Fatalf("DigiD after the wallet choice = %v, want a code", q)
	}
}
