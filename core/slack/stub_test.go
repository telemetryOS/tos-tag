package slack

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

func TestStubIngressAcknowledgesOnlyAfterAcceptance(t *testing.T) {
	stub := NewStubIngress(4)
	accepted := make(chan struct{})
	release := make(chan struct{})
	if err := stub.Start(context.Background(), func(_ context.Context, _ types.SlackEnvelope) (AcceptResult, error) {
		close(accepted)
		<-release
		return AcceptResult{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stub.Stop(context.Background()) })

	done := make(chan types.SlackAck, 1)
	go func() {
		ack, _ := stub.Inject(context.Background(), types.SlackEnvelope{EnvelopeID: "env-1"})
		done <- ack
	}()
	<-accepted
	select {
	case <-done:
		t.Fatal("acknowledged before durable handler returned")
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	select {
	case ack := <-done:
		if ack.EnvelopeID != "env-1" {
			t.Fatalf("unexpected ack: %#v", ack)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ack")
	}
}

func TestStubIngressDoesNotAckFailure(t *testing.T) {
	stub := NewStubIngress(1)
	want := errors.New("store unavailable")
	if err := stub.Start(context.Background(), func(context.Context, types.SlackEnvelope) (AcceptResult, error) {
		return AcceptResult{}, want
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stub.Stop(context.Background()) })
	if _, err := stub.Inject(context.Background(), types.SlackEnvelope{EnvelopeID: "env-1"}); !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
	if len(stub.Acks()) != 0 {
		t.Fatal("failed envelope was acknowledged")
	}
}

func TestStubDeliveryRetryAndIdempotency(t *testing.T) {
	stub := NewStubDelivery()
	stub.FailNext("delivery-1", 1)
	req := types.SlackDeliveryRequest{IdempotencyKey: "delivery-1"}
	if _, err := stub.Send(context.Background(), req); !errors.Is(err, ErrTransient) {
		t.Fatalf("first send: got %v", err)
	}
	first, err := stub.Send(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := stub.Send(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.MessageTS != second.MessageTS || !second.Duplicate {
		t.Fatalf("idempotency failed: first=%#v second=%#v", first, second)
	}
	if len(stub.Requests()) != 1 {
		t.Fatalf("expected one logical delivery, got %d", len(stub.Requests()))
	}
}
