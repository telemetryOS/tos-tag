package schedule

import (
	"context"
	"time"
)

type DueRunner interface {
	RunDue(context.Context) error
}

// Service owns the shared bounded poll lifecycle for recurring work.
type Service struct {
	runner DueRunner
	poll   time.Duration
	cancel context.CancelFunc
	done   chan struct{}
}

func NewService(runner DueRunner, poll time.Duration) *Service {
	return &Service{runner: runner, poll: poll}
}

func (s *Service) Start(parent context.Context) {
	if s == nil || s.runner == nil || s.poll <= 0 || s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(s.poll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.runner.RunDue(ctx)
			}
		}
	}()
}

func (s *Service) Stop(ctx context.Context) error {
	if s == nil || s.cancel == nil {
		return nil
	}
	s.cancel()
	select {
	case <-s.done:
		s.cancel = nil
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
