package usage

import (
	"context"
	"testing"
)

func TestUsageContainsOnlyStructuredCounters(t *testing.T) {
	store := NewMemory()
	if err := store.Record(context.Background(), Event{OrganizationID: "o", Category: "model", ProviderID: "openai", ModelID: "gpt", Calls: 1, InputTokens: 10, OutputTokens: 2}); err != nil {
		t.Fatal(err)
	}
	events, err := store.List(context.Background(), "o", 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}
