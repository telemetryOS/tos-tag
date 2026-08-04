package observer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

func baseEnvelope(at time.Time) types.SlackEnvelope {
	return types.SlackEnvelope{
		OrganizationID: "org-1",
		EnvelopeID:     "env-1",
		EventID:        "event-1",
		TeamID:         "team-1",
		ChannelID:      "channel-1",
		MessageTS:      "100.1",
		UserID:         "user-1",
		Kind:           types.SlackEventMessage,
		Text:           "hello",
		EventTime:      at,
	}
}

func TestMemoryStoreDedupeAndSequences(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(30*24*time.Hour, func() time.Time { return now })
	first, err := store.Accept(context.Background(), baseEnvelope(now))
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.Accept(context.Background(), baseEnvelope(now))
	if err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || !duplicate.Duplicate {
		t.Fatalf("unexpected duplicate flags: first=%v second=%v", first.Duplicate, duplicate.Duplicate)
	}
	if first.Observation.PublicID != duplicate.Observation.PublicID || first.Observation.ReceivedSeq != 1 || first.Observation.OrganizationReceivedSeq != 1 {
		t.Fatalf("dedupe changed identity or sequence: first=%#v duplicate=%#v", first.Observation, duplicate.Observation)
	}
}

func TestOutputReservationIsIdempotentForOneAttemptAndExclusiveAcrossAttempts(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(24*time.Hour, func() time.Time { return now })
	accepted, err := store.Accept(context.Background(), baseEnvelope(now))
	if err != nil {
		t.Fatal(err)
	}
	observationID := accepted.Observation.PublicID
	for attempt := 0; attempt < 2; attempt++ {
		won, err := store.ReserveOutput(context.Background(), observationID, "observation/output")
		if err != nil || !won {
			t.Fatalf("same reservation attempt %d won=%v err=%v", attempt, won, err)
		}
	}
	if won, err := store.ReserveOutput(context.Background(), observationID, "different/output"); err != nil || won {
		t.Fatalf("competing reservation won=%v err=%v", won, err)
	}
	if err := store.FinalizeOutput(context.Background(), observationID, "observation/output", "job-1", ""); err != nil {
		t.Fatal(err)
	}
	if won, err := store.ReserveOutput(context.Background(), observationID, "observation/output"); err != nil || won {
		t.Fatalf("finalized reservation was reacquired won=%v err=%v", won, err)
	}
}

func TestEditAndDeleteDoNotRenewExpiry(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	clock := now
	store := NewMemoryStore(30*24*time.Hour, func() time.Time { return clock })
	if _, err := store.Accept(context.Background(), baseEnvelope(now)); err != nil {
		t.Fatal(err)
	}
	original, err := store.CurrentMessage(context.Background(), "org-1", "team-1", "channel-1", "100.1")
	if err != nil {
		t.Fatal(err)
	}

	clock = now.Add(10 * 24 * time.Hour)
	edit := baseEnvelope(clock)
	edit.EnvelopeID, edit.EventID = "env-2", "event-2"
	edit.Kind, edit.TargetTS, edit.Text = types.SlackEventEdit, "100.1", "updated"
	if _, err := store.Accept(context.Background(), edit); err != nil {
		t.Fatal(err)
	}
	updated, err := store.CurrentMessage(context.Background(), "org-1", "team-1", "channel-1", "100.1")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Text != "updated" || !updated.ExpiresAt.Equal(original.ExpiresAt) {
		t.Fatalf("edit renewed or failed projection: original=%#v updated=%#v", original, updated)
	}

	deleteEvent := edit
	deleteEvent.EnvelopeID, deleteEvent.EventID = "env-3", "event-3"
	deleteEvent.Kind, deleteEvent.Text = types.SlackEventDelete, ""
	if _, err := store.Accept(context.Background(), deleteEvent); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.CurrentMessage(context.Background(), "org-1", "team-1", "channel-1", "100.1")
	if err != nil {
		t.Fatal(err)
	}
	if !deleted.Deleted || deleted.Text != "" || !deleted.ExpiresAt.Equal(original.ExpiresAt) {
		t.Fatalf("bad deleted projection: %#v", deleted)
	}
}

func TestRedeliveredOriginalCannotUndoDelete(t *testing.T) {
	originalAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	clock := originalAt
	store := NewMemoryStore(30*24*time.Hour, func() time.Time { return clock })
	original := baseEnvelope(originalAt)
	if _, err := store.Accept(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	clock = originalAt.Add(time.Minute)
	deleted := original
	deleted.EnvelopeID, deleted.EventID = "env-delete", "event-delete"
	deleted.Kind, deleted.TargetTS, deleted.EventTime = types.SlackEventDelete, original.MessageTS, clock
	if _, err := store.Accept(context.Background(), deleted); err != nil {
		t.Fatal(err)
	}
	redelivery := original
	redelivery.EnvelopeID, redelivery.EventID = "env-redelivery", "event-redelivery"
	if _, err := store.Accept(context.Background(), redelivery); err != nil {
		t.Fatal(err)
	}
	message, err := store.CurrentMessage(context.Background(), "org-1", "team-1", "channel-1", original.MessageTS)
	if err != nil {
		t.Fatal(err)
	}
	if !message.Deleted || message.Text != "" || message.SourceEventID != deleted.EventID {
		t.Fatalf("stale original restored deleted content: %#v", message)
	}
}

func TestRecentEnforcesExpiryAndAuthorizedChannels(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	clock := now
	store := NewMemoryStore(24*time.Hour, func() time.Time { return clock })
	envelope := baseEnvelope(now)
	if _, err := store.Accept(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Recent(context.Background(), "org-1", []string{"different"}, now.Add(-time.Hour), 10); len(got) != 0 {
		t.Fatalf("unauthorized channel leaked %d messages", len(got))
	}
	if got, _ := store.Recent(context.Background(), "org-1", []string{"channel-1"}, now.Add(-time.Hour), 10); len(got) != 1 {
		t.Fatalf("expected current message, got %d", len(got))
	}
	clock = now.Add(24*time.Hour + time.Second)
	if got, _ := store.Recent(context.Background(), "org-1", []string{"channel-1"}, now.Add(-time.Hour), 10); len(got) != 0 {
		t.Fatalf("expired message leaked %d results", len(got))
	}
	if _, err := store.CurrentMessage(context.Background(), "org-1", "team-1", "channel-1", "100.1"); !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("got %v, want not found", err)
	}
}

func TestImportedHistoryIsResolvedContextAndNeverClaimed(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(30*24*time.Hour, func() time.Time { return now })
	envelope := baseEnvelope(now)
	envelope.BotID = "B-agent"
	accepted, err := store.Import(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Observation.ScopeState != "authorized" || accepted.Observation.DecisionState != "resolved" {
		t.Fatalf("imported observation state = %#v", accepted.Observation)
	}
	if _, err := store.ClaimPending(context.Background(), "worker", time.Minute); !errors.Is(err, ErrNoPendingObservation) {
		t.Fatalf("imported history became an ambient trigger: %v", err)
	}
	message, err := store.CurrentMessage(context.Background(), "org-1", "team-1", "channel-1", "100.1")
	if err != nil || message.BotID != "B-agent" {
		t.Fatalf("imported bot provenance was not retained: message=%#v err=%v", message, err)
	}
	duplicate, err := store.Accept(context.Background(), envelope)
	if err != nil || !duplicate.Duplicate || duplicate.Observation.PublicID != accepted.Observation.PublicID {
		t.Fatalf("live/history idempotency failed: duplicate=%#v err=%v", duplicate, err)
	}
}

func TestClaimPendingSkipsExpiredAndUsesReceiptOrder(t *testing.T) {
	clock := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(time.Hour, func() time.Time { return clock })
	expired := baseEnvelope(clock.Add(-2 * time.Hour))
	expired.EventID, expired.EnvelopeID = "expired", "expired"
	if _, err := store.Accept(context.Background(), expired); err != nil {
		t.Fatal(err)
	}
	first := baseEnvelope(clock)
	first.EventID, first.EnvelopeID, first.ChannelID = "first", "first", "channel-a"
	if _, err := store.Accept(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	second := baseEnvelope(clock)
	second.EventID, second.EnvelopeID, second.ChannelID = "second", "second", "channel-b"
	if _, err := store.Accept(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimPending(context.Background(), "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.EventID != "first" {
		t.Fatalf("claimed %q, want oldest unexpired receipt", claimed.EventID)
	}
}

func TestProjectionFreezesAuthorAndClearsSubtypeLikeMongo(t *testing.T) {
	clock := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(time.Hour, func() time.Time { return clock })
	original := baseEnvelope(clock)
	original.Subtype = "thread_broadcast"
	if _, err := store.Accept(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Second)
	edit := original
	edit.EventID, edit.EnvelopeID = "edit", "edit"
	edit.Kind, edit.TargetTS, edit.Text = types.SlackEventEdit, original.MessageTS, "updated"
	edit.UserID, edit.BotID, edit.Subtype = "different-user", "different-bot", ""
	if _, err := store.Accept(context.Background(), edit); err != nil {
		t.Fatal(err)
	}
	message, err := store.CurrentMessage(context.Background(), original.OrganizationID, original.TeamID, original.ChannelID, original.MessageTS)
	if err != nil {
		t.Fatal(err)
	}
	if message.AuthorID != original.UserID || message.BotID != original.BotID || message.Subtype != "" {
		t.Fatalf("projection semantics diverged: %#v", message)
	}
}
