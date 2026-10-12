// Package httpapi is the HTTP interface of the authorization server: the
// RFC 8414 metadata, the JWKS, the authorization endpoint and the token
// endpoint. It translates requests to the authorization core and its answers
// to the responses of RFC 6749.
package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

func NewHandler(service *authorization.Service) http.Handler {
	h := &handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+authorization.MetadataPath, h.metadata)
	mux.HandleFunc("GET "+authorization.JWKSPath, h.jwks)
	mux.HandleFunc("GET "+authorization.AuthorizePath, h.authorize)
	mux.HandleFunc("POST "+authorization.TokenPath, h.token)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

type handler struct{ service *authorization.Service }

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

// authorizeErrorPage answers an authorization request that cannot be
// redirected safely: the client or its redirect_uri is unknown
// (RFC 6749 section 4.1.2.1).
func authorizeErrorPage(w http.ResponseWriter, description string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte("invalid_request: " + description + "\n"))
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
