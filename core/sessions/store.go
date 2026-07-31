// Package sessions maps Slack root threads to durable session generations.
package sessions

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

type Session struct {
	ID                types.SessionID `json:"id"`
	OrganizationID    string          `json:"organization_id"`
	TeamID            string          `json:"team_id"`
	ChannelID         string          `json:"channel_id"`
	RootThreadTS      string          `json:"root_thread_ts"`
	CurrentGeneration int64           `json:"current_generation"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type Store interface {
	Resolve(context.Context, string, string, string, string) (Session, bool, error)
	Restart(context.Context, string, string, string, string) (Session, error)
	Find(context.Context, string, string, string, string) (Session, error)
}

type MemoryStore struct {
	mu    sync.Mutex
	now   func() time.Time
	byKey map[string]Session
}

func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{now: now, byKey: make(map[string]Session)}
}

func (s *MemoryStore) Resolve(_ context.Context, organizationID, teamID, channelID, rootThreadTS string) (Session, bool, error) {
	if organizationID == "" || teamID == "" || channelID == "" || rootThreadTS == "" {
		return Session{}, false, fmt.Errorf("session scope is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := Key(organizationID, teamID, channelID, rootThreadTS)
	if current, ok := s.byKey[key]; ok {
		return current, false, nil
	}
	now := s.now().UTC()
	created := Session{ID: types.SessionID(types.NewID("ses")), OrganizationID: organizationID, TeamID: teamID, ChannelID: channelID, RootThreadTS: rootThreadTS, CurrentGeneration: 1, CreatedAt: now, UpdatedAt: now}
	s.byKey[key] = created
	return created, true, nil
}

func (s *MemoryStore) Restart(_ context.Context, organizationID, teamID, channelID, rootThreadTS string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := Key(organizationID, teamID, channelID, rootThreadTS)
	current, ok := s.byKey[key]
	if !ok {
		return Session{}, fmt.Errorf("session not found")
	}
	current.CurrentGeneration++
	current.UpdatedAt = s.now().UTC()
	s.byKey[key] = current
	return current, nil
}

func (s *MemoryStore) Find(_ context.Context, organizationID, teamID, channelID, rootThreadTS string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.byKey[Key(organizationID, teamID, channelID, rootThreadTS)]
	if !ok {
		return Session{}, fmt.Errorf("session not found")
	}
	return current, nil
}

func Key(organizationID, teamID, channelID, rootThreadTS string) string {
	return organizationID + "/" + teamID + "/" + channelID + "/" + rootThreadTS
}
