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

// The read extension (extensie lezen), as its OpenAPI defines it:
// POST /data-processing-operations, a request body carrying at least one
// selector, and a response of metadata plus dataProcessingOperations.
//
// Two things differ from the write side, and both are the standard's own doing
// rather than ours: the read side names fields in camelCase where the core
// standard uses snake_case, and it represents times as RFC 3339 where the core
// uses milliseconds since the epoch. Each side follows its own specification.
//
// Errors are application/problem+json, per the extension.

// readRequest is DataProcessingOperationRequest. The three selectors the
// standard names are traceId and the two dpl.core attributes; startTime and
// endTime narrow a result rather than selecting one.
type readRequest struct {
	TraceID    string         `json:"traceId,omitempty"`
	StartTime  *time.Time     `json:"startTime,omitempty"`
	EndTime    *time.Time     `json:"endTime,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// dataProcessingOperation is one record as the read extension renders it.
type dataProcessingOperation struct {
	TraceID      string         `json:"traceId"`
	SpanID       string         `json:"spanId"`
	ParentSpanID string         `json:"parentSpanId,omitempty"`
	Status       string         `json:"status"`
	Name         string         `json:"name"`
	StartTime    string         `json:"startTime"`
	EndTime      string         `json:"endTime"`
	Resource     *readResource  `json:"resource,omitempty"`
	Attributes   map[string]any `json:"attributes,omitempty"`
}

type readResource struct {
	Attributes map[string]any `json:"attributes"`
}

type readMetadata struct {
	// LogbookID is the URI of this read API, which is what a record's
	// dpl.read.nextLogbookId points at — so a reader that followed one
	// arrives somewhere that identifies itself the same way.
	LogbookID        string `json:"logbookId"`
	OrganizationName string `json:"organizationName"`
}

type readResponse struct {
	Metadata                 readMetadata              `json:"metadata"`
	DataProcessingOperations []dataProcessingOperation `json:"dataProcessingOperations"`
}

// readStatus maps the stored status onto the read extension's enum. The two
// vocabularies differ — the record carries the OTel spelling, the read API
// asks for Ok/Error/Unset — so the translation is explicit rather than a cast.
func readStatus(status string) string {
	switch strings.ToUpper(status) {
	case ldv.StatusOK:
		return "Ok"
	case ldv.StatusError:
		return "Error"
	default:
		return "Unset"
	}
}

// listDataProcessingOperations serves the read extension.
func (h *Handler) listDataProcessingOperations(w http.ResponseWriter, r *http.Request) {
	if !authorized(r, h.readToken) {
		writeProblem(w, http.StatusUnauthorized, "unauthorized", "a valid bearer token is required to read this logboek")
		return
	}

	var request readRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeProblem(w, http.StatusBadRequest, "malformed_request", err.Error())
		return
	}

	query := ldv.Query{
		TraceID:              ldv.NormalizeTraceID(request.TraceID),
		ProcessingActivityID: fromReadAttributes(request.Attributes, ldv.AttrProcessingActivityID),
		DataSubjectID:        fromReadAttributes(request.Attributes, ldv.AttrDataSubjectID),
		DataSubjectIDType:    fromReadAttributes(request.Attributes, ldv.AttrDataSubjectIDType),
		StartTime:            request.StartTime,
		EndTime:              request.EndTime,
	}

	page, err := h.logbook.Read(r.Context(), query)
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
		operation := dataProcessingOperation{
			// The schema types traceId as a uuid where the record stores the
			// W3C hex form. Same sixteen bytes, two spellings.
			TraceID:      asUUID(stored.TraceID),
			SpanID:       stored.SpanID,
			ParentSpanID: stored.ParentSpanID,
			Status:       readStatus(stored.Status),
			Name:         stored.Name,
			StartTime:    stored.StartTime.UTC().Format(time.RFC3339Nano),
			EndTime:      stored.EndTime.UTC().Format(time.RFC3339Nano),
			Attributes:   toReadAttributes(stored.Attributes),
		}
		if len(stored.Resource) > 0 {
			operation.Resource = &readResource{Attributes: stored.Resource}
		}
		operations = append(operations, operation)
	}

	writeJSON(w, http.StatusOK, readResponse{
		Metadata: readMetadata{
			LogbookID:        h.logbookID,
			OrganizationName: h.logbook.Register().Verantwoordelijke,
		},
		DataProcessingOperations: operations,
	})
}
