package consumerhttp

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"time"

	"gbo-demo/dienstverlener-backend/consentrequest"
)

// ConsentFlow serves the provider's side of a consent request: start and
// complete. The browser reaches it through its frontend's /api proxy, so the
// session cookie belongs to the frontend's origin. It is HttpOnly and
// SameSite=Lax, and Secure when the frontend is served over https. A granted
// consent gets a new session id.
//
// SameSite=Lax does not stop requests from other ports on the same host or
// from sibling subdomains: those are same-site. So every request that acts
// with the session cookie must be JSON (another origin cannot send that
// without a CORS preflight, which these routes do not pass) and, when the
// browser says where it comes from (Sec-Fetch-Site), come from the same
// origin.
//
// Demo shortcut: the demo serves every frontend on one host, so the browser
// sends each provider's session cookie to every frontend's /api proxy, and a
// co-hosted provider could replay it against another provider's backend. A
// real deployment gives each provider its own origin.
type ConsentFlow struct {
	Requester *consentrequest.Requester
	// CookieName must differ per provider: browsers do not tell cookies of
	// different ports on one host apart.
	CookieName string
	Secure     bool
	MaxAge     time.Duration
}

type startRequest struct {
	Service    string   `json:"service"`
	Scopes     []string `json:"scopes"`
	ValidUntil string   `json:"valid_until,omitempty"`
}

type completeRequest struct {
	Code  string `json:"code,omitempty"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// completeResponse never carries the consent token.
type completeResponse struct {
	Outcome   consentrequest.Outcome `json:"outcome"`
	ConsentID string                 `json:"consent_id,omitempty"`
	Scopes    []string               `json:"scopes,omitempty"`
}

// maxFlowBody bounds the body of a start or complete request.
const maxFlowBody = 64 << 10

type flowError struct {
	Error       string `json:"error"`
	Reason      string `json:"reason,omitempty"`
	Description string `json:"error_description,omitempty"`
}

// handleStart pushes the request and answers with the URL the browser goes
// to: the portal, with only the client_id and the request_uri.
func (f *ConsentFlow) handleStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if status, description := crossOrigin(r); status != 0 {
		writeJSON(w, status, flowError{Error: "invalid_request", Description: description})
		return
	}
	var body startRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxFlowBody)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, flowError{Error: "invalid_request", Description: "invalid request body"})
		return
	}
	log := loggerFromCtx(r.Context())
	started, err := f.Requester.Start(r.Context(), f.sessionID(r), consentrequest.Request{
		Service:    body.Service,
		Scopes:     body.Scopes,
		ValidUntil: body.ValidUntil,
	})
	var refusal *consentrequest.Refusal
	switch {
	case errors.Is(err, consentrequest.ErrInvalidRequest):
		writeJSON(w, http.StatusBadRequest, flowError{Error: "invalid_request", Description: err.Error()})
		return
	case errors.As(err, &refusal):
		log.Warn("consent request refused", "consent_service", body.Service, "error", refusal.Code, "reason", refusal.Reason)
		writeJSON(w, refusalStatus(refusal), flowError{Error: refusal.Code, Reason: refusal.Reason, Description: refusal.Description})
		return
	case err != nil:
		log.Error("consent request failed", "consent_service", body.Service, "err", err.Error())
		writeJSON(w, http.StatusBadGateway, flowError{Error: "consent_portal_unavailable"})
		return
	}
	log.Info("consent request pushed", "consent_service", body.Service)
	f.setCookie(w, started.SessionID)
	writeJSON(w, http.StatusOK, map[string]string{"redirect_to": started.RedirectTo})
}

// handleComplete takes what the browser brought back from the portal. A
// granted, denied or failed decision is a 200 with its outcome.
func (f *ConsentFlow) handleComplete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if status, description := crossOrigin(r); status != 0 {
		writeJSON(w, status, flowError{Error: "invalid_request", Description: description})
		return
	}
	var body completeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxFlowBody)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, flowError{Error: "invalid_request", Description: "invalid request body"})
		return
	}
	log := loggerFromCtx(r.Context())
	done, err := f.Requester.Complete(r.Context(), f.sessionID(r), consentrequest.Return{
		Code:  body.Code,
		State: body.State,
		Error: body.Error,
	})
	var refusal *consentrequest.Refusal
	switch {
	case errors.Is(err, consentrequest.ErrInvalidReturn):
		writeJSON(w, http.StatusBadRequest, flowError{Error: "invalid_request", Description: err.Error()})
		return
	case errors.Is(err, consentrequest.ErrNoPendingRequest):
		writeJSON(w, http.StatusBadRequest, flowError{Error: "no_pending_request", Description: err.Error()})
		return
	case errors.Is(err, consentrequest.ErrStateMismatch):
		log.Warn("consent return with another state")
		writeJSON(w, http.StatusBadRequest, flowError{Error: "state_mismatch", Description: err.Error()})
		return
	case errors.As(err, &refusal):
		log.Warn("code exchange refused", "error", refusal.Code, "description", refusal.Description)
		writeJSON(w, http.StatusBadGateway, flowError{Error: "code_exchange_failed", Reason: refusal.Code})
		return
	case err != nil:
		log.Error("code exchange failed", "err", err.Error())
		writeJSON(w, http.StatusBadGateway, flowError{Error: "code_exchange_failed"})
		return
	}
	log.Info("consent request completed", "outcome", string(done.Outcome), "consent_id", done.ConsentID, "error", done.Error)
	if done.SessionID != "" {
		f.setCookie(w, done.SessionID)
	}
	writeJSON(w, http.StatusOK, completeResponse{Outcome: done.Outcome, ConsentID: done.ConsentID, Scopes: done.Scopes})
}

// refusalStatus is 400 for a refusal the caller's request caused, such as an
// unknown data set or an unregistered service, 503 when the portal could not
// check the request, and 502 when the provider's own registration or
// configuration is at fault (an inactive or unknown client, an unregistered
// return location): the browser cannot fix those.
func refusalStatus(refusal *consentrequest.Refusal) int {
	switch {
	case refusal.Code == "temporarily_unavailable":
		return http.StatusServiceUnavailable
	case refusal.Code == "unauthorized_client", refusal.Code == "invalid_client",
		refusal.Reason == "RETURN_URL_NOT_REGISTERED":
		return http.StatusBadGateway
	default:
		return http.StatusBadRequest
	}
}

// crossOrigin refuses a request that a page on another origin could have
// sent with the session cookie and no CORS preflight: a body that is not
// application/json (415), or a Sec-Fetch-Site other than same-origin (403).
// A request without Sec-Fetch-Site (an older browser, curl) is let through on
// the content type alone. It returns 0 for a request that may use the
// session.
func crossOrigin(r *http.Request) (int, string) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return http.StatusUnsupportedMediaType, "the body must be application/json"
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return http.StatusForbidden, "the session is only used from the same origin"
	}
	return 0, ""
}

// hasSession reports whether the request carries this provider's session
// cookie.
func (f *ConsentFlow) hasSession(r *http.Request) bool {
	_, err := r.Cookie(f.CookieName)
	return err == nil
}

// consentToken is the consent token of the request's session, if any.
func (f *ConsentFlow) consentToken(r *http.Request) string {
	token, _ := f.Requester.ConsentToken(r.Context(), f.sessionID(r))
	return token
}

func (f *ConsentFlow) sessionID(r *http.Request) string {
	c, err := r.Cookie(f.CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func (f *ConsentFlow) setCookie(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{
		Name:     f.CookieName,
		Value:    id,
		Path:     "/api",
		MaxAge:   int(f.MaxAge.Seconds()),
		HttpOnly: true,
		Secure:   f.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}
