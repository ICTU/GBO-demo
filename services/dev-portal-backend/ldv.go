// Package main — Logboek Dataverwerkingen access for the dev-portal.
//
// LDV puts each Verantwoordelijke's records in that Verantwoordelijke's own
// logbook, and only trace metadata crosses a boundary. So a chain view cannot
// be one query: it is a query per logbook, joined on the trace id — which is
// the Fsc-Transaction-Id, the same value the ADL decision and the FSC txlog
// carry for that request.
//
// That is the whole "one trace id, three standards" claim, made checkable:
// this endpoint returns the LDV half, and the portal puts it next to the
// decision (/explain) and the transport records (/fsc/txlog) it already has.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ldvLogbook is one Verantwoordelijke's logbook the portal may query.
type ldvLogbook struct {
	// ID is the value a record's dpl.read.nextLogbookId points at, so a
	// cross-organisation pointer resolves to something in this list.
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"-"`
}

// ldvRecord is the stored record as the logbook returns it. The portal only
// renders it, so the attributes stay an open map.
type ldvRecord struct {
	TraceID      string            `json:"trace_id"`
	SpanID       string            `json:"span_id"`
	ParentSpanID string            `json:"parent_span_id,omitempty"`
	Name         string            `json:"name"`
	Status       string            `json:"status"`
	StartTime    string            `json:"start_time"`
	EndTime      string            `json:"end_time"`
	ReceivedAt   string            `json:"received_at"`
	Resource     map[string]string `json:"resource,omitempty"`
	Attributes   map[string]any    `json:"attributes"`
}

// ldvLogbookResult wraps one logbook's answer plus an optional error, so the
// UI can show that a logbook was unreachable without breaking the lookup —
// and so an empty logbook is visibly different from a broken one.
type ldvLogbookResult struct {
	Logbook   ldvLogbook  `json:"logbook"`
	Records   []ldvRecord `json:"records"`
	Truncated bool        `json:"truncated"`
	Error     string      `json:"error,omitempty"`
}

type ldvChainResponse struct {
	TraceID  string             `json:"trace_id"`
	Logbooks []ldvLogbookResult `json:"logbooks"`
}

// parseLdvLogbooks reads the "id=name=url,id=name=url" configuration.
func parseLdvLogbooks(raw string) []ldvLogbook {
	logbooks := []ldvLogbook{}
	for _, entry := range strings.Split(raw, ",") {
		parts := strings.SplitN(strings.TrimSpace(entry), "=", 3)
		if len(parts) != 3 {
			continue
		}
		id, name, address := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
		if id == "" || address == "" {
			continue
		}
		if name == "" {
			name = id
		}
		logbooks = append(logbooks, ldvLogbook{ID: id, Name: name, URL: strings.TrimRight(address, "/")})
	}
	return logbooks
}

// handleLdvChain: GET /api/dev/ldv/<traceId>
//
// Queries every configured logbook in parallel. Returns an empty list when
// none are configured, the way the txlog endpoint does when fsc-infra is not
// running.
func handleLdvChain(cfg config) http.HandlerFunc {
	client := &http.Client{Timeout: 5 * time.Second}

	fetch := func(logbook ldvLogbook, traceID string) ldvLogbookResult {
		result := ldvLogbookResult{Logbook: logbook, Records: []ldvRecord{}}
		endpoint := logbook.URL + "/logboek/records?traceID=" + url.QueryEscape(traceID)
		request, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		if cfg.LdvReadToken != "" {
			request.Header.Set("Authorization", "Bearer "+cfg.LdvReadToken)
		}
		response, err := client.Do(request)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
			result.Error = fmt.Sprintf("status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
			return result
		}
		var payload struct {
			Records   []ldvRecord `json:"records"`
			Truncated bool        `json:"truncated"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload); err != nil {
			result.Error = err.Error()
			return result
		}
		if payload.Records != nil {
			result.Records = payload.Records
		}
		result.Truncated = payload.Truncated
		return result
	}

	return func(w http.ResponseWriter, r *http.Request) {
		traceID := strings.TrimPrefix(r.URL.Path, "/ldv/")
		if traceID == "" || strings.Contains(traceID, "/") {
			http.Error(w, "usage: /ldv/<traceId>", http.StatusBadRequest)
			return
		}
		// The portal holds the Fsc-Transaction-Id; a logbook stores the OTel
		// form of it. One value, two spellings.
		normalized := strings.ToLower(strings.ReplaceAll(traceID, "-", ""))

		results := make([]ldvLogbookResult, len(cfg.LdvLogbooks))
		var wg sync.WaitGroup
		for i, logbook := range cfg.LdvLogbooks {
			wg.Add(1)
			go func(i int, logbook ldvLogbook) {
				defer wg.Done()
				results[i] = fetch(logbook, normalized)
			}(i, logbook)
		}
		wg.Wait()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ldvChainResponse{TraceID: normalized, Logbooks: results})
	}
}
