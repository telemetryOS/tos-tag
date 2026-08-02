package memory

import (
	"context"
	"testing"
	"time"
)

func TestRecallEnforcesPrivateAndThreadScope(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	for _, record := range []Record{
		{OrganizationID: "org", ChannelID: "public", Scope: ScopeChannel, ScopeKey: "public/channel", Text: "public context", Facts: []Fact{{Text: "public fact"}}, SourceHash: "a", Status: StatusActive, ExpiresAt: now.Add(time.Hour)},
		{OrganizationID: "org", ChannelID: "private-a", Scope: ScopeChannel, ScopeKey: "private-a/channel", Restricted: true, Text: "private context", Facts: []Fact{{Text: "private fact"}}, SourceHash: "b", Status: StatusActive, ExpiresAt: now.Add(time.Hour)},
		{OrganizationID: "org", ChannelID: "public", RootThreadTS: "1", Scope: ScopeThread, ScopeKey: "public/thread/1", Text: "thread context", Facts: []Fact{{Text: "thread fact"}}, SourceHash: "c", Status: StatusActive, ExpiresAt: now.Add(time.Hour)},
	} {
		if _, _, err := store.PutGenerated(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	public, err := store.Recall(context.Background(), "org", "public", "2", now, 20)
	if err != nil || len(public) != 1 || public[0].Text != "public context" {
		t.Fatalf("public recall = %#v, %v", public, err)
	}
	private, _ := store.Recall(context.Background(), "org", "private-a", "2", now, 20)
	if len(private) != 2 {
		t.Fatalf("private destination recall = %#v", private)
	}
	thread, _ := store.Recall(context.Background(), "org", "public", "1", now, 20)
	if len(thread) != 2 {
		t.Fatalf("same-thread recall = %#v", thread)
	}
}

func TestRecallSkipsGeneratedMemoryWithoutSourceLinkedFacts(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	_, _, _ = store.PutGenerated(context.Background(), Record{OrganizationID: "org", ChannelID: "c", Scope: ScopeChannel, ScopeKey: "c/channel", Text: "No durable facts found.", SourceHash: "a", Status: StatusActive, ExpiresAt: now.Add(time.Hour)})
	if records, err := store.Recall(context.Background(), "org", "c", "", now, 20); err != nil || len(records) != 0 {
		t.Fatalf("zero-fact generated memory was recalled: %#v, %v", records, err)
	}
	corrected, _ := store.Correct(context.Background(), "org", store.byScope["org/c/channel"], "Operator-reviewed context", "admin")
	if records, err := store.Recall(context.Background(), "org", "c", "", now, 20); err != nil || len(records) != 1 || records[0].ID != corrected.ID {
		t.Fatalf("operator memory was not recalled: %#v, %v", records, err)
	}
}

func TestCorrectPinAndForget(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	record, _, _ := store.PutGenerated(context.Background(), Record{OrganizationID: "org", ChannelID: "c", Scope: ScopeChannel, ScopeKey: "c/channel", Text: "generated", SourceHash: "a", Status: StatusActive, NaturalExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(time.Hour)})
	corrected, err := store.Correct(context.Background(), "org", record.ID, "human correction", "admin")
	if err != nil || corrected.Origin != "operator" || !corrected.ExpiresAt.IsZero() {
		t.Fatalf("corrected = %#v, %v", corrected, err)
	}
	pinned, _ := store.SetPinned(context.Background(), "org", record.ID, true, "admin")
	if !pinned.Pinned {
		t.Fatal("memory was not pinned")
	}
	forgotten, _ := store.Forget(context.Background(), "org", record.ID, "admin")
	if forgotten.Status != StatusForgotten || forgotten.Text != "" || len(forgotten.SourceIDs) != 0 {
		t.Fatalf("forgotten memory retained content: %#v", forgotten)
	}
}
