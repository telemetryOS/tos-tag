// Package harness defines the project-owned boundary around OpenCode or any
// future agent runtime. MongoDB remains authoritative outside this boundary.
package harness

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

var ErrSessionNotFound = errors.New("harness session not found")

type Session struct {
	ID        string    `json:"id"`
	Title     string    `json:"title,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type JobSessionSpec struct {
	Title          string
	OrganizationID string
	WorkspaceID    string
	ChannelID      string
	JobID          string
	LeaseToken     string
	SteeringEpoch  int64
	ExpiresAt      time.Time
}

type JobScopedHarness interface {
	CreateJobSession(context.Context, JobSessionSpec) (Session, error)
}

type Prompt struct {
	Text         string `json:"text"`
	System       string `json:"system,omitempty"`
	Model        string `json:"model"`
	Variant      string `json:"variant,omitempty"`
	RequestID    string `json:"request_id"`
	SlackFormat  string `json:"slack_format"`
	ToolSnapshot string `json:"tool_snapshot,omitempty"`
}

type Event struct {
	ID        string         `json:"id"`
	SessionID string         `json:"session_id"`
	Type      string         `json:"type"`
	Data      map[string]any `json:"data,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type PermissionDecision struct {
	PermissionID string `json:"permission_id"`
	Approved     bool   `json:"approved"`
	Reason       string `json:"reason,omitempty"`
}

type Harness interface {
	Health(context.Context) error
	CreateSession(context.Context, string) (Session, error)
	Prompt(context.Context, string, Prompt) error
	Events(context.Context, string) (<-chan Event, <-chan error)
	Permission(context.Context, string, PermissionDecision) error
	Abort(context.Context, string) error
}

// Fake is deterministic and is the only harness used by default tests/evals.
type Fake struct {
	mu       sync.Mutex
	sessions map[string]Session
	events   map[string][]Event
	prompts  map[string][]Prompt
	aborted  map[string]bool
}

func NewFake() *Fake {
	return &Fake{sessions: make(map[string]Session), events: make(map[string][]Event), prompts: make(map[string][]Prompt), aborted: make(map[string]bool)}
}

func (*Fake) Health(context.Context) error { return nil }

func (f *Fake) CreateSession(_ context.Context, title string) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	session := Session{ID: types.NewID("harness"), Title: title, CreatedAt: time.Now().UTC()}
	f.sessions[session.ID] = session
	return session, nil
}

func (f *Fake) Prompt(_ context.Context, sessionID string, prompt Prompt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[sessionID]; !ok {
		return ErrSessionNotFound
	}
	f.prompts[sessionID] = append(f.prompts[sessionID], prompt)
	f.events[sessionID] = append(f.events[sessionID],
		Event{ID: types.NewID("event"), SessionID: sessionID, Type: "message.delta", Data: map[string]any{"text": "deterministic fake response"}, CreatedAt: time.Now().UTC()},
		Event{ID: types.NewID("event"), SessionID: sessionID, Type: "session.idle", CreatedAt: time.Now().UTC()},
	)
	return nil
}

func (f *Fake) Events(ctx context.Context, sessionID string) (<-chan Event, <-chan error) {
	out := make(chan Event)
	errs := make(chan error, 1)
	f.mu.Lock()
	events := append([]Event(nil), f.events[sessionID]...)
	_, exists := f.sessions[sessionID]
	f.mu.Unlock()
	go func() {
		defer close(out)
		defer close(errs)
		if !exists {
			errs <- ErrSessionNotFound
			return
		}
		for _, event := range events {
			select {
			case out <- event:
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}
		}
	}()
	return out, errs
}

func (f *Fake) Permission(_ context.Context, sessionID string, decision PermissionDecision) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[sessionID]; !ok {
		return ErrSessionNotFound
	}
	f.events[sessionID] = append(f.events[sessionID], Event{ID: types.NewID("event"), SessionID: sessionID, Type: "permission.resolved", Data: map[string]any{"permission_id": decision.PermissionID, "approved": decision.Approved}, CreatedAt: time.Now().UTC()})
	return nil
}

func (f *Fake) Abort(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[sessionID]; !ok {
		return ErrSessionNotFound
	}
	f.aborted[sessionID] = true
	return nil
}

func (f *Fake) Prompts(sessionID string) []Prompt {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Prompt(nil), f.prompts[sessionID]...)
}
