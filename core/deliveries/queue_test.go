package deliveries

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

func deliverySpec() Spec {
	return Spec{OrganizationID: "org", JobID: "job", IdempotencyKey: "job/final", Destination: types.SlackDestination{TeamID: "team", ChannelID: "channel", ThreadTS: "100.1"}, Result: types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: "*Done*"}}}, MaxAttempts: 2}
}

func TestDeliveryQueueDoesNotDuplicateLogicalSend(t *testing.T) {
	queue := NewMemoryQueue(nil)
	first, created, err := queue.Enqueue(context.Background(), deliverySpec())
	if err != nil || !created {
		t.Fatalf("enqueue: %v %v", created, err)
	}
	second, created, _ := queue.Enqueue(context.Background(), deliverySpec())
	if created || first.ID != second.ID {
		t.Fatalf("duplicate delivery: %#v %#v", first, second)
	}
}

func TestDeliveryQueueAcceptsExactlyOneDurableSource(t *testing.T) {
	queue := NewMemoryQueue(nil)
	direct := deliverySpec()
	direct.JobID = ""
	direct.DecisionID = "decision-1"
	direct.IdempotencyKey = "decision/1/direct"
	record, created, err := queue.Enqueue(context.Background(), direct)
	if err != nil || !created || record.DecisionID != "decision-1" || record.JobID != "" {
		t.Fatalf("direct decision delivery = %#v, created=%v, err=%v", record, created, err)
	}
	invalid := direct
	invalid.JobID = "job-1"
	if _, _, err := queue.Enqueue(context.Background(), invalid); err == nil {
		t.Fatal("delivery with both job and decision sources was accepted")
	}
}

func TestDeliveryRetryDoesNotCreateNewRecord(t *testing.T) {
	queue := NewMemoryQueue(nil)
	record, _, _ := queue.Enqueue(context.Background(), deliverySpec())
	record, _ = queue.Claim(context.Background(), "worker", time.Minute)
	if _, err := queue.Complete(context.Background(), record.ID, "forged", types.SlackDeliveryResult{}); !errors.Is(err, ErrDeliveryLeaseLost) {
		t.Fatalf("forged completion: %v", err)
	}
	record, err := queue.Retry(context.Background(), record.ID, record.Lease.Token, "rate_limited", 0)
	if err != nil || record.Status != StatusRetryWait {
		t.Fatalf("retry: %#v %v", record, err)
	}
	record, err = queue.Claim(context.Background(), "worker", time.Minute)
	if err != nil || record.Attempt != 2 {
		t.Fatalf("reclaim: %#v %v", record, err)
	}
	record, err = queue.Complete(context.Background(), record.ID, record.Lease.Token, types.SlackDeliveryResult{MessageTS: "stub.1"})
	if err != nil || record.Status != StatusDelivered || record.SlackMessageTS != "stub.1" {
		t.Fatalf("complete: %#v %v", record, err)
	}
}
