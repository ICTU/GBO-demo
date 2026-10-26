package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"gbo-demo/qtsp-reference/internal/login"
)

// fakeWallet stands in for the NL Wallet verifier.
type fakeWallet struct {
	mu          sync.Mutex
	started     int
	startErr    error
	progress    login.Progress
	progressErr error
	identity    login.Identity
	identifyErr error
}

func (f *fakeWallet) Start(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return "", f.startErr
	}
	f.started++
	return fmt.Sprintf("session-%d", f.started), nil
}

func (f *fakeWallet) Progress(context.Context, string) (login.Progress, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.progress, f.progressErr
}

func (f *fakeWallet) Identify(context.Context, string) (login.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.identity, f.identifyErr
}

func (f *fakeWallet) set(edit func(*fakeWallet)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	edit(f)
}

var frouke = login.Identity{FamilyName: "Jansen", GivenName: "Frouke", Birthdate: "2000-03-24", Nationalities: []string{"NL", "BE"}, Means: login.MeansWallet}

type loginEnv struct {
	h      *Handler
	srv    *httptest.Server
	wallet *fakeWallet
}

func newLoginEnv(t *testing.T, wallet *fakeWallet) loginEnv {
	t.Helper()
	var port login.Wallet
	if wallet != nil {
		port = wallet
	}
	h := newHandler(nil, DemoAttributes, "http://localhost:4023", port)
	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)
	return loginEnv{h: h, srv: srv, wallet: wallet}
}

func browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (e loginEnv) do(t *testing.T, c *http.Client, method, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

// loggedInAs reports who the form names, or "" on the login page.
func (e loginEnv) loggedInAs(t *testing.T, c *http.Client) string {
	t.Helper()
	_, page := e.do(t, c, http.MethodGet, "/", nil)
	if !strings.Contains(page, `action="/start"`) {
		return ""
	}
	start := strings.Index(page, "Ingelogd als <strong>")
	if start < 0 {
		return "?"
	}
	rest := page[start+len("Ingelogd als <strong>"):]
	return rest[:strings.Index(rest, "</strong>")]
}

// T1: without a login the login page, with the wallet and the mock; a QTSP
// without a configured verifier offers the wallet disabled.
func TestLoginPage(t *testing.T) {
	e := newLoginEnv(t, &fakeWallet{})
	resp, page := e.do(t, browser(), http.MethodGet, "/", nil)
	for _, want := range []string{`<button class="qp-btn block" type="submit">Inloggen met NL Wallet</button>`, `action="/login/mock"`, "niet uw BSN", "hier wordt niets gecontroleerd",
		// Without test data from a request, the mock is prefilled with Frouke Jansen.
		`value="Jansen"`, `value="Frouke"`, `value="2000-03-24"`, `value="NL, BE"`} {
		if resp.StatusCode != http.StatusOK || !strings.Contains(page, want) {
			t.Errorf("login page lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, `action="/start"`) {
		t.Error("the request form without a login")
	}
	_, page = newLoginEnv(t, nil).do(t, browser(), http.MethodGet, "/", nil)
	if !strings.Contains(page, `<button class="qp-btn block" type="submit" disabled>Inloggen met NL Wallet</button> <small class="sec">niet beschikbaar</small>`) {
		t.Errorf("without a verifier the wallet is not disabled:\n%s", page)
	}
}

// T2: the wallet shows the QR code, then logs the citizen in with the
// disclosed attributes; the form names them.
func TestWalletLogin(t *testing.T) {
	e := newLoginEnv(t, &fakeWallet{progress: login.Progress{State: login.Waiting, Link: "https://app.example/deeplink/x"}})
	c := browser()
	resp, _ := e.do(t, c, http.MethodPost, "/login/wallet", url.Values{})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login/wallet" {
		t.Fatalf("POST /login/wallet = %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, page := e.do(t, c, http.MethodGet, "/login/wallet", nil); !strings.Contains(page, `src="data:image/png;base64,`) || !strings.Contains(page, `http-equiv="refresh"`) {
		t.Fatalf("wallet page without a QR code:\n%s", page)
	}
	if who := e.loggedInAs(t, c); who != "" {
		t.Fatalf("logged in as %q while the wallet is waiting", who)
	}
	e.wallet.set(func(f *fakeWallet) { f.progress, f.identity = login.Progress{State: login.Done}, frouke })
	if resp, _ := e.do(t, c, http.MethodGet, "/login/wallet", nil); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("after the disclosure = %d to %q, want the form", resp.StatusCode, resp.Header.Get("Location"))
	}
	if who := e.loggedInAs(t, c); who != "Frouke Jansen" {
		t.Fatalf("logged in as %q, want Frouke Jansen", who)
	}
	if _, page := e.do(t, c, http.MethodGet, "/", nil); strings.Contains(page, "(testgegevens, mock)") || !strings.Contains(page, `action="/logout"`) {
		t.Errorf("the form marks a wallet login as mock, or has no Uitloggen:\n%s", page)
	}
}

// T3 and T5: declined or expired, and a disclosure that does not identify,
// give the login page with the reason; the citizen is not logged in.
func TestWalletNotLoggedIn(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*fakeWallet)
		want string
	}{
		"T3 declined or expired": {func(f *fakeWallet) { f.progress = login.Progress{State: login.Failed} }, msgWalletFailed},
		"T5 unusable": {func(f *fakeWallet) {
			f.progress, f.identifyErr = login.Progress{State: login.Done}, errors.New("the PID lacks a requested attribute")
		}, msgWalletNotUsable},
	} {
		t.Run(name, func(t *testing.T) {
			e := newLoginEnv(t, &fakeWallet{})
			c := browser()
			e.do(t, c, http.MethodPost, "/login/wallet", url.Values{})
			e.wallet.set(tc.edit)
			resp, page := e.do(t, c, http.MethodGet, "/login/wallet", nil)
			if resp.StatusCode != http.StatusOK || !strings.Contains(page, tc.want) || !strings.Contains(page, `action="/login/mock"`) {
				t.Fatalf("%d:\n%s", resp.StatusCode, page)
			}
			if who := e.loggedInAs(t, c); who != "" {
				t.Errorf("logged in as %q", who)
			}
		})
	}
}

// T4: the verifier unavailable at the start, while following, or after the
// session is done: a 503 with the message; the mock login still works.
func TestWalletUnavailable(t *testing.T) {
	unavailable := fmt.Errorf("%w: connection refused", login.ErrUnavailable)
	for name, edit := range map[string]func(*fakeWallet){
		"start":  func(f *fakeWallet) { f.startErr = unavailable },
		"follow": func(f *fakeWallet) { f.progressErr = unavailable },
		"done":   func(f *fakeWallet) { f.progress, f.identifyErr = login.Progress{State: login.Done}, unavailable },
	} {
		t.Run(name, func(t *testing.T) {
			e := newLoginEnv(t, &fakeWallet{progress: login.Progress{State: login.Waiting}})
			c := browser()
			if name == "start" {
				e.wallet.set(edit)
			}
			resp, page := e.do(t, c, http.MethodPost, "/login/wallet", url.Values{})
			if name != "start" {
				e.wallet.set(edit)
				resp, page = e.do(t, c, http.MethodGet, "/login/wallet", nil)
			}
			if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(page, msgWalletUnavailable) {
				t.Fatalf("%d:\n%s", resp.StatusCode, page)
			}
			e.do(t, c, http.MethodPost, "/login/mock", mockForm())
			if who := e.loggedInAs(t, c); who != "Frouke Jansen" {
				t.Errorf("mock login after the wallet was unavailable: logged in as %q", who)
			}
		})
	}
}

// T6: a wallet session belongs to the browser that started it; another
// browser cannot follow it or log in with it.
func TestWalletSessionIsBoundToItsBrowser(t *testing.T) {
	e := newLoginEnv(t, &fakeWallet{progress: login.Progress{State: login.Waiting}})
	victim := browser()
	e.do(t, victim, http.MethodPost, "/login/wallet", url.Values{})
	e.wallet.set(func(f *fakeWallet) { f.progress, f.identity = login.Progress{State: login.Done}, frouke })
	other := browser()
	resp, page := e.do(t, other, http.MethodGet, "/login/wallet", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(page, `action="/login/mock"`) {
		t.Fatalf("another browser following the session = %d:\n%s", resp.StatusCode, page)
	}
	if who := e.loggedInAs(t, other); who != "" {
		t.Fatalf("another browser logged in as %q", who)
	}
	e.do(t, victim, http.MethodGet, "/login/wallet", nil)
	if who := e.loggedInAs(t, victim); who != "Frouke Jansen" {
		t.Errorf("the browser that started the session: logged in as %q", who)
	}
}

func mockForm() url.Values {
	return url.Values{"family_name": {"Jansen"}, "given_name": {"Frouke"}, "birthdate": {"2000-03-24"}, "nationalities": {"NL, BE"}}
}

// N2: invalid test data gives the login page again with the message and the
// typed values; valid test data logs in, marked as mock.
func TestMockLogin(t *testing.T) {
	e := newLoginEnv(t, nil)
	c := browser()
	bad := mockForm()
	bad.Set("birthdate", "24-03-2000")
	resp, page := e.do(t, c, http.MethodPost, "/login/mock", bad)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(page, msgMockInvalid) || !strings.Contains(page, `value="24-03-2000"`) {
		t.Fatalf("invalid test data = %d:\n%s", resp.StatusCode, page)
	}
	if who := e.loggedInAs(t, c); who != "" {
		t.Fatalf("logged in as %q with invalid test data", who)
	}
	e.do(t, c, http.MethodPost, "/login/mock", mockForm())
	if _, page := e.do(t, c, http.MethodGet, "/", nil); !strings.Contains(page, "Ingelogd als <strong>Frouke Jansen</strong> (testgegevens, mock)") {
		t.Errorf("form after the mock login:\n%s", page)
	}
}

// N1: a login lasts 30 minutes and ends with Uitloggen.
func TestLoginLifetimeAndLogout(t *testing.T) {
	e := newLoginEnv(t, nil)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	e.h.now = func() time.Time { return now }
	c := browser()
	e.do(t, c, http.MethodPost, "/login/mock", mockForm())
	now = now.Add(loginLifetime - time.Second)
	if who := e.loggedInAs(t, c); who != "Frouke Jansen" {
		t.Fatalf("within the lifetime: logged in as %q", who)
	}
	now = now.Add(2 * time.Second)
	if who := e.loggedInAs(t, c); who != "" {
		t.Fatalf("after the lifetime: logged in as %q", who)
	}
	e.do(t, c, http.MethodPost, "/login/mock", mockForm())
	if resp, _ := e.do(t, c, http.MethodPost, "/logout", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	if who := e.loggedInAs(t, c); who != "" {
		t.Errorf("after Uitloggen: logged in as %q", who)
	}
}

// T8: the QTSP logs no name, date of birth or nationality, also not when a
// wallet login fails.
func TestNoPersonalDataInTheLogs(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	e := newLoginEnv(t, &fakeWallet{progress: login.Progress{State: login.Done}, identity: frouke})
	c := browser()
	e.do(t, c, http.MethodPost, "/login/wallet", url.Values{})
	e.do(t, c, http.MethodGet, "/login/wallet", nil)
	e.do(t, c, http.MethodPost, "/login/mock", mockForm())
	e.wallet.set(func(f *fakeWallet) { f.identifyErr = errors.New("the PID lacks a requested attribute") })
	e.do(t, c, http.MethodPost, "/login/wallet", url.Values{})
	e.do(t, c, http.MethodGet, "/login/wallet", nil)
	for _, personal := range []string{"Jansen", "Frouke", "2000-03-24", `"BE"`} {
		if strings.Contains(logs.String(), personal) {
			t.Errorf("the logs contain %s:\n%s", personal, logs.String())
		}
	}
}
