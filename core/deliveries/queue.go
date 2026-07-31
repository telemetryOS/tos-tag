package deliveries

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusLeased    Status = "leased"
	StatusRetryWait Status = "retry_wait"
	StatusDelivered Status = "delivered"
	StatusAbandoned Status = "abandoned"
)

var (
	ErrNoPendingDelivery = errors.New("no pending delivery")
	ErrDeliveryNotFound  = errors.New("delivery not found")
	ErrDeliveryLeaseLost = errors.New("delivery lease lost")
)

type Lease struct {
	Owner     types.WorkerID `json:"owner"`
	Token     string         `json:"token"`
	ExpiresAt time.Time      `json:"expires_at"`
}

type Record struct {
	ID             types.DeliveryID       `json:"id"`
	OrganizationID string                 `json:"organization_id"`
	JobID          types.JobID            `json:"job_id"`
	IdempotencyKey string                 `json:"idempotency_key"`
	Destination    types.SlackDestination `json:"destination"`
	Result         types.SlackResult      `json:"result"`
	Status         Status                 `json:"status"`
	Attempt        int                    `json:"attempt"`
	MaxAttempts    int                    `json:"max_attempts"`
	RetryAt        time.Time              `json:"retry_at"`
	Lease          Lease                  `json:"lease"`
	SlackMessageTS string                 `json:"slack_message_ts,omitempty"`
	FailureReason  string                 `json:"failure_reason,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
	ExpiresAt      time.Time              `json:"expires_at"`
	Version        int64                  `json:"version"`
}

type Spec struct {
	OrganizationID string
	JobID          types.JobID
	IdempotencyKey string
	Destination    types.SlackDestination
	Result         types.SlackResult
	MaxAttempts    int
	ExpiresAt      time.Time
}

type Queue interface {
	Enqueue(context.Context, Spec) (Record, bool, error)
	Claim(context.Context, types.WorkerID, time.Duration) (Record, error)
	Complete(context.Context, types.DeliveryID, string, types.SlackDeliveryResult) (Record, error)
	Retry(context.Context, types.DeliveryID, string, string, time.Duration) (Record, error)
	Abandon(context.Context, types.DeliveryID, string, string) (Record, error)
	Get(context.Context, types.DeliveryID) (Record, error)
	List(context.Context) ([]Record, error)
	ListOrganization(context.Context, string) ([]Record, error)
}

type MemoryQueue struct {
	mu      sync.RWMutex
	now     func() time.Time
	records map[types.DeliveryID]Record
	byKey   map[string]types.DeliveryID
}

func NewMemoryQueue(now func() time.Time) *MemoryQueue {
	if now == nil {
		now = time.Now
	}
	return &MemoryQueue{now: now, records: make(map[types.DeliveryID]Record), byKey: make(map[string]types.DeliveryID)}
}

func (q *MemoryQueue) Enqueue(_ context.Context, spec Spec) (Record, bool, error) {
	if spec.OrganizationID == "" || spec.JobID == "" || spec.IdempotencyKey == "" || spec.Destination.TeamID == "" || spec.Destination.ChannelID == "" || spec.MaxAttempts <= 0 {
		return Record{}, false, errors.New("invalid delivery specification")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	key := spec.OrganizationID + "/" + spec.IdempotencyKey
	if id, ok := q.byKey[key]; ok {
		return q.records[id], false, nil
	}
	now := q.now().UTC()
	expiresAt := spec.ExpiresAt.UTC()
	if expiresAt.IsZero() {
		expiresAt = now.Add(24 * time.Hour)
	}
	record := Record{ID: types.DeliveryID(types.NewID("dlv")), OrganizationID: spec.OrganizationID, JobID: spec.JobID, IdempotencyKey: spec.IdempotencyKey, Destination: spec.Destination, Result: spec.Result, Status: StatusPending, MaxAttempts: spec.MaxAttempts, RetryAt: now, CreatedAt: now, UpdatedAt: now, ExpiresAt: expiresAt, Version: 1}
	q.records[record.ID], q.byKey[key] = record, record.ID
	return record, true, nil
}

func (q *MemoryQueue) Claim(_ context.Context, worker types.WorkerID, duration time.Duration) (Record, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now().UTC()
	ids := make([]types.DeliveryID, 0, len(q.records))
	for id := range q.records {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return q.records[ids[i]].CreatedAt.Before(q.records[ids[j]].CreatedAt) })
	for _, id := range ids {
		record := q.records[id]
		if !record.ExpiresAt.After(now) {
			continue
		}
		if record.Status == StatusLeased && !record.Lease.ExpiresAt.After(now) {
			record.Status, record.Lease = StatusPending, Lease{}
			record.Version++
			q.records[id] = record
		}
		if (record.Status != StatusPending && record.Status != StatusRetryWait) || record.RetryAt.After(now) {
			continue
		}
		if record.Attempt >= record.MaxAttempts {
			record.Status, record.FailureReason = StatusAbandoned, "attempts_exhausted"
			record.Version++
			q.records[id] = record
			continue
		}
		record.Status = StatusLeased
		record.Attempt++
		record.Lease = Lease{Owner: worker, Token: types.NewID("lease"), ExpiresAt: now.Add(duration)}
		record.UpdatedAt, record.Version = now, record.Version+1
		q.records[id] = record
		return record, nil
	}
	return Record{}, ErrNoPendingDelivery
}

func (q *MemoryQueue) Complete(_ context.Context, id types.DeliveryID, token string, result types.SlackDeliveryResult) (Record, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	record, err := q.leased(id, token)
	if err != nil {
		return Record{}, err
	}
	record.Status, record.SlackMessageTS, record.Lease = StatusDelivered, result.MessageTS, Lease{}
	record.UpdatedAt, record.Version = q.now().UTC(), record.Version+1
	q.records[id] = record
	return record, nil
}

func (q *MemoryQueue) Retry(_ context.Context, id types.DeliveryID, token, reason string, delay time.Duration) (Record, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	record, err := q.leased(id, token)
	if err != nil {
		return Record{}, err
	}
	record.Lease = Lease{}
	record.FailureReason = reason
	record.RetryAt = q.now().UTC().Add(delay)
	if record.Attempt >= record.MaxAttempts {
		record.Status = StatusAbandoned
	} else {
		record.Status = StatusRetryWait
	}
	record.UpdatedAt, record.Version = q.now().UTC(), record.Version+1
	q.records[id] = record
	return record, nil
}

func (q *MemoryQueue) Abandon(_ context.Context, id types.DeliveryID, token, reason string) (Record, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	record, err := q.leased(id, token)
	if err != nil {
		return Record{}, err
	}
	record.Status, record.Lease, record.FailureReason = StatusAbandoned, Lease{}, reason
	record.UpdatedAt, record.Version = q.now().UTC(), record.Version+1
	q.records[id] = record
	return record, nil
}

func (q *MemoryQueue) leased(id types.DeliveryID, token string) (Record, error) {
	record, ok := q.records[id]
	if !ok {
		return Record{}, ErrDeliveryNotFound
	}
	if record.Status != StatusLeased || record.Lease.Token != token || !record.Lease.ExpiresAt.After(q.now().UTC()) {
		return Record{}, ErrDeliveryLeaseLost
	}
	return record, nil
}

func (q *MemoryQueue) Get(_ context.Context, id types.DeliveryID) (Record, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	record, ok := q.records[id]
	if !ok {
		return Record{}, ErrDeliveryNotFound
	}
	return record, nil
}

func (q *MemoryQueue) List(_ context.Context) ([]Record, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	result := make([]Record, 0, len(q.records))
	for _, record := range q.records {
		result = append(result, record)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (q *MemoryQueue) ListOrganization(_ context.Context, organizationID string) ([]Record, error) {
	if organizationID == "" {
		return nil, errors.New("organization_id is required")
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	result := make([]Record, 0)
	for _, record := range q.records {
		if record.OrganizationID == organizationID {
			result = append(result, record)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}
