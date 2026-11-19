package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"ldv-logboek/internal/ldv"
)

// NewFSCReadHandler serves the read extension for the consent register,
// behind the source's Inway (#686). It is a listener of its own, next to the
// one with the write endpoint and the token-protected read API:
//
//   - No bearer token. The Inway is the PEP: it checked the FSC contract and
//     asked the source's PDP about the request and its body (LDV_READ_FSC).
//     That makes this listener safe only when nothing but the Inway reaches
//     it.
//   - Exactly one traceId and nothing else, as the PDP rule also demands.
//   - A reduced answer: traceId, spanId, name, status, startTime, endTime
//     and the processing activity. No data subject, no resource, no other
//     attributes. `name` is required by the response schema of OpenAPI
//     1.1.0, so it stays although #686 does not list it.
func NewFSCReadHandler(logbook *ldv.Logbook, logbookID string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /data-processing-operations", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", APIVersion)
		var request struct {
			TraceID string `json:"traceId"`
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeProblem(w, http.StatusBadRequest, "malformed_request", "the body must be {\"traceId\": \"<32 hex>\"} and nothing else")
			return
		}
		if !traceIDPattern.MatchString(request.TraceID) {
			writeProblem(w, http.StatusBadRequest, "invalid_trace_id", "traceId must be 32 lowercase hex characters")
			return
		}
		page, err := logbook.Read(r.Context(), ldv.Query{TraceID: request.TraceID})
		if err != nil {
			if errors.Is(err, ldv.ErrNoSelector) {
				writeProblem(w, http.StatusBadRequest, "no_selector", err.Error())
				return
			}
			slog.Error("reading log records", "err", err.Error())
			writeProblem(w, http.StatusInternalServerError, "storage_failure", "the records could not be read")
			return
		}
		operations := make([]dataProcessingOperation, 0, len(page.Records))
		for _, stored := range page.Records {
			operations = append(operations, reduced(stored))
		}
		writeJSON(w, http.StatusOK, readResponse{
			Metadata: readMetadata{
				LogbookID:        logbookID,
				OrganizationName: logbook.Register().Verantwoordelijke,
			},
			DataProcessingOperations: operations,
		})
	})
	return mux
}

// reduced keeps what the consent register needs to show a use and nothing
// that names the citizen.
func reduced(stored ldv.Stored) dataProcessingOperation {
	operation := dataProcessingOperation{
		TraceID:   stored.TraceID,
		SpanID:    stored.SpanID,
		Name:      stored.Name,
		Status:    readStatus(stored.Status),
		StartTime: stored.StartTime.UTC().Format(time.RFC3339Nano),
		EndTime:   stored.EndTime.UTC().Format(time.RFC3339Nano),
	}
	if activity := strings.TrimSpace(stored.ProcessingActivityID()); activity != "" {
		operation.Attributes = toReadAttributes(map[string]any{ldv.AttrProcessingActivityID: activity})
	}
	return operation
}
