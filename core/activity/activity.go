// Package activity provides the redacted, real-time operator activity feed.
package activity

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/RobertWHurst/blackbox"

	"github.com/telemetryos/tos-tag/types"
)

const defaultCapacity = 500

// Record is one safe operator-visible lifecycle event. Message may contain a
// bounded public Slack excerpt; restricted content must be replaced by the
// publisher before it reaches this boundary.
type Record struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id,omitempty"`
	Category       string         `json:"category"`
	Kind           string         `json:"kind"`
	Level          string         `json:"level"`
	Title          string         `json:"title"`
	Message        string         `json:"message,omitempty"`
	Summary        string         `json:"summary,omitempty"`
	Details        map[string]any `json:"details,omitempty"`
	OccurredAt     time.Time      `json:"occurred_at"`
}

type Publisher interface {
	Publish(Record)
}

type subscription struct {
	organizationID string
	channel        chan Record
}

// Hub retains a bounded in-memory window and fans new records out to SSE
// subscribers. Durable audit and JSONL logs remain separate authorities.
type Hub struct {
	mu          sync.Mutex
	capacity    int
	records     []Record
	next        int
	subscribers map[int]subscription
}

func New(capacity int) *Hub {
	if capacity <= 0 {
		capacity = defaultCapacity
	}
	return &Hub{capacity: capacity, subscribers: make(map[int]subscription)}
}

func (h *Hub) Publish(record Record) {
	if h == nil {
		return
	}
	record = normalize(record)
	h.mu.Lock()
	if len(h.records) == h.capacity {
		copy(h.records, h.records[1:])
		h.records[len(h.records)-1] = record
	} else {
		h.records = append(h.records, record)
	}
	for _, subscriber := range h.subscribers {
		if record.OrganizationID != "" && record.OrganizationID != subscriber.organizationID {
			continue
		}
		select {
		case subscriber.channel <- record:
		default:
		}
	}
	h.mu.Unlock()
}

func (h *Hub) Snapshot(organizationID string, limit int) []Record {
	if h == nil || strings.TrimSpace(organizationID) == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if limit <= 0 || limit > h.capacity {
		limit = h.capacity
	}
	result := make([]Record, 0, min(limit, len(h.records)))
	for index := len(h.records) - 1; index >= 0 && len(result) < limit; index-- {
		record := h.records[index]
		if record.OrganizationID == "" || record.OrganizationID == organizationID {
			result = append(result, clone(record))
		}
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func (h *Hub) Subscribe(organizationID string) (<-chan Record, func()) {
	channel := make(chan Record, 64)
	if h == nil || strings.TrimSpace(organizationID) == "" {
		close(channel)
		return channel, func() {}
	}
	h.mu.Lock()
	h.next++
	id := h.next
	h.subscribers[id] = subscription{organizationID: organizationID, channel: channel}
	h.mu.Unlock()
	return channel, func() {
		h.mu.Lock()
		if existing, ok := h.subscribers[id]; ok {
			delete(h.subscribers, id)
			close(existing.channel)
		}
		h.mu.Unlock()
	}
}

// Log implements blackbox.Target. It converts existing structured lifecycle
// logs into a safe feed record using a strict context allowlist.
func (h *Hub) Log(_ string, level blackbox.Level, values []any, context blackbox.Ctx, _ func() *blackbox.Source) {
	if h == nil || level < blackbox.Info {
		return
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprint(value))
	}
	title := bounded(strings.Join(parts, " "), 240)
	if title == "" {
		return
	}
	organizationID, _ := context["organization_id"].(string)
	details := make(map[string]any)
	for _, key := range []string{
		"channel_id", "observation_id", "decision_id", "job_id", "session_id", "attempt_id",
		"event_id", "event_type", "message_ts", "delivery_id", "approval_id", "tool_id",
		"operation_id", "model_profile", "model_id", "reasoning_effort", "outcome", "status",
		"duration_ms", "attempt", "error_type", "slack_error_code", "progress_step_id",
	} {
		if value, ok := safeValue(context[key]); ok {
			details[key] = value
		}
	}
	h.Publish(Record{
		OrganizationID: organizationID,
		Category:       categoryFor(title, details),
		Kind:           "log",
		Level:          level.String(),
		Title:          title,
		Details:        details,
	})
}

func normalize(record Record) Record {
	if record.ID == "" {
		record.ID = types.NewID("activity")
	}
	if record.OccurredAt.IsZero() {
		record.OccurredAt = time.Now().UTC()
	} else {
		record.OccurredAt = record.OccurredAt.UTC()
	}
	if record.Category == "" {
		record.Category = "system"
	}
	if record.Kind == "" {
		record.Kind = "lifecycle"
	}
	if record.Level == "" {
		record.Level = "info"
	}
	record.Title = bounded(record.Title, 240)
	record.Message = bounded(singleLine(record.Message), 600)
	record.Summary = bounded(record.Summary, 600)
	record.Details = cloneDetails(record.Details)
	return record
}

func clone(record Record) Record {
	record.Details = cloneDetails(record.Details)
	return record
}

func cloneDetails(source map[string]any) map[string]any {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		if safe, ok := safeValue(value); ok {
			result[key] = safe
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func safeValue(value any) (any, bool) {
	switch typed := value.(type) {
	case string:
		return bounded(typed, 240), true
	case bool:
		return typed, true
	case int:
		return typed, true
	case int32:
		return typed, true
	case int64:
		return typed, true
	case uint:
		return typed, true
	case uint32:
		return typed, true
	case uint64:
		return typed, true
	case float32:
		return typed, true
	case float64:
		return typed, true
	case []string:
		result := make([]string, 0, min(12, len(typed)))
		for _, item := range typed[:min(12, len(typed))] {
			result = append(result, bounded(item, 120))
		}
		return result, true
	default:
		return nil, false
	}
}

func categoryFor(title string, details map[string]any) string {
	lower := strings.ToLower(title)
	switch {
	case strings.Contains(lower, "pipeline worker") || strings.Contains(lower, "agent job"):
		return "agent"
	case strings.Contains(lower, "codex") || strings.Contains(lower, "app server") || strings.Contains(lower, "disposable worker"):
		return "codex"
	case details["tool_id"] != nil || strings.Contains(lower, "tool"):
		return "tool"
	case details["delivery_id"] != nil || strings.Contains(lower, "delivery"):
		return "delivery"
	case details["decision_id"] != nil || strings.HasPrefix(lower, "classification ") || strings.HasPrefix(lower, "classifier ") || strings.HasPrefix(lower, "openai classifier request"):
		return "classifier"
	case strings.Contains(lower, "slack"):
		return "slack"
	case details["job_id"] != nil || strings.Contains(lower, "job"):
		return "agent"
	default:
		return "system"
	}
}

func singleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func bounded(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	return strings.TrimSpace(value[:maximum-1]) + "…"
}
