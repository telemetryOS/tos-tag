package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/core/activity"
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
	organizationID, ok := requiredOrganization(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusNotImplemented, "streaming_unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Connection", "keep-alive")
	_, refreshes, unsubscribeRefreshes := s.events.subscribe()
	defer unsubscribeRefreshes()
	activities, unsubscribeActivities := s.activity.Subscribe(organizationID)
	defer unsubscribeActivities()
	_, _ = fmt.Fprint(w, "retry: 2000\nevent: ready\ndata: connected\n\n")
	for _, record := range s.activity.Snapshot(organizationID, 200) {
		writeActivityEvent(w, record)
	}
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case event := <-refreshes:
			_, _ = fmt.Fprintf(w, "event: refresh\ndata: %s\n\n", event)
			flusher.Flush()
		case record, open := <-activities:
			if !open {
				return
			}
			writeActivityEvent(w, record)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) listActivity(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := requiredOrganization(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.activity.Snapshot(organizationID, 200))
}

func writeActivityEvent(w http.ResponseWriter, record activity.Record) {
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	if record.ID != "" {
		_, _ = fmt.Fprintf(w, "id: %s\n", record.ID)
	}
	_, _ = fmt.Fprintf(w, "event: activity\ndata: %s\n\n", data)
}
