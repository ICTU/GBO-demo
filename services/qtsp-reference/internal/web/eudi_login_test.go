package web

// Logging in at the QTSP with an EUDI wallet through eudi-wallet-verifier
// (#639, cases QT1-QT7), with fakes of both verifiers. QT8 is the
// provisioning test in eudi-adapter, QL1 the live run.

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"gbo-demo/qtsp-reference/internal/login"
)

var froukeEUDI = login.Identity{FamilyName: "Jansen", GivenName: "Frouke", Birthdate: "2000-03-24", Nationalities: []string{"Nederlandse"}, Means: login.MeansEUDIWallet}

const eudiLoginButton = `<button class="qp-btn block" type="submit">Inloggen met EU-wallet (test)</button>`

// newEUDILoginEnv runs the web side with the NL Wallet (when nl is not nil)
// and the EU-wallet.
func newEUDILoginEnv(t *testing.T, nl, eudi *fakeWallet) loginEnv {
	t.Helper()
	var port login.Wallet
	if nl != nil {
		port = nl
	}
	h := newHandler(nil, DemoAttributes, "http://localhost:4023", port)
	WithEUDIWallet(eudi)(h)
	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)
	return loginEnv{h: h, srv: srv, wallet: eudi}
}

// QT1: the EU-wallet button only when its verifier is configured; the NL
// Wallet's button as before.
func TestEUDILoginPage(t *testing.T) {
	_, page := newLoginEnv(t, &fakeWallet{}).do(t, browser(), http.MethodGet, "/", nil)
	if strings.Contains(page, "EU-wallet") {
		t.Errorf("EU-wallet offered without its verifier:\n%s", page)
	}
	_, page = newEUDILoginEnv(t, &fakeWallet{}, &fakeWallet{}).do(t, browser(), http.MethodGet, "/", nil)
	if !strings.Contains(page, `action="/login/eudi-wallet"`) || !strings.Contains(page, eudiLoginButton) ||
		!strings.Contains(page, `<button class="qp-btn block" type="submit">Inloggen met NL Wallet</button>`) {
		t.Errorf("login page with both wallets:\n%s", page)
	}
	_, page = newEUDILoginEnv(t, nil, &fakeWallet{}).do(t, browser(), http.MethodGet, "/", nil)
	if !strings.Contains(page, eudiLoginButton) || !strings.Contains(page, `disabled>Inloggen met NL Wallet</button> <small class="sec">niet beschikbaar</small>`) {
		t.Errorf("login page with the EU-wallet only:\n%s", page)
	}
	// Choosing it without its verifier is unavailable.
	if resp, _ := newLoginEnv(t, &fakeWallet{}).do(t, browser(), http.MethodPost, "/login/eudi-wallet", url.Values{}); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("EU-wallet without its verifier = %d, want 503", resp.StatusCode)
	}
}

// QT2: the EU-wallet shows its QR code under its own name, then logs the
// citizen in with the disclosed attributes and means eudi-wallet.
func TestEUDILogin(t *testing.T) {
	link := "openid4vp://?client_id=x509_hash%3Aabc&request_uri=https%3A%2F%2Fverifier.test%2Fr"
	nl := &fakeWallet{}
	e := newEUDILoginEnv(t, nl, &fakeWallet{progress: login.Progress{State: login.Waiting, Link: link}})
	c := browser()
	resp, _ := e.do(t, c, http.MethodPost, "/login/eudi-wallet", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login/eudi-wallet" {
		t.Fatalf("POST /login/eudi-wallet = %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if nl.started != 0 {
		t.Fatal("the EU-wallet opened a session at the NL Wallet's verifier")
	}
	_, page := e.do(t, c, http.MethodGet, "/login/eudi-wallet", nil)
	if !strings.Contains(page, `src="data:image/png;base64,`) || !strings.Contains(page, "<h1>Inloggen met EU-wallet</h1>") ||
		!strings.Contains(page, "Scan deze code met de EU-wallet") || strings.Contains(page, "NL Wallet") {
		t.Fatalf("EU-wallet page:\n%s", page)
	}
	e.wallet.set(func(f *fakeWallet) { f.progress, f.identity = login.Progress{State: login.Done}, froukeEUDI })
	if resp, _ := e.do(t, c, http.MethodGet, "/login/eudi-wallet", nil); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("after the disclosure = %d to %q, want the form", resp.StatusCode, resp.Header.Get("Location"))
	}
	if who := e.loggedInAs(t, c); who != "Frouke Jansen" {
		t.Fatalf("logged in as %q, want Frouke Jansen", who)
	}
	if id := e.h.loggedIn(withCookies(t, e, c)); id == nil || id.Means != login.MeansEUDIWallet {
		t.Errorf("identity %+v, want means eudi-wallet", id)
	}
}

// withCookies is a request carrying the browser's cookies for the server.
func withCookies(t *testing.T, e loginEnv, c *http.Client) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	u, _ := url.Parse(e.srv.URL)
	for _, ck := range c.Jar.Cookies(u) {
		req.AddCookie(ck)
	}
	return req
}

// QT3: declined or expired, and a disclosure that does not identify: the
// login page with the reason, naming the EU-wallet; not logged in.
func TestEUDINotLoggedIn(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*fakeWallet)
		want string
	}{
		"declined or expired": {func(f *fakeWallet) { f.progress = login.Progress{State: login.Failed} },
			"Niet ingelogd met de EU-wallet: geweigerd in de wallet, of de tijd is verstreken."},
		"no usable disclosure": {func(f *fakeWallet) {
			f.progress = login.Progress{State: login.Done}
			f.identifyErr = errors.New("the PID lacks a requested attribute")
		}, "Niet ingelogd met de EU-wallet: de wallet gaf geen bruikbaar persoonsidentificatiebewijs."},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEUDILoginEnv(t, nil, &fakeWallet{})
			e.wallet.set(tc.edit)
			c := browser()
			e.do(t, c, http.MethodPost, "/login/eudi-wallet", url.Values{})
			resp, page := e.do(t, c, http.MethodGet, "/login/eudi-wallet", nil)
			if resp.StatusCode != http.StatusOK || !strings.Contains(page, tc.want) || !strings.Contains(page, eudiLoginButton) {
				t.Fatalf("= %d:\n%s", resp.StatusCode, page)
			}
			if who := e.loggedInAs(t, c); who != "" {
				t.Fatalf("logged in as %q", who)
			}
		})
	}
}

// QT4: the EU-wallet's verifier unavailable at the start, while following,
// or after the session is done: a 503 naming the EU-wallet; the mock still
// works.
func TestEUDIUnavailable(t *testing.T) {
	const want = "De EU-wallet is nu niet beschikbaar. Log in met testgegevens (mock)."
	e := newEUDILoginEnv(t, nil, &fakeWallet{startErr: errors.New("connection refused")})
	c := browser()
	if resp, page := e.do(t, c, http.MethodPost, "/login/eudi-wallet", url.Values{}); resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(page, want) {
		t.Fatalf("start = %d:\n%s", resp.StatusCode, page)
	}
	e.wallet.set(func(f *fakeWallet) { f.startErr, f.progressErr = nil, errors.New("connection refused") })
	e.do(t, c, http.MethodPost, "/login/eudi-wallet", url.Values{})
	if resp, page := e.do(t, c, http.MethodGet, "/login/eudi-wallet", nil); resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(page, want) {
		t.Fatalf("following = %d:\n%s", resp.StatusCode, page)
	}
	e.wallet.set(func(f *fakeWallet) {
		f.progressErr, f.progress = nil, login.Progress{State: login.Done}
		f.identifyErr = login.ErrUnavailable
	})
	if resp, page := e.do(t, c, http.MethodGet, "/login/eudi-wallet", nil); resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(page, want) {
		t.Fatalf("done, then unavailable = %d:\n%s", resp.StatusCode, page)
	}
	e.do(t, c, http.MethodPost, "/login/mock", mockForm())
	if who := e.loggedInAs(t, c); who != "Frouke Jansen" {
		t.Errorf("mock after the EU-wallet was unavailable: logged in as %q", who)
	}
}

// QT5: an EU-wallet session belongs to the browser that started it.
func TestEUDISessionIsBoundToItsBrowser(t *testing.T) {
	e := newEUDILoginEnv(t, nil, &fakeWallet{progress: login.Progress{State: login.Waiting}})
	victim := browser()
	e.do(t, victim, http.MethodPost, "/login/eudi-wallet", url.Values{})
	e.wallet.set(func(f *fakeWallet) { f.progress, f.identity = login.Progress{State: login.Done}, froukeEUDI })
	other := browser()
	if resp, page := e.do(t, other, http.MethodGet, "/login/eudi-wallet", nil); resp.StatusCode != http.StatusOK || !strings.Contains(page, `action="/login/mock"`) {
		t.Fatalf("another browser following the session = %d:\n%s", resp.StatusCode, page)
	}
	if who := e.loggedInAs(t, other); who != "" {
		t.Fatalf("another browser logged in as %q", who)
	}
	e.do(t, victim, http.MethodGet, "/login/eudi-wallet", nil)
	if who := e.loggedInAs(t, victim); who != "Frouke Jansen" {
		t.Errorf("the browser that started the session: logged in as %q", who)
	}
}

// QT6: a session of one wallet does not log in through the other wallet's
// page, either way round.
func TestEUDISessionIsBoundToItsWallet(t *testing.T) {
	done := func(id login.Identity) *fakeWallet {
		return &fakeWallet{progress: login.Progress{State: login.Done}, identity: id}
	}
	e := newEUDILoginEnv(t, done(frouke), done(froukeEUDI))
	c := browser()
	e.do(t, c, http.MethodPost, "/login/eudi-wallet", url.Values{})
	if resp, _ := e.do(t, c, http.MethodGet, "/login/wallet", nil); resp.StatusCode == http.StatusSeeOther {
		t.Fatal("the EU-wallet's session logged in through the NL Wallet's page")
	}
	if who := e.loggedInAs(t, c); who != "" {
		t.Fatalf("logged in as %q through the other page", who)
	}
	c = browser()
	e.do(t, c, http.MethodPost, "/login/wallet", url.Values{})
	if resp, _ := e.do(t, c, http.MethodGet, "/login/eudi-wallet", nil); resp.StatusCode == http.StatusSeeOther {
		t.Fatal("the NL Wallet's session logged in through the EU-wallet's page")
	}
	if who := e.loggedInAs(t, c); who != "" {
		t.Fatalf("logged in as %q through the other page", who)
	}
}

// QT7: no name, date of birth or nationality in the logs, also not when an
// EU-wallet login fails.
func TestEUDINoPersonalDataInTheLogs(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	e := newEUDILoginEnv(t, nil, &fakeWallet{progress: login.Progress{State: login.Done}, identity: froukeEUDI})
	c := browser()
	e.do(t, c, http.MethodPost, "/login/eudi-wallet", url.Values{})
	e.do(t, c, http.MethodGet, "/login/eudi-wallet", nil)
	e.wallet.set(func(f *fakeWallet) { f.identifyErr = errors.New("the PID lacks a requested attribute") })
	e.do(t, c, http.MethodPost, "/login/eudi-wallet", url.Values{})
	e.do(t, c, http.MethodGet, "/login/eudi-wallet", nil)
	for _, personal := range []string{"Jansen", "Frouke", "2000-03-24", "Nederlandse"} {
		if strings.Contains(logs.String(), personal) {
			t.Errorf("the logs contain %s:\n%s", personal, logs.String())
		}
	}
}
