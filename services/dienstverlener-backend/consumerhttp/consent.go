package consumerhttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"gbo-demo/dienstverlener-backend/consentrequest"
)

// ConsentFlow serves the provider's side of a consent request: start and
// complete. The browser reaches it through its frontend's /api proxy, so the
// session cookie belongs to the frontend's origin. It is HttpOnly and
// SameSite=Lax, and Secure when the frontend is served over https.
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

type flowError struct {
	Error       string `json:"error"`
	Reason      string `json:"reason,omitempty"`
	Description string `json:"error_description,omitempty"`
}

// handleStart pushes the request and answers with the URL the browser goes
// to: the portal, with only the client_id and the request_uri.
func (f *ConsentFlow) handleStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var body startRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
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
		status := http.StatusBadRequest
		if refusal.Code == "temporarily_unavailable" {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, flowError{Error: refusal.Code, Reason: refusal.Reason, Description: refusal.Description})
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
	var body completeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
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
	writeJSON(w, http.StatusOK, completeResponse{Outcome: done.Outcome, ConsentID: done.ConsentID, Scopes: done.Scopes})
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
