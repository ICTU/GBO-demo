// Package fschttp is the driving adapter for service providers: the
// endpoints the portal publishes as an FSC service. It runs on a listener
// of its own, because an Inway forwards every path under a service's
// endpoint; on the citizen's listener the portal endpoints would have been
// reachable over FSC too.
//
// The provider is the peer in the Fsc-Authorization token the Inway
// forwards, never a field of the request. This listener trusts that header
// without verifying the token, so it must be reachable only through the
// Inway. Demo shortcut: it listens on all interfaces of a container that is
// on both the fsc-infra and the backend compose network, so anything on
// those networks, the service provider backends included, can call it
// directly and claim any peer. A network of its own is not enough while one
// process listens on every interface: a real deployment binds this listener
// to an address only the Inway can reach, runs it as a separate process, or
// verifies the token's signature (or authenticates the Inway with mTLS).
package fschttp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"gbo-demo/consent-portal-backend/consent"
	ldv "gbo-demo/ldv-client"
)

// maxFormBytes bounds a pushed request's body.
const maxFormBytes = 64 << 10

// fscPeerID is the 20-character alphanumeric FSC Peer ID.
var fscPeerID = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)

// NewMux returns the routes of the FSC listener.
func NewMux(portal *consent.Portal) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /par", handlePAR(portal))
	return mux
}

// callingPeer is the peer the FSC access token was issued to: the `sub` of
// the token the Inway forwarded. Empty when the request did not come
// through FSC.
func callingPeer(r *http.Request) consent.PeerID {
	sub, _ := ldv.Claims(r.Header.Get("Fsc-Authorization"))["sub"].(string)
	if !fscPeerID.MatchString(sub) {
		return ""
	}
	return consent.PeerID(sub)
}

// pushedResponse is the success response of RFC 9126 §2.2.
type pushedResponse struct {
	RequestURI string `json:"request_uri"`
	ExpiresIn  int    `json:"expires_in"`
}

// errorResponse is an OAuth error response (RFC 6749 §5.2) with the reason
// code of the refusal.
type errorResponse struct {
	Error       string `json:"error"`
	Description string `json:"error_description,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// handlePAR serves POST /par, the pushed authorization request endpoint
// (RFC 9126). Parameters are read from the form-encoded body only.
func handlePAR(portal *consent.Portal) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provider := callingPeer(r)
		if provider == "" {
			writeJSON(w, http.StatusUnauthorized, errorResponse{
				Error:       "invalid_client",
				Description: "pushed authorization requests are only accepted over FSC",
			})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		if err := r.ParseForm(); err != nil || !isForm(r) {
			status, description := http.StatusBadRequest, "the request body must be application/x-www-form-urlencoded"
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				// RFC 9126 §2.3 names 413 for a request beyond the server's bound.
				status, description = http.StatusRequestEntityTooLarge, "the request body is larger than 64 KiB"
			}
			writeJSON(w, status, errorResponse{
				Error:       "invalid_request",
				Description: description,
				Reason:      string(consent.ReasonInitiationIncomplete),
			})
			return
		}

		form := r.PostForm
		if problem := unusableForm(form); problem != "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Error:       "invalid_request",
				Description: problem,
				Reason:      string(consent.ReasonInitiationIncomplete),
			})
			return
		}
		ref, err := portal.PushRequest(r.Context(), provider, consent.PushedRequest{
			ResponseType:        form.Get("response_type"),
			ClientID:            form.Get("client_id"),
			Service:             form.Get("service"),
			Scopes:              strings.Fields(form.Get("scope")),
			RedirectURI:         form.Get("redirect_uri"),
			State:               form.Get("state"),
			CodeChallenge:       form.Get("code_challenge"),
			CodeChallengeMethod: form.Get("code_challenge_method"),
			ValidUntil:          form.Get("valid_until"),
			ClientName:          form.Get("client_name"),
			Purpose:             form.Get("purpose"),
		})
		if err != nil {
			writeRefusal(r, w, provider, err)
			return
		}
		slog.InfoContext(r.Context(), "pushed request accepted", "provider", string(provider))
		writeJSON(w, http.StatusCreated, pushedResponse{
			RequestURI: ref.RequestURI,
			ExpiresIn:  int(math.Ceil(ref.ExpiresIn.Seconds())),
		})
	}
}

// unusableForm names what makes a form unusable as a pushed request, or "":
// a parameter sent more than once (RFC 6749 §3.1) or a request_uri, which a
// pushed request must not carry (RFC 9126 §2.1).
func unusableForm(form url.Values) string {
	if _, found := form["request_uri"]; found {
		return "a pushed request must not contain request_uri"
	}
	for _, values := range form {
		if len(values) > 1 {
			return "a parameter is sent more than once"
		}
	}
	return ""
}

func isForm(r *http.Request) bool {
	mediaType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), "application/x-www-form-urlencoded")
}

// writeRefusal maps a refusal to its OAuth error: an unusable or mismatched
// request is invalid_request, an inactive provider unauthorized_client, an
// unknown data set invalid_scope, and an unreadable admission register
// temporarily_unavailable (the request is refused, not let through).
func writeRefusal(r *http.Request, w http.ResponseWriter, provider consent.PeerID, err error) {
	var refusal *consent.Refusal
	if !errors.As(err, &refusal) {
		slog.ErrorContext(r.Context(), "pushed request failed", "provider", string(provider), "err", err.Error())
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "server_error"})
		return
	}
	status, code := http.StatusBadRequest, "invalid_request"
	switch refusal.Code {
	case consent.ReasonPartyNotActive:
		code = "unauthorized_client"
	case consent.ReasonUnknownScope:
		code = "invalid_scope"
	case consent.ReasonAdmissionRegisterUnavailable:
		status, code = http.StatusServiceUnavailable, "temporarily_unavailable"
	}
	slog.WarnContext(r.Context(), "pushed request refused", "provider", string(provider), "reason", string(refusal.Code), "err", refusal.Error())
	writeJSON(w, status, errorResponse{Error: code, Description: refusal.Detail, Reason: string(refusal.Code)})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("json encode failed", "err", err.Error())
	}
}
