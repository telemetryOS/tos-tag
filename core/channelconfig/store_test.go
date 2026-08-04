package channelconfig

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/orgconfig"
	"github.com/telemetryos/tos-tag/models"
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

func TestFailedDirectiveActivationPreservesCurrentRevision(t *testing.T) {
	store := NewStore()
	directive, err := store.PublishDirective(context.Background(), "org", "alerts", "Investigate alerts.", "U1", "source-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateDirective(context.Background(), "org", "alerts", "missing"); err == nil {
		t.Fatal("missing directive activation succeeded")
	}
	active, err := store.ActiveDirective(context.Background(), "org", "alerts")
	if err != nil || active.ID != directive.ID {
		t.Fatalf("active=%#v err=%v", active, err)
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

func TestListDirectivesWithoutChannelReturnsOrganizationHistory(t *testing.T) {
	store := NewStore()
	first, err := store.PublishDirective(context.Background(), "org", "alerts", "Investigate alerts.", "U1", "source-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PublishDirective(context.Background(), "org", "support", "Answer support questions.", "U1", "source-2")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = store.PublishDirective(context.Background(), "other-org", "alerts", "Do not include this.", "U1", "source-3")
	values, err := store.ListDirectives(context.Background(), "org", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0].ID != first.ID || values[1].ID != second.ID || !values[0].Active || !values[1].Active {
		t.Fatalf("organization directives=%#v", values)
	}
}

func TestListNotesWithoutChannelReturnsOrganizationHistory(t *testing.T) {
	store := NewStore()
	first, err := store.ProposeNote(context.Background(), "org", "alerts", "Alert note.", []string{"m1"}, "U1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ProposeNote(context.Background(), "org", "support", "Support note.", []string{"m2"}, "U1")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = store.ProposeNote(context.Background(), "other-org", "alerts", "Do not include this.", []string{"m3"}, "U1")
	values, err := store.ListNotes(context.Background(), "org", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0].ID != first.ID || values[1].ID != second.ID {
		t.Fatalf("organization notes=%#v", values)
	}
}

func TestEditorAllowsAnyWorkspaceUserInAnEnrolledChannelAndAuditsContentCommitment(t *testing.T) {
	ctx := context.Background()
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutWorkspace(ctx, models.Workspace{OrganizationID: "org", TeamID: "team", Enabled: true})
	_, err := scopes.PutChannel(ctx, orgconfig.ChannelPolicy{
		OrganizationID: "org", TeamID: "team", ChannelID: "alerts", Enrolled: true,
		ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 10, MaxConcurrentJobs: 8,
		ApproverUserIDs: []string{"U_APPROVER"}, MembershipRevision: "stale-on-purpose", MembershipRefreshedAt: time.Now().UTC().Add(-72 * time.Hour),
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
	if _, err := editor.Load(ctx, EditRequest{OrganizationID: "org", WorkspaceID: "team", ChannelID: "alerts", ActorID: "U_OTHER"}); err != nil {
		t.Fatalf("workspace user load error=%v", err)
	}
	saved, err := editor.Save(ctx, EditRequest{OrganizationID: "org", WorkspaceID: "team", ChannelID: "alerts", ActorID: "U_OTHER", Prompt: "Investigate each alert using OTel evidence.", SourceID: "view-1"})
	if err != nil || saved.Revision != 1 {
		t.Fatalf("saved=%#v err=%v", saved, err)
	}
	receipts := appender.List("org")
	if len(receipts) != 1 || receipts[0].ContentCommitment == "" || strings.Contains(receipts[0].ContentCommitment, "Investigate") {
		t.Fatalf("receipt=%#v", receipts)
	}
	if _, err := editor.Load(ctx, EditRequest{OrganizationID: "org", WorkspaceID: "other-team", ChannelID: "alerts", ActorID: "U_OTHER"}); !errors.Is(err, ErrDirectiveForbidden) {
		t.Fatalf("cross-workspace load error=%v", err)
	}
	if _, err := editor.Load(ctx, EditRequest{OrganizationID: "org", WorkspaceID: "team", ChannelID: "alerts"}); !errors.Is(err, ErrDirectiveForbidden) {
		t.Fatalf("missing actor load error=%v", err)
	}
	policy, err := scopes.Resolve(ctx, "org", "team", "alerts")
	if err != nil {
		t.Fatal(err)
	}
	policy.KillSwitch = true
	if _, err := scopes.PutChannel(ctx, policy); err != nil {
		t.Fatal(err)
	}
	if _, err := editor.Load(ctx, EditRequest{OrganizationID: "org", WorkspaceID: "team", ChannelID: "alerts", ActorID: "U_OTHER"}); !errors.Is(err, ErrDirectiveForbidden) {
		t.Fatalf("kill-switched channel load error=%v", err)
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
