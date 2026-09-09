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
	readToken  string
	// logbookID is the URI of this read API — the value a record's
	// dpl.read.nextLogbookId points at.
	logbookID string
	mux       *http.ServeMux
}

// NewHandler builds the routing tree. writeToken protects the write endpoint;
// empty means unauthenticated, which is refused by main — the token is the
// whole access model here.
//
// That access model is minimal on purpose: network-internal plus a shared
// bearer token. Who may write to and read from a Verantwoordelijke's logboek
// in reality is an open governance question (Q-08), and pretending to answer
// it with a demo authorisation scheme would be worse than saying so.
// readToken protects the read extension. It is separate from writeToken
// because writing and reading a logbook are different capabilities: every
// instrumented component must write, and almost nothing should read.
func NewHandler(logbook *ldv.Logbook, writeToken, readToken, logbookID string) *Handler {
	handler := &Handler{logbook: logbook, writeToken: writeToken, readToken: readToken, logbookID: logbookID, mux: http.NewServeMux()}
	handler.mux.HandleFunc("POST /logboek/records", handler.writeRecord)
	handler.mux.HandleFunc("POST /data-processing-operations", handler.listDataProcessingOperations)
	handler.mux.HandleFunc("GET /verwerkingsactiviteiten", handler.listActivities)
	handler.mux.HandleFunc("GET /verwerkingsactiviteiten/{id}/{version}", handler.getActivity)
	handler.mux.HandleFunc("GET /health", handler.health)
	return handler
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

func (h *Handler) writeRecord(w http.ResponseWriter, r *http.Request) {
	if !authorized(r, h.writeToken) {
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
		if errors.Is(err, ldv.ErrConflictingRecord) {
			// A different Dataverwerking under an identity that is taken.
			// Confirming it would tell the producer its record is logged
			// while the logbook holds something else.
			slog.Warn("rejected conflicting log record", "trace_id", record.TraceID, "span_id", record.SpanID)
			writeProblem(w, http.StatusConflict, "conflicting_record", err.Error())
			return
		}
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
		"verwerkingsactiviteiten": register.URIs(),
	})
}

func (h *Handler) getActivity(w http.ResponseWriter, r *http.Request) {
	register := h.logbook.Register()
	// Addressed by id and version, so the URI a record carries resolves when
	// someone actually dereferences it.
	activity, found := register.ResolveLocal(r.PathValue("id"), r.PathValue("version"))
	if !found {
		writeProblem(w, http.StatusNotFound, "unknown_verwerkingsactiviteit", "no such entry in this register")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"verantwoordelijke":     register.Verantwoordelijke,
		"disclaimer":            register.Disclaimer,
		"uri":                   activity.URI(register.BaseURI),
		"verwerkingsactiviteit": activity,
	})
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// authorized compares the bearer token in constant time.
//
// An unconfigured token authorizes nobody. Without this an empty server-side
// token matched a request that sent no Authorization header at all — both
// sides being the empty string — so a logbook started without
// LDV_READ_TOKEN served every record to anyone while logging that it would
// refuse every request. Absent credentials must never be a credential.
func authorized(r *http.Request, token string) bool {
	if token == "" {
		return false
	}
	presented := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
	return subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeProblem answers with a machine-readable error. The read extension
// specifies application/problem+json (RFC 9457), so that is what goes out —
// with the legacy `error`/`detail` pair kept alongside the standard fields,
// because the write side's producers already read them.
//
// The detail is echoed because every rejection here is a defect in the caller
// that someone has to fix.
func writeProblem(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":   "about:blank",
		"title":  http.StatusText(status),
		"status": status,
		"detail": detail,
		"error":  code,
	})
}
