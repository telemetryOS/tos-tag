// Package slack defines the project boundary for stubbed or live Slack ingress
// and delivery. Credentials remain confined to the live control-plane adapter.
package slack

import (
	"context"
	"errors"

	"github.com/telemetryos/tos-tag/core/automations"
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
	Command        string
	Mode           string
}

type ModeChangeResult struct {
	Mode               string
	Previous           string
	Changed            bool
	Enrolled           bool
	Restricted         bool
	KillSwitched       bool
	WorkspaceEnabled   bool
	BotIsMember        bool
	BotMembershipKnown bool
}

type ModeChangeHandler func(context.Context, ModeChangeRequest) (ModeChangeResult, error)

type AutomationListHandler func(context.Context, automations.Scope) ([]automations.Task, error)
type AutomationLoadHandler func(context.Context, automations.Scope, automations.Kind, string) (automations.Task, error)
type AutomationSaveHandler func(context.Context, automations.SaveRequest) (automations.Task, error)

type Ingress interface {
	Start(context.Context, Handler) error
	Stop(context.Context) error
}

type Delivery interface {
	Send(context.Context, types.SlackDeliveryRequest) (types.SlackDeliveryResult, error)
	React(context.Context, types.SlackReactionRequest) (types.SlackReactionResult, error)
	SetAgentStatus(context.Context, types.SlackAgentStatusRequest) (types.SlackAgentStatusResult, error)
	SetThreadTitle(context.Context, types.SlackThreadTitleRequest) (types.SlackThreadTitleResult, error)
}

// Media keeps private Slack file retrieval behind the control plane. Workers
// receive validated bytes, never Slack URLs or OAuth credentials.
type Media interface {
	DownloadImages(context.Context, string, []types.SlackImageRef) ([]types.SlackImageData, error)
}
