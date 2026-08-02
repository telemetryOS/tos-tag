// Package jobs owns durable job state transitions and lease fencing.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

type State string

const (
	StateQueued               State = "queued"
	StateLeased               State = "leased"
	StatePreparing            State = "preparing"
	StateRunning              State = "running"
	StateWaitingApproval      State = "waiting_approval"
	StateRetryWait            State = "retry_wait"
	StateCancelling           State = "cancelling"
	StateNeedsReconciliation  State = "needs_reconciliation"
	StateSucceeded            State = "succeeded"
	StateCompletedUndelivered State = "completed_undelivered"
	StateFailed               State = "failed"
	StateCancelled            State = "cancelled"
)

var (
	ErrNoRunnableJob = errors.New("no runnable job")
	ErrJobNotFound   = errors.New("job not found")
	ErrLeaseLost     = errors.New("job lease lost")
	ErrInvalidState  = errors.New("invalid job state transition")
)

type Lease struct {
	Owner     types.WorkerID `json:"owner"`
	Token     string         `json:"token"`
	ExpiresAt time.Time      `json:"expires_at"`
	Heartbeat time.Time      `json:"heartbeat_at"`
}

type Spec struct {
	OrganizationID         string
	WorkspaceID            string
	ChannelID              string
	RootThreadTS           string
	ReplyInChannel         bool
	SessionID              types.SessionID
	Generation             int64
	ObservationID          types.ObservationID
	RequesterID            string
	IdempotencyKey         string
	Kind                   string
	Input                  string
	MaxAttempts            int
	AdmissionReservationID string
	ResolvedModel          types.ResolvedModel
	RouteTrace             types.DecisionTrace
	ExpiresAt              time.Time
}

type Job struct {
	ID                     types.JobID         `json:"id"`
	OrganizationID         string              `json:"organization_id"`
	WorkspaceID            string              `json:"workspace_id"`
	ChannelID              string              `json:"channel_id"`
	RootThreadTS           string              `json:"root_thread_ts"`
	ReplyInChannel         bool                `json:"reply_in_channel,omitempty"`
	SessionID              types.SessionID     `json:"session_id"`
	Generation             int64               `json:"generation"`
	ObservationID          types.ObservationID `json:"observation_id,omitempty"`
	RequesterID            string              `json:"requester_id,omitempty"`
	IdempotencyKey         string              `json:"idempotency_key"`
	Kind                   string              `json:"kind"`
	Input                  string              `json:"input"`
	State                  State               `json:"state"`
	Attempt                int                 `json:"attempt"`
	MaxAttempts            int                 `json:"max_attempts"`
	AdmissionReservationID string              `json:"admission_reservation_id,omitempty"`
	ResolvedModel          types.ResolvedModel `json:"resolved_model"`
	RouteTrace             types.DecisionTrace `json:"route_trace"`
	SteeringEpoch          int64               `json:"steering_epoch"`
	Lease                  Lease               `json:"lease"`
	Result                 types.SlackResult   `json:"result,omitempty"`
	FailureReason          string              `json:"failure_reason,omitempty"`
	ApprovalID             string              `json:"approval_id,omitempty"`
	ApprovedActionHash     string              `json:"approved_action_hash,omitempty"`
	ProgressMessageTS      string              `json:"progress_message_ts,omitempty"`
	AvailableAt            time.Time           `json:"available_at"`
	CreatedAt              time.Time           `json:"created_at"`
	UpdatedAt              time.Time           `json:"updated_at"`
	ExpiresAt              time.Time           `json:"expires_at"`
	Version                int64               `json:"version"`
}

type Queue interface {
	Enqueue(context.Context, Spec) (Job, bool, error)
	Claim(context.Context, types.WorkerID, time.Duration) (Job, error)
	Transition(context.Context, types.JobID, string, State, func(*Job)) (Job, error)
	Heartbeat(context.Context, types.JobID, string, time.Duration) error
	Requeue(context.Context, types.JobID, string, string, time.Duration) (Job, error)
	ReleaseRetryWait(context.Context, types.JobID) (Job, error)
	Get(context.Context, types.JobID) (Job, error)
	List(context.Context) ([]Job, error)
	ListOrganization(context.Context, string) ([]Job, error)
	Cancel(context.Context, types.JobID, string) (Job, error)
	Interrupt(context.Context, types.JobID, string) (Job, error)
	MarkCompletedUndelivered(context.Context, types.JobID, string) (Job, error)
	SuspendForApproval(context.Context, types.JobID, string, string) (Job, error)
	ResumeFromApproval(context.Context, types.JobID, string, string) (Job, error)
}

func CanTransition(from, to State) bool {
	allowed := map[State]map[State]bool{
		StateQueued:              {StateLeased: true, StateCancelled: true},
		StateLeased:              {StatePreparing: true, StateRunning: true, StateQueued: true, StateNeedsReconciliation: true, StateCancelled: true},
		StatePreparing:           {StateRunning: true, StateRetryWait: true, StateFailed: true, StateCancelled: true},
		StateRunning:             {StateRunning: true, StateWaitingApproval: true, StateRetryWait: true, StateSucceeded: true, StateFailed: true, StateCancelling: true, StateNeedsReconciliation: true},
		StateWaitingApproval:     {StateQueued: true, StateFailed: true, StateCancelled: true},
		StateRetryWait:           {StateQueued: true, StateFailed: true, StateCancelled: true},
		StateCancelling:          {StateCancelled: true, StateFailed: true},
		StateNeedsReconciliation: {StateRetryWait: true, StateSucceeded: true, StateFailed: true},
		StateSucceeded:           {StateCompletedUndelivered: true},
	}
	return allowed[from][to]
}

func ValidateSpec(spec Spec) error {
	if spec.OrganizationID == "" || spec.WorkspaceID == "" || spec.ChannelID == "" || (spec.RootThreadTS == "" && spec.Kind != "routine" && spec.Kind != "heartbeat") || spec.SessionID == "" || spec.Generation <= 0 || spec.IdempotencyKey == "" || spec.Kind == "" || spec.MaxAttempts <= 0 {
		return fmt.Errorf("invalid job specification")
	}
	return nil
}
