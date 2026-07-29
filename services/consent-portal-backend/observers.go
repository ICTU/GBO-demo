package main

// APICall tracking, SSE narration and the dev-portal history post are three
// answers to one question — "something wants to watch this flow" — so they
// share one Observer seam. Each watcher keys on the field it cares about and
// ignores the rest, which keeps the payloads independent.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/google/uuid"
)

// Event is one thing worth watching in a portal flow. The three fields below
// Data are each consumed by exactly one kind of observer:
//
//	Step    -> SSEHub (the architecture panel)
//	Call    -> callLog (the per-request upstream-call cards)
//	Summary -> historyObserver (the dev-portal timeline)
type Event struct {
	Flow      string // "give_consent" | "revoke_consent"
	Step      string // step name for the SSE panel; empty means "not a step"
	Component string // demo topology node, e.g. "bsnk-mock"
	Data      any    // step payload, marshalled as-is to SSE
	Call      *APICall
	Summary   *FlowSummary
}

// Observer watches a flow. It returns nothing: an observer can never fail a
// citizen's request. That is the contract, enforced by the signature rather
// than by a comment and a discarded error.
type Observer interface {
	Observe(ctx context.Context, e Event)
}

// FanOut sends one event to several observers and is itself an Observer, so
// the core never learns there is more than one. Same shape as io.MultiWriter.
type FanOut []Observer

func (f FanOut) Observe(ctx context.Context, e Event) {
	for _, o := range f {
		if o == nil {
			continue
		}
		// One misbehaving observer must not take down the request it is
		// merely watching. This is the entire safety budget.
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("observer panicked", "panic", fmt.Sprint(r))
				}
			}()
			o.Observe(ctx, e)
		}()
	}
}

// ObserverFunc adapts a plain function, so a one-liner watcher costs one line.
type ObserverFunc func(context.Context, Event)

func (fn ObserverFunc) Observe(ctx context.Context, e Event) { fn(ctx, e) }

type nopObserver struct{}

func (nopObserver) Observe(context.Context, Event) {}

// FlowSummary is everything a whole-flow watcher needs, in core vocabulary.
type FlowSummary struct {
	Citizen           BSN
	DienstverlenerOIN string
	Scopes            []string
	ValiditySeconds   int
	TraceID           string
	ConsentID         string
	Outcome           string
	Response          any
	Trigger           string // "dev-portal" when the dev-portal drove this flow
}

// ── Observer in context ───────────────────────────────────────────────────

// The observer travels on the context for the same reason the OTel span does:
// it is request-scoped, never load-bearing for correctness, and threading it
// through every port method would put a demo concern in every signature.

type observerKey struct{}

func withObserver(ctx context.Context, o Observer) context.Context {
	return context.WithValue(ctx, observerKey{}, o)
}

func observerFrom(ctx context.Context) Observer {
	if o, ok := ctx.Value(observerKey{}).(Observer); ok && o != nil {
		return o
	}
	return nopObserver{}
}

// ── APICall tracking ──────────────────────────────────────────────────────

// APICall is the shape the dev-portal renders as upstream-call cards.
type APICall struct {
	ID           string          `json:"id"`
	Label        string          `json:"label"`
	Method       string          `json:"method"`
	URL          string          `json:"url"`
	Status       int             `json:"status"`
	RequestBody  json.RawMessage `json:"request_body,omitempty"`
	ResponseBody json.RawMessage `json:"response_body,omitempty"`
	DurationMS   int64           `json:"duration_ms"`
}

// callLog collects the upstream calls made during one request. It is
// per-request (its output goes into the response body), so unlike the SSE hub
// it is created per flow rather than wired in main.
type callLog struct {
	mu    sync.Mutex
	calls []APICall
}

func (c *callLog) Observe(_ context.Context, e Event) {
	if e.Call == nil {
		return
	}
	c.mu.Lock()
	c.calls = append(c.calls, *e.Call)
	c.mu.Unlock()
}

func (c *callLog) snapshot() []APICall {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]APICall, len(c.calls))
	copy(out, c.calls)
	return out
}

// ── SSE hub as an Observer ────────────────────────────────────────────────

// Observe makes the existing hub satisfy Observer directly — no wrapper type
// and no proxy layer. Events without a step (upstream-call records) are not
// panel steps, so they are skipped.
func (h *SSEHub) Observe(_ context.Context, e Event) {
	if e.Step == "" {
		return
	}
	h.emit(e.Step, e.Component, e.Data)
}

// ── dev-portal history as an Observer ─────────────────────────────────────

// historyObserver logs a completed citizen flow to dev-portal-backend so it
// shows up alongside developer-triggered runs. Best-effort by construction:
// Observe returns nothing, and every failure mode is logged and dropped.
type historyObserver struct {
	base string
	http *http.Client
}

func (h *historyObserver) Observe(ctx context.Context, e Event) {
	if h == nil || h.base == "" || e.Summary == nil {
		return
	}
	// Skip when the dev-portal itself is the trigger: its frontend already
	// logs the run. That rule is knowledge about this sink, so it lives here
	// rather than in the core.
	if e.Summary.Trigger == "dev-portal" {
		return
	}
	// WithoutCancel keeps the trace context while surviving the handler
	// returning. The previous implementation passed no context at all and
	// silently lost trace propagation.
	go h.post(context.WithoutCancel(ctx), *e.Summary)
}

func (h *historyObserver) post(ctx context.Context, s FlowSummary) {
	entry := map[string]any{
		"scenario_name": fmt.Sprintf("Citizen · BSN %s", s.Citizen),
		"tab":           "issuance",
		"payload": map[string]any{
			"citizen_bsn":        string(s.Citizen),
			"dienstverlener_oin": s.DienstverlenerOIN,
			"scopes":             s.Scopes,
			"validity_seconds":   s.ValiditySeconds,
		},
		"trace_id":   s.TraceID,
		"outcome":    s.Outcome,
		"consent_id": s.ConsentID,
		"response":   s.Response,
	}
	body, err := json.Marshal(entry)
	if err != nil {
		slog.Warn("dev-portal-backend history post: marshal failed", "err", err.Error())
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.base+"/history", bytes.NewReader(body))
	if err != nil {
		slog.Warn("dev-portal-backend history post: build failed", "err", err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.http.Do(req)
	if err != nil {
		slog.Warn("dev-portal-backend history post: unreachable", "err", err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Warn("dev-portal-backend history post: bad status", "status", resp.StatusCode)
	}
}

// newAPICall builds a card for one upstream call.
func newAPICall(label, method, url string, status int, reqBody, respBody []byte, durationMS int64) *APICall {
	call := &APICall{
		ID:         uuid.New().String(),
		Label:      label,
		Method:     method,
		URL:        url,
		Status:     status,
		DurationMS: durationMS,
	}
	if len(reqBody) > 0 {
		call.RequestBody = json.RawMessage(reqBody)
	}
	if len(respBody) > 0 {
		call.ResponseBody = json.RawMessage(respBody)
	}
	return call
}
