package jobs

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

type MemoryQueue struct {
	mu sync.RWMutex

	now           func() time.Time
	jobs          map[types.JobID]Job
	byIdempotency map[string]types.JobID
}

func NewMemoryQueue(now func() time.Time) *MemoryQueue {
	if now == nil {
		now = time.Now
	}
	return &MemoryQueue{now: now, jobs: make(map[types.JobID]Job), byIdempotency: make(map[string]types.JobID)}
}

func (q *MemoryQueue) Enqueue(_ context.Context, spec Spec) (Job, bool, error) {
	if err := ValidateSpec(spec); err != nil {
		return Job{}, false, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	key := spec.OrganizationID + "/" + spec.IdempotencyKey
	if id, ok := q.byIdempotency[key]; ok {
		return q.jobs[id], false, nil
	}
	now := q.now().UTC()
	expiresAt := spec.ExpiresAt.UTC()
	if expiresAt.IsZero() {
		expiresAt = now.Add(24 * time.Hour)
	}
	job := Job{
		ID: types.JobID(types.NewID("job")), OrganizationID: spec.OrganizationID, WorkspaceID: spec.WorkspaceID,
		ChannelID: spec.ChannelID, RootThreadTS: spec.RootThreadTS, ReplyInChannel: spec.ReplyInChannel, SessionID: spec.SessionID, Generation: spec.Generation,
		ObservationID: spec.ObservationID, RequesterID: spec.RequesterID, IdempotencyKey: spec.IdempotencyKey, Kind: spec.Kind, Input: spec.Input,
		Images: append([]types.SlackImageRef(nil), spec.Images...),
		State:  StateQueued, MaxAttempts: spec.MaxAttempts, SteeringEpoch: 1, AvailableAt: now, CreatedAt: now, UpdatedAt: now, ExpiresAt: expiresAt, Version: 1,
		AdmissionReservationID: spec.AdmissionReservationID,
		ResolvedModel:          spec.ResolvedModel, RouteTrace: spec.RouteTrace,
	}
	q.jobs[job.ID] = job
	q.byIdempotency[key] = job.ID
	return job, true, nil
}

func (q *MemoryQueue) Claim(_ context.Context, worker types.WorkerID, leaseDuration time.Duration) (Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now().UTC()
	ids := make([]types.JobID, 0, len(q.jobs))
	for id := range q.jobs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := q.jobs[ids[i]], q.jobs[ids[j]]
		if left.AvailableAt.Equal(right.AvailableAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		return left.AvailableAt.Before(right.AvailableAt)
	})
	for _, id := range ids {
		job := q.jobs[id]
		if !job.ExpiresAt.After(now) {
			continue
		}
		if (job.State == StateLeased || job.State == StatePreparing || job.State == StateRunning) && !job.Lease.ExpiresAt.After(now) {
			if job.State == StateLeased {
				job.State = StateQueued
			} else {
				job.State = StateNeedsReconciliation
			}
			job.Lease = Lease{}
			job.Version++
			job.UpdatedAt = now
			q.jobs[id] = job
		}
		if job.State != StateQueued || job.AvailableAt.After(now) || job.Attempt >= job.MaxAttempts {
			continue
		}
		writerBusy := false
		for otherID, other := range q.jobs {
			if otherID != id && other.OrganizationID == job.OrganizationID && other.SessionID == job.SessionID && other.Generation == job.Generation && (other.State == StateLeased || other.State == StatePreparing || other.State == StateRunning || other.State == StateWaitingApproval || other.State == StateCancelling) {
				writerBusy = true
				break
			}
		}
		if writerBusy {
			continue
		}
		job.State = StateLeased
		job.Attempt++
		job.Lease = Lease{Owner: worker, Token: types.NewID("lease"), ExpiresAt: now.Add(leaseDuration), Heartbeat: now}
		job.UpdatedAt = now
		job.Version++
		q.jobs[id] = job
		return job, nil
	}
	return Job{}, ErrNoRunnableJob
}

func (q *MemoryQueue) Cancel(_ context.Context, id types.JobID, reason string) (Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.jobs[id]
	if !ok {
		return Job{}, ErrJobNotFound
	}
	switch job.State {
	case StateQueued, StateRetryWait, StateWaitingApproval:
		job.State = StateCancelled
		job.Lease = Lease{}
	case StateLeased, StatePreparing, StateRunning:
		job.State = StateCancelling
		job.SteeringEpoch++
	default:
		return Job{}, ErrInvalidState
	}
	job.FailureReason = reason
	job.UpdatedAt = q.now().UTC()
	job.Version++
	q.jobs[id] = job
	return job, nil
}
func (q *MemoryQueue) MarkCompletedUndelivered(_ context.Context, id types.JobID, reason string) (Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.jobs[id]
	if !ok {
		return Job{}, ErrJobNotFound
	}
	if job.State != StateSucceeded && job.State != StateCompletedUndelivered {
		return Job{}, ErrInvalidState
	}
	job.State = StateCompletedUndelivered
	job.FailureReason = reason
	job.UpdatedAt = q.now().UTC()
	job.Version++
	q.jobs[id] = job
	return job, nil
}

func (q *MemoryQueue) Transition(_ context.Context, id types.JobID, leaseToken string, to State, mutate func(*Job)) (Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.jobs[id]
	if !ok {
		return Job{}, ErrJobNotFound
	}
	if job.Lease.Token == "" || job.Lease.Token != leaseToken || !job.Lease.ExpiresAt.After(q.now().UTC()) {
		return Job{}, ErrLeaseLost
	}
	if !CanTransition(job.State, to) {
		return Job{}, ErrInvalidState
	}
	job.State = to
	if mutate != nil {
		mutate(&job)
	}
	if to == StateSucceeded || to == StateFailed || to == StateCancelled || to == StateRetryWait || to == StateNeedsReconciliation || to == StateWaitingApproval {
		job.Lease = Lease{}
	}
	job.UpdatedAt = q.now().UTC()
	job.Version++
	q.jobs[id] = job
	return job, nil
}

func (q *MemoryQueue) SetProgressMessageTS(_ context.Context, id types.JobID, leaseToken, messageTS string) (Job, error) {
	if messageTS == "" {
		return Job{}, ErrInvalidState
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.jobs[id]
	if !ok {
		return Job{}, ErrJobNotFound
	}
	if job.State != StateRunning || job.Lease.Token == "" || job.Lease.Token != leaseToken || !job.Lease.ExpiresAt.After(q.now().UTC()) {
		return Job{}, ErrLeaseLost
	}
	job.ProgressMessageTS = messageTS
	job.UpdatedAt = q.now().UTC()
	job.Version++
	q.jobs[id] = job
	return job, nil
}

func (q *MemoryQueue) SuspendForApproval(ctx context.Context, id types.JobID, leaseToken, approvalID string) (Job, error) {
	if approvalID == "" {
		return Job{}, ErrInvalidState
	}
	return q.Transition(ctx, id, leaseToken, StateWaitingApproval, func(job *Job) {
		job.ApprovalID = approvalID
		job.ApprovedActionHash = ""
	})
}

func (q *MemoryQueue) ResumeFromApproval(_ context.Context, id types.JobID, approvalID, actionHash string) (Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.jobs[id]
	if !ok {
		return Job{}, ErrJobNotFound
	}
	if job.State != StateWaitingApproval || job.ApprovalID != approvalID || actionHash == "" {
		return Job{}, ErrInvalidState
	}
	job.State = StateQueued
	job.ApprovedActionHash = actionHash
	if job.Attempt > 0 {
		job.Attempt--
	}
	job.AvailableAt = q.now().UTC()
	job.SteeringEpoch++
	job.UpdatedAt = q.now().UTC()
	job.Version++
	q.jobs[id] = job
	return job, nil
}

func (q *MemoryQueue) Heartbeat(_ context.Context, id types.JobID, leaseToken string, duration time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.jobs[id]
	if !ok {
		return ErrJobNotFound
	}
	now := q.now().UTC()
	if job.Lease.Token != leaseToken || !job.Lease.ExpiresAt.After(now) {
		return ErrLeaseLost
	}
	job.Lease.Heartbeat = now
	job.Lease.ExpiresAt = now.Add(duration)
	job.UpdatedAt = now
	job.Version++
	q.jobs[id] = job
	return nil
}

func (q *MemoryQueue) Requeue(_ context.Context, id types.JobID, leaseToken, reason string, delay time.Duration) (Job, error) {
	return q.Transition(context.Background(), id, leaseToken, StateRetryWait, func(job *Job) {
		job.FailureReason = reason
		job.AvailableAt = q.now().UTC().Add(delay)
	})
}

func (q *MemoryQueue) ReleaseRetryWait(_ context.Context, id types.JobID) (Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.jobs[id]
	if !ok {
		return Job{}, ErrJobNotFound
	}
	if job.State != StateRetryWait || job.AvailableAt.After(q.now().UTC()) {
		return Job{}, ErrInvalidState
	}
	if job.Attempt >= job.MaxAttempts {
		job.State = StateFailed
		job.FailureReason = "attempts_exhausted"
	} else {
		job.State = StateQueued
	}
	job.Version++
	job.UpdatedAt = q.now().UTC()
	q.jobs[id] = job
	return job, nil
}

func (q *MemoryQueue) Get(_ context.Context, id types.JobID) (Job, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	job, ok := q.jobs[id]
	if !ok {
		return Job{}, ErrJobNotFound
	}
	return job, nil
}

func (q *MemoryQueue) Count(_ context.Context) (int, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.jobs), nil
}

func (q *MemoryQueue) List(_ context.Context) ([]Job, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	result := make([]Job, 0, len(q.jobs))
	for _, job := range q.jobs {
		result = append(result, job)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (q *MemoryQueue) ListReconciliation(_ context.Context, now time.Time) ([]Job, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	result := make([]Job, 0)
	for _, job := range q.jobs {
		switch job.State {
		case StateWaitingApproval, StateNeedsReconciliation:
			result = append(result, job)
		case StateQueued:
			if !job.ExpiresAt.After(now) || job.Attempt >= job.MaxAttempts {
				result = append(result, job)
			}
		case StateRetryWait:
			if !job.AvailableAt.After(now) {
				result = append(result, job)
			}
		case StateSucceeded:
			if !job.FinalDeliveryEnqueued && job.ExpiresAt.After(now) {
				result = append(result, job)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (q *MemoryQueue) MarkFinalDeliveryEnqueued(_ context.Context, id types.JobID) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	job, ok := q.jobs[id]
	if !ok {
		return ErrJobNotFound
	}
	if job.State != StateSucceeded {
		return ErrInvalidState
	}
	job.FinalDeliveryEnqueued = true
	job.UpdatedAt = q.now().UTC()
	job.Version++
	q.jobs[id] = job
	return nil
}

func (q *MemoryQueue) ListOrganization(_ context.Context, organizationID string) ([]Job, error) {
	if organizationID == "" {
		return nil, errors.New("organization_id is required")
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	result := make([]Job, 0)
	for _, job := range q.jobs {
		if job.OrganizationID == organizationID {
			result = append(result, job)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	if len(result) > organizationListLimit {
		result = result[:organizationListLimit]
	}
	return result, nil
}
