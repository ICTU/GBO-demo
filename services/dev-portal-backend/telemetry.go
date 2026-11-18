package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── Telemetry backends ──────────────────────────────────────────────────
//
// The portal reads two things from the observability stack: a whole trace
// (the span inspector and the FSC transaction lookup), and the embedded OPA's
// console decision log (the engine detail next to the ADL). Locally, under
// the Aspire AppHost, both come from the Aspire dashboard's telemetry API.
// The simulation deployment keeps Jaeger and Loki, because the dashboard is
// no durable store. TELEMETRY_API_URL picks the dashboard; without it the
// portal uses JAEGER_QUERY_URL and LOKI_URL as before.

// dashboardGet calls the Aspire dashboard's telemetry API with its API key.
func dashboardGet(ctx context.Context, cfg config, path string, query url.Values) ([]byte, error) {
	u := strings.TrimSuffix(cfg.TelemetryAPIURL, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if cfg.TelemetryAPIKey != "" {
		req.Header.Set("x-api-key", cfg.TelemetryAPIKey)
	}
	resp, err := upstreamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telemetry API unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("telemetry API read: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telemetry API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

var errNotFound = fmt.Errorf("not found")

// ── Traces ──────────────────────────────────────────────────────────────

// jaegerTrace is the shape the portal's trace views read, Jaeger's query
// API response, whichever backend served the trace.
type jaegerTrace struct {
	TraceID   string                   `json:"traceID"`
	Spans     []jaegerSpan             `json:"spans"`
	Processes map[string]jaegerProcess `json:"processes"`
}

type jaegerSpan struct {
	TraceID       string            `json:"traceID"`
	SpanID        string            `json:"spanID"`
	OperationName string            `json:"operationName"`
	StartTime     int64             `json:"startTime"` // microseconds
	Duration      int64             `json:"duration"`  // microseconds
	ProcessID     string            `json:"processID"`
	References    []jaegerReference `json:"references,omitempty"`
	Tags          []jaegerTag       `json:"tags,omitempty"`
}

type jaegerReference struct {
	RefType string `json:"refType"`
	TraceID string `json:"traceID"`
	SpanID  string `json:"spanID"`
}

type jaegerTag struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
	Type  string `json:"type"`
}

type jaegerProcess struct {
	ServiceName string `json:"serviceName"`
}

// dashboardTraceResponse is the dashboard API's envelope around OTLP
// ResourceSpans.
type dashboardTraceResponse struct {
	Data otlpExportRequest `json:"data"`
}

// jaegerFromOTLP converts OTLP ResourceSpans into the Jaeger shape: one
// process per service, span times in microseconds, the parent as a CHILD_OF
// reference, attributes as tags and an error status as error=true.
func jaegerFromOTLP(traceID string, req otlpExportRequest) jaegerTrace {
	trace := jaegerTrace{TraceID: traceID, Spans: []jaegerSpan{}, Processes: map[string]jaegerProcess{}}
	processIDs := map[string]string{}
	for _, rs := range req.ResourceSpans {
		service := stringAttr(rs.Resource.Attributes, "service.name")
		pid, ok := processIDs[service]
		if !ok {
			pid = "p" + strconv.Itoa(len(processIDs)+1)
			processIDs[service] = pid
			trace.Processes[pid] = jaegerProcess{ServiceName: service}
		}
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				id := normaliseHex(sp.TraceID)
				if id != traceID {
					continue
				}
				start := parseInt(sp.StartTimeUnixNano)
				end := parseInt(sp.EndTimeUnixNano)
				span := jaegerSpan{
					TraceID:       id,
					SpanID:        normaliseHex(sp.SpanID),
					OperationName: sp.Name,
					StartTime:     start / 1000,
					Duration:      (end - start) / 1000,
					ProcessID:     pid,
				}
				if parent := normaliseHex(sp.ParentSpanID); parent != "" {
					span.References = []jaegerReference{{RefType: "CHILD_OF", TraceID: id, SpanID: parent}}
				}
				attrs := spanAttrs(sp.Attributes)
				keys := make([]string, 0, len(attrs))
				for k := range attrs {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					span.Tags = append(span.Tags, jaegerTag{Key: k, Value: attrs[k], Type: "string"})
				}
				// OTLP status code 2 is ERROR.
				if sp.Status.Code == 2 {
					span.Tags = append(span.Tags,
						jaegerTag{Key: "error", Value: true, Type: "bool"},
						jaegerTag{Key: "otel.status_code", Value: "ERROR", Type: "string"})
				}
				trace.Spans = append(trace.Spans, span)
			}
		}
	}
	sort.Slice(trace.Spans, func(i, j int) bool { return trace.Spans[i].StartTime < trace.Spans[j].StartTime })
	return trace
}

// handleTrace serves one trace in the Jaeger shape: {"data": [trace]}, or an
// empty data list when no backend knows the trace (yet).
func handleTrace(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		traceID := strings.ToLower(strings.TrimPrefix(r.URL.Path, "/traces/"))
		if !isHex(traceID) || len(traceID) > 32 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "trace id must be hex"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
		defer cancel()

		if cfg.TelemetryAPIURL != "" {
			body, err := dashboardGet(ctx, cfg, "/api/telemetry/traces/"+traceID, nil)
			if err == errNotFound {
				writeJSON(w, http.StatusOK, map[string]any{"data": []jaegerTrace{}})
				return
			}
			if err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
				return
			}
			var resp dashboardTraceResponse
			if err := json.Unmarshal(body, &resp); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "telemetry API decode: " + err.Error()})
				return
			}
			trace := jaegerFromOTLP(traceID, resp.Data)
			if len(trace.Spans) == 0 {
				writeJSON(w, http.StatusOK, map[string]any{"data": []jaegerTrace{}})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": []jaegerTrace{trace}})
			return
		}

		// Jaeger answers in this shape already.
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
			strings.TrimSuffix(cfg.JaegerQueryURL, "/")+"/api/traces/"+traceID, nil)
		resp, err := upstreamClient.Do(req)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "jaeger unreachable: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		if resp.StatusCode == http.StatusNotFound {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 32<<20))
	}
}

func isHex(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return s != ""
}

// ── Decision log ────────────────────────────────────────────────────────

type dashboardLogsResponse struct {
	Data struct {
		ResourceLogs []struct {
			ScopeLogs []struct {
				LogRecords []struct {
					TimeUnixNano json.Number `json:"timeUnixNano"`
					Body         struct {
						StringValue *string `json:"stringValue,omitempty"`
					} `json:"body"`
				} `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	} `json:"data"`
}

// dashboardDecisionsForTrace reads the PDP's console decision log from the
// dashboard's OTLP logs. The PDP image's logtee exports every console line
// as a log record of the openftv-pdp resource.
func dashboardDecisionsForTrace(ctx context.Context, cfg config, txID string, since time.Time) ([]map[string]any, error) {
	body, err := dashboardGet(ctx, cfg, "/api/telemetry/logs", url.Values{"resource": {cfg.PdpResourceName}})
	if err == errNotFound {
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var resp dashboardLogsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("telemetry API decode: %w", err)
	}
	var lines []stampedLine
	for _, rl := range resp.Data.ResourceLogs {
		for _, sl := range rl.ScopeLogs {
			for _, rec := range sl.LogRecords {
				if rec.Body.StringValue == nil || !strings.Contains(*rec.Body.StringValue, "Decision Log") {
					continue
				}
				ts := parseInt(rec.TimeUnixNano)
				if ts != 0 && ts < since.UnixNano() {
					continue
				}
				lines = append(lines, stampedLine{ts: fmt.Sprintf("%020d", ts), line: *rec.Body.StringValue})
			}
		}
	}
	return decisionsForTransaction(lines, txID), nil
}

// stampedLine is one console line with a sortable timestamp.
type stampedLine struct {
	ts   string
	line string
}

// decisionsForTransaction parses the decision-log lines, keeps those of one
// FSC transaction, and returns them oldest first, deduplicated by decision_id.
func decisionsForTransaction(lines []stampedLine, txID string) []map[string]any {
	type stamped struct {
		ts  string
		raw map[string]any
	}
	var rows []stamped
	for _, l := range lines {
		var entry map[string]any
		if json.Unmarshal([]byte(l.line), &entry) != nil {
			continue
		}
		if traceIDOf(entry) == txID {
			rows = append(rows, stamped{ts: l.ts, raw: entry})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ts < rows[j].ts })
	seen := make(map[string]bool)
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		id, _ := r.raw["decision_id"].(string)
		if id != "" && seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, r.raw)
	}
	return out
}
