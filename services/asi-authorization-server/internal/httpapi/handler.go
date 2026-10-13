// Package httpapi is the HTTP interface of the authorization server: the
// RFC 8414 metadata, the JWKS, the authorization endpoint, the token
// endpoint and the client registration endpoint. It translates requests to the authorization core and its answers
// to the responses of RFC 6749.
package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

func NewHandler(service *authorization.Service) http.Handler {
	h := &handler{service: service, secureCookies: strings.HasPrefix(service.Issuer(), "https://")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+authorization.MetadataPath, h.metadata)
	mux.HandleFunc("GET "+authorization.JWKSPath, h.jwks)
	mux.HandleFunc("GET "+authorization.AuthorizePath, h.authorize)
	mux.HandleFunc("POST "+DecisionPath, h.decision)
	mux.HandleFunc("POST "+authorization.TokenPath, h.token)
	mux.HandleFunc("POST "+authorization.RegisterPath, h.register)
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
		ResponseType:        q.Get("response_type"),
		ClientID:            q.Get("client_id"),
		RedirectURI:         q.Get("redirect_uri"),
		Scope:               q.Get("scope"),
		State:               q.Get("state"),
		CodeChallenge:       q.Get("code_challenge"),
		CodeChallengeMethod: q.Get("code_challenge_method"),
		Resources:           q["resource"],
		LoginHint:           q.Get("login_hint"),
	})
	if err != nil {
		authorizeErrorPage(w, err.Error())
		return
	}
	if resp.Pending != nil {
		writeChooser(w, resp.Pending, h.secureCookies)
		return
	}
	redirectAuthorization(w, r, resp)
}

// decision receives the user's choice on the chooser page.
func (h *handler) decision(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		authorizeErrorPage(w, "the decision is not a form")
		return
	}
	tx := r.PostForm.Get("transaction")
	var binding string
	if c, err := r.Cookie(transactionCookiePrefix + tx); err == nil {
		binding = c.Value
	}
	choice := r.PostForm.Get("decision")
	if choice != "approve" && choice != "deny" {
		authorizeErrorPage(w, "the decision must be approve or deny")
		return
	}
	resp, err := h.service.Decide(authorization.Decision{
		TransactionID: tx,
		Binding:       binding,
		PersonID:      r.PostForm.Get("person"),
		Approve:       choice == "approve",
	})
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
