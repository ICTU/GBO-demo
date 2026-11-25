package rp

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

const maxBody = 1 << 20

// Public returns the wallet-facing handler: the request object by
// reference and the response_uri.
func Public(s *Service, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /request/{id}", func(w http.ResponseWriter, r *http.Request) {
		jwt, err := s.RequestObject(r.PathValue("id"))
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "invalid_request", "error_description": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/"+oid4vpRequestType)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(jwt))
	})
	mux.HandleFunc("POST /response/{id}", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if err := r.ParseForm(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		err := s.Respond(r.Context(), r.PathValue("id"), Response{
			Response: r.PostForm.Get("response"), Error: r.PostForm.Get("error"), ErrorDescription: r.PostForm.Get("error_description"),
		})
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, map[string]string{})
		case errors.Is(err, ErrUnknownSession), errors.Is(err, ErrRejected):
			log.Info("presentation refused", "error", err.Error())
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": err.Error()})
		default:
			log.Error("presentation failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		}
	})
	return mux
}

// oid4vpRequestType is the media type of a request object (RFC 9101).
const oid4vpRequestType = "oauth-authz-req+jwt"

// Internal returns the handler for the internal port, for the developer
// portal. Demo tool: it has no authentication of its own and must only be
// reachable from the portal backend.
func Internal(s *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /catalog", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.Catalog())
	})
	mux.HandleFunc("POST /presentations", func(w http.ResponseWriter, r *http.Request) {
		var req PresentationRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": "the body must be {relying_party, credential, claims}"})
			return
		}
		created, err := s.Create(req)
		switch {
		case err == nil:
			writeJSON(w, http.StatusCreated, created)
		case errors.Is(err, ErrUnknownParty):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_relying_party", "error_description": err.Error()})
		case errors.Is(err, ErrUnknownCredential):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_credential", "error_description": err.Error()})
		case errors.Is(err, ErrInvalidClaims):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_claims", "error_description": err.Error()})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		}
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"events": s.Events()})
	})
	return mux
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
