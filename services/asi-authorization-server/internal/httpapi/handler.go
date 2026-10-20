// Package httpapi is the HTTP interface of the authorization server: the
// RFC 8414 metadata, the JWKS, the authorization endpoint, the token
// endpoint and the client registration endpoint. It translates requests to the authorization core and its answers
// to the responses of RFC 6749.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"gbo-demo/asi-authorization-server/internal/authorization"
	"gbo-demo/asi-authorization-server/internal/digidmock"
	"gbo-demo/asi-authorization-server/internal/walletverifier"
)

func NewHandler(service *authorization.Service) http.Handler {
	h := &handler{service: service, secureCookies: strings.HasPrefix(service.Issuer(), "https://")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+authorization.MetadataPath, h.metadata)
	mux.HandleFunc("GET "+authorization.JWKSPath, h.jwks)
	mux.HandleFunc("GET "+authorization.AuthorizePath, h.authorize)
	mux.HandleFunc("POST "+DecisionPath, h.decision)
	mux.HandleFunc("GET "+digidmock.Path, h.digidPage)
	mux.HandleFunc("POST "+digidmock.Path, h.digidLogin)
	mux.HandleFunc("GET "+WalletPath, h.walletPage)
	mux.HandleFunc("POST "+authorization.TokenPath, h.token)
	mux.HandleFunc("POST "+authorization.RegisterPath, h.register)
	mux.Handle("GET "+StaticPath, staticHandler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

type handler struct {
	service       *authorization.Service
	secureCookies bool
}

func (h *handler) metadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.service.Metadata())
}

func (h *handler) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.service.JWKS())
}

// authorize is the authorization endpoint (RFC 6749 section 4.1.1).
func (h *handler) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	resp, err := h.service.Authorize(authorization.AuthorizationRequest{
		ResponseType:         q.Get("response_type"),
		ClientID:             q.Get("client_id"),
		RedirectURI:          q.Get("redirect_uri"),
		Scope:                q.Get("scope"),
		State:                q.Get("state"),
		CodeChallenge:        q.Get("code_challenge"),
		CodeChallengeMethod:  q.Get("code_challenge_method"),
		Resources:            q["resource"],
		AuthorizationDetails: q.Get(authorization.AuthorizationDetailsClaim),
	})
	if err != nil {
		authorizeErrorPage(w, err.Error())
		return
	}
	if resp.Pending != nil {
		writeRequestPage(w, resp.Pending, h.secureCookies, h.service.Offers(walletverifier.Means), "")
		return
	}
	redirectAuthorization(w, r, resp)
}

// decision receives the user's choice on the request page: deny, which
// completes the transaction, or a means to log in with. Logging in approves
// the request once the means identifies the user.
func (h *handler) decision(w http.ResponseWriter, r *http.Request) {
	tx, binding, ok := h.transactionForm(w, r)
	if !ok {
		return
	}
	switch choice := r.PostForm.Get("decision"); {
	case choice == "deny":
		resp, err := h.service.Deny(tx, binding)
		h.complete(w, r, tx, resp, err)
	case choice == "digid" && h.service.Offers(digidmock.Means):
		if !h.service.Pending(tx, binding) {
			authorizeErrorPage(w, "the authorization request is unknown, used, expired or was started in another browser")
			return
		}
		http.Redirect(w, r, digidmock.Path+"?"+url.Values{"transaction": {tx}}.Encode(), http.StatusSeeOther)
	case choice == "wallet" && h.service.Offers(walletverifier.Means):
		err := h.service.StartSession(r.Context(), tx, binding, walletverifier.Means)
		switch {
		case errors.Is(err, authorization.ErrMeansUnavailable):
			slog.Warn("NL Wallet verifier unavailable", "err", err)
			unavailablePage(w, walletUnavailable)
		case err != nil:
			authorizeErrorPage(w, err.Error())
		default:
			http.Redirect(w, r, WalletPath+"?"+url.Values{"transaction": {tx}}.Encode(), http.StatusSeeOther)
		}
	case choice == "wallet":
		authorizeErrorPage(w, "logging in with the NL Wallet is not available")
	default:
		authorizeErrorPage(w, "the decision must be deny, digid or wallet")
	}
}

// digidPage shows the login page of the DigiD mock for a pending
// transaction of this browser.
func (h *handler) digidPage(w http.ResponseWriter, r *http.Request) {
	tx := r.URL.Query().Get("transaction")
	if !h.service.Pending(tx, h.binding(r, tx)) {
		authorizeErrorPage(w, "the authorization request is unknown, used, expired or was started in another browser")
		return
	}
	method := r.URL.Query().Get("method")
	if !digidmock.KnownMethod(method) {
		method = "" // the choice of a method first
	}
	writeDigiDPage(w, http.StatusOK, tx, method, digidmock.DefaultUsername, digidmock.DefaultBSN, "")
}

// digidLogin passes what the user typed to the core, which has the DigiD
// mock identify the user and approves the request; when it does not, the
// page shows again.
func (h *handler) digidLogin(w http.ResponseWriter, r *http.Request) {
	tx, binding, ok := h.transactionForm(w, r)
	if !ok {
		return
	}
	resp, err := h.service.LogIn(r.Context(), authorization.Login{TransactionID: tx, Binding: binding, Means: digidmock.Means,
		Credential: strings.TrimSpace(r.PostForm.Get("bsn"))})
	if errors.Is(err, authorization.ErrNotIdentified) {
		method := r.PostForm.Get("method")
		if !digidmock.KnownMethod(method) {
			method = digidmock.Methods[0].ID
		}
		writeDigiDPage(w, http.StatusBadRequest, tx, method, r.PostForm.Get("username"), r.PostForm.Get("bsn"), "Vul als wachtwoord een BSN van 9 cijfers in.")
		return
	}
	h.complete(w, r, tx, resp, err)
}

// walletPage follows the wallet session of the transaction: the QR code
// while the user has not finished; the login once the wallet disclosed the
// PID; the request page again when the user declined in the wallet, the
// session expired, or the disclosure did not identify the user.
func (h *handler) walletPage(w http.ResponseWriter, r *http.Request) {
	tx := r.URL.Query().Get("transaction")
	binding := h.binding(r, tx)
	progress, err := h.service.SessionProgress(r.Context(), tx, binding)
	switch {
	case errors.Is(err, authorization.ErrMeansUnavailable):
		slog.Warn("NL Wallet verifier unavailable", "err", err)
		unavailablePage(w, walletUnavailable)
		return
	case err != nil:
		authorizeErrorPage(w, err.Error())
		return
	}
	switch progress.State {
	case authorization.SessionWaiting:
		writeWalletPage(w, tx, progress.Link)
	case authorization.SessionFailed:
		h.requestAgain(w, tx, binding, "Niet ingelogd met de NL Wallet: geweigerd in de wallet, of de tijd is verstreken.")
	case authorization.SessionDone:
		resp, err := h.service.LogIn(r.Context(), authorization.Login{TransactionID: tx, Binding: binding, Means: walletverifier.Means})
		if errors.Is(err, authorization.ErrMeansUnavailable) {
			slog.Warn("NL Wallet verifier unavailable", "err", err)
			unavailablePage(w, walletUnavailable)
			return
		}
		if errors.Is(err, authorization.ErrNotIdentified) {
			slog.Info("NL Wallet disclosure did not identify the user", "err", err)
			h.requestAgain(w, tx, binding, "Niet ingelogd met de NL Wallet: de wallet gaf geen bruikbaar persoonsidentificatiebewijs.")
			return
		}
		h.complete(w, r, tx, resp, err)
	}
}

// walletUnavailable says the NL Wallet cannot be used now; the transaction
// stays.
const walletUnavailable = "De NL Wallet is nu niet beschikbaar. Ga terug en log in met DigiD (mock), of weiger het verzoek."

// requestAgain shows the request page of a pending transaction with why the
// login did not succeed; the user chooses again.
func (h *handler) requestAgain(w http.ResponseWriter, tx, binding, message string) {
	pending, ok := h.service.Transaction(tx, binding)
	if !ok {
		authorizeErrorPage(w, "the authorization request is unknown, used, expired or was started in another browser")
		return
	}
	writeRequestPage(w, pending, h.secureCookies, h.service.Offers(walletverifier.Means), message)
}

// transactionForm reads a posted form with a transaction, and the binding
// this browser holds for it.
func (h *handler) transactionForm(w http.ResponseWriter, r *http.Request) (tx, binding string, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		authorizeErrorPage(w, "the request is not a form")
		return "", "", false
	}
	tx = r.PostForm.Get("transaction")
	return tx, h.binding(r, tx), true
}

func (h *handler) binding(r *http.Request, tx string) string {
	if c, err := r.Cookie(transactionCookiePrefix + tx); err == nil {
		return c.Value
	}
	return ""
}

// complete sends the answer that ended transaction tx to the client, or
// the error page when it cannot be redirected.
func (h *handler) complete(w http.ResponseWriter, r *http.Request, tx string, resp authorization.AuthorizationResponse, err error) {
	if err != nil {
		authorizeErrorPage(w, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: transactionCookiePrefix + tx, Path: authorization.AuthorizePath, MaxAge: -1, HttpOnly: true, Secure: h.secureCookies, SameSite: http.SameSiteStrictMode})
	redirectAuthorization(w, r, resp)
}

// redirectAuthorization sends the authorization response to the client: the
// code, or the error, with the state (RFC 6749 sections 4.1.2 and 4.1.2.1).
func redirectAuthorization(w http.ResponseWriter, r *http.Request, resp authorization.AuthorizationResponse) {
	params := url.Values{"code": {resp.Code}, "state": {resp.State}}
	if resp.Err != nil {
		params = url.Values{"error": {resp.Err.Code}, "error_description": {resp.Err.Description}, "state": {resp.State}}
	}
	redirectWith(w, r, resp.RedirectURI, params)
}

// token is the token endpoint (RFC 6749 section 4.1.3).
func (h *handler) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		tokenError(w, &authorization.Error{Code: "invalid_request", Description: "the request body is not a form"})
		return
	}
	f := r.PostForm
	_, _, basic := r.BasicAuth()
	resp, oauthErr := h.service.Token(authorization.TokenRequest{
		GrantType:           f.Get("grant_type"),
		Code:                f.Get("code"),
		RedirectURI:         f.Get("redirect_uri"),
		CodeVerifier:        f.Get("code_verifier"),
		Resources:           f["resource"],
		ClientID:            f.Get("client_id"),
		ClientAssertionType: f.Get("client_assertion_type"),
		ClientAssertion:     f.Get("client_assertion"),
		ClientSecret:        f.Get("client_secret"),
		BasicAuth:           basic,
		DPoP:                r.Header.Values("DPoP"),
	})
	if oauthErr != nil {
		tokenError(w, oauthErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": resp.AccessToken,
		"token_type":   resp.TokenType,
		"expires_in":   resp.ExpiresIn,
		"scope":        resp.Scope,
	})
}

// register is the client registration endpoint (RFC 7591 section 3).
func (h *handler) register(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		registrationError(w, &authorization.Error{Code: "invalid_client_metadata", Description: "the request body is not a JSON object"})
		return
	}
	var statement string
	if raw, ok := body["software_statement"]; ok {
		_ = json.Unmarshal(raw, &statement)
		delete(body, "software_statement")
	}
	c, oauthErr := h.service.Register(r.Context(), authorization.RegistrationRequest{Metadata: body, SoftwareStatement: statement})
	if oauthErr != nil {
		registrationError(w, oauthErr)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  c.ClientID,
		"client_id_issued_at":        c.ClientIDIssuedAt.Unix(),
		"redirect_uris":              c.RedirectURIs,
		"scope":                      c.Scope,
		"token_endpoint_auth_method": c.TokenEndpointAuthMethod,
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
		"jwks":                       c.JWKS,
	})
}

// registrationError writes an error response of RFC 7591 section 3.2.2:
// 400, or 503 when the register cannot be consulted.
func registrationError(w http.ResponseWriter, e *authorization.Error) {
	status := http.StatusBadRequest
	if e.Code == "temporarily_unavailable" {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]string{"error": e.Code, "error_description": e.Description})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// tokenError writes an error response of RFC 6749 section 5.2: 401 for a
// client that failed to authenticate, 500 for a server error, 400 otherwise.
func tokenError(w http.ResponseWriter, e *authorization.Error) {
	status := http.StatusBadRequest
	switch e.Code {
	case "invalid_client":
		status = http.StatusUnauthorized
	case "server_error":
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, map[string]string{"error": e.Code, "error_description": e.Description})
}

func redirectWith(w http.ResponseWriter, r *http.Request, redirectURI string, params url.Values) {
	u, _ := url.Parse(redirectURI)
	q := u.Query()
	for k, v := range params {
		if len(v) > 0 && v[0] != "" {
			q[k] = v
		}
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}
