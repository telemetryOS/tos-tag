package routines

import (
	"context"
	"errors"
	"github.com/telemetryos/tos-tag/core/jobs"
	"testing"
	"time"
)

func TestSchedulesAreIdempotentOrdinaryJobsAndLoopsAreSuppressed(t *testing.T) {
	now := time.Now().UTC()
	store := NewStore()
	routine, err := store.Put(Routine{ID: "daily", OrganizationID: "o", WorkspaceID: "w", ChannelID: "c", RootThreadTS: "r", SessionID: "s", Generation: 1, OwnerID: "user", Input: "brief", Interval: time.Hour, NextRun: now, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	queue := jobs.NewMemoryQueue(func() time.Time { return now })
	scheduler := NewScheduler(store, queue, nil)
	scheduler.now = func() time.Time { return now }
	if err := scheduler.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	values, _ := queue.List(context.Background())
	if len(values) != 1 || values[0].Kind != "routine" {
		t.Fatalf("jobs=%#v", values)
	}
	if _, err := scheduler.Trigger(context.Background(), routine, Trigger{SourceEventID: "event", OriginTag: "tos-tag:routine"}); !errors.Is(err, ErrLoopSuppressed) {
		t.Fatal(err)
	}
}

func TestAdvanceIsTenantScoped(t *testing.T) {
	now := time.Now().UTC()
	store := NewStore()
	_, err := store.Put(Routine{ID: "shared", OrganizationID: "org-a", WorkspaceID: "w", ChannelID: "c", SessionID: "s", Generation: 1, OwnerID: "user", Input: "brief", Interval: time.Hour, NextRun: now, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceContext(context.Background(), "org-b", "shared", now); err == nil {
		t.Fatal("cross-tenant routine advance succeeded")
	}
	due, err := store.DueContext(context.Background(), now, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("routine was changed across tenant boundary: due=%#v err=%v", due, err)
	}
}
