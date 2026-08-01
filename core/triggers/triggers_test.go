package triggers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/jobs"
	"github.com/telemetryos/tos-tag/types"
)

func testSubscription(now time.Time) Subscription {
	return Subscription{ID: "heartbeat", OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", RootThreadTS: "thread", SessionID: "session", Generation: 1, OwnerID: "owner", Kind: KindHeartbeat, Instruction: "Check whether intervention is useful.", Interval: time.Hour, NextRun: now, ClassifierGate: true, MinConfidence: .9, Enabled: true}
}

func TestHeartbeatGateControlsIdempotentJobAdmission(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	store := NewStore(func() time.Time { return now })
	if _, err := store.PutContext(context.Background(), testSubscription(now)); err != nil {
		t.Fatal(err)
	}
	queue := jobs.NewMemoryQueue(func() time.Time { return now })
	decisions := []bool{false, true}
	gate := GateFunc(func(context.Context, Subscription, string) (GateDecision, error) {
		accepted := decisions[0]
		decisions = decisions[1:]
		return GateDecision{Accepted: accepted, Decision: types.ClassificationDecision{Outcome: types.OutcomeStartBackgroundJob, Confidence: .95}}, nil
	})
	scheduler, err := NewScheduler(store, queue, gate, AuthorizerFunc(func(context.Context, Subscription) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	scheduler.now = func() time.Time { return now }
	if err := scheduler.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if values, _ := queue.List(context.Background()); len(values) != 0 {
		t.Fatalf("silent heartbeat created %d jobs", len(values))
	}
	now = now.Add(time.Hour)
	if err := scheduler.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	values, _ := queue.List(context.Background())
	if len(values) != 1 || values[0].Kind != "heartbeat" || values[0].IdempotencyKey != "trigger/heartbeat/2026-07-31T13:00:00Z" {
		t.Fatalf("heartbeat job = %#v", values)
	}
}

func TestHeartbeatFailsSilentOnClassifierOrAuthorizationError(t *testing.T) {
	now := time.Now().UTC()
	for _, test := range []struct {
		name       string
		gate       Gate
		authorizer Authorizer
	}{
		{"classifier", GateFunc(func(context.Context, Subscription, string) (GateDecision, error) {
			return GateDecision{}, errors.New("classifier unavailable")
		}), AuthorizerFunc(func(context.Context, Subscription) error { return nil })},
		{"authorization", GateFunc(func(context.Context, Subscription, string) (GateDecision, error) {
			t.Fatal("gate called after authorization denial")
			return GateDecision{}, nil
		}), AuthorizerFunc(func(context.Context, Subscription) error { return errors.New("denied") })},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(func() time.Time { return now })
			_, _ = store.PutContext(context.Background(), testSubscription(now))
			queue := jobs.NewMemoryQueue(func() time.Time { return now })
			scheduler, err := NewScheduler(store, queue, test.gate, test.authorizer)
			if err != nil {
				t.Fatal(err)
			}
			scheduler.now = func() time.Time { return now }
			if err := scheduler.RunDue(context.Background()); err != nil {
				t.Fatal(err)
			}
			if values, _ := queue.List(context.Background()); len(values) != 0 {
				t.Fatalf("denied heartbeat created %d jobs", len(values))
			}
		})
	}
}

func TestStoreRejectsCrossChannelSubscriptionOverwrite(t *testing.T) {
	now := time.Now().UTC()
	store := NewStore(func() time.Time { return now })
	original := testSubscription(now)
	if _, err := store.PutContext(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	conflicting := original
	conflicting.ChannelID = "other-channel"
	if _, err := store.PutContext(context.Background(), conflicting); !errors.Is(err, ErrScopeConflict) {
		t.Fatalf("cross-channel overwrite error = %v", err)
	}
	stored, _ := store.GetContext(context.Background(), original.OrganizationID, original.ID)
	if stored.ChannelID != original.ChannelID {
		t.Fatalf("cross-channel overwrite changed owner: %#v", stored)
	}
}
