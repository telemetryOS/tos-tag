package approvals

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/deliveries"
	"github.com/telemetryos/tos-tag/core/jobs"
	"github.com/telemetryos/tos-tag/types"
)

type RequestScope struct {
	JobID       types.JobID
	LeaseToken  string
	WorkspaceID string
	ChannelID   string
	ThreadTS    string
}

type SlackDecision struct {
	OrganizationID string
	WorkspaceID    string
	ChannelID      string
	UserID         string
	ApprovalID     string
	MessageTS      string
	Approve        bool
}

type ApproverAuthorizer interface {
	AuthorizeApprover(context.Context, string, string, string, string) error
}

type ApproverAuthorizerFunc func(context.Context, string, string, string, string) error

func (f ApproverAuthorizerFunc) AuthorizeApprover(ctx context.Context, organizationID, workspaceID, channelID, userID string) error {
	return f(ctx, organizationID, workspaceID, channelID, userID)
}

type AdmissionCompleter interface {
	Complete(context.Context, string)
}

type Coordinator struct {
	approvals  Repository
	jobs       jobs.Queue
	deliveries deliveries.Queue
	audit      audit.Appender
	authorizer ApproverAuthorizer
	admissions AdmissionCompleter
	now        func() time.Time
}

func NewCoordinator(repository Repository, queue jobs.Queue, deliveryQueue deliveries.Queue, auditAppender audit.Appender, authorizer ApproverAuthorizer, admissions AdmissionCompleter) (*Coordinator, error) {
	if repository == nil || queue == nil || deliveryQueue == nil || auditAppender == nil || authorizer == nil || admissions == nil {
		return nil, errors.New("approval coordinator dependencies are required")
	}
	return &Coordinator{approvals: repository, jobs: queue, deliveries: deliveryQueue, audit: auditAppender, authorizer: authorizer, admissions: admissions, now: time.Now}, nil
}

// SuspendAndNotify releases the active worker lease before publishing the
// human decision UI. The waiting job remains the sole writer for its session.
func (c *Coordinator) SuspendAndNotify(ctx context.Context, scope RequestScope, approval Approval) error {
	if scope.JobID == "" || scope.LeaseToken == "" || approval.ID == "" || approval.Action.JobID != string(scope.JobID) {
		return errors.New("approval suspension scope is invalid")
	}
	if _, err := c.jobs.SuspendForApproval(ctx, scope.JobID, scope.LeaseToken, approval.ID); err != nil {
		return fmt.Errorf("suspend job for approval: %w", err)
	}
	_, _, err := c.deliveries.Enqueue(ctx, deliveries.Spec{
		OrganizationID: approval.OrganizationID,
		JobID:          scope.JobID,
		IdempotencyKey: "approval/" + approval.ID + "/requested",
		Destination:    types.SlackDestination{TeamID: scope.WorkspaceID, ChannelID: scope.ChannelID, ThreadTS: scope.ThreadTS},
		Result:         types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentApproval, Approval: slackApproval(approval, "pending", time.Time{})}}},
		MaxAttempts:    3,
		ExpiresAt:      approval.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("enqueue Slack approval message: %w", err)
	}
	return nil
}

func (c *Coordinator) HandleSlackDecision(ctx context.Context, decision SlackDecision) error {
	if decision.OrganizationID == "" || decision.WorkspaceID == "" || decision.ChannelID == "" || decision.UserID == "" || decision.ApprovalID == "" {
		return errors.New("Slack approval decision is incomplete")
	}
	approval, err := c.approvals.GetContext(ctx, decision.OrganizationID, decision.ApprovalID)
	if err != nil {
		return err
	}
	if approval.Action.WorkspaceID != decision.WorkspaceID || approval.Action.ChannelID != decision.ChannelID || approval.Action.JobID == "" {
		return errors.New("Slack approval destination does not match the requested action")
	}
	if err := c.authorizer.AuthorizeApprover(ctx, decision.OrganizationID, decision.WorkspaceID, decision.ChannelID, decision.UserID); err != nil {
		return fmt.Errorf("Slack approver is not authorized: %w", err)
	}
	if approval.RequesterID == decision.UserID {
		return errors.New("independent approver required")
	}
	jobID := types.JobID(approval.Action.JobID)
	jobBefore, err := c.jobs.Get(ctx, jobID)
	if err != nil {
		return fmt.Errorf("load approval job: %w", err)
	}
	if jobBefore.OrganizationID != decision.OrganizationID || jobBefore.WorkspaceID != decision.WorkspaceID || jobBefore.ChannelID != decision.ChannelID || jobBefore.ApprovalID != approval.ID {
		return errors.New("Slack approval job does not match the requested action")
	}
	status := "denied"
	if decision.Approve {
		status = "approved"
	}
	now := c.now().UTC()
	if _, err := c.audit.Append(ctx, audit.AppendRequest{OrganizationID: decision.OrganizationID, Type: "tool.approval." + status + ".authorized", ActorID: decision.UserID, ResourceID: approval.ID, RetentionEpoch: now.Format("2006-01"), IdempotencyKey: "tool-approval/" + approval.ID + "/" + status + "/authorized", Metadata: map[string]any{"job_id": approval.Action.JobID, "channel_id": decision.ChannelID, "action_hash": approval.ActionHash}}); err != nil {
		return fmt.Errorf("record Slack approval authorization: %w", err)
	}
	if decision.Approve {
		if approval.ApprovedAt.IsZero() {
			approval, err = c.approvals.ApproveContext(ctx, decision.OrganizationID, decision.ApprovalID, decision.UserID)
			if err != nil {
				return err
			}
		}
		if _, err := c.jobs.ResumeFromApproval(ctx, jobID, approval.ID, approval.ActionHash); err != nil {
			job, getErr := c.jobs.Get(ctx, jobID)
			if getErr != nil || job.ApprovalID != approval.ID || job.ApprovedActionHash != approval.ActionHash || job.State == jobs.StateWaitingApproval {
				return fmt.Errorf("resume approved job: %w", err)
			}
		}
	} else {
		if approval.DeniedAt.IsZero() {
			approval, err = c.approvals.DenyContext(ctx, decision.OrganizationID, decision.ApprovalID, decision.UserID)
			if err != nil {
				return err
			}
		}
		if _, err := c.jobs.Cancel(ctx, jobID, "approval_denied"); err != nil {
			job, getErr := c.jobs.Get(ctx, jobID)
			if getErr != nil || (job.State != jobs.StateCancelled && job.State != jobs.StateCancelling) {
				return fmt.Errorf("cancel denied job: %w", err)
			}
		}
		c.admissions.Complete(ctx, jobBefore.AdmissionReservationID)
	}
	resolvedAt := approval.DeniedAt
	if decision.Approve {
		resolvedAt = approval.ApprovedAt
	}
	_, _, err = c.deliveries.Enqueue(ctx, deliveries.Spec{OrganizationID: decision.OrganizationID, JobID: jobID, IdempotencyKey: "approval/" + approval.ID + "/" + status, Destination: types.SlackDestination{TeamID: decision.WorkspaceID, ChannelID: decision.ChannelID, ThreadTS: approval.Action.ThreadTS, UpdateTS: decision.MessageTS}, Result: types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentApproval, Approval: slackApproval(approval, status, resolvedAt)}}}, MaxAttempts: 3, ExpiresAt: approval.CleanupAt})
	return err
}

func slackApproval(approval Approval, status string, resolvedAt time.Time) *types.SlackApproval {
	return &types.SlackApproval{
		ID: approval.ID, ActionHash: approval.ActionHash, ToolID: approval.Action.ToolID,
		OperationID: approval.Action.OperationID, Risk: approval.Action.Risk, Destination: approval.Action.Destination,
		Arguments: approval.Action.Arguments, ExpiresAt: approval.ExpiresAt, Status: status, ResolvedAt: resolvedAt,
	}
}
