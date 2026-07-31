package admission

import (
	"context"
	"errors"
	"github.com/telemetryos/tos-tag/core/orgconfig"
	"github.com/telemetryos/tos-tag/types"
	"testing"
	"time"
)

func TestLimitsAndIdempotentCompletion(t *testing.T) {
	now := time.Now().UTC()
	clock := now
	store := NewMemory(func() time.Time { return clock })
	policy := orgconfig.ChannelPolicy{OrganizationID: "o", TeamID: "t", ChannelID: "c", Enrolled: true, ParticipationMode: types.ModeAssist, Cooldown: time.Minute, MaxResponsesPerHour: 2, MaxConcurrentJobs: 1, MembershipRevision: "m", MembershipRefreshedAt: now}
	id, err := store.Admit(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Admit(context.Background(), policy); !errors.Is(err, ErrConcurrency) {
		t.Fatal(err)
	}
	store.Complete(context.Background(), id)
	store.Complete(context.Background(), id)
	if _, err := store.Admit(context.Background(), policy); !errors.Is(err, ErrCooldown) {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	if _, err := store.Admit(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
}
