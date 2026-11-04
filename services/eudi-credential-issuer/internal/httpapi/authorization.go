package httpapi

import (
	"errors"
	"html/template"
	"net/http"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/oid4vc-kit/clientattest"
	"gbo-demo/oid4vc-kit/oid4vci"
)

// The authorization code flow with a login at GBO's authorization server:
// the pushed authorization request, the authorization request the wallet
// opens in the browser, and the browser's return from the login.

func (h *handler) par(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := r.ParseForm(); err != nil {
		h.fail(w, &issuance.Error{Code: oid4vci.ErrInvalidRequest, Description: "the body must be a form"})
		return
	}
	f := r.PostForm
	if f.Has("request_uri") {
		// RFC 9126 section 2.1: a pushed request does not reference another.
		h.fail(w, &issuance.Error{Code: oid4vci.ErrInvalidRequest, Description: "request_uri is not allowed in a pushed authorization request"})
		return
	}
	resp, err := h.svc.PushAuthorization(r.Context(), issuance.PushedAuthorizationRequest{
		Target:               target(r),
		ResponseType:         f.Get("response_type"),
		ClientID:             f.Get("client_id"),
		RedirectURI:          f.Get("redirect_uri"),
		State:                f.Get("state"),
		CodeChallenge:        f.Get("code_challenge"),
		CodeChallengeMethod:  f.Get("code_challenge_method"),
		IssuerState:          f.Get("issuer_state"),
		Scope:                f.Get("scope"),
		AuthorizationDetails: f.Get("authorization_details"),
		ClientAttestation:    r.Header.Get(clientattest.AttestationHeader),
		ClientAttestationPoP: r.Header.Get(clientattest.PoPHeader),
		DPoP:                 r.Header.Values("DPoP"),
	})
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, resp)
}

// browser serves the endpoints the wallet opens in the browser.
func (h *handler) browser(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("endpoint") {
	case "authorize":
		h.authorize(w, r)
	case "callback":
		h.callback(w, r)
	default:
		http.NotFound(w, r)
	}
}

// authorize sends the browser to the login, or shows why it cannot.
func (h *handler) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	location, ret, err := h.svc.Authorize(r.Context(), target(r), q.Get("client_id"), q.Get("request_uri"))
	switch {
	case err == nil && ret != nil:
		h.log.Info("login unavailable", "page", ret.Page)
		h.walletReturn(w, r, ret)
	case err == nil:
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, location, http.StatusFound)
	default:
		h.authorizationFailed(w, err)
	}
}

// callback is the browser's return from the login.
func (h *handler) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ret, err := h.svc.Callback(r.Context(), target(r), issuance.LoginReturn{Code: q.Get("code"), State: q.Get("state"), Error: q.Get("error")})
	if err != nil {
		h.authorizationFailed(w, err)
		return
	}
	// Neither the code nor the BSN is logged: only how the login ended.
	h.log.Info("login returned", "error", description(q.Get("error")), "page", ret.Page, "code_for_wallet", ret.Page == issuance.ReturnRedirect && q.Get("error") == "")
	h.walletReturn(w, r, ret)
}

// authorizationFailed shows an error page: the request cannot be answered
// to a wallet.
func (h *handler) authorizationFailed(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, issuance.ErrUnknownProvider), errors.Is(err, issuance.ErrUnknownProfile):
		status = http.StatusNotFound
	case errors.Is(err, issuance.ErrInvalidAuthorization):
		h.log.Info("authorization refused", "error", err.Error())
	default:
		h.log.Error("authorization failed", "error", err)
		status = http.StatusInternalServerError
	}
	w.WriteHeader(status)
	_ = invalidAuthorizationTemplate.Execute(w, nil)
}

// walletReturn sends the browser to the wallet, or first shows the page
// whose button does.
func (h *handler) walletReturn(w http.ResponseWriter, r *http.Request, ret *issuance.WalletReturn) {
	w.Header().Set("Cache-Control", "no-store")
	if ret.Page == issuance.ReturnRedirect {
		http.Redirect(w, r, ret.Location, http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The location is the wallet's own redirect URI, one this issuer
	// trusts by configuration, so its scheme may be the wallet's.
	data := map[string]any{"Back": template.URL(ret.Location), "Organization": ret.Organization, "Attestation": ret.Attestation}
	tmpl := unavailableTemplate
	if ret.Page == issuance.ReturnNoData {
		tmpl = noDataReturnTemplate
	}
	if err := tmpl.Execute(w, data); err != nil {
		h.log.Error("page failed", "error", err)
	}
}

const pageHead = `<!doctype html>
<html lang="nl">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{block "title" .}}{{end}}</title>
<style>
body { font-family: system-ui, sans-serif; margin: 0; padding: 2rem 1rem; background: #f5f7fa; color: #1a1a1a; }
main { max-width: 32rem; margin: 0 auto; background: #fff; border-radius: 8px; padding: 1.5rem; }
h1 { font-size: 1.4rem; margin-top: 0; }
a.button { display: inline-block; margin-top: 1rem; padding: 0.7rem 1.2rem; background: #154273; color: #fff; border-radius: 4px; text-decoration: none; }
</style>
</head>
<body>
<main>
`

const pageFoot = `</main>
</body>
</html>
`

var noDataReturnTemplate = template.Must(template.New("no-data-return").Parse(`{{define "title"}}Geen gegevens gevonden{{end}}` + pageHead + `<h1>Geen gegevens gevonden</h1>
<p>{{.Organization}} heeft geen gegevens voor u voor: <strong>{{.Attestation}}</strong>.</p>
<p>Er is niets aan uw wallet toegevoegd.</p>
<a class="button" href="{{.Back}}">Terug naar de wallet</a>
` + pageFoot))

var unavailableTemplate = template.Must(template.New("unavailable").Parse(`{{define "title"}}Bron niet beschikbaar{{end}}` + pageHead + `<h1>Bron niet beschikbaar</h1>
<p>Uw gegevens kunnen nu niet worden opgehaald. Probeer het later opnieuw.</p>
<p>Er is niets aan uw wallet toegevoegd.</p>
<a class="button" href="{{.Back}}">Terug naar de wallet</a>
` + pageFoot))

var invalidAuthorizationTemplate = template.Must(template.New("invalid").Parse(`{{define "title"}}Aanvraag niet geldig{{end}}` + pageHead + `<h1>Aanvraag niet geldig</h1>
<p>Deze aanvraag is niet (meer) geldig. Start de aanvraag opnieuw vanuit uw wallet.</p>
` + pageFoot))
