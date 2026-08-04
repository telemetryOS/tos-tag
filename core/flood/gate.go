// Package flood provides a coarse, organization-wide pre-classifier budget.
// It is intentionally separate from response admission: exhausted buckets are
// denied before context construction or any classifier/provider call.
package flood

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrInvalidScope = errors.New("invalid flood-protection scope")

type Scope struct {
	OrganizationID string
	TeamID         string
}

type Result struct {
	Allowed     bool
	Count       int64
	Limit       int64
	WindowStart time.Time
	WindowEnd   time.Time
}

type Gate interface {
	Admit(context.Context, Scope) (Result, error)
}

type Memory struct {
	mu     sync.Mutex
	now    func() time.Time
	limit  int64
	window time.Duration
	counts map[string]int64
}

func NewMemory(limit int, window time.Duration, now func() time.Time) (*Memory, error) {
	if limit <= 0 || window <= 0 {
		return nil, fmt.Errorf("flood-protection limit and window must be positive")
	}
	if now == nil {
		now = time.Now
	}
	return &Memory{now: now, limit: int64(limit), window: window, counts: make(map[string]int64)}, nil
}

func (m *Memory) Admit(_ context.Context, scope Scope) (Result, error) {
	if err := validateScope(scope); err != nil {
		return Result{}, err
	}
	now := m.now().UTC()
	start := windowStart(now, m.window)
	key := bucketID(scope, start, m.window)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts[key]++
	count := m.counts[key]
	return Result{Allowed: count <= m.limit, Count: count, Limit: m.limit, WindowStart: start, WindowEnd: start.Add(m.window)}, nil
}

func validateScope(scope Scope) error {
	if scope.OrganizationID == "" || scope.TeamID == "" {
		return ErrInvalidScope
	}
	return nil
}

func windowStart(now time.Time, window time.Duration) time.Time {
	return time.Unix(0, now.UnixNano()/window.Nanoseconds()*window.Nanoseconds()).UTC()
}

func bucketID(scope Scope, start time.Time, window time.Duration) string {
	return fmt.Sprintf("%s/%s/%d/%d", scope.OrganizationID, scope.TeamID, start.UnixNano(), window.Nanoseconds())
}

var _ Gate = (*Memory)(nil)
