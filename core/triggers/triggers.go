// Package triggers owns durable event subscriptions and turns due heartbeats
// into ordinary, idempotent jobs only after a tool-free classifier gate.
package triggers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/core/jobs"
	"github.com/telemetryos/tos-tag/core/schedule"
	"github.com/telemetryos/tos-tag/types"
)

type Kind string

const KindHeartbeat Kind = "heartbeat"

var ErrScopeConflict = errors.New("trigger subscription scope conflict")

type Subscription struct {
	ID             string          `json:"id" bson:"public_id"`
	OrganizationID string          `json:"organization_id" bson:"organization_id"`
	WorkspaceID    string          `json:"workspace_id" bson:"workspace_id"`
	ChannelID      string          `json:"channel_id" bson:"channel_id"`
	RootThreadTS   string          `json:"root_thread_ts,omitempty" bson:"root_thread_ts,omitempty"`
	SessionID      types.SessionID `json:"session_id" bson:"session_id"`
	Generation     int64           `json:"generation" bson:"generation"`
	OwnerID        string          `json:"owner_id" bson:"owner_id"`
	Kind           Kind            `json:"kind" bson:"kind"`
	Instruction    string          `json:"instruction" bson:"instruction"`
	Cron           string          `json:"cron,omitempty" bson:"cron,omitempty"`
	Timezone       string          `json:"timezone,omitempty" bson:"timezone,omitempty"`
	Interval       time.Duration   `json:"interval" bson:"interval"`
	NextRun        time.Time       `json:"next_run" bson:"next_run"`
	ClassifierGate bool            `json:"classifier_gate" bson:"classifier_gate"`
	MinConfidence  float64         `json:"min_confidence" bson:"min_confidence"`
	Enabled        bool            `json:"enabled" bson:"enabled"`
	Version        int64           `json:"version" bson:"version"`
	CreatedAt      time.Time       `json:"created_at" bson:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at" bson:"updated_at"`
}

type GateDecision struct {
	Accepted bool                         `json:"accepted"`
	Decision types.ClassificationDecision `json:"decision"`
	PackID   types.RevisionID             `json:"context_pack_revision_id,omitempty"`
}

type Gate interface {
	EvaluateHeartbeat(context.Context, Subscription, string) (GateDecision, error)
}

type GateFunc func(context.Context, Subscription, string) (GateDecision, error)

func (f GateFunc) EvaluateHeartbeat(ctx context.Context, subscription Subscription, window string) (GateDecision, error) {
	return f(ctx, subscription, window)
}

type Authorizer interface {
	AuthorizeTrigger(context.Context, Subscription) error
}

type AuthorizerFunc func(context.Context, Subscription) error

func (f AuthorizerFunc) AuthorizeTrigger(ctx context.Context, subscription Subscription) error {
	return f(ctx, subscription)
}

type Repository interface {
	PutContext(context.Context, Subscription) (Subscription, error)
	GetContext(context.Context, string, string) (Subscription, error)
	List(context.Context, string) ([]Subscription, error)
	DueContext(context.Context, time.Time, int) ([]Subscription, error)
	AdvanceContext(context.Context, string, string, time.Time) error
}

type Store struct {
	mu            sync.Mutex
	now           func() time.Time
	subscriptions map[string]Subscription
}

func NewStore(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{now: now, subscriptions: make(map[string]Subscription)}
}

func (s *Store) PutContext(_ context.Context, subscription Subscription) (Subscription, error) {
	var err error
	subscription, err = Normalize(subscription, s.now().UTC())
	if err != nil {
		return Subscription{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	key := subscription.OrganizationID + "/" + subscription.ID
	if previous, ok := s.subscriptions[key]; ok {
		if previous.WorkspaceID != subscription.WorkspaceID || previous.ChannelID != subscription.ChannelID {
			return Subscription{}, ErrScopeConflict
		}
		subscription.Version = previous.Version + 1
		subscription.CreatedAt = previous.CreatedAt
	} else {
		subscription.Version = 1
		subscription.CreatedAt = now
	}
	subscription.UpdatedAt = now
	s.subscriptions[key] = subscription
	return subscription, nil
}

func (s *Store) GetContext(_ context.Context, organizationID, id string) (Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.subscriptions[organizationID+"/"+id]
	if !ok {
		return Subscription{}, errors.New("trigger subscription not found")
	}
	return value, nil
}

func (s *Store) List(_ context.Context, organizationID string) ([]Subscription, error) {
	if organizationID == "" {
		return nil, errors.New("organization is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]Subscription, 0)
	for _, subscription := range s.subscriptions {
		if subscription.OrganizationID == organizationID {
			values = append(values, subscription)
		}
	}
	sortSubscriptions(values)
	return values, nil
}

func (s *Store) DueContext(_ context.Context, now time.Time, limit int) ([]Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]Subscription, 0)
	for _, subscription := range s.subscriptions {
		if subscription.Enabled && !subscription.NextRun.After(now) {
			values = append(values, subscription)
		}
	}
	sortSubscriptions(values)
	if limit > 0 && len(values) > limit {
		values = values[:limit]
	}
	return values, nil
}

func (s *Store) AdvanceContext(_ context.Context, organizationID, id string, from time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := organizationID + "/" + id
	subscription, ok := s.subscriptions[key]
	if !ok {
		return errors.New("trigger subscription not found")
	}
	spec, err := schedule.Parse(subscription.Cron, subscription.Timezone, subscription.Interval)
	if err != nil {
		return err
	}
	subscription.NextRun = spec.Advance(subscription.NextRun, from)
	subscription.Version++
	subscription.UpdatedAt = s.now().UTC()
	s.subscriptions[key] = subscription
	return nil
}

func sortSubscriptions(values []Subscription) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].NextRun.Equal(values[j].NextRun) {
			return values[i].ID < values[j].ID
		}
		return values[i].NextRun.Before(values[j].NextRun)
	})
}

func Validate(subscription Subscription) error {
	_, err := Normalize(subscription, time.Now().UTC())
	return err
}

// Normalize validates a subscription and derives its first run when needed.
// Existing fixed-interval records remain valid while new subscriptions prefer
// a cron expression plus an explicit IANA timezone.
func Normalize(subscription Subscription, now time.Time) (Subscription, error) {
	if subscription.ID == "" || subscription.OrganizationID == "" || subscription.WorkspaceID == "" || subscription.ChannelID == "" || subscription.SessionID == "" || subscription.Generation <= 0 || subscription.OwnerID == "" || subscription.Kind != KindHeartbeat || subscription.Instruction == "" || !subscription.ClassifierGate || subscription.MinConfidence < 0 || subscription.MinConfidence > 1 {
		return Subscription{}, errors.New("invalid trigger subscription")
	}
	spec, err := schedule.Parse(subscription.Cron, subscription.Timezone, subscription.Interval)
	if err != nil {
		return Subscription{}, fmt.Errorf("invalid trigger subscription: %w", err)
	}
	subscription.Cron = spec.Cron
	subscription.Timezone = spec.Timezone
	if subscription.NextRun.IsZero() {
		subscription.NextRun = spec.Next(now)
	} else {
		subscription.NextRun = subscription.NextRun.UTC()
	}
	return subscription, nil
}

type Scheduler struct {
	store      Repository
	jobs       jobs.Queue
	gate       Gate
	authorizer Authorizer
	now        func() time.Time
}

func NewScheduler(store Repository, queue jobs.Queue, gate Gate, authorizer Authorizer) (*Scheduler, error) {
	if store == nil || queue == nil || gate == nil || authorizer == nil {
		return nil, errors.New("trigger scheduler dependencies are required")
	}
	return &Scheduler{store: store, jobs: queue, gate: gate, authorizer: authorizer, now: time.Now}, nil
}

func (s *Scheduler) RunDue(ctx context.Context) error {
	now := s.now().UTC()
	due, err := s.store.DueContext(ctx, now, 100)
	if err != nil {
		return err
	}
	for _, subscription := range due {
		window := subscription.NextRun.UTC().Format(time.RFC3339Nano)
		if err := s.authorizer.AuthorizeTrigger(ctx, subscription); err != nil {
			if advanceErr := s.store.AdvanceContext(ctx, subscription.OrganizationID, subscription.ID, now); advanceErr != nil {
				return advanceErr
			}
			continue
		}
		decision, gateErr := s.gate.EvaluateHeartbeat(ctx, subscription, window)
		if gateErr == nil && decision.Accepted && decision.Decision.Confidence >= subscription.MinConfidence {
			spec, specErr := schedule.Parse(subscription.Cron, subscription.Timezone, subscription.Interval)
			if specErr != nil {
				return specErr
			}
			_, _, err = s.jobs.Enqueue(ctx, jobs.Spec{
				OrganizationID: subscription.OrganizationID, WorkspaceID: subscription.WorkspaceID,
				ChannelID: subscription.ChannelID, RootThreadTS: subscription.RootThreadTS,
				RequesterID: subscription.OwnerID,
				SessionID:   subscription.SessionID, Generation: subscription.Generation,
				IdempotencyKey: "trigger/" + subscription.ID + "/" + window,
				Kind:           "heartbeat", Input: subscription.Instruction, MaxAttempts: 3,
				ExpiresAt: now.Add(spec.Window(subscription.NextRun)),
			})
			if err != nil {
				return fmt.Errorf("enqueue heartbeat trigger: %w", err)
			}
		}
		if err := s.store.AdvanceContext(ctx, subscription.OrganizationID, subscription.ID, now); err != nil {
			return err
		}
	}
	return nil
}

type Service struct {
	scheduler *Scheduler
	poll      time.Duration
	cancel    context.CancelFunc
	done      chan struct{}
}

func NewService(scheduler *Scheduler, poll time.Duration) *Service {
	return &Service{scheduler: scheduler, poll: poll}
}

func (s *Service) Start(parent context.Context) {
	if s == nil || s.scheduler == nil || s.poll <= 0 || s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(s.poll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.scheduler.RunDue(ctx)
			}
		}
	}()
}

func (s *Service) Stop(ctx context.Context) error {
	if s == nil || s.cancel == nil {
		return nil
	}
	s.cancel()
	select {
	case <-s.done:
		s.cancel = nil
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var _ Repository = (*Store)(nil)
