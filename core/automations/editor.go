// Package automations exposes channel-bound routine and heartbeat schedules to
// trusted operator surfaces without allowing a task to change its Slack
// destination.
package automations

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/orgconfig"
	"github.com/telemetryos/tos-tag/core/routines"
	"github.com/telemetryos/tos-tag/core/sessions"
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

type ListResult struct {
	Tasks           []Task
	Editable        bool
	DefaultTimezone string
}

type Editor struct {
	routines        routines.Repository
	triggers        triggers.Repository
	sessions        sessions.Store
	scopes          orgconfig.Resolver
	audit           audit.Appender
	globalOperators map[string]struct{}
	defaultTimezone string
	now             func() time.Time
}

func NewEditor(routineStore routines.Repository, triggerStore triggers.Repository, sessionStore sessions.Store, scopes orgconfig.Resolver, appender audit.Appender, globalOperatorUserIDs []string, defaultTimezone string) (*Editor, error) {
	if routineStore == nil || triggerStore == nil || sessionStore == nil || scopes == nil || appender == nil {
		return nil, errors.New("automation editor requires routine, trigger, session, scope, and audit stores")
	}
	defaultTimezone = strings.TrimSpace(defaultTimezone)
	if _, err := time.LoadLocation(defaultTimezone); err != nil {
		return nil, errors.New("automation editor default timezone is invalid")
	}
	globalOperators := make(map[string]struct{}, len(globalOperatorUserIDs))
	for _, userID := range globalOperatorUserIDs {
		if userID == "" {
			return nil, errors.New("automation editor global operator user IDs must not be empty")
		}
		globalOperators[userID] = struct{}{}
	}
	return &Editor{routines: routineStore, triggers: triggerStore, sessions: sessionStore, scopes: scopes, audit: appender, globalOperators: globalOperators, defaultTimezone: defaultTimezone, now: time.Now}, nil
}

func (e *Editor) List(ctx context.Context, scope Scope) (ListResult, error) {
	policy, err := e.authorize(ctx, scope)
	if err != nil {
		return ListResult{}, err
	}
	routineValues, err := e.routines.ListChannel(ctx, scope.OrganizationID, scope.WorkspaceID, scope.ChannelID)
	if err != nil {
		return ListResult{}, fmt.Errorf("list channel routines: %w", err)
	}
	triggerValues, err := e.triggers.ListChannel(ctx, scope.OrganizationID, scope.WorkspaceID, scope.ChannelID)
	if err != nil {
		return ListResult{}, fmt.Errorf("list channel trigger subscriptions: %w", err)
	}
	editable := e.canEdit(policy, scope.ActorID)
	result := make([]Task, 0, len(routineValues)+len(triggerValues))
	for _, value := range routineValues {
		task := routineTask(value)
		task.Editable = editable
		result = append(result, task)
	}
	for _, value := range triggerValues {
		task := triggerTask(value)
		task.Editable = editable
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
	return ListResult{Tasks: result, Editable: editable, DefaultTimezone: e.defaultTimezone}, nil
}

func (e *Editor) Load(ctx context.Context, scope Scope, kind Kind, id string) (Task, error) {
	policy, err := e.authorize(ctx, scope)
	if err != nil {
		return Task{}, err
	}
	if !e.canEdit(policy, scope.ActorID) {
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
	if !e.canEdit(policy, request.ActorID) {
		return Task{}, ErrForbidden
	}
	if request.SourceID == "" || request.ID == "" || request.Version < 0 || request.Instruction == "" || request.Cron == "" || request.Timezone == "" {
		return Task{}, ErrInvalid
	}
	if _, err := time.LoadLocation(request.Timezone); err != nil {
		return Task{}, ErrInvalid
	}
	var saved Task
	auditType := "automation.updated"
	if request.Version == 0 {
		if request.Kind != KindHeartbeat || !validStableID(request.ID) || request.MinConfidence < 0 || request.MinConfidence > 1 {
			return Task{}, ErrInvalid
		}
		if _, err := e.routines.GetContext(ctx, request.OrganizationID, request.WorkspaceID, request.ChannelID, request.ID); err == nil {
			return Task{}, ErrConflict
		}
		if _, err := e.triggers.GetContext(ctx, request.OrganizationID, request.WorkspaceID, request.ChannelID, request.ID); err == nil {
			return Task{}, ErrConflict
		}
		session, _, err := e.sessions.Resolve(ctx, request.OrganizationID, request.WorkspaceID, request.ChannelID, "automation:"+request.ID)
		if err != nil {
			return Task{}, fmt.Errorf("resolve automation session: %w", err)
		}
		value, err := e.triggers.CreateContext(ctx, triggers.Subscription{
			ID: request.ID, OrganizationID: request.OrganizationID, WorkspaceID: request.WorkspaceID, ChannelID: request.ChannelID,
			SessionID: session.ID, Generation: session.CurrentGeneration, OwnerID: request.ActorID, Kind: triggers.KindHeartbeat,
			Instruction: request.Instruction, Cron: request.Cron, Timezone: request.Timezone, ClassifierGate: true,
			MinConfidence: request.MinConfidence, Enabled: request.Enabled,
		})
		if err != nil {
			if errors.Is(err, triggers.ErrScopeConflict) {
				return Task{}, ErrConflict
			}
			return Task{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		saved = triggerTask(value)
		auditType = "automation.created"
	} else {
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
	}
	_, err = e.audit.Append(ctx, audit.AppendRequest{
		OrganizationID: request.OrganizationID,
		Type:           auditType,
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

func validStableID(value string) bool {
	if len(value) == 0 || len(value) > 80 || value != strings.TrimSpace(value) {
		return false
	}
	for index, character := range value {
		lowercase := character >= 'a' && character <= 'z'
		digit := character >= '0' && character <= '9'
		if !lowercase && !digit && (character != '-' || index == 0) {
			return false
		}
	}
	return true
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

func (e *Editor) canEdit(policy orgconfig.ChannelPolicy, actorID string) bool {
	if _, ok := e.globalOperators[actorID]; ok {
		return true
	}
	return channelApprover(policy, actorID)
}

func routineTask(value routines.Routine) Task {
	return Task{Kind: KindRoutine, ID: value.ID, Instruction: value.Input, Cron: value.Cron, Timezone: value.Timezone, Interval: value.Interval, NextRun: value.NextRun, Enabled: value.Enabled, Version: value.Version}
}

func triggerTask(value triggers.Subscription) Task {
	return Task{Kind: KindHeartbeat, ID: value.ID, Instruction: value.Instruction, Cron: value.Cron, Timezone: value.Timezone, Interval: value.Interval, NextRun: value.NextRun, MinConfidence: value.MinConfidence, Enabled: value.Enabled, Version: value.Version}
}
