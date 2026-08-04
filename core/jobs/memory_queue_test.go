package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

func spec() Spec {
	return Spec{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", RootThreadTS: "100.1", SessionID: "session", Generation: 1, IdempotencyKey: "obs/reply", Kind: "echo", Input: "hello", MaxAttempts: 2}
}

func TestOneWriterPerSessionGenerationAndJobOperations(t *testing.T) {
	queue := NewMemoryQueue(nil)
	base := Spec{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", RootThreadTS: "1", SessionID: "session", Generation: 1, Kind: "agent", Input: "x", MaxAttempts: 2}
	base.IdempotencyKey = "one"
	first, _, err := queue.Enqueue(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	base.IdempotencyKey = "two"
	second, _, err := queue.Enqueue(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := queue.Claim(context.Background(), "worker", time.Minute)
	if err != nil || claimed.ID != first.ID {
		t.Fatalf("claimed=%s err=%v", claimed.ID, err)
	}
	if _, err := queue.Claim(context.Background(), "other", time.Minute); !errors.Is(err, ErrNoRunnableJob) {
		t.Fatalf("second writer claimed: %v", err)
	}
	if _, err := queue.Cancel(context.Background(), claimed.ID, "operator"); err != nil {
		t.Fatal(err)
	}
	cancelled, err := queue.Transition(context.Background(), claimed.ID, claimed.Lease.Token, StateCancelled, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != StateCancelled {
		t.Fatal(cancelled.State)
	}
	next, err := queue.Claim(context.Background(), "other", time.Minute)
	if err != nil || next.ID != second.ID {
		t.Fatalf("next=%s err=%v", next.ID, err)
	}
}

func TestCompletedUndeliveredIsExplicit(t *testing.T) {
	queue := NewMemoryQueue(nil)
	spec := Spec{OrganizationID: "o", WorkspaceID: "w", ChannelID: "c", RootThreadTS: "r", SessionID: "s", Generation: 1, IdempotencyKey: "k", Kind: "agent", MaxAttempts: 1}
	job, _, _ := queue.Enqueue(context.Background(), spec)
	job, _ = queue.Claim(context.Background(), "worker", time.Minute)
	job, _ = queue.Transition(context.Background(), job.ID, job.Lease.Token, StateRunning, nil)
	job, _ = queue.Transition(context.Background(), job.ID, job.Lease.Token, StateSucceeded, nil)
	job, err := queue.MarkCompletedUndelivered(context.Background(), job.ID, "delivery exhausted")
	if err != nil || job.State != StateCompletedUndelivered {
		t.Fatalf("job=%#v err=%v", job, err)
	}
}

func TestQueueIdempotencyAndLeaseFencing(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	clock := now
	queue := NewMemoryQueue(func() time.Time { return clock })
	first, created, err := queue.Enqueue(context.Background(), spec())
	if err != nil || !created {
		t.Fatalf("enqueue: created=%v err=%v", created, err)
	}
	second, created, err := queue.Enqueue(context.Background(), spec())
	if err != nil || created || first.ID != second.ID {
		t.Fatalf("idempotency failed: %#v %#v %v", first, second, err)
	}

	claimed, err := queue.Claim(context.Background(), "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Attempt != 1 || claimed.State != StateLeased {
		t.Fatalf("bad claim: %#v", claimed)
	}
	if _, err := queue.Transition(context.Background(), claimed.ID, "forged", StateRunning, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("forged lease accepted: %v", err)
	}
	running, err := queue.Transition(context.Background(), claimed.ID, claimed.Lease.Token, StateRunning, nil)
	if err != nil || running.State != StateRunning {
		t.Fatalf("start: %#v %v", running, err)
	}
}

func TestExpiredRunningLeaseRequiresReconciliation(t *testing.T) {
	clock := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	queue := NewMemoryQueue(func() time.Time { return clock })
	job, _, _ := queue.Enqueue(context.Background(), spec())
	job, _ = queue.Claim(context.Background(), "worker", time.Minute)
	job, _ = queue.Transition(context.Background(), job.ID, job.Lease.Token, StateRunning, nil)
	clock = clock.Add(2 * time.Minute)
	if _, err := queue.Claim(context.Background(), "other", time.Minute); !errors.Is(err, ErrNoRunnableJob) {
		t.Fatalf("running job was blindly requeued: %v", err)
	}
	got, _ := queue.Get(context.Background(), job.ID)
	if got.State != StateNeedsReconciliation {
		t.Fatalf("got state %s", got.State)
	}
}

func TestFiniteAttemptsExhaust(t *testing.T) {
	clock := time.Now().UTC()
	queue := NewMemoryQueue(func() time.Time { return clock })
	job, _, _ := queue.Enqueue(context.Background(), spec())
	for attempt := 0; attempt < 2; attempt++ {
		job, _ = queue.Claim(context.Background(), types.WorkerID("worker"), time.Minute)
		job, _ = queue.Transition(context.Background(), job.ID, job.Lease.Token, StateRunning, nil)
		job, _ = queue.Requeue(context.Background(), job.ID, job.Lease.Token, "transient", 0)
		job, _ = queue.ReleaseRetryWait(context.Background(), job.ID)
	}
	if job.State != StateFailed || job.FailureReason != "attempts_exhausted" {
		t.Fatalf("attempts did not exhaust: %#v", job)
	}
}

func TestInvalidTransitionRejected(t *testing.T) {
	queue := NewMemoryQueue(nil)
	job, _, _ := queue.Enqueue(context.Background(), spec())
	job, _ = queue.Claim(context.Background(), "worker", time.Minute)
	if _, err := queue.Transition(context.Background(), job.ID, job.Lease.Token, StateSucceeded, nil); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("invalid transition accepted: %v", err)
	}
}

func TestApprovalResumeRestoresFinalAttemptClaimability(t *testing.T) {
	queue := NewMemoryQueue(nil)
	ctx := context.Background()
	job, _, err := queue.Enqueue(ctx, Spec{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", RootThreadTS: "1", SessionID: "session", Generation: 1, IdempotencyKey: "approval-final-attempt", Kind: "agent", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	job, _ = queue.Claim(ctx, "worker-1", time.Minute)
	job, _ = queue.Transition(ctx, job.ID, job.Lease.Token, StateRunning, nil)
	job, _ = queue.SuspendForApproval(ctx, job.ID, job.Lease.Token, "approval-1")
	resumed, err := queue.ResumeFromApproval(ctx, job.ID, "approval-1", "sha256:approved")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != StateQueued || resumed.Attempt != 0 {
		t.Fatalf("resumed job is not claimable: %#v", resumed)
	}
	claimed, err := queue.Claim(ctx, "worker-2", time.Minute)
	if err != nil || claimed.Attempt != 1 {
		t.Fatalf("fresh worker could not claim approved final attempt: %#v err=%v", claimed, err)
	}
}

func TestReconciliationListExcludesOrdinaryAndCheckpointedJobs(t *testing.T) {
	now := time.Now().UTC()
	queue := NewMemoryQueue(func() time.Time { return now })
	job, _, err := queue.Enqueue(context.Background(), Spec{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", RootThreadTS: "1", SessionID: "session", Generation: 1, IdempotencyKey: "reconcile-filter", Kind: "agent", MaxAttempts: 2, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if candidates, _ := queue.ListReconciliation(context.Background(), now); len(candidates) != 0 {
		t.Fatalf("ordinary queued job entered reconciliation: %#v", candidates)
	}
	job, _ = queue.Claim(context.Background(), "worker", time.Minute)
	job, _ = queue.Transition(context.Background(), job.ID, job.Lease.Token, StateRunning, nil)
	job, _ = queue.Transition(context.Background(), job.ID, job.Lease.Token, StateSucceeded, nil)
	if candidates, _ := queue.ListReconciliation(context.Background(), now); len(candidates) != 1 || candidates[0].ID != job.ID {
		t.Fatalf("uncheckpointed succeeded job missing: %#v", candidates)
	}
	if err := queue.MarkFinalDeliveryEnqueued(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if candidates, _ := queue.ListReconciliation(context.Background(), now); len(candidates) != 0 {
		t.Fatalf("checkpointed succeeded job remained in reconciliation: %#v", candidates)
	}
}
