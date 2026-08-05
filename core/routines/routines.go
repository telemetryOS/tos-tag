// Package routines turns schedules and subscribed events into ordinary jobs.
package routines

import (
	"context"
	"errors"
	"fmt"
	"github.com/telemetryos/tos-tag/core/jobs"
	"github.com/telemetryos/tos-tag/core/schedule"
	"github.com/telemetryos/tos-tag/types"
	"sort"
	"sync"
	"time"
)

var (
	ErrLoopSuppressed = errors.New("routine loop suppressed")
	ErrNotFound       = errors.New("routine not found")
	ErrUpdateConflict = errors.New("routine update conflict")
)

type Routine struct {
	ID             string          `json:"id" bson:"public_id"`
	OrganizationID string          `json:"organization_id" bson:"organization_id"`
	WorkspaceID    string          `json:"workspace_id" bson:"workspace_id"`
	ChannelID      string          `json:"channel_id" bson:"channel_id"`
	RootThreadTS   string          `json:"root_thread_ts" bson:"root_thread_ts"`
	SessionID      types.SessionID `json:"session_id" bson:"session_id"`
	Generation     int64           `json:"generation" bson:"generation"`
	OwnerID        string          `json:"owner_id" bson:"owner_id"`
	Input          string          `json:"input" bson:"input"`
	Cron           string          `json:"cron,omitempty" bson:"cron,omitempty"`
	Timezone       string          `json:"timezone,omitempty" bson:"timezone,omitempty"`
	Interval       time.Duration   `json:"interval" bson:"interval"`
	NextRun        time.Time       `json:"next_run" bson:"next_run"`
	Enabled        bool            `json:"enabled" bson:"enabled"`
	Version        int64           `json:"version" bson:"version"`
	CreatedAt      time.Time       `json:"created_at" bson:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at" bson:"updated_at"`
}
type Trigger struct {
	SourceEventID string
	OriginTag     string
	At            time.Time
}
type Authorizer interface {
	AuthorizeRoutine(context.Context, Routine) error
}
type AllowAll struct{}

func (AllowAll) AuthorizeRoutine(context.Context, Routine) error { return nil }

type AuthorizerFunc func(context.Context, Routine) error

func (f AuthorizerFunc) AuthorizeRoutine(ctx context.Context, routine Routine) error {
	return f(ctx, routine)
}

type Store struct {
	mu       sync.Mutex
	routines map[string]Routine
}

type Repository interface {
	PutContext(context.Context, Routine) (Routine, error)
	UpdateContext(context.Context, Routine, int64) (Routine, error)
	GetContext(context.Context, string, string, string, string) (Routine, error)
	DueContext(context.Context, time.Time, int) ([]Routine, error)
	AdvanceContext(context.Context, string, string, string, string, time.Time) error
	List(context.Context, string) ([]Routine, error)
	ListChannel(context.Context, string, string, string) ([]Routine, error)
}

func NewStore() *Store { return &Store{routines: make(map[string]Routine)} }
func (s *Store) Put(r Routine) (Routine, error) {
	var err error
	r, err = normalize(r, time.Now().UTC())
	if err != nil {
		return Routine{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	key := routineKey(r.OrganizationID, r.WorkspaceID, r.ChannelID, r.ID)
	if old, ok := s.routines[key]; ok {
		r.Version = old.Version + 1
		r.CreatedAt = old.CreatedAt
	} else {
		r.Version = 1
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	s.routines[key] = r
	return r, nil
}
func (s *Store) PutContext(_ context.Context, r Routine) (Routine, error) { return s.Put(r) }
func (s *Store) UpdateContext(_ context.Context, r Routine, expectedVersion int64) (Routine, error) {
	var err error
	r, err = normalize(r, time.Now().UTC())
	if err != nil {
		return Routine{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := routineKey(r.OrganizationID, r.WorkspaceID, r.ChannelID, r.ID)
	old, ok := s.routines[key]
	if !ok {
		return Routine{}, ErrNotFound
	}
	if old.Version != expectedVersion {
		return Routine{}, ErrUpdateConflict
	}
	r.Version = old.Version + 1
	r.CreatedAt = old.CreatedAt
	r.UpdatedAt = time.Now().UTC()
	s.routines[key] = r
	return r, nil
}
func (s *Store) GetContext(_ context.Context, organizationID, workspaceID, channelID, id string) (Routine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	routine, ok := s.routines[routineKey(organizationID, workspaceID, channelID, id)]
	if !ok {
		return Routine{}, ErrNotFound
	}
	return routine, nil
}
func (s *Store) Due(now time.Time) []Routine {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []Routine
	for _, r := range s.routines {
		if r.Enabled && !r.NextRun.After(now) {
			due = append(due, r)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].NextRun.Equal(due[j].NextRun) {
			return due[i].ID < due[j].ID
		}
		return due[i].NextRun.Before(due[j].NextRun)
	})
	return due
}
func (s *Store) DueContext(_ context.Context, now time.Time, limit int) ([]Routine, error) {
	values := s.Due(now)
	if limit > 0 && len(values) > limit {
		values = values[:limit]
	}
	return values, nil
}
func (s *Store) Advance(organizationID, workspaceID, channelID, id string, from time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := routineKey(organizationID, workspaceID, channelID, id)
	r, ok := s.routines[key]
	if !ok {
		return ErrNotFound
	}
	spec, err := schedule.Parse(r.Cron, r.Timezone, r.Interval)
	if err != nil {
		return err
	}
	r.NextRun = spec.Advance(r.NextRun, from)
	r.Version++
	s.routines[key] = r
	return nil
}
func (s *Store) AdvanceContext(_ context.Context, organizationID, workspaceID, channelID, id string, from time.Time) error {
	return s.Advance(organizationID, workspaceID, channelID, id, from)
}
func (s *Store) List(_ context.Context, organizationID string) ([]Routine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Routine, 0)
	for _, routine := range s.routines {
		if routine.OrganizationID == organizationID {
			result = append(result, routine)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].NextRun.Before(result[j].NextRun) })
	return result, nil
}

func (s *Store) ListChannel(_ context.Context, organizationID, workspaceID, channelID string) ([]Routine, error) {
	if organizationID == "" || workspaceID == "" || channelID == "" {
		return nil, errors.New("routine channel scope is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Routine, 0)
	for _, routine := range s.routines {
		if routine.OrganizationID == organizationID && routine.WorkspaceID == workspaceID && routine.ChannelID == channelID {
			result = append(result, routine)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].NextRun.Before(result[j].NextRun) })
	return result, nil
}

func routineKey(organizationID, workspaceID, channelID, id string) string {
	return organizationID + "/" + workspaceID + "/" + channelID + "/" + id
}

type Scheduler struct {
	store      Repository
	jobs       jobs.Queue
	authorizer Authorizer
	now        func() time.Time
}

func NewScheduler(store Repository, queue jobs.Queue, authorizer Authorizer) *Scheduler {
	if authorizer == nil {
		authorizer = AllowAll{}
	}
	return &Scheduler{store: store, jobs: queue, authorizer: authorizer, now: time.Now}
}
func (s *Scheduler) RunDue(ctx context.Context) error {
	now := s.now().UTC()
	due, err := s.store.DueContext(ctx, now, 100)
	if err != nil {
		return err
	}
	for _, routine := range due {
		if err := s.authorizer.AuthorizeRoutine(ctx, routine); err != nil {
			if advanceErr := s.store.AdvanceContext(ctx, routine.OrganizationID, routine.WorkspaceID, routine.ChannelID, routine.ID, now); advanceErr != nil {
				return advanceErr
			}
			continue
		}
		window := routine.NextRun.UTC().Format(time.RFC3339Nano)
		spec, specErr := schedule.Parse(routine.Cron, routine.Timezone, routine.Interval)
		if specErr != nil {
			return specErr
		}
		_, _, err := s.jobs.Enqueue(ctx, jobs.Spec{OrganizationID: routine.OrganizationID, WorkspaceID: routine.WorkspaceID, ChannelID: routine.ChannelID, RootThreadTS: routine.RootThreadTS, SessionID: routine.SessionID, Generation: routine.Generation, IdempotencyKey: "routine/" + routine.ChannelID + "/" + routine.ID + "/" + window, Kind: "routine", Input: routine.Input, MaxAttempts: 3, ExpiresAt: now.Add(spec.Window(routine.NextRun))})
		if err != nil {
			return err
		}
		if err := s.store.AdvanceContext(ctx, routine.OrganizationID, routine.WorkspaceID, routine.ChannelID, routine.ID, now); err != nil {
			return err
		}
	}
	return nil
}

func normalize(r Routine, now time.Time) (Routine, error) {
	if r.ID == "" || r.OrganizationID == "" || r.WorkspaceID == "" || r.ChannelID == "" || r.SessionID == "" || r.Generation <= 0 || r.OwnerID == "" || r.Input == "" {
		return Routine{}, fmt.Errorf("invalid routine")
	}
	spec, err := schedule.Parse(r.Cron, r.Timezone, r.Interval)
	if err != nil {
		return Routine{}, fmt.Errorf("invalid routine: %w", err)
	}
	r.Cron = spec.Cron
	r.Timezone = spec.Timezone
	if r.NextRun.IsZero() {
		r.NextRun = spec.Next(now)
	} else {
		r.NextRun = r.NextRun.UTC()
	}
	return r, nil
}

type Service = schedule.Service

func NewService(scheduler *Scheduler, poll time.Duration) *Service {
	return schedule.NewService(scheduler, poll)
}

var _ Repository = (*Store)(nil)

func (s *Scheduler) Trigger(ctx context.Context, routine Routine, trigger Trigger) (jobs.Job, error) {
	if trigger.OriginTag != "" {
		return jobs.Job{}, ErrLoopSuppressed
	}
	if trigger.SourceEventID == "" {
		return jobs.Job{}, fmt.Errorf("source event ID required")
	}
	if err := s.authorizer.AuthorizeRoutine(ctx, routine); err != nil {
		return jobs.Job{}, err
	}
	job, _, err := s.jobs.Enqueue(ctx, jobs.Spec{OrganizationID: routine.OrganizationID, WorkspaceID: routine.WorkspaceID, ChannelID: routine.ChannelID, RootThreadTS: routine.RootThreadTS, SessionID: routine.SessionID, Generation: routine.Generation, IdempotencyKey: "routine/" + routine.ChannelID + "/" + routine.ID + "/event/" + trigger.SourceEventID, Kind: "routine", Input: routine.Input, MaxAttempts: 3})
	return job, err
}
