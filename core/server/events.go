package server

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

type eventHub struct {
	mu          sync.Mutex
	next        int
	subscribers map[int]chan string
}

func newEventHub() *eventHub { return &eventHub{subscribers: make(map[int]chan string)} }
func (h *eventHub) Publish(event string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, subscriber := range h.subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
}
func (h *eventHub) subscribe() (int, <-chan string, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	id := h.next
	channel := make(chan string, 8)
	h.subscribers[id] = channel
	return id, channel, func() { h.mu.Lock(); delete(h.subscribers, id); h.mu.Unlock() }
}
func (s *Server) eventStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusNotImplemented, "streaming_unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	_, events, unsubscribe := s.events.subscribe()
	defer unsubscribe()
	_, _ = fmt.Fprint(w, "event: refresh\ndata: initial\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case event := <-events:
			_, _ = fmt.Fprintf(w, "event: refresh\ndata: %s\n\n", event)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
