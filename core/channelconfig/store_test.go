package channelconfig

import (
	"context"
	"strings"
	"testing"
)

func TestDirectiveActivationAndRollback(t *testing.T) {
	store := NewStore()
	first, err := store.DraftDirective(context.Background(), "org", "alerts", "Prefer concise incident updates.", "admin-1")
	if err != nil {
		t.Fatal(err)
	}
	second, _ := store.DraftDirective(context.Background(), "org", "alerts", "Include a status link.", "admin-1")
	if _, err := store.ActivateDirective(context.Background(), "org", "alerts", second.ID); err != nil {
		t.Fatal(err)
	}
	if active, _ := store.ActiveDirective(context.Background(), "org", "alerts"); active.ID != second.ID {
		t.Fatalf("active = %#v", active)
	}
	if _, err := store.ActivateDirective(context.Background(), "org", "alerts", first.ID); err != nil {
		t.Fatal(err)
	}
	if active, _ := store.ActiveDirective(context.Background(), "org", "alerts"); active.ID != first.ID {
		t.Fatal("rollback did not restore prior directive")
	}
}

func TestNotesRequireSourceAndIndependentReview(t *testing.T) {
	store := NewStore()
	if _, err := store.ProposeNote(context.Background(), "org", "support", "system uses region west", nil, "agent"); err == nil {
		t.Fatal("unsourced note accepted")
	}
	note, err := store.ProposeNote(context.Background(), "org", "support", "system uses region west", []string{"support/1"}, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReviewNote(context.Background(), "org", "support", note.ID, "agent", true); err == nil {
		t.Fatal("self-review was accepted")
	}
	active, err := store.ReviewNote(context.Background(), "org", "support", note.ID, "human-reviewer", true)
	if err != nil {
		t.Fatal(err)
	}
	if active.State != NoteActive || !strings.Contains(DelimitedNoteData(active), "<channel-note") {
		t.Fatalf("active note = %#v", active)
	}
}
