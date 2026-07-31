package contextpacks

import (
	"errors"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/config"
	"github.com/telemetryos/tos-tag/types"
)

func tinyBudget() config.ContextPackConfig {
	return config.ContextPackConfig{MaxTokens: 12, System: 2, Thread: 2, Channel: 2, RecentOrg: 2, Evidence: 1, Situation: 1, Headroom: 2}
}

func candidate(id, channel string, partition types.ContextPartition, text string, at time.Time) types.ContextCandidate {
	return types.ContextCandidate{ID: id, Version: 1, OrganizationID: "org-1", ChannelID: channel, Partition: partition, Text: text, ObservedAt: at, DisclosureClass: types.DisclosureDestinationSafe}
}

func TestBuilderIsDeterministicAndBounded(t *testing.T) {
	builder, err := New(tinyBudget(), WordTokenizer{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	request := Request{
		OrganizationID: "org-1", TargetObservationID: "obs-1", OrganizationWatermark: 42,
		PolicyRevision: "policy-1", MembershipRevision: "members-1", CreatedAt: now,
		Candidates: []types.ContextCandidate{
			candidate("sys", "", types.PartitionSystem, "safe rules", now),
			candidate("a-new", "a", types.PartitionRecentOrg, "a new", now),
			candidate("a-old", "a", types.PartitionRecentOrg, "a old", now.Add(-time.Minute)),
			candidate("b-new", "b", types.PartitionRecentOrg, "b new", now),
		},
	}
	first, err := builder.Build(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := builder.Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ContentHash != second.ContentHash || first.TotalTokens > tinyBudget().MaxTokens-tinyBudget().Headroom {
		t.Fatalf("non-deterministic or over budget: first=%#v second=%#v", first, second)
	}
	if len(first.Sources) != 2 || first.Sources[1].ID != "a-new" {
		t.Fatalf("unexpected bounded sources: %#v", first.Sources)
	}
}

func TestBuilderFairOrdersChannels(t *testing.T) {
	got := fairOrder([]types.ContextCandidate{
		{ID: "a1", ChannelID: "a", Priority: 10},
		{ID: "a2", ChannelID: "a", Priority: 9},
		{ID: "b1", ChannelID: "b", Priority: 1},
	})
	if got[0].ID != "a1" || got[1].ID != "b1" || got[2].ID != "a2" {
		t.Fatalf("noisy channel monopolized ordering: %#v", got)
	}
}

func TestBuilderRejectsOversizedRequiredSource(t *testing.T) {
	builder, _ := New(tinyBudget(), WordTokenizer{})
	now := time.Now().UTC()
	item := candidate("required", "", types.PartitionSystem, "one two three", now)
	item.Required = true
	_, err := builder.Build(Request{OrganizationID: "org-1", TargetObservationID: "obs-1", CreatedAt: now, Candidates: []types.ContextCandidate{item}})
	if !errors.Is(err, ErrRequiredSourceTooLarge) {
		t.Fatalf("got %v, want required source error", err)
	}
}

func TestBuilderExcludesExpiredAndOtherTenantSources(t *testing.T) {
	builder, _ := New(tinyBudget(), WordTokenizer{})
	now := time.Now().UTC()
	expired := candidate("expired", "a", types.PartitionChannel, "old", now)
	expired.SourceExpiresAt = now.Add(-time.Second)
	other := candidate("other", "a", types.PartitionChannel, "secret", now)
	other.OrganizationID = "org-2"
	pack, err := builder.Build(Request{OrganizationID: "org-1", TargetObservationID: "obs-1", CreatedAt: now, Candidates: []types.ContextCandidate{expired, other}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Sources) != 0 {
		t.Fatalf("unauthorized/expired sources leaked: %#v", pack.Sources)
	}
}
