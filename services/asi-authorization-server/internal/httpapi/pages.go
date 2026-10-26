package httpapi

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"html/template"
	"io/fs"
	"net/http"
	"slices"
	"strings"

	"gbo-demo/asi-authorization-server/internal/authorization"
	"gbo-demo/asi-authorization-server/internal/digidmock"
	"rsc.io/qr"
)

// The pages of the authorization endpoint: the request page, where the user
// sees who asks what at which source and logs in with a means or denies, the
// login page of the DigiD mock (package digidmock), and the error page for a
// request that cannot be redirected. In Dutch, in a government look of the
// demo's own (static/azsp.css), served from this server: no external assets.

// WalletPath follows the user's NL Wallet session: the QR code, then the
// login.
const WalletPath = authorization.AuthorizePath + "/wallet"

// WalletReturnPath receives the browser after a same-device wallet session.
const WalletReturnPath = WalletPath + "/return"

// DecisionPath receives the user's choice on the request page: deny, or a
// means to log in with.
const DecisionPath = authorization.AuthorizePath + "/decision"

// transactionCookiePrefix names the cookie that binds a transaction to the
// browser that started it; the transaction identifier completes the name.
const transactionCookiePrefix = "azsp_tx_"

// StaticPath serves the pages' style sheets and images (static/).
const StaticPath = "/static/"

//go:embed static/*.css static/*.png
var staticFiles embed.FS

// staticHandler serves the style sheets and images. Each has an ETag of its
// content and Cache-Control no-cache: the browser keeps a copy but asks
// whether it changed (304 if not), so a new release shows at once.
func staticHandler() http.Handler {
	files, _ := fs.Sub(staticFiles, "static")
	etags := map[string]string{}
	_ = fs.WalkDir(files, ".", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if raw, err := fs.ReadFile(files, name); err == nil {
				sum := sha256.Sum256(raw)
				etags[name] = `"` + hex.EncodeToString(sum[:8]) + `"`
			}
		}
		return nil
	})
	server := http.StripPrefix(StaticPath, http.FileServerFS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r) // no directory listings
			return
		}
		if etag, ok := etags[strings.TrimPrefix(r.URL.Path, StaticPath)]; ok {
			w.Header().Set("ETag", etag)
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		server.ServeHTTP(w, r)
	})
}

// page builds a page of the authorization server: the header with the coat
// of arms, the test environment banner, the body, and the footer.
func page(name, title, head, body string) *template.Template {
	return template.Must(template.New(name).Parse(`<!doctype html>
<html lang="nl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
` + head + `<title>` + title + ` (testomgeving)</title>
<link rel="stylesheet" href="` + StaticPath + `azsp.css"></head>
<body>
<header class="as-hd"><div class="as-hd-in"><img class="as-wapen" src="` + StaticPath + `rijkswapen.png" alt="Rijkswapen"><p class="as-title">Inloggen en toestemming geven<small>Testomgeving</small></p></div><div class="as-lint"></div></header>
<main class="as-main">
<div class="as-demo" role="note"><span aria-hidden="true">!</span><p><strong>Testomgeving — geen echte identificatie.</strong> Inloggen gaat met een mock van DigiD, waar niets wordt gecontroleerd, of met de NL Wallet uit de testomgeving.</p></div>
` + body + `
</main>
<footer class="as-ft">Testomgeving · inlogvoorziening</footer>
</body></html>
`))
}

// requestPage asks the user to log in, which gives the QTSP consent once
// the login succeeds, or to deny it. It says who asks what at which source,
// and that the user logs in at this service, not at the QTSP: the NL Wallet
// names the authorization server, not the QTSP. Message says why an earlier
// login did not succeed. The wallet button works when the NL Wallet verifier
// is configured.
var requestPage = page("request", "Toestemming geven", "", `{{if .Message}}<div class="as-alert" role="alert"><span aria-hidden="true">!</span><p><strong>{{.Message}}</strong></p></div>
{{end}}<form method="post" action="{{.Action}}" class="as-card">
<input type="hidden" name="transaction" value="{{.TransactionID}}">
<h1>Toestemming voor {{.ClientName}}</h1>
<p class="as-lead">U vraagt bij {{.ClientName}} een verklaring aan. Daarvoor wil {{.ClientName}} bij {{.Source}} {{.Operation}}.</p>
<dl class="as-dl">
<dt>Wie vraagt het</dt><dd>{{.ClientName}}, een vertrouwensdienst</dd>
<dt>Wat</dt><dd>{{.Request}}</dd>
<dt>Bij</dt><dd>{{.Source}}</dd>
{{if .Attributes}}<dt>Welke gegevens</dt><dd><ul>{{range .Attributes}}<li>{{.}}</li>{{end}}</ul></dd>{{end}}
</dl>
{{if .Attributes}}<p class="as-note">{{if .Retrieve}}{{.ClientName}} krijgt deze gegevens te zien.{{else}}{{.ClientName}} hoort alleen of uw gegevens kloppen. Uw gegevens uit {{.Source}} krijgt {{.ClientName}} niet te zien.{{end}}</p>{{end}}
<div class="as-sec">
<h2>Bevestig wie u bent</h2>
<p>Log in om te bevestigen dat het om uw gegevens gaat. Daarmee geeft u {{.ClientName}} toestemming voor alleen dit verzoek. U logt in bij deze inlogvoorziening, niet bij {{.ClientName}}. Daarna gaat u terug naar {{.ClientName}}.</p>
<div class="as-choice">
{{if .Wallet}}<button type="submit" class="as-btn" name="decision" value="wallet">Inloggen met NL Wallet</button>{{else}}<button type="submit" class="as-btn" name="decision" value="wallet" disabled>Inloggen met NL Wallet</button>{{end}}
<button type="submit" class="as-btn" name="decision" value="digid"><img src="`+StaticPath+`Logo_of_DigiD.png" alt="">Inloggen met DigiD (mock)</button>
</div>
{{if not .Wallet}}<p class="as-hint">De NL Wallet is nu niet beschikbaar.</p>
{{end}}<button type="submit" class="as-deny" name="decision" value="deny">Geen toestemming geven</button>
<p class="as-hint">Er wordt dan niets bij {{.Source}} gevraagd en u gaat terug naar {{.ClientName}}.</p>
</div>
</form>`)

// walletPage shows the QR code of the disclosure session and follows the
// session by refreshing itself; it needs no script.
var walletPage = page("wallet", "Inloggen met NL Wallet", `<meta http-equiv="refresh" content="2">
`, `<div class="as-card">
<h1>Inloggen met NL Wallet</h1>
{{if .QR}}<div class="as-qr">
<section aria-labelledby="wallet-other-device">
<h2 id="wallet-other-device">Met een ander apparaat</h2>
<p>Scan deze code met de NL Wallet op uw telefoon.</p>
<div class="as-qr-box"><img src="{{.QR}}" alt="QR-code voor de NL Wallet" width="280" height="280"></div>
</section>
<section aria-labelledby="wallet-same-device">
<h2 id="wallet-same-device">Op dit apparaat</h2>
{{if .SameDeviceEnabled}}<a class="as-btn" href="{{.SameDeviceLink}}">Open NL Wallet</a>
{{else}}<button type="button" class="as-btn sec" disabled>Open NL Wallet</button>
<p class="as-hint">Inloggen op hetzelfde apparaat vereist HTTPS.</p>{{end}}
</section>
</div>
{{else}}<p>Ga verder in de NL Wallet op uw telefoon.</p>{{end}}
<form method="post" action="{{.Action}}">
<input type="hidden" name="transaction" value="{{.TransactionID}}">
<button type="submit" class="as-deny" name="decision" value="deny">Weigeren</button>
</form>
</div>`)

var errorPage = page("error", "Verzoek niet mogelijk", "", `<div class="as-card"><div class="as-err"><div class="as-err-ic" aria-hidden="true">!</div><div>
<h1>Dit verzoek kan niet worden afgehandeld</h1>
<p>invalid_request: {{.Description}}</p>
</div></div></div>`)

var unavailableTemplate = page("unavailable", "Niet beschikbaar", "", `<div class="as-card"><div class="as-err"><div class="as-err-ic" aria-hidden="true">!</div><div>
<h1>Niet beschikbaar</h1>
<p>{{.Message}}</p>
</div></div></div>`)

// operationText says in plain language what the client asks, after "wil bij
// <source>"; requestText names it as a noun phrase. An authorization is for
// one data service.
func operationText(scope string) (operation, request string) {
	var ops, reqs []string
	for _, sc := range strings.Fields(scope) {
		switch sc {
		case "verify":
			ops = append(ops, "laten controleren of uw gegevens kloppen")
			reqs = append(reqs, "controleren of uw gegevens kloppen")
		case "retrieve":
			ops = append(ops, "gegevens over u opvragen")
			reqs = append(reqs, "gegevens over u opvragen")
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
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'")
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
		"Retrieve":      slices.Contains(strings.Fields(tx.Scope), "retrieve"),
		"Action":        DecisionPath,
		"TransactionID": tx.ID,
		"Wallet":        wallet,
		"Message":       message,
	})
}

// writeWalletPage shows the same-device link and the QR code, or, once the
// wallet has the request, that the user continues in the wallet. Same-device
// login needs an HTTPS issuer; local HTTP keeps the choice visible but
// disabled.
func writeWalletPage(w http.ResponseWriter, tx string, progress authorization.Progress, sameDeviceEnabled bool) {
	var image template.URL
	if progress.Link != "" {
		if code, err := qr.Encode(progress.Link, qr.M); err == nil {
			image = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()))
		}
	}
	writePage(w, http.StatusOK, walletPage, map[string]any{
		"QR":                image,
		"SameDeviceLink":    progress.SameDeviceLink,
		"SameDeviceEnabled": sameDeviceEnabled && progress.SameDeviceLink != "",
		"Action":            DecisionPath,
		"TransactionID":     tx,
	})
}

// writeDigiDPage shows the login page of the DigiD mock for transaction tx,
// with message when the BSN was refused.
func writeDigiDPage(w http.ResponseWriter, status int, tx, method, username, bsn, message string) {
	writePage(w, status, digidmock.Page, map[string]any{
		"Action":        digidmock.Path,
		"Cancel":        DecisionPath,
		"TransactionID": tx,
		"Methods":       digidmock.Methods,
		"Method":        method,
		"Username":      username,
		"BSN":           bsn,
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
