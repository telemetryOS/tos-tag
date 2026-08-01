package channelconfig

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/orgconfig"
)

var (
	ErrDirectiveForbidden = errors.New("channel directive configuration is not authorized")
	ErrDirectiveInvalid   = errors.New("channel directive is invalid")
)

type EditRequest struct {
	OrganizationID string
	WorkspaceID    string
	ChannelID      string
	ActorID        string
	Prompt         string
	SourceID       string
}

type EditResult struct {
	Prompt   string
	Revision int64
	ID       string
}

type Editor struct {
	repository Repository
	scopes     orgconfig.Resolver
	audit      audit.Appender
	now        func() time.Time
}

func NewEditor(repository Repository, scopes orgconfig.Resolver, appender audit.Appender) (*Editor, error) {
	if repository == nil || scopes == nil || appender == nil {
		return nil, errors.New("directive editor requires repository, scope resolver, and audit appender")
	}
	return &Editor{repository: repository, scopes: scopes, audit: appender, now: time.Now}, nil
}

func (e *Editor) Load(ctx context.Context, request EditRequest) (EditResult, error) {
	if err := e.authorize(ctx, request); err != nil {
		return EditResult{}, err
	}
	directive, err := e.repository.ActiveDirective(ctx, request.OrganizationID, request.ChannelID)
	if err != nil {
		// ActiveDirective intentionally exposes a small interface-level error.
		// A successful list distinguishes a genuinely empty projection from a
		// repository failure so the modal never opens blank during an outage.
		if _, listErr := e.repository.ListDirectives(ctx, request.OrganizationID, request.ChannelID); listErr != nil {
			return EditResult{}, fmt.Errorf("load channel directive: %w", listErr)
		}
		return EditResult{}, nil
	}
	return EditResult{Prompt: directive.Prompt, Revision: directive.Revision, ID: directive.ID}, nil
}

func (e *Editor) Save(ctx context.Context, request EditRequest) (EditResult, error) {
	if err := e.authorize(ctx, request); err != nil {
		return EditResult{}, err
	}
	if request.SourceID == "" {
		return EditResult{}, fmt.Errorf("%w: source ID is required", ErrDirectiveInvalid)
	}
	directive, err := e.repository.PublishDirective(ctx, request.OrganizationID, request.ChannelID, request.Prompt, request.ActorID, request.SourceID)
	if err != nil {
		return EditResult{}, fmt.Errorf("%w: %v", ErrDirectiveInvalid, err)
	}
	_, err = e.audit.Append(ctx, audit.AppendRequest{
		OrganizationID: request.OrganizationID,
		Type:           "directive.activated",
		ActorID:        request.ActorID,
		ResourceID:     directive.ID,
		Metadata:       map[string]any{"channel_id": request.ChannelID, "revision": directive.Revision, "source": "slack_modal"},
		RetentionEpoch: e.now().UTC().Format("2006-01"),
		IdempotencyKey: "directive/" + directive.ID + "/activated",
		Content:        []byte(directive.Prompt),
	})
	if err != nil {
		return EditResult{}, fmt.Errorf("record directive audit receipt: %w", err)
	}
	return EditResult{Prompt: directive.Prompt, Revision: directive.Revision, ID: directive.ID}, nil
}

func (e *Editor) authorize(ctx context.Context, request EditRequest) error {
	if request.OrganizationID == "" || request.WorkspaceID == "" || request.ChannelID == "" || request.ActorID == "" {
		return ErrDirectiveForbidden
	}
	policy, err := e.scopes.Resolve(ctx, request.OrganizationID, request.WorkspaceID, request.ChannelID)
	if err != nil || !policy.Enrolled || policy.KillSwitch || !policy.MembershipRefreshedAt.After(e.now().UTC().Add(-24*time.Hour)) || !slices.Contains(policy.ApproverUserIDs, request.ActorID) {
		return ErrDirectiveForbidden
	}
	return nil
}
