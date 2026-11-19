// Package httpapi holds the two driving adapters of the index, each on a
// listener of its own:
//
//   - the FSC listener, published as the FSC service consent-usage-index
//     behind cr-inway: POST /registrations, for the sources;
//   - the internal listener, for the consent register only:
//     GET /registrations?grondslag_type=consent&grondslag_ref={consent_id}.
//
// They are separate because an Inway forwards every path under a service's
// endpoint: on one listener, the lookup would be reachable over FSC by every
// source with a connection.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"consent-usage-index/internal/usage"
)

const maxBodyBytes = 16 << 10

// Pinger is what the health check asks of the store.
type Pinger interface {
	Ping(ctx context.Context) error
}

// registrationJSON is the wire form of a registration.
type registrationJSON struct {
	LogbookID     string `json:"logbook_id"`
	TraceID       string `json:"trace_id"`
	GrondslagType string `json:"grondslag_type"`
	GrondslagRef  string `json:"grondslag_ref"`
	RegisteredAt  string `json:"registered_at,omitempty"`
}

func toJSON(r usage.Registration) registrationJSON {
	return registrationJSON{
		LogbookID:     r.LogbookID,
		TraceID:       r.TraceID,
		GrondslagType: r.GrondslagType,
		GrondslagRef:  r.GrondslagRef,
		RegisteredAt:  r.RegisteredAt.UTC().Format(time.RFC3339Nano),
	}
}

// NewFSCHandler serves the registrations of the sources.
//
// It trusts the Fsc-Authorization header the Inway forwards: the Inway
// validated the access token and the connection contract, and the token's
// `sub` is the calling peer. That makes this listener safe only when nothing
// but the Inway can reach it.
func NewFSCHandler(index *usage.Index, store Pinger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/health", health(store))
	mux.HandleFunc("/registrations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var body registrationJSON
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil || body.RegisteredAt != "" {
			writeError(w, http.StatusBadRequest, "the body must be a registration: logbook_id, trace_id, grondslag_type, grondslag_ref")
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeError(w, http.StatusBadRequest, "the body must hold one registration")
			return
		}

		stored, created, err := index.Register(r.Context(), CallingPeer(r), usage.Request{
			LogbookID:     body.LogbookID,
			TraceID:       body.TraceID,
			GrondslagType: body.GrondslagType,
			GrondslagRef:  body.GrondslagRef,
		})
		switch {
		case errors.Is(err, usage.ErrNotOverFSC):
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		case errors.Is(err, usage.ErrForeignLogbook):
			writeError(w, http.StatusForbidden, err.Error())
			return
		case errors.Is(err, usage.ErrInvalid):
			writeError(w, http.StatusBadRequest, err.Error())
			return
		case err != nil:
			slog.Error("store registration", "err", err.Error())
			writeError(w, http.StatusInternalServerError, "the registration could not be stored")
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		slog.Info("registration", "logbook_id", stored.LogbookID, "created", created)
		writeJSON(w, status, toJSON(stored))
	})
	return mux
}

// NewInternalHandler serves the lookup to the consent register. The bearer
// token is what keeps it to the register: the listener sits on a network
// that other services share.
func NewInternalHandler(index *usage.Index, store Pinger, readToken string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/health", health(store))
	mux.HandleFunc("/registrations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !validBearer(r, readToken) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="consent-usage-index"`)
			writeError(w, http.StatusUnauthorized, "a valid read token is required")
			return
		}
		query := r.URL.Query()
		registrations, err := index.Lookup(r.Context(), query.Get("grondslag_type"), query.Get("grondslag_ref"))
		if errors.Is(err, usage.ErrInvalid) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			slog.Error("look up registrations", "err", err.Error())
			writeError(w, http.StatusInternalServerError, "the registrations could not be read")
			return
		}
		out := make([]registrationJSON, 0, len(registrations))
		for _, registration := range registrations {
			out = append(out, toJSON(registration))
		}
		writeJSON(w, http.StatusOK, map[string]any{"registrations": out})
	})
	return mux
}

func validBearer(r *http.Request, token string) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func health(store Pinger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := store.Ping(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "the store does not answer")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}

var peerIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)

// CallingPeer is the `sub` of the FSC access token the Inway forwards in
// Fsc-Authorization: the peer the token was issued to. Empty when there is
// none. The signature is not checked here; the Inway checked it.
func CallingPeer(r *http.Request) string {
	parts := strings.Split(r.Header.Get("Fsc-Authorization"), ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if json.Unmarshal(payload, &claims) != nil || !peerIDPattern.MatchString(claims.Sub) {
		return ""
	}
	return claims.Sub
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
