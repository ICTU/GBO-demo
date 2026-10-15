package httpapi

// Logging in with the NL Wallet (#546, cases X1-X8), with a fake of the
// verifier behind the core's SessionIdentifier port. X3 and X10 are in the
// end-to-end tests, X9 is the recorded run with the real wallet.

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

var frouke = authorization.Identity{BSN: bsnFrouke, FamilyName: "Jansen", GivenName: "Frouke", Birthdate: "2000-03-24", Nationalities: []string{"NL", "BE"}}

// startWallet chooses the NL Wallet on the request page and returns the
// wallet page's path and the session the fake verifier opened.
func (as *testAS) startWallet(t *testing.T, tx pendingTx) (path, session string) {
	t.Helper()
	resp := as.decide(t, tx.id, tx.cookie, url.Values{"decision": {"wallet"}})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, WalletPath+"?") {
		t.Fatalf("choosing the wallet = %d to %q, want 303 to %s", resp.StatusCode, loc, WalletPath)
	}
	as.wallet.mu.Lock()
	defer as.wallet.mu.Unlock()
	return loc, fmt.Sprintf("session-%d", as.wallet.next)
}

// X1: the page shows the QR code of the session, and follows it.
func TestWalletShowsTheQRCode(t *testing.T) {
	as := newTestAS(t)
	tx := as.startAuthorization(t, authorizeParams(newPKCE()))
	path, session := as.startWallet(t, tx)
	resp, page := as.get(t, path, tx.cookie)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, `<img src="data:image/png;base64,`) || !strings.Contains(page, `http-equiv="refresh"`) {
		t.Fatalf("wallet page = %d:\n%s", resp.StatusCode, page)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "img-src data:") || strings.Contains(csp, "script-src") {
		t.Errorf("Content-Security-Policy = %q, want data images and no script", csp)
	}
	// Once the wallet has the request: no QR code, continue in the wallet.
	as.wallet.set(session, func(s *fakeWalletSession) { s.progress.Link = "" })
	if _, page := as.get(t, path, tx.cookie); strings.Contains(page, "<img") || !strings.Contains(page, "Ga verder in de NL Wallet") {
		t.Errorf("wallet page while the wallet has the request:\n%s", page)
	}
}

// X2: a disclosed PID logs in: the code goes to the client, and the token
// carries the BSN, the PID attributes and the means nl-wallet.
func TestWalletLogin(t *testing.T) {
	as := newTestAS(t)
	p := newPKCE()
	tx := as.startAuthorization(t, authorizeParams(p))
	path, session := as.startWallet(t, tx)
	as.wallet.set(session, func(s *fakeWalletSession) {
		s.progress = authorization.Progress{State: authorization.SessionDone}
		s.identity = frouke
	})
	resp, _ := as.get(t, path, tx.cookie)
	q := redirectParams(t, resp)
	if q.Get("code") == "" || q.Get("state") != "state-123" {
		t.Fatalf("redirect after the wallet = %v, want a code and the state", q)
	}
	r := as.postToken(t, as.tokenForm(t, q.Get("code"), p), as.proof(t, nil))
	token, _ := r.body["access_token"].(string)
	_, claims := verifyAccessToken(t, as, token)
	for field, want := range map[string]any{authorization.BSNClaim: bsnFrouke, "family_name": "Jansen", "given_name": "Frouke", "birthdate": "2000-03-24", "acr": "nl-wallet"} {
		if claims[field] != want {
			t.Errorf("%s = %v, want %v", field, claims[field], want)
		}
	}
	if n, _ := claims["nationalities"].([]any); len(n) != 2 {
		t.Errorf("nationalities = %v, want [NL BE]", claims["nationalities"])
	}
}

// X4: declined in the wallet, or expired: back to the request page, and the
// transaction stays for another means. X6: a disclosure that does not
// identify the user, likewise.
func TestWalletNotLoggedIn(t *testing.T) {
	for name, edit := range map[string]func(*fakeWalletSession){
		"X4: declined or expired": func(s *fakeWalletSession) { s.progress = authorization.Progress{State: authorization.SessionFailed} },
		"X6: no usable disclosure": func(s *fakeWalletSession) {
			s.progress = authorization.Progress{State: authorization.SessionDone}
			s.identifyErr = errors.New("the PID has no BSN")
		},
	} {
		t.Run(name, func(t *testing.T) {
			as := newTestAS(t)
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			path, session := as.startWallet(t, tx)
			as.wallet.set(session, edit)
			resp, page := as.get(t, path, tx.cookie)
			if resp.StatusCode != http.StatusOK || !strings.Contains(page, "Niet ingelogd met de NL Wallet") || !strings.Contains(page, `value="digid"`) {
				t.Fatalf("= %d:\n%s", resp.StatusCode, page)
			}
			if q := redirectParams(t, as.digidLogin(t, tx.id, tx.cookie, bsnFrouke)); q.Get("code") == "" {
				t.Fatalf("DigiD after the wallet = %v, want a code", q)
			}
		})
	}
}

// X5: the verifier is unreachable, when starting or while following: a page
// that says so, and the transaction stays for DigiD.
func TestWalletUnavailable(t *testing.T) {
	as := newTestAS(t)
	as.wallet.startErr = errors.New("connection refused")
	tx := as.startAuthorization(t, authorizeParams(newPKCE()))
	resp := as.decide(t, tx.id, tx.cookie, url.Values{"decision": {"wallet"}})
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Location") != "" {
		t.Fatalf("start with the verifier unreachable = %d, Location %q; want 503", resp.StatusCode, resp.Header.Get("Location"))
	}
	as.wallet.startErr = nil
	path, session := as.startWallet(t, tx)
	as.wallet.set(session, func(s *fakeWalletSession) { s.progressErr = errors.New("connection refused") })
	if resp, page := as.get(t, path, tx.cookie); resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(page, "niet beschikbaar") {
		t.Fatalf("following with the verifier unreachable = %d:\n%s", resp.StatusCode, page)
	}
	if q := redirectParams(t, as.digidLogin(t, tx.id, tx.cookie, bsnFrouke)); q.Get("code") == "" {
		t.Fatalf("DigiD after the verifier was unreachable = %v, want a code", q)
	}
}

// X7: a session belongs to its transaction. Another transaction cannot take
// a finished session over, also not with the session or transaction of the
// victim at hand; the session of another browser cannot be followed.
func TestWalletSessionIsBoundToItsTransaction(t *testing.T) {
	as := newTestAS(t)
	victim := as.startAuthorization(t, authorizeParams(newPKCE()))
	_, victimSession := as.startWallet(t, victim)
	as.wallet.set(victimSession, func(s *fakeWalletSession) {
		s.progress = authorization.Progress{State: authorization.SessionDone}
		s.identity = frouke
	})
	attacker := as.startAuthorization(t, authorizeParams(newPKCE()))
	attackerPath, _ := as.startWallet(t, attacker)
	if resp, page := as.get(t, attackerPath, attacker.cookie); resp.StatusCode != http.StatusOK || !strings.Contains(page, "<img") {
		t.Fatalf("the attacker's own session = %d, want its QR code, not a login:\n%s", resp.StatusCode, page)
	}
	// The victim's transaction with the attacker's cookie.
	c := *attacker.cookie
	c.Name = victim.cookie.Name
	if resp, _ := as.get(t, WalletPath+"?transaction="+victim.id, &c); resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
		t.Fatalf("the victim's session from another browser = %d, Location %q; want 400", resp.StatusCode, resp.Header.Get("Location"))
	}
	// A login that names the victim's session as credential takes only the
	// attacker's own session.
	if _, err := as.service.LogIn(t.Context(), authorization.Login{TransactionID: attacker.id, Binding: attacker.cookie.Value, Means: "nl-wallet", Credential: victimSession}); !errors.Is(err, authorization.ErrNotIdentified) {
		t.Fatalf("LogIn with the victim's session = %v, want not identified", err)
	}
}

// A transaction without a wallet session has no wallet page.
func TestWalletPageWithoutASession(t *testing.T) {
	as := newTestAS(t)
	tx := as.startAuthorization(t, authorizeParams(newPKCE()))
	if resp, _ := as.get(t, WalletPath+"?transaction="+tx.id, tx.cookie); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("= %d, want 400", resp.StatusCode)
	}
}
