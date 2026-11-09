package httpapi

// Logging in with an EUDI wallet through eudi-wallet-verifier (#639, cases
// T10-T16), with a fake of the verifier behind the core's SessionIdentifier
// port. T1-T9 are the verifier's own tests; L1-L3 are live runs.

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

// startEUDI chooses the EU-wallet on the request page and returns the
// wallet page's path and the session the fake verifier opened.
func (as *testAS) startEUDI(t *testing.T, tx pendingTx) (path, session string) {
	t.Helper()
	resp := as.decide(t, tx.id, tx.cookie, url.Values{"decision": {"eudi-wallet"}})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, EUDIWalletPath+"?") {
		t.Fatalf("choosing the EU-wallet = %d to %q, want 303 to %s", resp.StatusCode, loc, EUDIWalletPath)
	}
	as.eudi.mu.Lock()
	defer as.eudi.mu.Unlock()
	return loc, fmt.Sprintf("session-%d", as.eudi.next)
}

const eudiButton = `value="eudi-wallet">Inloggen met EU-wallet (test)</button>`

// T10: the EU-wallet button is on the request page only when its verifier
// is configured; the NL Wallet's button does not depend on it.
func TestEUDIWalletChoice(t *testing.T) {
	for name, tc := range map[string]struct {
		as       *testAS
		eudi, nl bool
	}{
		"NL Wallet only": {as: newTestAS(t), nl: true},
		"both":           {as: newTestASWithEUDI(t, true, ""), eudi: true, nl: true},
		"EU-wallet only": {as: newTestASWithEUDI(t, false, ""), eudi: true},
	} {
		t.Run(name, func(t *testing.T) {
			tx := tc.as.startAuthorization(t, authorizeParams(newPKCE()))
			if got := strings.Contains(tx.page, eudiButton); got != tc.eudi {
				t.Errorf("EU-wallet button = %v, want %v:\n%s", got, tc.eudi, tx.page)
			}
			if strings.Contains(tx.page, `value="eudi-wallet" disabled`) || strings.Contains(tx.page, "De EU-wallet is nu niet beschikbaar") {
				t.Errorf("the EU-wallet shows as unavailable instead of not at all:\n%s", tx.page)
			}
			nlEnabled := strings.Contains(tx.page, `value="wallet">Inloggen met NL Wallet</button>`)
			nlDisabled := strings.Contains(tx.page, `value="wallet" disabled>Inloggen met NL Wallet</button>`) && strings.Contains(tx.page, "De NL Wallet is nu niet beschikbaar")
			if nlEnabled != tc.nl || nlDisabled == tc.nl {
				t.Errorf("NL Wallet button enabled %v, disabled %v; want enabled %v:\n%s", nlEnabled, nlDisabled, tc.nl, tx.page)
			}
		})
	}
	// Choosing it while it is not configured cannot be redirected.
	as := newTestAS(t)
	tx := as.startAuthorization(t, authorizeParams(newPKCE()))
	if resp := as.decide(t, tx.id, tx.cookie, url.Values{"decision": {"eudi-wallet"}}); resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
		t.Fatalf("choosing an unconfigured EU-wallet = %d to %q, want 400", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// T11: cross-device login with the EU-wallet. The page shows the QR code
// and the openid4vp:// link, named for the EU-wallet; the token carries the
// PID identity and acr/amr eudi-wallet.
func TestEUDIWalletLogin(t *testing.T) {
	as := newTestASWithEUDI(t, true, "https://as.test")
	p := newPKCE()
	tx := as.startAuthorization(t, authorizeParams(p))
	path, session := as.startEUDI(t, tx)
	as.eudi.mu.Lock()
	returnURLTemplate := as.eudi.returnURLTemplate
	as.eudi.mu.Unlock()
	if want := "https://as.test" + EUDIWalletReturnPath + "?transaction=" + url.QueryEscape(tx.id) + "&session_token={session_token}"; returnURLTemplate != want {
		t.Errorf("return URL template = %q, want %q", returnURLTemplate, want)
	}
	if as.wallet.next != 0 {
		t.Error("choosing the EU-wallet opened a session at the NL Wallet's verifier")
	}
	resp, page := as.get(t, path, tx.cookie)
	_, same := as.eudi.links(session)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, `src="data:image/png;base64,`) || !strings.Contains(page, "<h1>Inloggen met EU-wallet</h1>") ||
		!strings.Contains(page, "Scan deze code met de EU-wallet") || strings.Contains(page, "NL Wallet") {
		t.Fatalf("EU-wallet page = %d:\n%s", resp.StatusCode, page)
	}
	if !strings.Contains(page, `href="`+html.EscapeString(same)+`"`) || !strings.Contains(page, ">Open EU-wallet</a>") {
		t.Errorf("EU-wallet page has no openid4vp:// same-device link %q:\n%s", same, page)
	}

	as.eudi.set(session, func(s *fakeWalletSession) {
		s.progress = authorization.Progress{State: authorization.SessionDone}
		s.identity = authorization.Identity{BSN: bsnFrouke, FamilyName: "Jansen", GivenName: "Frouke", Birthdate: "2000-03-24", Nationalities: []string{"Nederlandse"}}
	})
	resp, _ = as.get(t, path, tx.cookie)
	q := redirectParams(t, resp)
	if q.Get("code") == "" || q.Get("state") != "state-123" {
		t.Fatalf("redirect after the EU-wallet = %v, want a code and the state", q)
	}
	r := as.postToken(t, as.tokenForm(t, q.Get("code"), p), as.proof(t, nil))
	token, _ := r.body["access_token"].(string)
	_, claims := verifyAccessToken(t, as, token)
	for field, want := range map[string]any{authorization.BSNClaim: bsnFrouke, "family_name": "Jansen", "given_name": "Frouke", "birthdate": "2000-03-24", "acr": "eudi-wallet"} {
		if claims[field] != want {
			t.Errorf("%s = %v, want %v", field, claims[field], want)
		}
	}
	if amr, _ := claims["amr"].([]any); len(amr) != 1 || amr[0] != "eudi-wallet" {
		t.Errorf("amr = %v, want [eudi-wallet]", claims["amr"])
	}
	// The test PID has no place of birth; the token carries none, as with
	// the NL Wallet (an empty object, see REQ-86's note in the analysis).
	if pob, _ := claims["place_of_birth"].(map[string]any); len(pob) != 0 {
		t.Errorf("place_of_birth = %v, want none", claims["place_of_birth"])
	}
}

// eudiReturn is the return URL of the EU-wallet for transaction and
// session, with nonce when set.
func eudiReturn(transaction, session, nonce string) string {
	v := url.Values{"transaction": {transaction}, "session_token": {session}}
	if nonce != "" {
		v.Set("nonce", nonce)
	}
	return EUDIWalletReturnPath + "?" + v.Encode()
}

// T12: a same-device return completes the login only for the session of
// this transaction, in this browser, with its nonce; a failed return leaves
// the transaction for another attempt.
func TestEUDIWalletSameDeviceReturn(t *testing.T) {
	setup := func(t *testing.T) (*testAS, pendingTx, string, pkce) {
		as := newTestASWithEUDI(t, true, "https://as.test")
		p := newPKCE()
		tx := as.startAuthorization(t, authorizeParams(p))
		_, session := as.startEUDI(t, tx)
		as.eudi.set(session, func(s *fakeWalletSession) {
			s.progress = authorization.Progress{State: authorization.SessionDone}
			s.identity = frouke
			s.returnNonce = "return-nonce"
		})
		return as, tx, session, p
	}
	t.Run("another browser", func(t *testing.T) {
		as, tx, session, _ := setup(t)
		other := as.startAuthorization(t, authorizeParams(newPKCE()))
		if resp, _ := as.get(t, eudiReturn(tx.id, session, "return-nonce"), other.cookie); resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
			t.Fatalf("return in another browser = %d to %q, want 400", resp.StatusCode, resp.Header.Get("Location"))
		}
		if q := redirectParams(t, mustGet(t, as, eudiReturn(tx.id, session, "return-nonce"), tx)); q.Get("code") == "" {
			t.Fatal("the owner's return no longer completes")
		}
	})
	t.Run("another session", func(t *testing.T) {
		as, tx, session, _ := setup(t)
		if resp, _ := as.get(t, eudiReturn(tx.id, "another-session", "return-nonce"), tx.cookie); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("return with another session = %d, want 400", resp.StatusCode)
		}
		if q := redirectParams(t, mustGet(t, as, eudiReturn(tx.id, session, "return-nonce"), tx)); q.Get("code") == "" {
			t.Fatal("the correct return no longer completes")
		}
	})
	for name, nonce := range map[string]string{"no nonce": "", "wrong nonce": "other"} {
		t.Run(name, func(t *testing.T) {
			as, tx, session, _ := setup(t)
			resp, page := as.get(t, eudiReturn(tx.id, session, nonce), tx.cookie)
			if resp.StatusCode != http.StatusOK || !strings.Contains(page, "Niet ingelogd met de EU-wallet: de terugkeer uit de wallet was niet geldig.") || !strings.Contains(page, eudiButton) {
				t.Fatalf("return with %s = %d:\n%s", name, resp.StatusCode, page)
			}
			if q := redirectParams(t, as.digidLogin(t, tx.id, tx.cookie, bsnFrouke)); q.Get("code") == "" {
				t.Fatalf("DigiD after the failed return = %v, want a code", q)
			}
		})
	}
	t.Run("the return of the NL Wallet's path", func(t *testing.T) {
		as, tx, session, _ := setup(t)
		nlReturn := WalletReturnPath + "?" + url.Values{"transaction": {tx.id}, "session_token": {session}, "nonce": {"return-nonce"}}.Encode()
		if resp, _ := as.get(t, nlReturn, tx.cookie); resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
			t.Fatalf("EU-wallet session returned on the NL Wallet's path = %d to %q, want 400", resp.StatusCode, resp.Header.Get("Location"))
		}
	})
	t.Run("logs in", func(t *testing.T) {
		as, tx, session, p := setup(t)
		q := redirectParams(t, mustGet(t, as, eudiReturn(tx.id, session, "return-nonce"), tx))
		r := as.postToken(t, as.tokenForm(t, q.Get("code"), p), as.proof(t, nil))
		token, _ := r.body["access_token"].(string)
		if _, claims := verifyAccessToken(t, as, token); claims["acr"] != "eudi-wallet" || claims[authorization.BSNClaim] != bsnFrouke {
			t.Errorf("token after the return: acr %v, BSN %v", claims["acr"], claims[authorization.BSNClaim])
		}
	})
}

func mustGet(t *testing.T, as *testAS, path string, tx pendingTx) *http.Response {
	t.Helper()
	resp, _ := as.get(t, path, tx.cookie)
	return resp
}

// T13: when the same-device session is done, the waiting page in the
// browser that started it does not log in without the return's nonce; it
// keeps waiting for the return, which logs in (HAIP 1.0 section 5.1).
func TestEUDIWalletSameDeviceWaitsForTheReturn(t *testing.T) {
	as := newTestASWithEUDI(t, true, "https://as.test")
	tx := as.startAuthorization(t, authorizeParams(newPKCE()))
	path, session := as.startEUDI(t, tx)
	as.eudi.set(session, func(s *fakeWalletSession) {
		s.progress = authorization.Progress{State: authorization.SessionDone}
		s.identity = frouke
		s.returnNonce = "return-nonce"
	})
	resp, page := as.get(t, path, tx.cookie)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Location") != "" || !strings.Contains(page, `http-equiv="refresh"`) ||
		!strings.Contains(page, "Ga verder in de EU-wallet") || strings.Contains(page, "niet beschikbaar") {
		t.Fatalf("waiting page of a done same-device session = %d to %q:\n%s", resp.StatusCode, resp.Header.Get("Location"), page)
	}
	if q := redirectParams(t, mustGet(t, as, eudiReturn(tx.id, session, "return-nonce"), tx)); q.Get("code") == "" {
		t.Fatal("the return does not log in after the waiting page was shown")
	}
}

// T14: a session of one wallet cannot log in as the other: the core takes
// only the session it kept for the transaction's means.
func TestEUDIWalletSessionIsBoundToItsMeans(t *testing.T) {
	as := newTestASWithEUDI(t, true, "")
	tx := as.startAuthorization(t, authorizeParams(newPKCE()))
	_, session := as.startEUDI(t, tx)
	as.eudi.set(session, func(s *fakeWalletSession) {
		s.progress = authorization.Progress{State: authorization.SessionDone}
		s.identity = frouke
	})
	if resp, _ := as.get(t, WalletPath+"?transaction="+tx.id, tx.cookie); resp.Header.Get("Location") != "" {
		t.Fatalf("the EU-wallet session through the NL Wallet's page redirected to %q", resp.Header.Get("Location"))
	}
	if _, err := as.service.LogIn(t.Context(), authorization.Login{TransactionID: tx.id, Binding: tx.cookie.Value, Means: "nl-wallet"}); !errors.Is(err, authorization.ErrNotIdentified) {
		t.Fatalf("LogIn as nl-wallet with the EU-wallet's session = %v, want not identified", err)
	}
	// And the other way round.
	as2 := newTestASWithEUDI(t, true, "")
	tx2 := as2.startAuthorization(t, authorizeParams(newPKCE()))
	_, nlSession := as2.startWallet(t, tx2)
	as2.wallet.set(nlSession, func(s *fakeWalletSession) {
		s.progress = authorization.Progress{State: authorization.SessionDone}
		s.identity = frouke
	})
	if _, err := as2.service.LogIn(t.Context(), authorization.Login{TransactionID: tx2.id, Binding: tx2.cookie.Value, Means: "eudi-wallet"}); !errors.Is(err, authorization.ErrNotIdentified) {
		t.Fatalf("LogIn as eudi-wallet with the NL Wallet's session = %v, want not identified", err)
	}
}

// T15: the EU-wallet's verifier is unreachable, when starting or while
// following: a 503 page that names the EU-wallet, and the transaction stays.
func TestEUDIWalletUnavailable(t *testing.T) {
	as := newTestASWithEUDI(t, true, "")
	as.eudi.startErr = errors.New("connection refused")
	tx := as.startAuthorization(t, authorizeParams(newPKCE()))
	if resp := as.decide(t, tx.id, tx.cookie, url.Values{"decision": {"eudi-wallet"}}); resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Location") != "" {
		t.Fatalf("start with the verifier unreachable = %d to %q, want 503", resp.StatusCode, resp.Header.Get("Location"))
	}
	as.eudi.startErr = nil
	path, session := as.startEUDI(t, tx)
	as.eudi.set(session, func(s *fakeWalletSession) { s.progressErr = errors.New("connection refused") })
	if resp, page := as.get(t, path, tx.cookie); resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(page, "De EU-wallet is nu niet beschikbaar") {
		t.Fatalf("following with the verifier unreachable = %d:\n%s", resp.StatusCode, page)
	}
	if q := redirectParams(t, as.digidLogin(t, tx.id, tx.cookie, bsnFrouke)); q.Get("code") == "" {
		t.Fatalf("DigiD after the EU-wallet was unreachable = %v, want a code", q)
	}
}

// T16: declined in the wallet or expired, or a disclosure that does not
// identify the user: the request page again, naming the EU-wallet, and the
// transaction stays.
func TestEUDIWalletNotLoggedIn(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*fakeWalletSession)
		want string
	}{
		"declined or expired": {func(s *fakeWalletSession) { s.progress = authorization.Progress{State: authorization.SessionFailed} },
			"Niet ingelogd met de EU-wallet: geweigerd in de wallet, of de tijd is verstreken."},
		"no usable disclosure": {func(s *fakeWalletSession) {
			s.progress = authorization.Progress{State: authorization.SessionDone}
			s.identifyErr = errors.New("the PID has no BSN")
		}, "Niet ingelogd met de EU-wallet: de wallet gaf geen bruikbaar persoonsidentificatiebewijs."},
	} {
		t.Run(name, func(t *testing.T) {
			as := newTestASWithEUDI(t, true, "")
			tx := as.startAuthorization(t, authorizeParams(newPKCE()))
			path, session := as.startEUDI(t, tx)
			as.eudi.set(session, tc.edit)
			resp, page := as.get(t, path, tx.cookie)
			if resp.StatusCode != http.StatusOK || !strings.Contains(page, tc.want) || !strings.Contains(page, eudiButton) {
				t.Fatalf("= %d:\n%s", resp.StatusCode, page)
			}
			if q := redirectParams(t, as.digidLogin(t, tx.id, tx.cookie, bsnFrouke)); q.Get("code") == "" {
				t.Fatalf("DigiD after the EU-wallet = %v, want a code", q)
			}
		})
	}
}
