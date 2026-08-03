package consent

// APICall tracking, SSE narration and the dev-portal history post are three
// answers to one question — "something wants to watch this flow" — so they
// share one Observer seam. Each watcher keys on the field it cares about and
// ignores the rest, which keeps the payloads independent.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
)

// Event is one thing worth watching in a portal flow. The three fields below
// Data are each consumed by exactly one kind of observer:
//
//	Step    -> portalhttp.Hub (the architecture panel)
//	Call    -> portalhttp call log (the per-request upstream-call cards)
//	Summary -> devportal.History (the dev-portal timeline)
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

// APICall is the shape the dev-portal renders as upstream-call cards. It
// lives here rather than in a transport package because both the adapters
// that produce it and the handler that returns it need to name it.
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

// NewAPICall builds a card for one upstream call.
func NewAPICall(id, label, method, url string, status int, reqBody, respBody []byte, durationMS int64) *APICall {
	call := &APICall{
		ID:         id,
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

// CallLog collects the upstream calls made during one request. It is
// per-request — its output goes into the response body — so unlike the SSE
// hub it is created per flow rather than wired once in main.
type CallLog struct {
	mu    sync.Mutex
	calls []APICall
}

func (c *CallLog) Observe(_ context.Context, e Event) {
	if e.Call == nil {
		return
	}
	c.mu.Lock()
	c.calls = append(c.calls, *e.Call)
	c.mu.Unlock()
}

// Snapshot returns the calls recorded so far.
func (c *CallLog) Snapshot() []APICall {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]APICall, len(c.calls))
	copy(out, c.calls)
	return out
}

// ── Observer in context ───────────────────────────────────────────────────

// The observer travels on the context for the same reason the OTel span does:
// it is request-scoped, never load-bearing for correctness, and threading it
// through every port method would put a demo concern in every signature.

type observerKey struct{}

// WithObserver attaches a request-scoped observer to ctx.
func WithObserver(ctx context.Context, o Observer) context.Context {
	return context.WithValue(ctx, observerKey{}, o)
}

// ObserverFrom returns the request-scoped observer, or a no-op if there is
// none. Adapters call this to report upstream calls.
func ObserverFrom(ctx context.Context) Observer {
	if o, ok := ctx.Value(observerKey{}).(Observer); ok && o != nil {
		return o
	}
	return nopObserver{}
}
