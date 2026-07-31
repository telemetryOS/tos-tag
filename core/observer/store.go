// Package observer owns the durable observation and current-message boundary.
package observer

import (
	"context"
	"errors"
	"time"

	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

type Acceptance struct {
	Observation models.Observation
	Duplicate   bool
}

type Store interface {
	Accept(context.Context, types.SlackEnvelope) (Acceptance, error)
	ClaimPending(context.Context, string, time.Duration) (models.Observation, error)
	CompleteDecision(context.Context, string, string, string, string) error
	SetRestricted(context.Context, string, bool) error
	Recent(context.Context, string, []string, time.Time, int) ([]models.ChannelMessage, error)
	CurrentMessage(context.Context, string, string, string, string) (models.ChannelMessage, error)
	Channels(context.Context, string) ([]string, error)
	MarkOutput(context.Context, string, string, string) (bool, error)
	LateCandidates(context.Context, string, time.Time, time.Time, int) ([]models.Observation, error)
}

var ErrNoPendingObservation = errors.New("no pending observation")

func ValidateEnvelope(envelope types.SlackEnvelope) error {
	if envelope.OrganizationID == "" || envelope.TeamID == "" || envelope.ChannelID == "" || envelope.EventID == "" || envelope.EnvelopeID == "" {
		return ErrInvalidEnvelope
	}
	if envelope.MessageTS == "" && envelope.TargetTS == "" {
		return ErrInvalidEnvelope
	}
	switch envelope.Kind {
	case types.SlackEventMessage, types.SlackEventEdit, types.SlackEventDelete:
	default:
		return ErrInvalidEnvelope
	}
	return nil
}

func eventTime(envelope types.SlackEnvelope, now time.Time) time.Time {
	if !envelope.EventTime.IsZero() {
		return envelope.EventTime.UTC()
	}
	if !envelope.ReceivedAt.IsZero() {
		return envelope.ReceivedAt.UTC()
	}
	return now.UTC()
}

func projectionMessageTS(envelope types.SlackEnvelope) string {
	if envelope.TargetTS != "" {
		return envelope.TargetTS
	}
	return envelope.MessageTS
}

func projectionEventRank(kind types.SlackEventKind) int {
	switch kind {
	case types.SlackEventDelete:
		return 3
	case types.SlackEventEdit:
		return 2
	default:
		return 1
	}
}

func projectionIsNewer(current models.ChannelMessage, eventAt time.Time, rank int) bool {
	return current.SourceEventAt.IsZero() || eventAt.After(current.SourceEventAt) || (eventAt.Equal(current.SourceEventAt) && rank >= current.SourceEventRank)
}
