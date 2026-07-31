package conversationalsearch

import (
	"context"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/observer"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

type recordingSource struct {
	Source
	channels []string
}

func (s *recordingSource) Recent(ctx context.Context, organizationID string, channelIDs []string, since time.Time, limit int) ([]models.ChannelMessage, error) {
	s.channels = append([]string(nil), channelIDs...)
	return s.Source.Recent(ctx, organizationID, channelIDs, since, limit)
}

func TestSearchUsesCompleteAudienceIntersection(t *testing.T) {
	store := observer.NewMemoryStore(30*24*time.Hour, nil)
	accept := func(eventID, channelID, text string) {
		now := time.Now().UTC()
		_, err := store.Accept(context.Background(), types.SlackEnvelope{OrganizationID: "org", TeamID: "team", ChannelID: channelID, EnvelopeID: "env-" + eventID, EventID: eventID, MessageTS: eventID, UserID: "user", Kind: types.SlackEventMessage, Text: text, EventTime: now, ReceivedAt: now})
		if err != nil {
			t.Fatal(err)
		}
	}
	accept("1", "support", "customer asks about outage")
	accept("2", "alerts", "production outage is active")
	accept("3", "private", "private outage details")
	recording := &recordingSource{Source: store}
	searcher, err := New(recording)
	if err != nil {
		t.Fatal(err)
	}
	results, err := searcher.Search(context.Background(), Request{OrganizationID: "org", Query: "outage", TargetChannelID: "support", RequesterChannels: []string{"support", "alerts", "private"}, PrincipalChannels: []string{"support", "alerts", "private"}, AudienceChannels: []string{"support", "alerts", "private"}, RestrictedChannels: []string{"private"}, MembershipRevision: "membership/v1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	if contains(recording.channels, "private") {
		t.Fatalf("private channel reached the source query: %v", recording.channels)
	}
	for _, result := range results {
		if result.ChannelID == "private" {
			t.Fatal("private channel leaked through incomplete audience intersection")
		}
	}
}

func TestPrivateDestinationIncludesItselfButNotAnotherPrivateChannel(t *testing.T) {
	store := observer.NewMemoryStore(30*24*time.Hour, nil)
	now := time.Now().UTC()
	for _, item := range []struct {
		channel string
		text    string
	}{
		{channel: "management", text: "management roadmap"},
		{channel: "other-private", text: "other private roadmap"},
		{channel: "public-status", text: "public roadmap"},
	} {
		_, err := store.Accept(context.Background(), types.SlackEnvelope{OrganizationID: "org", TeamID: "team", ChannelID: item.channel, EnvelopeID: item.channel, EventID: item.channel, MessageTS: item.channel, UserID: "user", Kind: types.SlackEventMessage, Text: item.text, EventTime: now, ReceivedAt: now})
		if err != nil {
			t.Fatal(err)
		}
	}
	recording := &recordingSource{Source: store}
	searcher, _ := New(recording)
	allChannels := []string{"management", "other-private", "public-status"}
	results, err := searcher.Search(context.Background(), Request{OrganizationID: "org", Query: "roadmap", TargetChannelID: "management", RequesterChannels: allChannels, PrincipalChannels: allChannels, AudienceChannels: allChannels, RestrictedChannels: []string{"management", "other-private"}, MembershipRevision: "membership/v1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(recording.channels) != 2 || !contains(recording.channels, "management") || !contains(recording.channels, "public-status") || contains(recording.channels, "other-private") {
		t.Fatalf("private destination query channels = %v", recording.channels)
	}
	if len(results) != 2 {
		t.Fatalf("private destination results = %#v", results)
	}
	for _, result := range results {
		if result.ChannelID == "other-private" {
			t.Fatalf("other private channel leaked: %#v", result)
		}
	}
}

func TestStaleMembershipFailsClosedToCurrentChannel(t *testing.T) {
	store := observer.NewMemoryStore(30*24*time.Hour, nil)
	now := time.Now().UTC()
	for _, channel := range []string{"support", "alerts"} {
		_, err := store.Accept(context.Background(), types.SlackEnvelope{OrganizationID: "org", TeamID: "team", ChannelID: channel, EnvelopeID: channel, EventID: channel, MessageTS: channel, UserID: "user", Kind: types.SlackEventMessage, Text: "outage", EventTime: now, ReceivedAt: now})
		if err != nil {
			t.Fatal(err)
		}
	}
	searcher, _ := New(store)
	request := Request{OrganizationID: "org", Query: "outage", TargetChannelID: "support", RequesterChannels: []string{"support", "alerts"}, PrincipalChannels: []string{"support", "alerts"}, AudienceChannels: []string{"support", "alerts"}, RestrictedChannels: []string{}, MembershipRevision: "stale/v1", MembershipStale: true, Limit: 10}
	results, err := searcher.Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ChannelID != "support" {
		t.Fatalf("stale membership results = %#v", results)
	}
}
