package channelconfig

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/orgconfig"
	"github.com/telemetryos/tos-tag/types"
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

func TestPublishDirectiveIsIdempotentAndActive(t *testing.T) {
	store := NewStore()
	first, err := store.PublishDirective(context.Background(), "org", "alerts", "Investigate every alert.", "U1", "view-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PublishDirective(context.Background(), "org", "alerts", "forged retry content", "U1", "view-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.Prompt != "Investigate every alert." || !second.Active {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	values, _ := store.ListDirectives(context.Background(), "org", "alerts")
	if len(values) != 1 {
		t.Fatalf("directive count=%d", len(values))
	}
}

func TestEditorRequiresChannelApproverAndAuditsContentCommitment(t *testing.T) {
	ctx := context.Background()
	scopes := orgconfig.NewMemory()
	_, err := scopes.PutChannel(ctx, orgconfig.ChannelPolicy{
		OrganizationID: "org", TeamID: "team", ChannelID: "alerts", Enrolled: true,
		ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 10, MaxConcurrentJobs: 8,
		ApproverUserIDs: []string{"U_APPROVER"}, MembershipRevision: "m1", MembershipRefreshedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	appender, err := audit.NewMemoryAppender([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	editor, err := NewEditor(NewStore(), scopes, appender)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := editor.Load(ctx, EditRequest{OrganizationID: "org", WorkspaceID: "team", ChannelID: "alerts", ActorID: "U_OTHER"}); !errors.Is(err, ErrDirectiveForbidden) {
		t.Fatalf("unauthorized load error=%v", err)
	}
	saved, err := editor.Save(ctx, EditRequest{OrganizationID: "org", WorkspaceID: "team", ChannelID: "alerts", ActorID: "U_APPROVER", Prompt: "Investigate each alert using OTel evidence.", SourceID: "view-1"})
	if err != nil || saved.Revision != 1 {
		t.Fatalf("saved=%#v err=%v", saved, err)
	}
	receipts := appender.List("org")
	if len(receipts) != 1 || receipts[0].ContentCommitment == "" || strings.Contains(receipts[0].ContentCommitment, "Investigate") {
		t.Fatalf("receipt=%#v", receipts)
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
