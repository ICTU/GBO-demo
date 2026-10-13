package httpapi

import (
	"html/template"
	"net/http"
	"strings"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

// The pages of the authorization endpoint: the test-person chooser, where the
// user is identified and authorizes the request, and the error page for a
// request that cannot be redirected. In Dutch, without external assets.

// DecisionPath receives the user's decision on the chooser.
const DecisionPath = authorization.AuthorizePath + "/decision"

// transactionCookiePrefix names the cookie that binds a transaction to the
// browser that started it; the transaction identifier completes the name.
const transactionCookiePrefix = "azsp_tx_"

var pageStyle = template.CSS(`body{font-family:system-ui,sans-serif;max-width:40rem;margin:2rem auto;padding:0 1rem;color:#1a1a1a}
.banner{background:#fff3cd;border:2px solid #b8860b;padding:.75rem 1rem;margin-bottom:1.5rem}
fieldset{border:1px solid #ccc;padding:1rem}label{display:block;margin:.4rem 0}
button{margin:1rem .5rem 0 0;padding:.5rem 1.2rem;font-size:1rem}`)

const banner = `<p class="banner"><strong>Testomgeving — geen echte identificatie.</strong> U kiest een testpersoon; er wordt niets gecontroleerd. Dit is een stub en voldoet niet aan ETSI TS 119 478 REQ-AZSP-6.1.3.1-02.</p>`

var chooserPage = template.Must(template.New("chooser").Parse(`<!doctype html>
<html lang="nl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Inloggen (testomgeving)</title><style>{{.Style}}</style></head><body>
` + banner + `
<h1>Toegang verlenen</h1>
<p>Client <code>{{.ClientID}}</code> vraagt toestemming om {{.Operation}}.</p>
<form method="post" action="{{.Action}}">
<input type="hidden" name="transaction" value="{{.TransactionID}}">
<fieldset><legend>Kies een testpersoon</legend>
{{range .Persons}}<label><input type="radio" name="person" value="{{.ID}}" required{{if .Selected}} checked{{end}}> {{.Name}}</label>
{{end}}</fieldset>
<button type="submit" name="decision" value="approve">Toestaan</button>
<button type="submit" name="decision" value="deny" formnovalidate>Weigeren</button>
</form></body></html>
`))

var errorPage = template.Must(template.New("error").Parse(`<!doctype html>
<html lang="nl"><head><meta charset="utf-8"><title>Verzoek niet mogelijk</title><style>{{.Style}}</style></head><body>
` + banner + `
<h1>Dit verzoek kan niet worden afgehandeld</h1>
<p>invalid_request: {{.Description}}</p>
</body></html>
`))

type chooserPerson struct {
	ID, Name string
	Selected bool
}

// operationText describes the requested operations to the user.
func operationText(scope string) string {
	var parts []string
	for _, sc := range strings.Fields(scope) {
		switch sc {
		case "verify":
			parts = append(parts, "gegevens van u te laten verifiëren bij de authentieke bron")
		case "retrieve":
			parts = append(parts, "gegevens van u op te halen bij de authentieke bron")
		}
	}
	return strings.Join(parts, " en ")
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

func writeChooser(w http.ResponseWriter, tx *authorization.Transaction, secure bool) {
	persons := make([]chooserPerson, len(tx.Persons))
	for i, p := range tx.Persons {
		persons[i] = chooserPerson{ID: p.ID, Name: p.Name, Selected: p.ID == tx.Preselected}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     transactionCookiePrefix + tx.ID,
		Value:    tx.Binding,
		Path:     authorization.AuthorizePath,
		MaxAge:   int(authorization.TransactionLifetime.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	writePage(w, http.StatusOK, chooserPage, map[string]any{
		"ClientID":      tx.ClientID,
		"Operation":     operationText(tx.Scope),
		"Action":        DecisionPath,
		"TransactionID": tx.ID,
		"Persons":       persons,
	})
}

// authorizeErrorPage answers a request that cannot be redirected safely
// (RFC 6749 section 4.1.2.1).
func authorizeErrorPage(w http.ResponseWriter, description string) {
	writePage(w, http.StatusBadRequest, errorPage, map[string]any{"Description": description})
}
