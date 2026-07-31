// Package admission enforces channel response cooldown, budgets, and concurrency.
package admission

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/core/orgconfig"
	"github.com/telemetryos/tos-tag/types"
)

var (
	ErrKillSwitch  = errors.New("channel kill switch is active")
	ErrCooldown    = errors.New("channel response cooldown is active")
	ErrBudget      = errors.New("channel response budget is exhausted")
	ErrConcurrency = errors.New("channel concurrency limit is reached")
)

type Controller interface {
	Admit(context.Context, orgconfig.ChannelPolicy) (string, error)
	Complete(context.Context, string)
}
type state struct {
	hour      time.Time
	responses int
	active    int
	last      time.Time
}
type reservation struct {
	key       string
	completed bool
}
type Memory struct {
	mu           sync.Mutex
	now          func() time.Time
	states       map[string]state
	reservations map[string]reservation
	next         uint64
}

func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{now: now, states: make(map[string]state), reservations: make(map[string]reservation)}
}

func (m *Memory) Admit(_ context.Context, policy orgconfig.ChannelPolicy) (string, error) {
	if err := orgconfig.ValidateChannel(policy); err != nil {
		return "", err
	}
	if policy.KillSwitch || !policy.Enrolled {
		return "", ErrKillSwitch
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	key := policy.OrganizationID + "/" + policy.TeamID + "/" + policy.ChannelID
	current := m.states[key]
	hour := now.Truncate(time.Hour)
	if !current.hour.Equal(hour) {
		current.hour = hour
		current.responses = 0
	}
	if current.active >= policy.MaxConcurrentJobs {
		return "", ErrConcurrency
	}
	if current.responses >= policy.MaxResponsesPerHour {
		return "", ErrBudget
	}
	if !current.last.IsZero() && now.Sub(current.last) < policy.Cooldown {
		return "", ErrCooldown
	}
	m.next++
	id := types.NewID("admit")
	current.responses++
	current.active++
	current.last = now
	m.states[key] = current
	m.reservations[id] = reservation{key: key}
	return id, nil
}
func (m *Memory) Complete(_ context.Context, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	reservation, ok := m.reservations[id]
	if !ok || reservation.completed {
		return
	}
	current := m.states[reservation.key]
	if current.active > 0 {
		current.active--
	}
	m.states[reservation.key] = current
	reservation.completed = true
	m.reservations[id] = reservation
}
