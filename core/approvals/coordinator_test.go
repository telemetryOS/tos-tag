package approvals

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/admission"
	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/deliveries"
	"github.com/telemetryos/tos-tag/core/jobs"
	"github.com/telemetryos/tos-tag/types"
)

type recordingAdmissionCompleter struct{ completed []string }

func (r *recordingAdmissionCompleter) Complete(_ context.Context, id string) {
	r.completed = append(r.completed, id)
}

type failingAuditAppender struct{}

func (failingAuditAppender) Append(context.Context, audit.AppendRequest) (audit.Receipt, error) {
	return audit.Receipt{}, errors.New("audit unavailable")
}

func TestCoordinatorSuspendsNotifiesAndResumesFreshWorker(t *testing.T) {
	ctx := context.Background()
	queue := jobs.NewMemoryQueue(nil)
	deliveryQueue := deliveries.NewMemoryQueue(nil)
	store := NewStore()
	auditLog, err := audit.NewMemoryAppender([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(store, queue, deliveryQueue, auditLog, ApproverAuthorizerFunc(func(_ context.Context, _, _, _, userID string) error {
		if userID != "human" {
			return errors.New("approver denied")
		}
		return nil
	}), admission.NewMemory(nil))
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := queue.Enqueue(ctx, jobs.Spec{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", RootThreadTS: "100.1", SessionID: "session", Generation: 1, IdempotencyKey: "job", Kind: "agent", MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	job, err = queue.Claim(ctx, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	job, err = queue.Transition(ctx, job.ID, job.Lease.Token, jobs.StateRunning, nil)
	if err != nil {
		t.Fatal(err)
	}
	action := Action{OrganizationID: "org", JobID: string(job.ID), WorkspaceID: "team", ChannelID: "channel", ThreadTS: "100.1", ToolID: "linear", ToolVersion: "1", OperationID: "create", Arguments: map[string]any{"title": "incident"}, Destination: "team/channel", Risk: "write"}
	approval, err := store.RequestContext(ctx, action, "agent:"+string(job.ID), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.SuspendAndNotify(ctx, RequestScope{JobID: job.ID, LeaseToken: job.Lease.Token, WorkspaceID: "team", ChannelID: "channel", ThreadTS: "100.1"}, approval); err != nil {
		t.Fatal(err)
	}
	waiting, _ := queue.Get(ctx, job.ID)
	if waiting.State != jobs.StateWaitingApproval || waiting.Lease.Token != "" || waiting.ApprovalID != approval.ID {
		t.Fatalf("unexpected suspended job: %#v", waiting)
	}
	deliveryRecords, _ := deliveryQueue.List(ctx)
	if len(deliveryRecords) != 1 || deliveryRecords[0].Result.Segments[0].Kind != types.SlackSegmentApproval {
		t.Fatalf("missing Slack-native approval delivery: %#v", deliveryRecords)
	}
	if err := coordinator.HandleSlackDecision(ctx, SlackDecision{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", UserID: "human", ApprovalID: approval.ID, MessageTS: "200.2", Approve: true}); err != nil {
		t.Fatal(err)
	}
	resumed, _ := queue.Get(ctx, job.ID)
	if resumed.State != jobs.StateQueued || resumed.ApprovedActionHash != approval.ActionHash || resumed.SteeringEpoch <= job.SteeringEpoch {
		t.Fatalf("unexpected resumed job: %#v", resumed)
	}
	if _, err := store.ConsumeContext(ctx, "org", approval.ID, action); err != nil {
		t.Fatal(err)
	}
	deliveryRecords, _ = deliveryQueue.List(ctx)
	if len(deliveryRecords) != 2 {
		t.Fatalf("expected approval request and resolved update, got %d", len(deliveryRecords))
	}
	resolved := deliveryRecords[1]
	if resolved.Destination.UpdateTS != "200.2" || resolved.Result.Segments[0].Kind != types.SlackSegmentApproval || resolved.Result.Segments[0].Approval.Status != "approved" || resolved.Result.Segments[0].Approval.ResolvedAt.IsZero() {
		t.Fatalf("resolved approval was not an in-place Slack update: %#v", resolved)
	}
}

func TestCoordinatorRejectsUnauthorizedAndSelfApproval(t *testing.T) {
	ctx := context.Background()
	store := NewStore()
	queue := jobs.NewMemoryQueue(nil)
	deliveryQueue := deliveries.NewMemoryQueue(nil)
	auditLog, _ := audit.NewMemoryAppender([]byte("01234567890123456789012345678901"))
	coordinator, err := NewCoordinator(store, queue, deliveryQueue, auditLog, ApproverAuthorizerFunc(func(_ context.Context, _, _, _, userID string) error {
		if userID == "guest" {
			return errors.New("not authorized")
		}
		return nil
	}), &recordingAdmissionCompleter{})
	if err != nil {
		t.Fatal(err)
	}
	action := Action{OrganizationID: "org", JobID: "job", WorkspaceID: "team", ChannelID: "channel", ToolID: "tool", ToolVersion: "1", OperationID: "write", Arguments: map[string]any{}, Destination: "team/channel", Risk: "write"}
	approval, err := store.RequestContext(ctx, action, "requester", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, userID := range []string{"guest", "requester"} {
		if err := coordinator.HandleSlackDecision(ctx, SlackDecision{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", UserID: userID, ApprovalID: approval.ID, Approve: true}); err == nil {
			t.Fatalf("approval by %q was accepted", userID)
		}
	}
	unchanged, _ := store.GetContext(ctx, "org", approval.ID)
	if !unchanged.ApprovedAt.IsZero() {
		t.Fatal("rejected approval mutated durable state")
	}
}

func TestCoordinatorFailsClosedBeforeApprovalWhenAuditUnavailable(t *testing.T) {
	ctx := context.Background()
	queue := jobs.NewMemoryQueue(nil)
	store := NewStore()
	coordinator, err := NewCoordinator(store, queue, deliveries.NewMemoryQueue(nil), failingAuditAppender{}, ApproverAuthorizerFunc(func(context.Context, string, string, string, string) error { return nil }), &recordingAdmissionCompleter{})
	if err != nil {
		t.Fatal(err)
	}
	job, _, _ := queue.Enqueue(ctx, jobs.Spec{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", RootThreadTS: "1", SessionID: "session", Generation: 1, IdempotencyKey: "job-audit", Kind: "agent", MaxAttempts: 2})
	job, _ = queue.Claim(ctx, "worker", time.Minute)
	job, _ = queue.Transition(ctx, job.ID, job.Lease.Token, jobs.StateRunning, nil)
	action := Action{OrganizationID: "org", JobID: string(job.ID), WorkspaceID: "team", ChannelID: "channel", ToolID: "tool", ToolVersion: "1", OperationID: "write", Arguments: map[string]any{}, Destination: "team/channel", Risk: "write"}
	approval, _ := store.RequestContext(ctx, action, "requester", time.Minute)
	if err := coordinator.SuspendAndNotify(ctx, RequestScope{JobID: job.ID, LeaseToken: job.Lease.Token, WorkspaceID: "team", ChannelID: "channel", ThreadTS: "1"}, approval); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.HandleSlackDecision(ctx, SlackDecision{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", UserID: "operator", ApprovalID: approval.ID, Approve: true}); err == nil {
		t.Fatal("approval succeeded without its required audit authorization receipt")
	}
	unchangedApproval, _ := store.GetContext(ctx, "org", approval.ID)
	unchangedJob, _ := queue.Get(ctx, job.ID)
	if !unchangedApproval.ApprovedAt.IsZero() || unchangedJob.State != jobs.StateWaitingApproval {
		t.Fatalf("audit failure mutated approval/job: %#v %#v", unchangedApproval, unchangedJob)
	}
}

func TestCoordinatorDenialReleasesAdmission(t *testing.T) {
	ctx := context.Background()
	queue := jobs.NewMemoryQueue(nil)
	store := NewStore()
	deliveryQueue := deliveries.NewMemoryQueue(nil)
	auditLog, _ := audit.NewMemoryAppender([]byte("01234567890123456789012345678901"))
	admissions := &recordingAdmissionCompleter{}
	coordinator, _ := NewCoordinator(store, queue, deliveryQueue, auditLog, ApproverAuthorizerFunc(func(context.Context, string, string, string, string) error { return nil }), admissions)
	job, _, _ := queue.Enqueue(ctx, jobs.Spec{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", RootThreadTS: "1", SessionID: "session", Generation: 1, IdempotencyKey: "job-deny", Kind: "agent", MaxAttempts: 2, AdmissionReservationID: "admit-1"})
	job, _ = queue.Claim(ctx, "worker", time.Minute)
	job, _ = queue.Transition(ctx, job.ID, job.Lease.Token, jobs.StateRunning, nil)
	action := Action{OrganizationID: "org", JobID: string(job.ID), WorkspaceID: "team", ChannelID: "channel", ToolID: "tool", ToolVersion: "1", OperationID: "write", Arguments: map[string]any{}, Destination: "team/channel", Risk: "write"}
	approval, _ := store.RequestContext(ctx, action, "requester", time.Minute)
	_ = coordinator.SuspendAndNotify(ctx, RequestScope{JobID: job.ID, LeaseToken: job.Lease.Token, WorkspaceID: "team", ChannelID: "channel", ThreadTS: "1"}, approval)
	if err := coordinator.HandleSlackDecision(ctx, SlackDecision{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", UserID: "operator", ApprovalID: approval.ID, Approve: false}); err != nil {
		t.Fatal(err)
	}
	cancelled, _ := queue.Get(ctx, job.ID)
	if cancelled.State != jobs.StateCancelled || len(admissions.completed) != 1 || admissions.completed[0] != "admit-1" {
		t.Fatalf("denial did not cancel and release admission: %#v %v", cancelled, admissions.completed)
	}
}
