// Package slack defines the project boundary for stubbed or live Slack ingress
// and delivery. Credentials remain confined to the live control-plane adapter.
package slack

import (
	"context"
	"errors"

	"github.com/telemetryos/tos-tag/types"
)

var (
	ErrNotStarted = errors.New("slack stub not started")
	ErrStopped    = errors.New("slack stub stopped")
	ErrTransient  = errors.New("stubbed transient Slack failure")
)

type AcceptResult struct {
	Duplicate bool
	Ignored   bool
}

type Handler func(context.Context, types.SlackEnvelope) (AcceptResult, error)

type ApprovalInteraction struct {
	OrganizationID string
	WorkspaceID    string
	ChannelID      string
	UserID         string
	ApprovalID     string
	MessageTS      string
	Approve        bool
}

type ApprovalInteractionHandler func(context.Context, ApprovalInteraction) error

type Ingress interface {
	Start(context.Context, Handler) error
	Stop(context.Context) error
}

type Delivery interface {
	Send(context.Context, types.SlackDeliveryRequest) (types.SlackDeliveryResult, error)
	React(context.Context, types.SlackReactionRequest) (types.SlackReactionResult, error)
}
