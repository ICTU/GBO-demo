package portalhttp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"gbo-demo/consent-portal-backend/consent"
)

// StreamEvent is the portal-side stream shape (step + component + data).
// step names are portal-specific (pseudonymizing, consent_granted, etc.).
type StreamEvent struct {
	Step      string          `json:"step"`
	Component string          `json:"component,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// Hub fans portal steps out to connected architecture panels. It satisfies
// consent.Observer directly — no wrapper type, no proxy layer.
type Hub struct {
	mu      sync.Mutex
	clients map[string]chan StreamEvent
}

func NewHub() *Hub {
	return &Hub{clients: make(map[string]chan StreamEvent)}
}

func (h *Hub) Subscribe() (string, chan StreamEvent) {
	id := uuid.New().String()
	ch := make(chan StreamEvent, 32)
	h.mu.Lock()
	h.clients[id] = ch
	h.mu.Unlock()
	return id, ch
}

func (h *Hub) Unsubscribe(id string) {
	h.mu.Lock()
	if ch, ok := h.clients[id]; ok {
		close(ch)
		delete(h.clients, id)
	}
	h.mu.Unlock()
}

func (h *Hub) Broadcast(evt StreamEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.clients {
		select {
		case ch <- evt:
		default:
		}
	}
}

// Observe renders a flow step as a stream event. Events without a step
// (upstream-call records) are not panel steps, so they are skipped.
func (h *Hub) Observe(_ context.Context, e consent.Event) {
	if e.Step == "" {
		return
	}
	raw, _ := json.Marshal(e.Data)
	h.Broadcast(StreamEvent{Step: e.Step, Component: e.Component, Data: json.RawMessage(raw)})
}

func handleSSE(hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		// Use a ResponseController rather than a w.(http.Flusher) assertion:
		// the access-log middleware wraps the writer, and a plain assertion
		// fails against that wrapper (which is why this endpoint used to
		// answer "streaming not supported"). ResponseController follows the
		// wrapper's Unwrap chain down to the real writer.
		// Commit the status before the first flush. Flushing an uncommitted
		// response makes net/http write the header itself, which the otelhttp
		// wrapper does not see — it then writes its own and the server logs a
		// "superfluous WriteHeader" line on every connect.
		w.WriteHeader(http.StatusOK)

		rc := http.NewResponseController(w)
		if err := rc.Flush(); err != nil {
			// The status is already committed, so http.Error would be another
			// superfluous write; the client sees an empty stream and retries.
			slog.Error("streaming not supported by this ResponseWriter", "err", err.Error())
			return
		}

		id, ch := hub.Subscribe()
		defer hub.Unsubscribe(id)

		fmt.Fprintf(w, "data: %s\n\n", `{"step":"connected"}`)
		_ = rc.Flush()

		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case evt, open := <-ch:
				if !open {
					return
				}
				b, _ := json.Marshal(evt)
				fmt.Fprintf(w, "data: %s\n\n", string(b))
				_ = rc.Flush()
			case <-ticker.C:
				fmt.Fprintf(w, ": ping\n\n")
				_ = rc.Flush()
			case <-r.Context().Done():
				return
			}
		}
	}
}
