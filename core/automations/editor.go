// Package automations exposes channel-bound routine and heartbeat schedules to
// trusted operator surfaces without allowing an existing task to change its
// Slack destination.
package automations

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/orgconfig"
	"github.com/telemetryos/tos-tag/core/routines"
	"github.com/telemetryos/tos-tag/core/triggers"
)

type Kind string

const (
	KindRoutine   Kind = "routine"
	KindHeartbeat Kind = "heartbeat"
)

var (
	ErrForbidden = errors.New("channel automation access is not authorized")
	ErrInvalid   = errors.New("channel automation is invalid")
	ErrConflict  = errors.New("channel automation changed while it was being edited")
	ErrNotFound  = errors.New("channel automation not found")
)

type Scope struct {
	OrganizationID string
	WorkspaceID    string
	ChannelID      string
	ActorID        string
}

type Task struct {
	Kind          Kind
	ID            string
	Instruction   string
	Cron          string
	Timezone      string
	Interval      time.Duration
	NextRun       time.Time
	MinConfidence float64
	Enabled       bool
	Version       int64
	Editable      bool
}

type SaveRequest struct {
	Scope
	Kind          Kind
	ID            string
	Instruction   string
	Cron          string
	Timezone      string
	MinConfidence float64
	Enabled       bool
	Version       int64
	SourceID      string
}

type Editor struct {
	routines routines.Repository
	triggers triggers.Repository
	scopes   orgconfig.Resolver
	audit    audit.Appender
	now      func() time.Time
}

func NewEditor(routineStore routines.Repository, triggerStore triggers.Repository, scopes orgconfig.Resolver, appender audit.Appender) (*Editor, error) {
	if routineStore == nil || triggerStore == nil || scopes == nil || appender == nil {
		return nil, errors.New("automation editor requires routine, trigger, scope, and audit stores")
	}
	return &Editor{routines: routineStore, triggers: triggerStore, scopes: scopes, audit: appender, now: time.Now}, nil
}

func (e *Editor) List(ctx context.Context, scope Scope) ([]Task, error) {
	policy, err := e.authorize(ctx, scope)
	if err != nil {
		return nil, err
	}
	routineValues, err := e.routines.ListChannel(ctx, scope.OrganizationID, scope.WorkspaceID, scope.ChannelID)
	if err != nil {
		return nil, fmt.Errorf("list channel routines: %w", err)
	}
	triggerValues, err := e.triggers.ListChannel(ctx, scope.OrganizationID, scope.WorkspaceID, scope.ChannelID)
	if err != nil {
		return nil, fmt.Errorf("list channel trigger subscriptions: %w", err)
	}
	result := make([]Task, 0, len(routineValues)+len(triggerValues))
	for _, value := range routineValues {
		task := routineTask(value)
		task.Editable = channelApprover(policy, scope.ActorID)
		result = append(result, task)
	}
	for _, value := range triggerValues {
		task := triggerTask(value)
		task.Editable = channelApprover(policy, scope.ActorID)
		result = append(result, task)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].NextRun.Equal(result[j].NextRun) {
			if result[i].ID == result[j].ID {
				return result[i].Kind < result[j].Kind
			}
			return result[i].ID < result[j].ID
		}
		return result[i].NextRun.Before(result[j].NextRun)
	})
	return result, nil
}

func (e *Editor) Load(ctx context.Context, scope Scope, kind Kind, id string) (Task, error) {
	policy, err := e.authorize(ctx, scope)
	if err != nil {
		return Task{}, err
	}
	if !channelApprover(policy, scope.ActorID) {
		return Task{}, ErrForbidden
	}
	if id == "" {
		return Task{}, ErrNotFound
	}
	switch kind {
	case KindRoutine:
		value, err := e.routines.GetContext(ctx, scope.OrganizationID, scope.WorkspaceID, scope.ChannelID, id)
		if err != nil {
			return Task{}, ErrNotFound
		}
		task := routineTask(value)
		task.Editable = true
		return task, nil
	case KindHeartbeat:
		value, err := e.triggers.GetContext(ctx, scope.OrganizationID, scope.WorkspaceID, scope.ChannelID, id)
		if err != nil {
			return Task{}, ErrNotFound
		}
		task := triggerTask(value)
		task.Editable = true
		return task, nil
	default:
		return Task{}, ErrNotFound
	}
}

func (e *Editor) Save(ctx context.Context, request SaveRequest) (Task, error) {
	policy, err := e.authorize(ctx, request.Scope)
	if err != nil {
		return Task{}, err
	}
	if !channelApprover(policy, request.ActorID) {
		return Task{}, ErrForbidden
	}
	if request.SourceID == "" || request.ID == "" || request.Version <= 0 || request.Instruction == "" || request.Cron == "" || request.Timezone == "" {
		return Task{}, ErrInvalid
	}
	var saved Task
	switch request.Kind {
	case KindRoutine:
		current, err := e.routines.GetContext(ctx, request.OrganizationID, request.WorkspaceID, request.ChannelID, request.ID)
		if err != nil {
			return Task{}, ErrNotFound
		}
		if current.Version != request.Version {
			return Task{}, ErrConflict
		}
		if current.Cron != request.Cron || current.Timezone != request.Timezone || current.Interval != 0 {
			current.NextRun = time.Time{}
		}
		current.Input, current.Cron, current.Timezone, current.Interval, current.Enabled = request.Instruction, request.Cron, request.Timezone, 0, request.Enabled
		value, err := e.routines.UpdateContext(ctx, current, request.Version)
		if err != nil {
			if errors.Is(err, routines.ErrUpdateConflict) {
				return Task{}, ErrConflict
			}
			return Task{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		saved = routineTask(value)
	case KindHeartbeat:
		current, err := e.triggers.GetContext(ctx, request.OrganizationID, request.WorkspaceID, request.ChannelID, request.ID)
		if err != nil {
			return Task{}, ErrNotFound
		}
		if current.Version != request.Version {
			return Task{}, ErrConflict
		}
		if request.MinConfidence < 0 || request.MinConfidence > 1 {
			return Task{}, ErrInvalid
		}
		if current.Cron != request.Cron || current.Timezone != request.Timezone || current.Interval != 0 {
			current.NextRun = time.Time{}
		}
		current.Instruction, current.Cron, current.Timezone, current.Interval = request.Instruction, request.Cron, request.Timezone, 0
		current.MinConfidence, current.Enabled = request.MinConfidence, request.Enabled
		value, err := e.triggers.UpdateContext(ctx, current, request.Version)
		if err != nil {
			if errors.Is(err, triggers.ErrUpdateConflict) {
				return Task{}, ErrConflict
			}
			return Task{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		saved = triggerTask(value)
	default:
		return Task{}, ErrInvalid
	}
	_, err = e.audit.Append(ctx, audit.AppendRequest{
		OrganizationID: request.OrganizationID,
		Type:           "automation.updated",
		ActorID:        request.ActorID,
		ResourceID:     string(request.Kind) + "/" + request.ChannelID + "/" + request.ID,
		Metadata:       map[string]any{"channel_id": request.ChannelID, "automation_kind": string(request.Kind), "enabled": saved.Enabled, "version": saved.Version, "source": "slack_modal"},
		RetentionEpoch: e.now().UTC().Format("2006-01"),
		IdempotencyKey: fmt.Sprintf("automation/%s/%s/%s/%d", request.Kind, request.ChannelID, request.ID, saved.Version),
		Content:        []byte(saved.Instruction),
	})
	if err != nil {
		return Task{}, fmt.Errorf("record automation audit receipt: %w", err)
	}
	return saved, nil
}

func (e *Editor) authorize(ctx context.Context, scope Scope) (orgconfig.ChannelPolicy, error) {
	if scope.OrganizationID == "" || scope.WorkspaceID == "" || scope.ChannelID == "" || scope.ActorID == "" {
		return orgconfig.ChannelPolicy{}, ErrForbidden
	}
	policy, err := e.scopes.Resolve(ctx, scope.OrganizationID, scope.WorkspaceID, scope.ChannelID)
	if err != nil || !policy.Enrolled || policy.KillSwitch || !policy.WorkspaceEnabled {
		return orgconfig.ChannelPolicy{}, ErrForbidden
	}
	return policy, nil
}

func channelApprover(policy orgconfig.ChannelPolicy, actorID string) bool {
	for _, userID := range policy.ApproverUserIDs {
		if userID == actorID {
			return true
		}
	}
	return false
}

func routineTask(value routines.Routine) Task {
	return Task{Kind: KindRoutine, ID: value.ID, Instruction: value.Input, Cron: value.Cron, Timezone: value.Timezone, Interval: value.Interval, NextRun: value.NextRun, Enabled: value.Enabled, Version: value.Version}
}

func triggerTask(value triggers.Subscription) Task {
	return Task{Kind: KindHeartbeat, ID: value.ID, Instruction: value.Instruction, Cron: value.Cron, Timezone: value.Timezone, Interval: value.Interval, NextRun: value.NextRun, MinConfidence: value.MinConfidence, Enabled: value.Enabled, Version: value.Version}
}
