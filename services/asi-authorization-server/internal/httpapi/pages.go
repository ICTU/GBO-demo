package httpapi

import (
	"html/template"
	"net/http"
	"strings"

	"gbo-demo/asi-authorization-server/internal/authorization"
	"gbo-demo/asi-authorization-server/internal/digidmock"
)

// The pages of the authorization endpoint: the request page, where the user
// sees who asks what at which source and logs in with a means or denies, the
// login page of the DigiD mock (package digidmock), and the error page for a
// request that cannot be redirected. In Dutch, without external assets.

// DecisionPath receives the user's choice on the request page: deny, or a
// means to log in with.
const DecisionPath = authorization.AuthorizePath + "/decision"

// transactionCookiePrefix names the cookie that binds a transaction to the
// browser that started it; the transaction identifier completes the name.
const transactionCookiePrefix = "azsp_tx_"

var pageStyle = template.CSS(`body{font-family:system-ui,sans-serif;max-width:40rem;margin:2rem auto;padding:0 1rem;color:#1a1a1a}
.banner{background:#fff3cd;border:2px solid #b8860b;padding:.75rem 1rem;margin-bottom:1.5rem}
fieldset{border:1px solid #ccc;padding:1rem}label{display:block;margin:.4rem 0}
button{margin:1rem .5rem 0 0;padding:.5rem 1.2rem;font-size:1rem}`)

const banner = `<p class="banner"><strong>Testomgeving — geen echte identificatie.</strong> Inloggen gaat met een mock van DigiD; er wordt niets gecontroleerd. Dit voldoet niet aan ETSI TS 119 478 REQ-AZSP-6.1.3.1-02.</p>`

// requestPage asks the user to log in, which approves the request once the
// login succeeds, or to deny it. The wallet follows in slice 7 of #336.
var requestPage = template.Must(template.New("request").Parse(`<!doctype html>
<html lang="nl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Toestemming geven (testomgeving)</title><style>{{.Style}}</style></head><body>
` + banner + `
<h1>Toestemming geven</h1>
<p><strong>{{.ClientName}}</strong> vraagt toestemming om {{.Operation}}.</p>
<p>Log in om toestemming te geven:</p>
<form method="post" action="{{.Action}}">
<input type="hidden" name="transaction" value="{{.TransactionID}}">
<p><button type="submit" name="decision" value="digid">Inloggen met DigiD (mock)</button></p>
<p><button type="submit" name="decision" value="wallet" disabled>Inloggen met NL Wallet</button> <small>volgt</small></p>
<p><button type="submit" name="decision" value="deny">Weigeren</button></p>
</form></body></html>
`))

var errorPage = template.Must(template.New("error").Parse(`<!doctype html>
<html lang="nl"><head><meta charset="utf-8"><title>Verzoek niet mogelijk</title><style>{{.Style}}</style></head><body>
` + banner + `
<h1>Dit verzoek kan niet worden afgehandeld</h1>
<p>invalid_request: {{.Description}}</p>
</body></html>
`))

// operationText describes the requested operations to the user.
// operationText names what the client asks, and at which authentic source:
// an authorization is for one data service.
func operationText(scope, source string) string {
	var parts []string
	for _, sc := range strings.Fields(scope) {
		switch sc {
		case "verify":
			parts = append(parts, "gegevens van u te laten verifiëren")
		case "retrieve":
			parts = append(parts, "gegevens van u op te halen")
		}
	}
	return strings.Join(parts, " en ") + " bij " + source
}

// writePage sends a page that must not be cached or framed.
func writePage(w http.ResponseWriter, status int, page *template.Template, data map[string]any) {
	data["Style"] = pageStyle
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(status)
	_ = page.Execute(w, data)
}

// writeRequestPage shows the request and binds the transaction to this
// browser with a cookie.
func writeRequestPage(w http.ResponseWriter, tx *authorization.Transaction, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     transactionCookiePrefix + tx.ID,
		Value:    tx.Binding,
		Path:     authorization.AuthorizePath,
		MaxAge:   int(authorization.TransactionLifetime.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	writePage(w, http.StatusOK, requestPage, map[string]any{
		"ClientName":    tx.ClientName,
		"Operation":     operationText(tx.Scope, tx.Source),
		"Action":        DecisionPath,
		"TransactionID": tx.ID,
	})
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

// authorizeErrorPage answers a request that cannot be redirected safely
// (RFC 6749 section 4.1.2.1).
func authorizeErrorPage(w http.ResponseWriter, description string) {
	writePage(w, http.StatusBadRequest, errorPage, map[string]any{"Description": description})
}
