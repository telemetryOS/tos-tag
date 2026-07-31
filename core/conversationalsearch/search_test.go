package conversationalsearch

import (
	"context"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/observer"
	"github.com/telemetryos/tos-tag/types"
)

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
	searcher, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	results, err := searcher.Search(context.Background(), Request{OrganizationID: "org", Query: "outage", TargetChannelID: "support", RequesterChannels: []string{"support", "alerts", "private"}, PrincipalChannels: []string{"support", "alerts", "private"}, AudienceChannels: []string{"support", "alerts"}, MembershipRevision: "membership/v1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	for _, result := range results {
		if result.ChannelID == "private" {
			t.Fatal("private channel leaked through incomplete audience intersection")
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
	request := Request{OrganizationID: "org", Query: "outage", TargetChannelID: "support", RequesterChannels: []string{"support", "alerts"}, PrincipalChannels: []string{"support", "alerts"}, AudienceChannels: []string{"support", "alerts"}, MembershipRevision: "stale/v1", MembershipStale: true, Limit: 10}
	results, err := searcher.Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ChannelID != "support" {
		t.Fatalf("stale membership results = %#v", results)
	}
}
