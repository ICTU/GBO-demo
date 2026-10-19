package httpapi

import (
	"embed"
	"encoding/base64"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"gbo-demo/asi-authorization-server/internal/authorization"
	"gbo-demo/asi-authorization-server/internal/digidmock"
	"rsc.io/qr"
)

// The pages of the authorization endpoint: the request page, where the user
// sees who asks what at which source and logs in with a means or denies, the
// login page of the DigiD mock (package digidmock), and the error page for a
// request that cannot be redirected. In Dutch, in NL Design System's
// Voorbeeld theme, served from this server: no external assets.

// WalletPath follows the user's NL Wallet session: the QR code, then the
// login.
const WalletPath = authorization.AuthorizePath + "/wallet"

// DecisionPath receives the user's choice on the request page: deny, or a
// means to log in with.
const DecisionPath = authorization.AuthorizePath + "/decision"

// transactionCookiePrefix names the cookie that binds a transaction to the
// browser that started it; the transaction identifier completes the name.
const transactionCookiePrefix = "azsp_tx_"

// StaticPath serves the pages' style sheets (static/): NL Design System's
// Voorbeeld theme and Utrecht components, and the layout on top.
const StaticPath = "/static/"

//go:embed static/*.css static/nlds/*.css
var staticFiles embed.FS

// staticHandler serves the style sheets, which may be cached.
func staticHandler() http.Handler {
	files, _ := fs.Sub(staticFiles, "static")
	server := http.StripPrefix(StaticPath, http.FileServerFS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r) // no directory listings
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		server.ServeHTTP(w, r)
	})
}

// page builds a page of the authorization server: the header, the test
// environment banner, and body in a card.
func page(name, title, head, body string) *template.Template {
	return template.Must(template.New(name).Parse(`<!doctype html>
<html lang="nl" class="voorbeeld-theme"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
` + head + `<title>` + title + ` (testomgeving)</title>
<link rel="stylesheet" href="` + StaticPath + `nlds/voorbeeld-theme.css">
<link rel="stylesheet" href="` + StaticPath + `nlds/utrecht-components.css">
<link rel="stylesheet" href="` + StaticPath + `azsp.css"></head>
<body>
<header class="azsp-header"><div class="azsp-header__content">
<p class="azsp-header__service">Autorisatieserver van de bron</p><p class="azsp-header__demo">GBO demo</p>
</div></header>
<main class="azsp-main"><div class="azsp-card">
<div class="utrecht-alert utrecht-alert--warning" role="note"><div class="utrecht-alert__content"><div class="utrecht-alert__message">
<p class="utrecht-paragraph"><strong>Testomgeving — geen echte identificatie.</strong> Inloggen gaat met een mock van DigiD, waar niets wordt gecontroleerd, of met de NL Wallet uit de testomgeving.</p>
</div></div></div>
` + body + `
</div></main></body></html>
`))
}

const (
	primaryButton   = `class="utrecht-button utrecht-button--primary-action"`
	secondaryButton = `class="utrecht-button utrecht-button--secondary-action"`
	subtleButton    = `class="utrecht-button utrecht-button--subtle"`
)

// requestPage asks the user to log in, which approves the request once the
// login succeeds, or to deny it. It says who asks what at which source, and
// that the user logs in at the source and approves the request of the QTSP:
// the NL Wallet names the authorization server, not the QTSP. Message says
// why an earlier login did not succeed. The wallet button works when the NL
// Wallet verifier is configured.
var requestPage = page("request", "Toestemming geven", "", `<h1 class="utrecht-heading-1">Toestemming geven</h1>
<p class="utrecht-paragraph utrecht-paragraph--lead"><strong>{{.ClientName}}</strong> wil bij {{.Source}} {{.Operation}}.</p>
<dl class="azsp-request">
<dt>Aanvrager</dt><dd>{{.ClientName}}, een vertrouwensdienst</dd>
<dt>Verzoek</dt><dd>{{.Request}}</dd>
<dt>Bron</dt><dd>{{.Source}}</dd>
<dt>Gegevens</dt><dd><ul class="azsp-attributes">{{range .Attributes}}<li>{{.}}</li>{{end}}</ul></dd>
</dl>
{{if .Message}}<div class="utrecht-alert utrecht-alert--error" role="alert"><div class="utrecht-alert__content"><div class="utrecht-alert__message"><p class="utrecht-paragraph"><strong>{{.Message}}</strong></p></div></div></div>{{end}}
<h2 class="utrecht-heading-2">Inloggen</h2>
<p class="utrecht-paragraph">U logt in bij de bron. Daarmee geeft u {{.ClientName}} toestemming voor dit verzoek.</p>
<form method="post" action="{{.Action}}">
<input type="hidden" name="transaction" value="{{.TransactionID}}">
<div class="azsp-means">
{{if .Wallet}}<button type="submit" `+primaryButton+` name="decision" value="wallet">Inloggen met NL Wallet</button>{{else}}<button type="submit" `+primaryButton+` name="decision" value="wallet" disabled>Inloggen met NL Wallet</button>
<p class="utrecht-paragraph">De NL Wallet is nu niet beschikbaar.</p>{{end}}
<button type="submit" `+secondaryButton+` name="decision" value="digid">Inloggen met DigiD (mock)</button>
<button type="submit" `+subtleButton+` name="decision" value="deny">Weigeren</button>
</div>
</form>`)

// walletPage shows the QR code of the disclosure session and follows the
// session by refreshing itself; it needs no script.
var walletPage = page("wallet", "Inloggen met NL Wallet", `<meta http-equiv="refresh" content="2">
`, `<h1 class="utrecht-heading-1">Inloggen met NL Wallet</h1>
{{if .QR}}<p class="utrecht-paragraph">Scan deze code met de NL Wallet op uw telefoon.</p>
<img class="azsp-qr" src="{{.QR}}" alt="QR-code voor de NL Wallet" width="280" height="280">
{{else}}<p class="utrecht-paragraph">Ga verder in de NL Wallet op uw telefoon.</p>{{end}}
<form method="post" action="{{.Action}}">
<input type="hidden" name="transaction" value="{{.TransactionID}}">
<div class="azsp-means"><button type="submit" `+subtleButton+` name="decision" value="deny">Weigeren</button></div>
</form>`)

var errorPage = page("error", "Verzoek niet mogelijk", "", `<h1 class="utrecht-heading-1">Dit verzoek kan niet worden afgehandeld</h1>
<p class="utrecht-paragraph">invalid_request: {{.Description}}</p>`)

var unavailableTemplate = page("unavailable", "Niet beschikbaar", "", `<h1 class="utrecht-heading-1">Niet beschikbaar</h1>
<p class="utrecht-paragraph">{{.Message}}</p>`)

// operationText says in plain language what the client asks, after "wil bij
// <source>"; requestText names it as a noun phrase. An authorization is for
// one data service.
func operationText(scope string) (operation, request string) {
	var ops, reqs []string
	for _, sc := range strings.Fields(scope) {
		switch sc {
		case "verify":
			ops = append(ops, "laten controleren of gegevens over u kloppen")
			reqs = append(reqs, "controleren of gegevens kloppen")
		case "retrieve":
			ops = append(ops, "gegevens over u opvragen")
			reqs = append(reqs, "gegevens opvragen")
		}
	}
	request = strings.Join(reqs, " en ")
	if request != "" {
		request = strings.ToUpper(request[:1]) + request[1:]
	}
	return strings.Join(ops, " en "), request
}

func requestedAttributes(ids []string) []string {
	labels := map[string]string{
		"family_name": "Achternaam",
		"given_name":  "Voornamen",
		"birth_date":  "Geboortedatum",
		"nationality": "Nationaliteit",
		"address":     "Adres",
	}
	attributes := make([]string, 0, len(ids))
	for _, id := range ids {
		name := id
		parts := strings.Split(strings.TrimSuffix(id, "/"), "/")
		if len(parts) >= 2 {
			key := parts[len(parts)-2]
			name = strings.ReplaceAll(key, "_", " ")
			if label := labels[key]; label != "" {
				name = label
			}
		}
		attributes = append(attributes, name)
	}
	return attributes
}

// writePage sends a page that must not be cached or framed. Its style
// sheets come from this server; it has no script.
func writePage(w http.ResponseWriter, status int, page *template.Template, data map[string]any) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src data:; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(status)
	_ = page.Execute(w, data)
}

// writeRequestPage shows the request, with message when an earlier login
// did not succeed, and binds the transaction to this browser with a cookie.
func writeRequestPage(w http.ResponseWriter, tx *authorization.Transaction, secure, wallet bool, message string) {
	http.SetCookie(w, &http.Cookie{
		Name:     transactionCookiePrefix + tx.ID,
		Value:    tx.Binding,
		Path:     authorization.AuthorizePath,
		MaxAge:   int(authorization.TransactionLifetime.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	operation, request := operationText(tx.Scope)
	writePage(w, http.StatusOK, requestPage, map[string]any{
		"ClientName":    tx.ClientName,
		"Operation":     operation,
		"Request":       request,
		"Source":        tx.Source,
		"Attributes":    requestedAttributes(tx.Attributes),
		"Action":        DecisionPath,
		"TransactionID": tx.ID,
		"Wallet":        wallet,
		"Message":       message,
	})
}

// writeWalletPage shows the QR code of link, or, once the wallet has it,
// that the user continues in the wallet.
func writeWalletPage(w http.ResponseWriter, tx, link string) {
	var image template.URL
	if link != "" {
		if code, err := qr.Encode(link, qr.M); err == nil {
			image = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()))
		}
	}
	writePage(w, http.StatusOK, walletPage, map[string]any{"QR": image, "Action": DecisionPath, "TransactionID": tx})
}

// writeDigiDPage shows the login page of the DigiD mock for transaction tx,
// with message when the BSN was refused.
func writeDigiDPage(w http.ResponseWriter, status int, tx, username, message string) {
	writePage(w, status, digidmock.Page, map[string]any{
		"Action":        digidmock.Path,
		"Cancel":        DecisionPath,
		"TransactionID": tx,
		"Username":      username,
		"Message":       message,
	})
}

// unavailablePage says a means is unavailable; the transaction stays.
func unavailablePage(w http.ResponseWriter, message string) {
	writePage(w, http.StatusServiceUnavailable, unavailableTemplate, map[string]any{"Message": message})
}

// authorizeErrorPage answers a request that cannot be redirected safely
// (RFC 6749 section 4.1.2.1).
func authorizeErrorPage(w http.ResponseWriter, description string) {
	writePage(w, http.StatusBadRequest, errorPage, map[string]any{"Description": description})
}
