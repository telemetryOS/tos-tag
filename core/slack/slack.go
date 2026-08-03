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
	Duplicate       bool
	Ignored         bool
	ResolvedContext bool
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

type BotMembershipChange struct {
	OrganizationID string
	WorkspaceID    string
	ChannelID      string
	EventID        string
	Joined         bool
}

type BotMembershipHandler func(context.Context, BotMembershipChange) error

type DirectiveConfigurationRequest struct {
	OrganizationID string
	WorkspaceID    string
	ChannelID      string
	UserID         string
	Prompt         string
	InteractionID  string
}

type DirectiveConfiguration struct {
	Prompt   string
	Revision int64
}

type DirectiveLoadHandler func(context.Context, DirectiveConfigurationRequest) (DirectiveConfiguration, error)
type DirectiveSaveHandler func(context.Context, DirectiveConfigurationRequest) (DirectiveConfiguration, error)

// ModeChangeRequest is a /tag-mode slash command scoped to one channel. Mode
// is the raw requested mode; an empty Mode asks for the current mode only.
type ModeChangeRequest struct {
	OrganizationID string
	WorkspaceID    string
	ChannelID      string
	UserID         string
	Mode           string
}

type ModeChangeResult struct {
	Mode     string
	Previous string
	Changed  bool
}

type ModeChangeHandler func(context.Context, ModeChangeRequest) (ModeChangeResult, error)

type Ingress interface {
	Start(context.Context, Handler) error
	Stop(context.Context) error
}

type Delivery interface {
	Send(context.Context, types.SlackDeliveryRequest) (types.SlackDeliveryResult, error)
	React(context.Context, types.SlackReactionRequest) (types.SlackReactionResult, error)
	StartProgress(context.Context, types.SlackProgressStartRequest) (types.SlackProgressResult, error)
	UpdateProgress(context.Context, types.SlackProgressUpdateRequest) (types.SlackProgressResult, error)
}
