package main

import (
	"compress/gzip"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TraceHub stores spans per trace_id in-memory and pushes new ones to
// connected SSE subscribers. Replaces the Jaeger-Query polling loop in the
// dev-portal frontend: the OTel collector forwards every span here directly
// (via a second pipeline next to the Jaeger exporter), so the UI sees nodes
// light up within ~100 ms instead of waiting ~1 s for Jaeger ingest.

type traceSpan struct {
	TraceID    string            `json:"trace_id"`
	SpanID     string            `json:"span_id"`
	ParentID   string            `json:"parent_id,omitempty"`
	Service    string            `json:"service"`
	Name       string            `json:"name"`
	StartNanos int64             `json:"start_nanos"`
	EndNanos   int64             `json:"end_nanos"`
	StatusCode int               `json:"status_code"` // 0=unset, 1=ok, 2=error
	Attributes map[string]string `json:"attributes,omitempty"`
}

type traceEntry struct {
	spans       []traceSpan
	subscribers []chan traceSpan
	lastSeen    time.Time
	notified    bool // watch-next has fired for this trace_id
}

// Entry-point spans whose appearance marks the start of a real chain-flow.
// Background reads (GET /portal/consents, SSE, login) deliberately don't
// match — otherwise just navigating the burger-FE would trigger watch-next.
type entryPointRule struct {
	service    string
	method     string
	pathPrefix string
}

var entryPointRules = []entryPointRule{
	{"consent-portal-backend", "POST", "/portal/consents"},
	{"consent-portal-backend", "DELETE", "/portal/consents/"},
	{"dienstverlener-backend", "POST", "/api/dvtp/query"},
	// EUDI: the issuance-server POSTs to eudi-adapter's root on every
	// wallet-triggered issuance. Only /-POST is a flow-entry (health is GET).
	{"eudi-adapter", "POST", "/"},
}

func isEntryPointSpan(s traceSpan) bool {
	method := s.Attributes["http.method"]
	target := s.Attributes["http.target"]
	if target == "" {
		target = s.Attributes["http.route"]
	}
	for _, r := range entryPointRules {
		if r.service == s.Service && r.method == method && strings.HasPrefix(target, r.pathPrefix) {
			return true
		}
	}
	return false
}

type traceHub struct {
	mu           sync.Mutex
	traces       map[string]*traceEntry
	ttl          time.Duration
	nextWatchers []*watcher // notified once when a NEW trace_id first appears
}

// watcher is one armed watch-next connection, narrowed to its own developer's
// flows. session rides a cookie the browser-started flows return as
// X-Demo-Session; EUDI starts on a phone, so it is narrowed by usecase.
type watcher struct {
	session string
	usecase string
	ch      chan watchEvent
}

// traceOrigin is what an entry-point span says about where its flow came
// from. Both fields are empty for a flow nobody tagged — a raw curl, or a
// browser that never had the dev-portal open.
type traceOrigin struct {
	session string
	usecase string
}

func originOf(s traceSpan) traceOrigin {
	return traceOrigin{
		session: s.Attributes["gbo.demo.session"],
		usecase: s.Attributes["gbo.usecase"],
	}
}

// matches reports whether this watcher wants a trace from o. A filter only
// rejects what positively contradicts it, so an untagged flow still wakes
// everyone: the failure we want is one run too many, never waiting forever.
func (w *watcher) matches(o traceOrigin) bool {
	if w.session != "" && o.session != "" && w.session != o.session {
		return false
	}
	if w.usecase != "" && o.usecase != "" && w.usecase != o.usecase {
		return false
	}
	return true
}

// takeWatchers splits the armed watchers into those that want this trace and
// those that stay armed. Callers must hold h.mu.
func (h *traceHub) takeWatchers(o traceOrigin) []*watcher {
	var notify []*watcher
	remaining := h.nextWatchers[:0]
	for _, w := range h.nextWatchers {
		if w.matches(o) {
			notify = append(notify, w)
			continue
		}
		remaining = append(remaining, w)
	}
	h.nextWatchers = remaining
	return notify
}

func newTraceHub(ttl time.Duration) *traceHub {
	h := &traceHub{traces: make(map[string]*traceEntry), ttl: ttl}
	go h.cleanupLoop()
	return h
}

func (h *traceHub) cleanupLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		cutoff := time.Now().Add(-h.ttl)
		h.mu.Lock()
		for id, e := range h.traces {
			if e.lastSeen.Before(cutoff) {
				for _, ch := range e.subscribers {
					close(ch)
				}
				delete(h.traces, id)
			}
		}
		h.mu.Unlock()
	}
}

// ingest appends a span to its trace and fan-outs to subscribers. The first
// time we see a given trace_id we ALSO notify watch-next subscribers — used
// by the dev-portal "wachten op volgende run" button to auto-pick up a trace
// initiated elsewhere (burger-FE, afnemer-mock, …).
func (h *traceHub) ingest(s traceSpan) {
	h.mu.Lock()
	e, ok := h.traces[s.TraceID]
	if !ok {
		e = &traceEntry{}
		h.traces[s.TraceID] = e
	}
	e.spans = append(e.spans, s)
	e.lastSeen = time.Now()
	subs := append([]chan traceSpan(nil), e.subscribers...)
	var watchers []*watcher
	// Only fire watch-next on the FIRST entry-point-service span we see for
	// a given trace_id. Spans within a trace arrive in arbitrary order via
	// the collector, so the literal "first span seen" can be a downstream
	// service (fsc-mock, pep, …) which doesn't tell us issuance vs use.
	if !e.notified && isEntryPointSpan(s) {
		e.notified = true
		// Only the watchers that wanted this trace are consumed: disarming
		// all of them, as this used to, ended everyone else's wait too.
		watchers = h.takeWatchers(originOf(s))
	}
	h.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- s:
		default: // drop on slow subscriber
		}
	}
	if len(watchers) > 0 {
		// Shared: went to several sessions, so nobody can say whose run it is.
		evt := watchEvent{TraceID: s.TraceID, Service: s.Service, Shared: len(watchers) > 1}
		for _, w := range watchers {
			select {
			case w.ch <- evt:
			default:
			}
		}
	}
}

type watchEvent struct {
	TraceID string `json:"trace_id"`
	Service string `json:"service"`
	Shared  bool   `json:"shared,omitempty"`
}

// watchNext registers a one-shot watcher that receives the trace_id (+ first
// span's service, so the frontend can auto-switch to issuance/use tab) of
// the next NEW trace to appear in the hub. session limits it to this
// dev-portal session's flows, usecase to one EUDI usecase; either may be
// empty. The cleanup fn deregisters the watcher if the caller goes away.
func (h *traceHub) watchNext(session, usecase string) (chan watchEvent, func()) {
	w := &watcher{session: session, usecase: usecase, ch: make(chan watchEvent, 1)}
	h.mu.Lock()
	h.nextWatchers = append(h.nextWatchers, w)
	h.mu.Unlock()
	cleanup := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		out := h.nextWatchers[:0]
		for _, c := range h.nextWatchers {
			if c != w {
				out = append(out, c)
			}
		}
		h.nextWatchers = out
	}
	return w.ch, cleanup
}

// subscribe registers a subscriber and returns the channel + a snapshot of
// already-collected spans, plus a deregister fn.
func (h *traceHub) subscribe(traceID string) (chan traceSpan, []traceSpan, func()) {
	ch := make(chan traceSpan, 64)
	h.mu.Lock()
	e, ok := h.traces[traceID]
	if !ok {
		e = &traceEntry{lastSeen: time.Now()}
		h.traces[traceID] = e
	}
	snapshot := append([]traceSpan(nil), e.spans...)
	e.subscribers = append(e.subscribers, ch)
	h.mu.Unlock()
	cleanup := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		ent := h.traces[traceID]
		if ent == nil {
			return
		}
		out := ent.subscribers[:0]
		for _, s := range ent.subscribers {
			if s != ch {
				out = append(out, s)
			}
		}
		ent.subscribers = out
		close(ch)
	}
	return ch, snapshot, cleanup
}

// ── OTLP-HTTP JSON receiver ─────────────────────────────────────────────
//
// The OTel collector's `otlphttp` exporter (encoding: json) POSTs traces
// here as { "resourceSpans": [{ "resource": ..., "scopeSpans": [...] }] }.
// We pull out service.name from the resource attributes, then iterate spans.

type otlpExportRequest struct {
	ResourceSpans []struct {
		Resource struct {
			Attributes []otlpKeyValue `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []otlpSpan `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

type otlpKeyValue struct {
	Key   string `json:"key"`
	Value struct {
		StringValue *string         `json:"stringValue,omitempty"`
		IntValue    *json.Number    `json:"intValue,omitempty"`
		BoolValue   *bool           `json:"boolValue,omitempty"`
		DoubleValue *float64        `json:"doubleValue,omitempty"`
		ArrayValue  json.RawMessage `json:"arrayValue,omitempty"`
		KvlistValue json.RawMessage `json:"kvlistValue,omitempty"`
	} `json:"value"`
}

type otlpSpan struct {
	TraceID           string         `json:"traceId"`
	SpanID            string         `json:"spanId"`
	ParentSpanID      string         `json:"parentSpanId,omitempty"`
	Name              string         `json:"name"`
	StartTimeUnixNano json.Number    `json:"startTimeUnixNano"`
	EndTimeUnixNano   json.Number    `json:"endTimeUnixNano"`
	Attributes        []otlpKeyValue `json:"attributes"`
	Status            struct {
		Code int `json:"code"`
	} `json:"status"`
}

func handleOTLPTraces(hub *traceHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		reader := io.LimitReader(r.Body, 8<<20)
		// The OTel collector's otlphttp exporter gzip-compresses by default.
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(reader)
			if err != nil {
				http.Error(w, "gzip: "+err.Error(), http.StatusBadRequest)
				return
			}
			defer func() { _ = gz.Close() }()
			reader = gz
		}
		body, err := io.ReadAll(reader)
		if err != nil {
			http.Error(w, "read: "+err.Error(), http.StatusBadRequest)
			return
		}
		var req otlpExportRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "json: "+err.Error(), http.StatusBadRequest)
			return
		}
		count := 0
		for _, rs := range req.ResourceSpans {
			svc := stringAttr(rs.Resource.Attributes, "service.name")
			for _, ss := range rs.ScopeSpans {
				for _, sp := range ss.Spans {
					traceID := normaliseHex(sp.TraceID)
					if traceID == "" {
						continue
					}
					hub.ingest(traceSpan{
						TraceID:    traceID,
						SpanID:     normaliseHex(sp.SpanID),
						ParentID:   normaliseHex(sp.ParentSpanID),
						Service:    svc,
						Name:       sp.Name,
						StartNanos: parseInt(sp.StartTimeUnixNano),
						EndNanos:   parseInt(sp.EndTimeUnixNano),
						StatusCode: sp.Status.Code,
						Attributes: spanAttrs(sp.Attributes),
					})
					count++
				}
			}
		}
		slog.Debug("otlp traces ingested", "spans", count)
		// OTLP success response: empty PartialSuccess.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"partialSuccess":{}}`))
	}
}

func stringAttr(attrs []otlpKeyValue, key string) string {
	for _, a := range attrs {
		if a.Key == key && a.Value.StringValue != nil {
			return *a.Value.StringValue
		}
	}
	return ""
}

func spanAttrs(attrs []otlpKeyValue) map[string]string {
	out := make(map[string]string, len(attrs))
	for _, a := range attrs {
		switch {
		case a.Value.StringValue != nil:
			out[a.Key] = *a.Value.StringValue
		case a.Value.IntValue != nil:
			out[a.Key] = a.Value.IntValue.String()
		case a.Value.BoolValue != nil:
			out[a.Key] = strconv.FormatBool(*a.Value.BoolValue)
		case a.Value.DoubleValue != nil:
			out[a.Key] = strconv.FormatFloat(*a.Value.DoubleValue, 'f', -1, 64)
		}
	}
	return out
}

func parseInt(n json.Number) int64 {
	v, _ := n.Int64()
	return v
}

// OTLP encodes trace/span IDs as base64-standard (with padding) per the
// proto-JSON canonical mapping. Convert to lowercase hex so we can match
// against the W3C traceparent format that the frontend uses.
func normaliseHex(s string) string {
	if s == "" {
		return ""
	}
	// Try hex first — some collectors emit hex strings directly.
	if _, err := hex.DecodeString(s); err == nil {
		return s
	}
	// Fallback: base64 → bytes → hex.
	if raw, err := base64.StdEncoding.DecodeString(s); err == nil {
		return hex.EncodeToString(raw)
	}
	if raw, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return hex.EncodeToString(raw)
	}
	return ""
}

// ── SSE subscriber ──────────────────────────────────────────────────────

func handleChainEvents(hub *traceHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		traceID := r.URL.Query().Get("trace_id")
		if traceID == "" {
			http.Error(w, "trace_id required", http.StatusBadRequest)
			return
		}
		rc := http.NewResponseController(w)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		// SSE needs the response to start streaming immediately — disable
		// the otelhttp middleware's default behaviour of waiting for full
		// body buffering via the ResponseController.

		ch, snapshot, cleanup := hub.subscribe(traceID)
		defer cleanup()

		flush := func() { _ = rc.Flush() }

		for _, s := range snapshot {
			writeSSE(w, "span", s)
		}
		flush()

		ctx := r.Context()
		idle := time.NewTimer(15 * time.Second)
		defer idle.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-idle.C:
				writeSSE(w, "close", map[string]string{"reason": "idle"})
				flush()
				return
			case s, ok := <-ch:
				if !ok {
					return
				}
				writeSSE(w, "span", s)
				flush()
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(15 * time.Second)
			}
		}
	}
}

func writeSSE(w io.Writer, event string, payload any) {
	b, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}

// handleWatchNext streams a single SSE event with the trace_id of the next
// new trace ingested into the hub, then closes. Frontend uses this for the
// "watch"-button: pick up any flow started in another tab (burger-FE,
// afnemer-mock) without manual trace_id juggling. Self-traffic (dev-portal-
// backend's own /history /events /watch-next spans) is filtered in ingest()
// so it never reaches the watcher.
//
// ?session=<id> narrows the wait to this dev-portal session's own flows;
// ?usecase=<key> does the same for EUDI, which carries no session id.
func handleWatchNext(hub *traceHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		q := r.URL.Query()
		ch, cleanup := hub.watchNext(q.Get("session"), q.Get("usecase"))
		defer cleanup()

		_, _ = w.Write([]byte(": watching\n\n"))
		_ = rc.Flush()

		ctx := r.Context()
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, "trace", evt)
			_ = rc.Flush()
		}
	}
}
