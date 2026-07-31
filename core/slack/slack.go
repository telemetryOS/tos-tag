// Package slack defines the credential-free project boundary for Slack ingress
// and delivery. The current initiative intentionally ships only the stub.
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
}

type Handler func(context.Context, types.SlackEnvelope) (AcceptResult, error)

type Ingress interface {
	Start(context.Context, Handler) error
	Stop(context.Context) error
}

type Delivery interface {
	Send(context.Context, types.SlackDeliveryRequest) (types.SlackDeliveryResult, error)
}
