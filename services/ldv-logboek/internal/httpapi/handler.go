// Package httpapi is the logbook's driving adapter.
//
// Transport is plain HTTPS+JSON. LDV only RECOMMENDS OTLP and leaves the
// protocol free, and a JSON endpoint keeps the demo inspectable with curl —
// a deliberate simplification, recorded here so it is not mistaken for a
// reading of the standard.
package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"ldv-logboek/internal/ldv"
)

// maxBodyBytes bounds one record. Records are small; anything larger is a
// producer sending the payload it processed rather than a record about it.
const maxBodyBytes = 256 << 10

// Handler serves the write endpoint and the verwerkingsactiviteiten register
// the records point at.
type Handler struct {
	logbook    *ldv.Logbook
	writeToken string
	mux        *http.ServeMux
}

// NewHandler builds the routing tree. writeToken protects the write endpoint;
// empty means unauthenticated, which is refused by main — the token is the
// whole access model here.
//
// That access model is minimal on purpose: network-internal plus a shared
// bearer token. Who may write to and read from a Verantwoordelijke's logboek
// in reality is an open governance question (Q-08), and pretending to answer
// it with a demo authorisation scheme would be worse than saying so.
func NewHandler(logbook *ldv.Logbook, writeToken string) *Handler {
	handler := &Handler{logbook: logbook, writeToken: writeToken, mux: http.NewServeMux()}
	handler.mux.HandleFunc("POST /logboek/records", handler.writeRecord)
	handler.mux.HandleFunc("GET /verwerkingsactiviteiten", handler.listActivities)
	handler.mux.HandleFunc("GET /verwerkingsactiviteiten/{reference}", handler.getActivity)
	handler.mux.HandleFunc("GET /health", handler.health)
	return handler
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

func (h *Handler) writeRecord(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "a valid bearer token is required to write to this logboek")
		return
	}
	var record ldv.Record
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		writeProblem(w, http.StatusBadRequest, "malformed_record", err.Error())
		return
	}

	confirmation, err := h.logbook.Write(r.Context(), record)
	if err != nil {
		if errors.Is(err, ldv.ErrInvalidRecord) {
			// 422, not 400: the JSON parsed fine, the record is not lawful.
			// The producer is expected to treat this as a defect in itself,
			// not as a transient failure to retry.
			slog.Warn("rejected log record", "err", err.Error(), "trace_id", record.TraceID, "span_id", record.SpanID)
			writeProblem(w, http.StatusUnprocessableEntity, "invalid_record", err.Error())
			return
		}
		slog.Error("storing log record", "err", err.Error(), "trace_id", record.TraceID, "span_id", record.SpanID)
		writeProblem(w, http.StatusInternalServerError, "storage_failure", "the record could not be stored")
		return
	}

	// 200 on a replay, 201 on a new record — the producer can tell whether its
	// retry created anything.
	status := http.StatusCreated
	if confirmation.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, confirmation)
}

// listActivities serves the register index. Unauthenticated: the register is
// a public description of what the Verantwoordelijke does, not a log.
func (h *Handler) listActivities(w http.ResponseWriter, r *http.Request) {
	register := h.logbook.Register()
	writeJSON(w, http.StatusOK, map[string]any{
		"verantwoordelijke":       register.Verantwoordelijke,
		"disclaimer":              register.Disclaimer,
		"verwerkingsactiviteiten": register.References(),
	})
}

func (h *Handler) getActivity(w http.ResponseWriter, r *http.Request) {
	register := h.logbook.Register()
	activity, found := register.Resolve(r.PathValue("reference"))
	if !found {
		writeProblem(w, http.StatusNotFound, "unknown_verwerkingsactiviteit", "no such entry in this register")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"verantwoordelijke":     register.Verantwoordelijke,
		"disclaimer":            register.Disclaimer,
		"verwerkingsactiviteit": activity,
	})
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// authorized compares the bearer token in constant time.
func (h *Handler) authorized(r *http.Request) bool {
	presented := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
	return subtle.ConstantTimeCompare([]byte(presented), []byte(h.writeToken)) == 1
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeProblem answers with a small machine-readable error. The detail is
// echoed to the producer because every rejection here is a defect in the
// producer that someone has to fix.
func writeProblem(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, map[string]string{"error": code, "detail": detail})
}
