package sse

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/enondev/endpoint-lighthouse-api/internal/observability"
)

// eventBufLen is the number of recent events kept for SSE replay on reconnect.
const eventBufLen = 50

type Event struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Data  string `json:"data"`
}

type Hub struct {
	mu          sync.RWMutex
	clients     map[chan Event]struct{}
	eventBuf    []Event   // ring buffer of recent events for replay
	eventCounter int64    // monotonically increasing event ID
	logger      *slog.Logger
}

func NewHub(logger *slog.Logger) *Hub {
	return &Hub{
		clients:   make(map[chan Event]struct{}),
		eventBuf:  make([]Event, 0, eventBufLen),
		logger:    logger,
	}
}

func (h *Hub) Broadcast(eventType string, jsonPayload string) {
	h.mu.Lock()
	h.eventCounter++
	ev := Event{ID: h.eventCounter, Type: eventType, Data: jsonPayload}

	// Store in ring buffer for replay on reconnect
	h.eventBuf = append(h.eventBuf, ev)
	if len(h.eventBuf) > eventBufLen {
		h.eventBuf = h.eventBuf[1:]
	}

	clientCount := len(h.clients)
	clients := make([]chan Event, 0, len(h.clients))
	for ch := range h.clients {
		clients = append(clients, ch)
	}
	h.mu.Unlock()

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

	// Replay events missed while disconnected (Last-Event-ID)
	lastID := int64(0)
	if lid := r.Header.Get("Last-Event-ID"); lid != "" {
		if parsed, err := strconv.ParseInt(lid, 10, 64); err == nil {
			lastID = parsed
		}
	}

	h.mu.RLock()
	events := make([]Event, 0, len(h.eventBuf))
	for _, ev := range h.eventBuf {
		if ev.ID > lastID {
			events = append(events, ev)
		}
	}
	h.mu.RUnlock()

	for _, ev := range events {
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, ev.Data)
	}
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
