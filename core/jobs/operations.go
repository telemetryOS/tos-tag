package jobs

import (
	"context"
	"fmt"

	"github.com/telemetryos/tos-tag/core/sessions"
	"github.com/telemetryos/tos-tag/types"
)

type Operations struct {
	Queue    Queue
	Sessions sessions.Store
}

func (o Operations) Cancel(ctx context.Context, id, reason string) (Job, error) {
	return o.Queue.Cancel(ctx, types.JobID(id), reason)
}
func (o Operations) Interrupt(ctx context.Context, id, reason string) (Job, error) {
	return o.Queue.Interrupt(ctx, types.JobID(id), reason)
}
func (o Operations) Restart(ctx context.Context, id string) (Job, error) {
	current, err := o.Queue.Get(ctx, types.JobID(id))
	if err != nil {
		return Job{}, err
	}
	session, err := o.Sessions.Restart(ctx, current.OrganizationID, current.WorkspaceID, current.ChannelID, current.RootThreadTS)
	if err != nil {
		return Job{}, err
	}
	created, _, err := o.Queue.Enqueue(ctx, Spec{OrganizationID: current.OrganizationID, WorkspaceID: current.WorkspaceID, ChannelID: current.ChannelID, RootThreadTS: current.RootThreadTS, SessionID: session.ID, Generation: session.CurrentGeneration, ObservationID: current.ObservationID, IdempotencyKey: current.IdempotencyKey + fmt.Sprintf("/restart/%d", session.CurrentGeneration), Kind: current.Kind, Input: current.Input, MaxAttempts: current.MaxAttempts, ResolvedModel: current.ResolvedModel, RouteTrace: current.RouteTrace, ExpiresAt: current.ExpiresAt})
	return created, err
}
func (o Operations) Branch(ctx context.Context, id, newRootThreadTS string) (Job, error) {
	if newRootThreadTS == "" {
		return Job{}, fmt.Errorf("branch root thread is required")
	}
	current, err := o.Queue.Get(ctx, types.JobID(id))
	if err != nil {
		return Job{}, err
	}
	session, _, err := o.Sessions.Resolve(ctx, current.OrganizationID, current.WorkspaceID, current.ChannelID, newRootThreadTS)
	if err != nil {
		return Job{}, err
	}
	created, _, err := o.Queue.Enqueue(ctx, Spec{OrganizationID: current.OrganizationID, WorkspaceID: current.WorkspaceID, ChannelID: current.ChannelID, RootThreadTS: newRootThreadTS, SessionID: session.ID, Generation: session.CurrentGeneration, ObservationID: current.ObservationID, IdempotencyKey: current.IdempotencyKey + "/branch/" + newRootThreadTS, Kind: current.Kind, Input: current.Input, MaxAttempts: current.MaxAttempts, ResolvedModel: current.ResolvedModel, RouteTrace: current.RouteTrace, ExpiresAt: current.ExpiresAt})
	return created, err
}
