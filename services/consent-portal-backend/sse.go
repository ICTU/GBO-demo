package main

import (
	"encoding/json"
	"sync"

	"github.com/google/uuid"
)

// SSEEvent is the portal-side stream shape (step + component + data).
// step names are portal-specific (pseudonymizing, consent_granted, etc.).
type SSEEvent struct {
	Step      string          `json:"step"`
	Component string          `json:"component,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

type SSEHub struct {
	mu      sync.Mutex
	clients map[string]chan SSEEvent
}

func NewSSEHub() *SSEHub {
	return &SSEHub{clients: make(map[string]chan SSEEvent)}
}

func (h *SSEHub) Subscribe() (string, chan SSEEvent) {
	id := uuid.New().String()
	ch := make(chan SSEEvent, 32)
	h.mu.Lock()
	h.clients[id] = ch
	h.mu.Unlock()
	return id, ch
}

func (h *SSEHub) Unsubscribe(id string) {
	h.mu.Lock()
	if ch, ok := h.clients[id]; ok {
		close(ch)
		delete(h.clients, id)
	}
	h.mu.Unlock()
}

func (h *SSEHub) Broadcast(evt SSEEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.clients {
		select {
		case ch <- evt:
		default:
		}
	}
}

func (h *SSEHub) emit(step, component string, data any) {
	raw, _ := json.Marshal(data)
	h.Broadcast(SSEEvent{Step: step, Component: component, Data: json.RawMessage(raw)})
}
