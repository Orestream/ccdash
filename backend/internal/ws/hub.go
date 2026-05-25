// Package ws implements a small fan-out hub that broadcasts JSON events to all
// connected dashboard clients. It is transport-agnostic: the hub deals in
// pre-marshalled byte slices and per-subscriber channels, so the HTTP layer
// owns the actual WebSocket connection.
package ws

import (
	"encoding/json"
	"sync"
	"time"
)

// Event is the envelope sent to every client. See docs/API.md for the type set.
type Event struct {
	Type    string `json:"type"`
	Ts      string `json:"ts"`
	Payload any    `json:"payload"`
}

// Hub fans out events to all current subscribers. It is safe for concurrent use.
type Hub struct {
	mu   sync.RWMutex
	subs map[chan []byte]struct{}
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[chan []byte]struct{})}
}

// Subscribe registers a new client and returns its buffered receive channel.
func (h *Hub) Subscribe() chan []byte {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes a client and closes its channel. Safe to call once.
func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// Broadcast marshals an event and delivers it to every subscriber. Slow
// consumers whose buffer is full are skipped rather than blocking the caller.
func (h *Hub) Broadcast(eventType string, payload any) {
	data, err := json.Marshal(Event{
		Type:    eventType,
		Ts:      time.Now().UTC().Format(time.RFC3339Nano),
		Payload: payload,
	})
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		select {
		case ch <- data:
		default:
			// Drop for slow clients; they will resync via REST.
		}
	}
}

// SubscriberCount reports how many clients are currently connected.
func (h *Hub) SubscriberCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}
