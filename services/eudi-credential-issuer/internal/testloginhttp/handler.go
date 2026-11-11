// Package testloginhttp serves the browser adapter of the demo person picker.
package testloginhttp

import (
	"errors"
	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/eudi-credential-issuer/internal/testlogin"
	"html/template"
	"net/http"
	"net/url"
	"strings"
)

// Handler permits the configured wallet callbacks in form-action because WebKit
// also applies that directive to redirects following the same-origin form POST.
func Handler(service *testlogin.Service, walletRedirects ...string) http.Handler {
	formAction := "'self'"
	for _, target := range walletRedirects {
		u, err := url.Parse(target)
		if err == nil && u.Scheme != "" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && !strings.ContainsAny(target, " \t\r\n;'\"") {
			formAction += " " + target
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action "+formAction+"; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		var err error
		switch r.Method {
		case http.MethodGet:
			ticket := r.URL.Query().Get("ticket")
			var people []testlogin.Person
			people, err = service.Choices(r.Context(), ticket)
			if err == nil {
				_ = page.Execute(w, struct {
					Ticket string
					People []testlogin.Person
				}{ticket, people})
				return
			}
		case http.MethodPost:
			r.Body = http.MaxBytesReader(w, r.Body, 8192)
			if r.ParseForm() != nil {
				http.Error(w, "Aanvraag niet geldig", 400)
				return
			}
			var location string
			location, err = service.Choose(r.Context(), r.PostForm.Get("ticket"), r.PostForm.Get("bsn"))
			if err == nil {
				http.Redirect(w, r, location, http.StatusSeeOther)
				return
			}
		default:
			w.Header().Set("Allow", "GET, POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		status := http.StatusBadRequest
		message := "Deze aanvraag is niet (meer) geldig. Start opnieuw vanuit uw wallet."
		if errors.Is(err, testlogin.ErrUnknownPerson) {
			message = "Kies een testpersoon uit de lijst."
		}
		if errors.Is(err, issuance.ErrLoginUnavailable) {
			status = http.StatusServiceUnavailable
			message = "Inloggen is nu niet beschikbaar. Probeer het later opnieuw."
		}
		w.WriteHeader(status)
		_ = failure.Execute(w, message)
	})
}

const head = `<!doctype html><html lang="nl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>GBO test-PID · Kies een testpersoon</title><style>
:root{color-scheme:light;--blue:#154273;--ink:#172b3d;--line:#d9e2ec}*{box-sizing:border-box}body{margin:0;background:#f4f7fa;color:var(--ink);font:1rem/1.55 Verdana,sans-serif;border-top:6px solid var(--blue)}main{max-width:36rem;margin:2.5rem auto;padding:2rem;background:white;border:1px solid var(--line)}.eyebrow{font-size:.75rem;letter-spacing:.1em;text-transform:uppercase;color:var(--blue);font-weight:bold}h1{font:normal 2rem/1.2 Georgia,serif;margin:.6rem 0 1rem}p{margin:.6rem 0 1.3rem}.notice{padding:.85rem 1rem;border-left:3px solid #d89000;background:#fff6df;font-size:.9rem}fieldset{border:0;padding:0;margin:1.5rem 0}legend{font-weight:bold;margin-bottom:.5rem}label{display:flex;align-items:center;gap:.9rem;padding:.9rem;border:1px solid var(--line);margin-top:.6rem;cursor:pointer}label:has(input:checked){border-color:var(--blue);background:#eef5fc}input{width:1.2rem;height:1.2rem;accent-color:var(--blue);flex:none}small{display:block;color:#526777;font-size:.8rem}button{background:var(--blue);border:2px solid var(--blue);color:white;font:inherit;font-weight:bold;padding:.75rem 1.15rem;cursor:pointer}button:hover{background:#0d2d51}button:focus-visible,input:focus-visible{outline:3px solid #d89000;outline-offset:3px}@media(max-width:40rem){main{margin:.75rem .75rem calc(1rem + env(safe-area-inset-bottom));padding:1rem}h1{font-size:1.75rem;margin:.4rem 0 .65rem}p{margin:.4rem 0 .85rem}.notice{padding:.6rem .75rem}fieldset{margin:.85rem 0}label{padding:.6rem .75rem;margin-top:.4rem}button{width:100%;min-height:44px}}
</style></head><body><main><div class="eyebrow">GBO · Testomgeving</div>`

var page = template.Must(template.New("persons").Parse(head + `<h1>Kies een testpersoon</h1><p>De gegevens van deze persoon worden als test-PID aan uw wallet toegevoegd.</p><p class="notice">Dit zijn fictieve personen. U identificeert zich hiermee niet.</p><form method="post"><input type="hidden" name="ticket" value="{{.Ticket}}"><fieldset><legend>Voor wie wilt u een test-PID?</legend>{{range .People}}<label><input type="radio" name="bsn" value="{{.BSN}}" required><span>{{.Name}}<small>Test-BSN {{.BSN}}</small></span></label>{{end}}</fieldset><button type="submit">Test-PID toevoegen</button></form></main></body></html>`))
var failure = template.Must(template.New("error").Parse(head + `<h1>Aanvraag niet beschikbaar</h1><p>{{.}}</p></main></body></html>`))
