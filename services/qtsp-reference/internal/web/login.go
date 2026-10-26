package web

import (
	"encoding/base64"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"gbo-demo/qtsp-reference/internal/login"
	"rsc.io/qr"
)

// The citizen logs in at the QTSP before a request starts (slice 14 of
// #336): with the NL Wallet, or with test data (a mock that checks nothing).
// A login lasts loginLifetime and serves several requests; the browser keeps
// only an opaque id in a cookie. A wallet session is kept with the login of
// this browser and never taken from the browser.

const (
	loginCookie   = "qtsp_login"
	loginLifetime = 30 * time.Minute
)

// The messages on the login page.
const (
	msgWalletFailed      = "Niet ingelogd met de NL Wallet: geweigerd in de wallet, of de tijd is verstreken."
	msgWalletNotUsable   = "Niet ingelogd met de NL Wallet: de wallet gaf geen bruikbaar persoonsidentificatiebewijs."
	msgWalletUnavailable = "De NL Wallet is nu niet beschikbaar. Log in met testgegevens (mock)."
	msgMockInvalid       = "Vul alle velden in: achternaam, voornamen, geboortedatum (JJJJ-MM-DD) en nationaliteit (landcodes, bijvoorbeeld NL, BE)."
)

// loginState is the login of a browser: pending while a wallet session runs,
// then the identity.
type loginState struct {
	identity      *login.Identity
	walletSession string
	// request is the request of the request API to start after the login.
	request string
	expires time.Time
}

// loginHint is test data for the mock login, laid down with a request of the
// request API: the login page shows it prefilled. It stays on the server.
type loginHint struct {
	FamilyName    string   `json:"family_name"`
	GivenName     string   `json:"given_name"`
	Birthdate     string   `json:"birthdate"`
	Nationalities []string `json:"nationalities"`
}

func (m loginHint) empty() bool {
	return m.FamilyName == "" && m.GivenName == "" && m.Birthdate == "" && len(m.Nationalities) == 0
}

// loggedIn returns the identity of this browser's login, if it has one.
func (h *Handler) loggedIn(r *http.Request) *login.Identity {
	_, s, ok := h.loginOf(r)
	if !ok {
		return nil
	}
	return s.identity
}

// loginOf returns this browser's login, pending or not, if it is valid: its
// id and a copy.
func (h *Handler) loginOf(r *http.Request) (string, loginState, bool) {
	c, err := r.Cookie(loginCookie)
	if err != nil {
		return "", loginState{}, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, other := range h.logins {
		if h.now().After(other.expires) {
			delete(h.logins, id)
		}
	}
	s := h.logins[c.Value]
	if s == nil {
		return "", loginState{}, false
	}
	return c.Value, *s, true
}

// newLogin replaces this browser's login with s.
func (h *Handler) newLogin(w http.ResponseWriter, r *http.Request, s *loginState) {
	h.dropLogin(r)
	s.expires = h.now().Add(loginLifetime)
	id := randomID()
	h.mu.Lock()
	h.logins[id] = s
	h.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: id, Path: "/", HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode, MaxAge: int(loginLifetime.Seconds())})
}

func (h *Handler) dropLogin(r *http.Request) {
	if c, err := r.Cookie(loginCookie); err == nil {
		h.mu.Lock()
		delete(h.logins, c.Value)
		h.mu.Unlock()
	}
}

// defaultMock prefills the mock login when a request brings no test data:
// test person Frouke Jansen, whom most ASI scenarios of the demo use.
var defaultMock = loginHint{FamilyName: "Jansen", GivenName: "Frouke", Birthdate: "2000-03-24", Nationalities: []string{"NL", "BE"}}

// loginPage shows the two means; request is a request of the request API to
// start after the login, whose test data prefills the mock, and otherwise
// defaultMock does.
func (h *Handler) loginPage(w http.ResponseWriter, status int, request, message string, mock loginHint) {
	if mock.empty() && request != "" {
		h.mu.Lock()
		if req := h.requests[request]; req != nil && req.hint != nil {
			mock = *req.hint
		}
		h.mu.Unlock()
	}
	if mock.empty() {
		mock = defaultMock
	}
	render(w, status, loginTemplate, map[string]any{
		"Request": request, "Message": message, "Wallet": h.wallet != nil,
		"FamilyName": mock.FamilyName, "GivenName": mock.GivenName, "Birthdate": mock.Birthdate,
		"Nationalities": strings.Join(mock.Nationalities, ", "),
	})
}

// next continues after a login: the request of the request API, or the form.
func (h *Handler) next(w http.ResponseWriter, r *http.Request, request string) {
	if request != "" {
		http.Redirect(w, r, "/requests/"+request+"/start", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) mockLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		errorPage(w, http.StatusBadRequest, "Het formulier kon niet worden gelezen.")
		return
	}
	f := r.PostForm
	id, err := login.Mock(f.Get("family_name"), f.Get("given_name"), f.Get("birthdate"), f.Get("nationalities"))
	if err != nil {
		typed := loginHint{FamilyName: f.Get("family_name"), GivenName: f.Get("given_name"), Birthdate: f.Get("birthdate")}
		if n := strings.TrimSpace(f.Get("nationalities")); n != "" {
			typed.Nationalities = []string{n}
		}
		h.loginPage(w, http.StatusBadRequest, f.Get("request"), msgMockInvalid, typed)
		return
	}
	h.newLogin(w, r, &loginState{identity: &id})
	h.next(w, r, f.Get("request"))
}

// walletStart opens a disclosure session and keeps it with a pending login
// of this browser.
func (h *Handler) walletStart(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		errorPage(w, http.StatusBadRequest, "Het formulier kon niet worden gelezen.")
		return
	}
	if h.wallet == nil {
		errorPage(w, http.StatusServiceUnavailable, msgWalletUnavailable)
		return
	}
	session, err := h.wallet.Start(r.Context())
	if err != nil {
		slog.Warn("NL Wallet verifier unavailable", "err", err)
		errorPage(w, http.StatusServiceUnavailable, msgWalletUnavailable)
		return
	}
	h.newLogin(w, r, &loginState{walletSession: session, request: r.PostForm.Get("request")})
	http.Redirect(w, r, "/login/wallet", http.StatusSeeOther)
}

// walletPage follows the pending login's session: the QR code while the
// citizen has not finished, the login once the wallet disclosed, the login
// page again when declined, expired or unusable.
func (h *Handler) walletPage(w http.ResponseWriter, r *http.Request) {
	loginID, s, ok := h.loginOf(r)
	if !ok || h.wallet == nil || (s.walletSession == "" && s.identity == nil) {
		h.loginPage(w, http.StatusOK, "", "", loginHint{})
		return
	}
	if s.identity != nil {
		h.next(w, r, s.request)
		return
	}
	progress, err := h.wallet.Progress(r.Context(), s.walletSession)
	if err != nil {
		slog.Warn("NL Wallet verifier unavailable", "err", err)
		errorPage(w, http.StatusServiceUnavailable, msgWalletUnavailable)
		return
	}
	switch progress.State {
	case login.Waiting:
		var image template.URL
		if progress.Link != "" {
			if code, err := qr.Encode(progress.Link, qr.M); err == nil {
				image = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()))
			}
		}
		render(w, http.StatusOK, walletTemplate, map[string]any{"QR": image})
	case login.Failed:
		h.dropLogin(r)
		h.loginPage(w, http.StatusOK, s.request, msgWalletFailed, loginHint{})
	case login.Done:
		id, err := h.wallet.Identify(r.Context(), s.walletSession)
		switch {
		case errors.Is(err, login.ErrUnavailable):
			slog.Warn("NL Wallet verifier unavailable", "err", err)
			errorPage(w, http.StatusServiceUnavailable, msgWalletUnavailable)
		case err != nil:
			slog.Info("NL Wallet disclosure did not identify the citizen", "err", err)
			h.dropLogin(r)
			h.loginPage(w, http.StatusOK, s.request, msgWalletNotUsable, loginHint{})
		default:
			h.mu.Lock()
			if pending := h.logins[loginID]; pending != nil && pending.walletSession == s.walletSession {
				pending.identity, pending.walletSession = &id, ""
			}
			h.mu.Unlock()
			h.next(w, r, s.request)
		}
	}
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	h.dropLogin(r)
	http.SetCookie(w, &http.Cookie{Name: loginCookie, Value: "", Path: "/", HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// who names the logged-in citizen in the header, with Uitloggen.
const who = `{{with .Who}}<span class="qp-user">Ingelogd als <strong>{{.Name}}</strong>{{if .Mocked}} (testgegevens, mock){{end}} ·
<form method="post" action="/logout"><button type="submit">Uitloggen</button></form></span>{{end}}`

var loginTemplate = template.Must(template.New("login").Parse(head + `
<title>Inloggen</title><style>` + style + `</style></head><body>
` + top + `
<main class="qp-main"><div class="qp-card qp-narrow">
<h1>Inloggen</h1>
{{if .Message}}<div class="qp-errsum" role="alert"><b>{{.Message}}</b></div>{{end}}
<p class="lead">Log in om een verklaring aan te vragen. De vertrouwensdienst vraagt uw naam, geboortedatum en nationaliteit uit uw persoonsidentificatiebewijs, niet uw BSN.</p>
<form method="post" action="/login/wallet">
<input type="hidden" name="request" value="{{.Request}}">
{{if .Wallet}}<button class="qp-btn block" type="submit">Inloggen met NL Wallet</button>{{else}}<button class="qp-btn block" type="submit" disabled>Inloggen met NL Wallet</button> <small class="sec">niet beschikbaar</small>{{end}}
</form>
<div class="qp-or">of</div>
<form method="post" action="/login/mock">
<input type="hidden" name="request" value="{{.Request}}">
<fieldset><legend>Inloggen met testgegevens (mock)</legend>
<div class="qp-note warn"><span aria-hidden="true">!</span><div>Mock: hier wordt niets gecontroleerd. Alleen voor de demo en tests.</div></div>
<div class="qp-grid2">
<div class="qp-fld"><label for="family_name">Achternaam</label><input type="text" id="family_name" name="family_name" value="{{.FamilyName}}" autocomplete="off"></div>
<div class="qp-fld"><label for="given_name">Voornamen</label><input type="text" id="given_name" name="given_name" value="{{.GivenName}}" autocomplete="off"></div>
<div class="qp-fld"><label for="birthdate">Geboortedatum <span class="hint">(JJJJ-MM-DD)</span></label><input type="text" id="birthdate" name="birthdate" value="{{.Birthdate}}" inputmode="numeric" autocomplete="off"></div>
<div class="qp-fld"><label for="nationalities">Nationaliteit <span class="hint">(bv. NL, BE)</span></label><input type="text" id="nationalities" name="nationalities" value="{{.Nationalities}}" autocomplete="off"></div>
</div>
<button class="qp-btn sec" type="submit">Inloggen met testgegevens (mock)</button>
</fieldset>
</form>
</div></main>
` + foot))

var walletTemplate = template.Must(template.New("wallet").Parse(head + `
<meta http-equiv="refresh" content="2">
<title>Inloggen met NL Wallet</title><style>` + style + `</style></head><body>
` + top + `
<main class="qp-main"><div class="qp-card qp-narrow">
<h1>Inloggen met NL Wallet</h1>
{{if .QR}}<p class="lead">Scan deze code met de NL Wallet op uw telefoon.</p>
<div class="qp-qrwrap">
<div class="qp-qr"><img src="{{.QR}}" alt="QR-code voor de NL Wallet" width="280" height="280"></div>
<div><ol class="qp-steps"><li>Open de NL Wallet</li><li>Kies <strong>QR-code scannen</strong></li><li>Bevestig welke gegevens u deelt</li></ol>
<div class="qp-live">Deze pagina ververst zichzelf</div></div>
</div>
{{else}}<p class="lead">Ga verder in de NL Wallet op uw telefoon.</p><div class="qp-live">Deze pagina ververst zichzelf</div>{{end}}
<div class="qp-actions"><a href="/">Terug</a></div>
</div></main>
` + foot))
