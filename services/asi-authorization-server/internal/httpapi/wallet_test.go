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

// Y1: the page always shows the same-device button and the cross-device QR
// code. HTTPS makes the button a universal link; on local HTTP it stays
// visible but disabled, with an explanation.
func TestWalletShowsTheQRCode(t *testing.T) {
	for name, tc := range map[string]struct {
		issuer  string
		enabled bool
	}{
		"HTTPS":      {issuer: "https://as.test", enabled: true},
		"local HTTP": {enabled: false},
	} {
		t.Run(name, func(t *testing.T) {
			as := newTestASWithIssuer(t, testResource, nil, true, tc.issuer)
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			path, session := as.startWallet(t, tx)
			as.wallet.mu.Lock()
			returnURLTemplate := as.wallet.returnURLTemplate
			as.wallet.mu.Unlock()
			wantReturnURLTemplate := ""
			if tc.enabled {
				wantReturnURLTemplate = "https://as.test" + WalletReturnPath + "?transaction=" + url.QueryEscape(tx.id) + "&session_token={session_token}"
			}
			if returnURLTemplate != wantReturnURLTemplate {
				t.Errorf("return URL template = %q, want %q", returnURLTemplate, wantReturnURLTemplate)
			}
			resp, page := as.get(t, path, tx.cookie)
			if resp.StatusCode != http.StatusOK || !strings.Contains(page, `src="data:image/png;base64,`) || !strings.Contains(page, `http-equiv="refresh"`) {
				t.Fatalf("wallet page = %d:\n%s", resp.StatusCode, page)
			}
			if tc.enabled {
				if !strings.Contains(page, `href="https://app.example/deeplink/same/`+session+`"`) || !strings.Contains(page, `>Open NL Wallet</a>`) {
					t.Errorf("HTTPS wallet page has no same-device universal link:\n%s", page)
				}
			} else if !strings.Contains(page, `disabled>Open NL Wallet</button>`) || !strings.Contains(page, "Inloggen op hetzelfde apparaat vereist HTTPS") {
				t.Errorf("local wallet page has no disabled same-device choice and explanation:\n%s", page)
			}
			if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "img-src 'self' data:") || strings.Contains(csp, "script-src") {
				t.Errorf("Content-Security-Policy = %q, want data images and no script", csp)
			}
			// Once the wallet has the request: no choices, continue in the wallet.
			as.wallet.set(session, func(s *fakeWalletSession) {
				s.progress.Link = ""
				s.progress.SameDeviceLink = ""
			})
			if _, page := as.get(t, path, tx.cookie); strings.Contains(page, "QR-code voor de NL Wallet") || strings.Contains(page, "Open NL Wallet") || !strings.Contains(page, "Ga verder in de NL Wallet") {
				t.Errorf("wallet page while the wallet has the request:\n%s", page)
			}
		})
	}
}

// Y2: after same-device disclosure, the wallet returns this browser with the
// session token and verifier nonce. Login completes exactly like the existing
// cross-device flow and the token carries the PID identity and wallet means.
func TestWalletSameDeviceReturnLogsIn(t *testing.T) {
	as := newTestASWithIssuer(t, testResource, nil, true, "https://as.test")
	p := newPKCE()
	tx := as.startAuthorization(t, authorizeParams(p))
	_, session := as.startWallet(t, tx)
	as.wallet.set(session, func(s *fakeWalletSession) {
		s.progress = authorization.Progress{State: authorization.SessionDone}
		s.identity = frouke
		s.returnNonce = "return-nonce"
	})
	returnPath := "/authorize/wallet/return?" + url.Values{
		"transaction":   {tx.id},
		"session_token": {session},
		"nonce":         {"return-nonce"},
	}.Encode()
	resp, _ := as.get(t, returnPath, tx.cookie)
	q := redirectParams(t, resp)
	if q.Get("code") == "" || q.Get("state") != "state-123" {
		t.Fatalf("redirect after the wallet return = %v, want a code and the state", q)
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

// Y3: a return URL opened in another browser cannot complete or consume the
// wallet session. The browser that owns the transaction can still return.
func TestWalletSameDeviceReturnIsBoundToTheBrowser(t *testing.T) {
	as := newTestASWithIssuer(t, testResource, nil, true, "https://as.test")
	victim := as.startAuthorization(t, authorizeParams(newPKCE()))
	_, session := as.startWallet(t, victim)
	as.wallet.set(session, func(s *fakeWalletSession) {
		s.progress = authorization.Progress{State: authorization.SessionDone}
		s.identity = frouke
		s.returnNonce = "return-nonce"
	})
	other := as.startAuthorization(t, authorizeParams(newPKCE()))
	returnPath := WalletReturnPath + "?" + url.Values{
		"transaction":   {victim.id},
		"session_token": {session},
		"nonce":         {"return-nonce"},
	}.Encode()
	if resp, _ := as.get(t, returnPath, other.cookie); resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
		t.Fatalf("wallet return in another browser = %d to %q, want 400 without redirect", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := as.get(t, returnPath, victim.cookie); redirectParams(t, resp).Get("code") == "" {
		t.Fatal("the owner's wallet return no longer completes after the other browser tried it")
	}
}

// S5: even in the browser that owns the transaction, the returned session
// token must equal the session the core stored with that transaction.
func TestWalletSameDeviceReturnRequiresTheKeptSession(t *testing.T) {
	as := newTestASWithIssuer(t, testResource, nil, true, "https://as.test")
	tx := as.startAuthorization(t, authorizeParams(newPKCE()))
	_, session := as.startWallet(t, tx)
	as.wallet.set(session, func(s *fakeWalletSession) {
		s.progress = authorization.Progress{State: authorization.SessionDone}
		s.identity = frouke
		s.returnNonce = "return-nonce"
	})
	wrongReturn := WalletReturnPath + "?" + url.Values{
		"transaction":   {tx.id},
		"session_token": {"another-session"},
		"nonce":         {"return-nonce"},
	}.Encode()
	if resp, _ := as.get(t, wrongReturn, tx.cookie); resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
		t.Fatalf("return with another session = %d to %q, want 400 without redirect", resp.StatusCode, resp.Header.Get("Location"))
	}
	correctReturn := WalletReturnPath + "?" + url.Values{
		"transaction":   {tx.id},
		"session_token": {session},
		"nonce":         {"return-nonce"},
	}.Encode()
	if resp, _ := as.get(t, correctReturn, tx.cookie); redirectParams(t, resp).Get("code") == "" {
		t.Fatal("the correct return no longer completes after a wrong session token")
	}
}

// Y4: a missing or incorrect verifier nonce returns to the request page with
// a message and leaves the transaction available for another login attempt.
func TestWalletSameDeviceReturnRequiresItsNonce(t *testing.T) {
	for name, nonce := range map[string]string{"missing": "", "incorrect": "other-nonce"} {
		t.Run(name, func(t *testing.T) {
			as := newTestASWithIssuer(t, testResource, nil, true, "https://as.test")
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			_, session := as.startWallet(t, tx)
			as.wallet.set(session, func(s *fakeWalletSession) {
				s.progress = authorization.Progress{State: authorization.SessionDone}
				s.identity = frouke
				s.returnNonce = "return-nonce"
			})
			values := url.Values{"transaction": {tx.id}, "session_token": {session}}
			if nonce != "" {
				values.Set("nonce", nonce)
			}
			resp, page := as.get(t, WalletReturnPath+"?"+values.Encode(), tx.cookie)
			if resp.StatusCode != http.StatusOK || !strings.Contains(page, "terugkeer uit de wallet was niet geldig") || !strings.Contains(page, `value="digid"`) {
				t.Fatalf("return with %s nonce = %d:\n%s", name, resp.StatusCode, page)
			}
			if q := redirectParams(t, as.digidLogin(t, tx.id, tx.cookie, bsnFrouke)); q.Get("code") == "" {
				t.Fatalf("DigiD after return with %s nonce = %v, want a code", name, q)
			}
		})
	}
}

// Y5: when the citizen declines in the wallet and is returned, the request
// page shows the existing decline message and the transaction remains.
func TestWalletSameDeviceDeclineReturnsToTheRequest(t *testing.T) {
	as := newTestASWithIssuer(t, testResource, nil, true, "https://as.test")
	tx := as.startAuthorization(t, authorizeParams(newPKCE()))
	_, session := as.startWallet(t, tx)
	as.wallet.set(session, func(s *fakeWalletSession) {
		s.progress = authorization.Progress{State: authorization.SessionFailed}
		s.returnNonce = "return-nonce"
	})
	returnPath := WalletReturnPath + "?" + url.Values{
		"transaction":   {tx.id},
		"session_token": {session},
		"nonce":         {"return-nonce"},
	}.Encode()
	resp, page := as.get(t, returnPath, tx.cookie)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, "geweigerd in de wallet, of de tijd is verstreken") || !strings.Contains(page, `value="digid"`) {
		t.Fatalf("return after declining in the wallet = %d:\n%s", resp.StatusCode, page)
	}
	if q := redirectParams(t, as.digidLogin(t, tx.id, tx.cookie, bsnFrouke)); q.Get("code") == "" {
		t.Fatalf("DigiD after declining in the wallet = %v, want a code", q)
	}
}

// Y6: manually returning to the waiting page while disclosure is still in
// progress keeps that page polling; it neither logs in nor starts over.
func TestWalletWaitingPageRemainsWhileDisclosureIsPending(t *testing.T) {
	as := newTestASWithIssuer(t, testResource, nil, true, "https://as.test")
	tx := as.startAuthorization(t, authorizeParams(newPKCE()))
	path, session := as.startWallet(t, tx)
	as.wallet.set(session, func(s *fakeWalletSession) {
		s.progress = authorization.Progress{State: authorization.SessionWaiting}
	})
	resp, page := as.get(t, path, tx.cookie)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Location") != "" || !strings.Contains(page, `http-equiv="refresh"`) || !strings.Contains(page, "Ga verder in de NL Wallet") {
		t.Fatalf("pending waiting page = %d to %q:\n%s", resp.StatusCode, resp.Header.Get("Location"), page)
	}
}

// Y7: the existing cross-device QR flow still logs in with the disclosed PID.
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
	// The session is done, but the disclosed attributes cannot be fetched.
	as.wallet.set(session, func(s *fakeWalletSession) {
		s.progressErr = nil
		s.progress = authorization.Progress{State: authorization.SessionDone}
		s.identifyErr = fmt.Errorf("%w: GET disclosed_attributes: 502", authorization.ErrMeansUnavailable)
	})
	if resp, page := as.get(t, path, tx.cookie); resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(page, "niet beschikbaar") {
		t.Fatalf("done, then the verifier unreachable = %d:\n%s", resp.StatusCode, page)
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
