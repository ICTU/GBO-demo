// Package main — Logboek Dataverwerkingen access for the dev-portal.
//
// LDV keeps each Verantwoordelijke's records in that Verantwoordelijke's own
// logbook, and only trace metadata crosses a boundary. The read extension
// says how a reader puts a chain back together: start at the logbook of the
// application that started the processing, query it on the trace id, and
// follow dpl.read.nextLogbookId to the next logbook. This endpoint does that,
// and says for every logbook how it got there.
//
// A logbook can belong to a request without any record pointing at it — the
// consent register's, whose status is checked by the PDP, which writes a
// decision log rather than an LDV record. Those are queried as well once the
// pointers run out, and marked as reached without one: the view shows the gap
// rather than papering over it.
//
// The trace id is the request's own, the one on traceparent. The FSC
// transaction log and the PDP decision key on the Fsc-Transaction-Id instead;
// for a request that crosses FSC once the two are the same value.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ldvLogbook is one Verantwoordelijke's logbook the portal may query.
type ldvLogbook struct {
	// ID is the URI of the logbook's read API — the value a record's
	// dpl.read.nextLogbookId holds — so a pointer resolves to an entry here.
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"-"`
	// Start marks the logbook of an application that starts a processing:
	// where a reader begins.
	Start bool `json:"-"`
}

// How the chain view reached a logbook.
const (
	reachedAsStart   = "start"
	reachedByPointer = "pointer"
	reachedWithout   = "none"
)

// ldvRecord is one dataProcessingOperation as the read extension returns it:
// camelCase names and RFC 3339 times, unlike the write side. The portal only
// renders it, so the attributes stay an open map.
type ldvRecord struct {
	TraceID      string         `json:"traceId"`
	SpanID       string         `json:"spanId"`
	ParentSpanID string         `json:"parentSpanId,omitempty"`
	Name         string         `json:"name"`
	Status       string         `json:"status"`
	StartTime    string         `json:"startTime"`
	EndTime      string         `json:"endTime"`
	Resource     *ldvResource   `json:"resource,omitempty"`
	Attributes   map[string]any `json:"attributes"`
}

// ldvResource mirrors the nesting the standard defines: an object with an
// attributes field, not a map of attributes directly.
type ldvResource struct {
	Attributes map[string]any `json:"attributes"`
}

// ldvLogbookResult wraps one logbook's answer plus an optional error, so the
// UI can show that a logbook was unreachable without breaking the lookup —
// and so an empty logbook is visibly different from a broken one.
type ldvLogbookResult struct {
	Logbook ldvLogbook  `json:"logbook"`
	Records []ldvRecord `json:"records"`
	Error   string      `json:"error,omitempty"`
	// ReachedVia is how the reader got here: a start logbook, a
	// nextLogbookId in another logbook's records, or neither.
	ReachedVia string `json:"reached_via"`
	// From is the logbook whose record pointed here.
	From string `json:"from,omitempty"`
}

type ldvChainResponse struct {
	TraceID  string             `json:"trace_id"`
	Logbooks []ldvLogbookResult `json:"logbooks"`
}

// parseLdvLogbooks reads the "id=name=url[=start],…" configuration. The id is
// the logbook's read-API URI, so a nextLogbookId resolves to an address;
// "start" marks where a reader begins.
func parseLdvLogbooks(raw string) []ldvLogbook {
	logbooks := []ldvLogbook{}
	for _, entry := range strings.Split(raw, ",") {
		parts := strings.SplitN(strings.TrimSpace(entry), "=", 4)
		if len(parts) < 3 {
			continue
		}
		id, name, address := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
		if id == "" || address == "" {
			continue
		}
		if name == "" {
			name = id
		}
		logbooks = append(logbooks, ldvLogbook{
			ID:    id,
			Name:  name,
			URL:   strings.TrimRight(address, "/"),
			Start: len(parts) == 4 && strings.TrimSpace(parts[3]) == "start",
		})
	}
	return logbooks
}

// nextLogbookIDs collects the pointers in a logbook's records, each once. The
// read extension nests the attribute: dpl.read.nextLogbookId.
func nextLogbookIDs(records []ldvRecord) []string {
	seen := map[string]bool{}
	var ids []string
	for _, record := range records {
		dpl, _ := record.Attributes["dpl"].(map[string]any)
		read, _ := dpl["read"].(map[string]any)
		id, _ := read["nextLogbookId"].(string)
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// handleLdvChain: GET /api/dev/ldv/<traceId>
//
// Returns an empty list when no logbooks are configured, the way the txlog
// endpoint does when fsc-infra is not running.
func handleLdvChain(cfg config) http.HandlerFunc {
	client := &http.Client{Timeout: 5 * time.Second}

	fetch := func(logbook ldvLogbook, traceID string) ldvLogbookResult {
		result := ldvLogbookResult{Logbook: logbook, Records: []ldvRecord{}}
		// The read extension is POST with a body, not a query string.
		body, err := json.Marshal(map[string]any{"traceId": traceID})
		if err != nil {
			result.Error = err.Error()
			return result
		}
		request, err := http.NewRequest(http.MethodPost, logbook.URL+"/data-processing-operations", bytes.NewReader(body))
		if err != nil {
			result.Error = err.Error()
			return result
		}
		request.Header.Set("Content-Type", "application/json")
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
			Metadata struct {
				LogbookID        string `json:"logbookId"`
				OrganizationName string `json:"organizationName"`
			} `json:"metadata"`
			Operations []ldvRecord `json:"dataProcessingOperations"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload); err != nil {
			result.Error = err.Error()
			return result
		}
		if payload.Operations != nil {
			result.Records = payload.Operations
		}
		// The logbook names itself; prefer that over our configured label, so
		// the view shows the logbook's own identity rather than ours for it.
		if payload.Metadata.OrganizationName != "" {
			result.Logbook.Name = payload.Metadata.OrganizationName
		}
		if payload.Metadata.LogbookID != "" {
			result.Logbook.ID = payload.Metadata.LogbookID
		}
		return result
	}

	return func(w http.ResponseWriter, r *http.Request) {
		traceID := strings.TrimPrefix(r.URL.Path, "/ldv/")
		if traceID == "" || strings.Contains(traceID, "/") {
			http.Error(w, "usage: /ldv/<traceId>", http.StatusBadRequest)
			return
		}
		// A trace id that doubles as a transaction id may arrive hyphenated;
		// a logbook stores the W3C spelling. One value, two spellings.
		normalized := strings.ToLower(strings.ReplaceAll(traceID, "-", ""))

		byID := make(map[string]ldvLogbook, len(cfg.LdvLogbooks))
		for _, logbook := range cfg.LdvLogbooks {
			byID[logbook.ID] = logbook
		}

		type hop struct {
			logbook   ldvLogbook
			via, from string
		}
		visited := map[string]bool{}
		var queue []hop
		for _, logbook := range cfg.LdvLogbooks {
			if logbook.Start {
				visited[logbook.ID] = true
				queue = append(queue, hop{logbook: logbook, via: reachedAsStart})
			}
		}

		results := []ldvLogbookResult{}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			result := fetch(current.logbook, normalized)
			result.ReachedVia, result.From = current.via, current.from
			results = append(results, result)
			for _, next := range nextLogbookIDs(result.Records) {
				if visited[next] {
					continue
				}
				visited[next] = true
				logbook, known := byID[next]
				if !known {
					// The chain continues somewhere this portal has no address
					// for. Shown, so a reader knows that it does.
					results = append(results, ldvLogbookResult{
						Logbook:    ldvLogbook{ID: next, Name: next},
						Records:    []ldvRecord{},
						Error:      "no read API configured for this logbook",
						ReachedVia: reachedByPointer,
						From:       current.logbook.ID,
					})
					continue
				}
				queue = append(queue, hop{logbook: logbook, via: reachedByPointer, from: current.logbook.ID})
			}
		}

		// The logbooks no pointer led to. Part of the request or not, a reader
		// following the standard would only find them by knowing to ask.
		for _, logbook := range cfg.LdvLogbooks {
			if visited[logbook.ID] {
				continue
			}
			result := fetch(logbook, normalized)
			result.ReachedVia = reachedWithout
			results = append(results, result)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ldvChainResponse{TraceID: normalized, Logbooks: results})
	}
}
