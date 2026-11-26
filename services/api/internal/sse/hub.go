package sse

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/enondev/endpoint-lighthouse-api/internal/observability"
)

type Event struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

type Hub struct {
	mu      sync.RWMutex
	clients map[chan Event]struct{}
	logger  *slog.Logger
}

func NewHub(logger *slog.Logger) *Hub {
	return &Hub{
		clients: make(map[chan Event]struct{}),
		logger:  logger,
	}
}

func (h *Hub) Broadcast(eventType string, jsonPayload string) {
	h.mu.RLock()
	clientCount := len(h.clients)
	ev := Event{Type: eventType, Data: jsonPayload}
	clients := make([]chan Event, 0, len(h.clients))
	for ch := range h.clients {
		clients = append(clients, ch)
	}
	h.mu.RUnlock()

	observability.SetSSEClientsConnected(clientCount)
	observability.RecordSSEBroadcast(eventType)

	for _, ch := range clients {
		select {
		case ch <- ev:
		default:
			// Drop message for slow consumers rather than blocking the hub
			h.logger.Debug("dropped sse event for slow consumer", slog.String("event", eventType))
		}
	}
}

func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Prevent proxy buffering

	ch := make(chan Event, 16)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()

	observability.SetSSEClientsConnected(h.clientCount())

	defer func() {
		h.mu.Lock()
		delete(h.clients, ch)
		h.mu.Unlock()
		close(ch)
		observability.SetSSEClientsConnected(h.clientCount())
	}()

	// Initial handshake comment
	_, _ = fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case ev := <-ch:
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, ev.Data)
			flusher.Flush()
		}
	}
}

func (h *Hub) clientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
