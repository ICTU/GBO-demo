package verifier

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// maxBody bounds what a client or wallet may post.
const maxBody = 1 << 20

// PublicHandler is the wallet API: the session status, the request object
// and the response endpoint. The status is public, as in
// verification_server: the session token is the secret of the session.
func PublicHandler(s *Service, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /disclosure/sessions/{token}", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Status(r.PathValue("token"), r.URL.Query().Get("session_type"))
		if err != nil {
			writeError(w, log, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /disclosure/sessions/{token}/request", func(w http.ResponseWriter, r *http.Request) {
		request, err := s.Request(r.PathValue("token"), r.URL.Query().Get("session_type"))
		if err != nil {
			writeError(w, log, err)
			return
		}
		w.Header().Set("Content-Type", "application/oauth-authz-req+jwt")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, request)
	})
	mux.HandleFunc("POST /disclosure/sessions/{token}/response", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if err := r.ParseForm(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		form := ResponseForm{Response: r.PostForm.Get("response"), Error: r.PostForm.Get("error"), State: r.PostForm.Get("state")}
		redirect, err := s.Respond(r.Context(), r.PathValue("token"), form)
		w.Header().Set("Cache-Control", "no-store")
		var e *Error
		switch {
		case err == nil && form.Error != "":
			log.Info("disclosure ended by the wallet", "error", form.Error)
			writeJSON(w, http.StatusOK, struct{}{})
		case err == nil && redirect != "":
			log.Info("disclosure done", "return", true)
			writeJSON(w, http.StatusOK, map[string]string{"redirect_uri": redirect})
		case err == nil:
			log.Info("disclosure done", "return", false)
			writeJSON(w, http.StatusOK, struct{}{})
		case errors.As(err, &e):
			// The reason goes to the log only, never to the relying party.
			log.Info("disclosure refused", "type", e.Type, "detail", e.Detail)
			writeJSON(w, e.Status, map[string]string{"error": "invalid_request"})
		default:
			log.Error("disclosure failed", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		}
	})
	return mux
}

// InternalHandler is the relying parties' API behind the API key: starting
// a session and fetching the disclosed attributes.
func InternalHandler(s *Service, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /disclosure/sessions", func(w http.ResponseWriter, r *http.Request) {
		var req StartRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
		if err := dec.Decode(&req); err != nil {
			writeError(w, log, refuse(http.StatusBadRequest, "invalid_request", "the body is not a session request"))
			return
		}
		token, err := s.Start(req)
		if err != nil {
			writeError(w, log, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"session_token": token})
	})
	mux.HandleFunc("GET /disclosure/sessions/{token}/disclosed_attributes", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Disclosed(r.PathValue("token"), r.URL.Query().Get("nonce"))
		if err != nil {
			writeError(w, log, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, out)
	})
	key := []byte(s.cfg.APIKey)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), key) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"type": "unauthorized", "status": http.StatusUnauthorized})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// writeError answers in verification_server's error shape: the type and
// the status.
func writeError(w http.ResponseWriter, log *slog.Logger, err error) {
	var e *Error
	if !errors.As(err, &e) {
		log.Error("request failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"type": "server_error", "status": http.StatusInternalServerError})
		return
	}
	writeJSON(w, e.Status, map[string]any{"type": e.Type, "status": e.Status, "detail": e.Detail})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
