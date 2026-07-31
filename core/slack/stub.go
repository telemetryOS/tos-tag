package slack

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

type injectRequest struct {
	envelope types.SlackEnvelope
	result   chan injectResult
}

type injectResult struct {
	ack types.SlackAck
	err error
}

type StubIngress struct {
	queue chan injectRequest

	mu      sync.RWMutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	done    chan struct{}
	acks    []types.SlackAck
}

func NewStubIngress(queueSize int) *StubIngress {
	if queueSize <= 0 {
		queueSize = 1
	}
	return &StubIngress{queue: make(chan injectRequest, queueSize)}
}

func (s *StubIngress) Start(parent context.Context, handler Handler) error {
	if handler == nil {
		return fmt.Errorf("Slack handler is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started && !s.stopped {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	s.started = true
	s.stopped = false
	go s.run(ctx, handler, s.done)
	return nil
}

func (s *StubIngress) run(ctx context.Context, handler Handler, done chan struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-s.queue:
			accepted, err := handler(ctx, req.envelope)
			if err != nil {
				req.result <- injectResult{err: err}
				continue
			}
			ack := types.SlackAck{EnvelopeID: req.envelope.EnvelopeID, AcceptedAt: time.Now().UTC(), Duplicate: accepted.Duplicate}
			s.mu.Lock()
			s.acks = append(s.acks, ack)
			s.mu.Unlock()
			req.result <- injectResult{ack: ack}
		}
	}
}

func (s *StubIngress) Inject(ctx context.Context, envelope types.SlackEnvelope) (types.SlackAck, error) {
	s.mu.RLock()
	started, stopped := s.started, s.stopped
	s.mu.RUnlock()
	if !started {
		return types.SlackAck{}, ErrNotStarted
	}
	if stopped {
		return types.SlackAck{}, ErrStopped
	}
	result := make(chan injectResult, 1)
	select {
	case s.queue <- injectRequest{envelope: envelope, result: result}:
	case <-ctx.Done():
		return types.SlackAck{}, ctx.Err()
	}
	select {
	case got := <-result:
		return got.ack, got.err
	case <-ctx.Done():
		return types.SlackAck{}, ctx.Err()
	}
}

func (s *StubIngress) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.started || s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *StubIngress) Acks() []types.SlackAck {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]types.SlackAck(nil), s.acks...)
}

type StubDelivery struct {
	mu sync.Mutex

	sequence         uint64
	results          map[string]types.SlackDeliveryResult
	requests         []types.SlackDeliveryRequest
	failures         map[string]int
	reactions        map[string]types.SlackReactionResult
	reactionRequests []types.SlackReactionRequest
}

func NewStubDelivery() *StubDelivery {
	return &StubDelivery{
		results:   make(map[string]types.SlackDeliveryResult),
		failures:  make(map[string]int),
		reactions: make(map[string]types.SlackReactionResult),
	}
}

func (s *StubDelivery) React(_ context.Context, req types.SlackReactionRequest) (types.SlackReactionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.reactions[req.IdempotencyKey]; ok {
		existing.Duplicate = true
		return existing, nil
	}
	result := types.SlackReactionResult{AppliedAt: time.Now().UTC()}
	s.reactions[req.IdempotencyKey] = result
	s.reactionRequests = append(s.reactionRequests, req)
	return result, nil
}

func (s *StubDelivery) FailNext(idempotencyKey string, count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[idempotencyKey] = count
}

func (s *StubDelivery) Send(_ context.Context, req types.SlackDeliveryRequest) (types.SlackDeliveryResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.results[req.IdempotencyKey]; ok {
		existing.Duplicate = true
		return existing, nil
	}
	if remaining := s.failures[req.IdempotencyKey]; remaining > 0 {
		s.failures[req.IdempotencyKey] = remaining - 1
		return types.SlackDeliveryResult{}, ErrTransient
	}
	seq := atomic.AddUint64(&s.sequence, 1)
	result := types.SlackDeliveryResult{
		MessageTS:   fmt.Sprintf("stub.%06d", seq),
		DeliveredAt: time.Now().UTC(),
	}
	s.results[req.IdempotencyKey] = result
	s.requests = append(s.requests, req)
	return result, nil
}

func (s *StubDelivery) Requests() []types.SlackDeliveryRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]types.SlackDeliveryRequest(nil), s.requests...)
}

func (s *StubDelivery) ReactionRequests() []types.SlackReactionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]types.SlackReactionRequest(nil), s.reactionRequests...)
}
