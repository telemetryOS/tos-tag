package automations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/orgconfig"
	"github.com/telemetryos/tos-tag/core/routines"
	"github.com/telemetryos/tos-tag/core/sessions"
	"github.com/telemetryos/tos-tag/core/triggers"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

func TestEditorListsAndUpdatesOnlyTheRequestedChannel(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	routineStore := routines.NewStore()
	triggerStore := triggers.NewStore(func() time.Time { return now })
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org", Name: "Org"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org", TeamID: "team", Name: "Team", Enabled: true})
	for _, channelID := range []string{"alerts", "operations"} {
		_, _ = scopes.PutChannel(context.Background(), orgconfig.ChannelPolicy{OrganizationID: "org", TeamID: "team", ChannelID: channelID, Name: channelID, Enrolled: true, ParticipationMode: types.ModeAssist, Cooldown: time.Second, MaxResponsesPerHour: 10, MaxConcurrentJobs: 2, ApproverUserIDs: []string{"user"}, MembershipRevision: "m1", MembershipRefreshedAt: now})
	}
	base := triggers.Subscription{ID: "daily", OrganizationID: "org", WorkspaceID: "team", ChannelID: "alerts", SessionID: "s1", Generation: 1, OwnerID: "owner", Kind: triggers.KindHeartbeat, Instruction: "Check alerts.", Cron: "0 9 * * *", Timezone: "UTC", ClassifierGate: true, MinConfidence: .8, Enabled: true}
	saved, err := triggerStore.PutContext(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	other := base
	other.ChannelID, other.SessionID = "operations", "s2"
	if _, err := triggerStore.PutContext(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	appender, _ := audit.NewMemoryAppender([]byte("01234567890123456789012345678901"))
	editor, err := NewEditor(routineStore, triggerStore, sessions.NewMemoryStore(func() time.Time { return now }), scopes, appender, nil, "America/Vancouver")
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{OrganizationID: "org", WorkspaceID: "team", ChannelID: "alerts", ActorID: "user"}
	listed, err := editor.List(context.Background(), scope)
	if err != nil || len(listed.Tasks) != 1 || listed.Tasks[0].Instruction != "Check alerts." || listed.DefaultTimezone != "America/Vancouver" {
		t.Fatalf("listed=%#v err=%v", listed, err)
	}
	updated, err := editor.Save(context.Background(), SaveRequest{Scope: scope, Kind: KindHeartbeat, ID: "daily", Instruction: "Check unresolved alerts.", Cron: "30 9 * * *", Timezone: "America/Vancouver", MinConfidence: .9, Enabled: false, Version: saved.Version, SourceID: "view-1"})
	if err != nil || updated.Instruction != "Check unresolved alerts." || updated.Enabled || updated.Version != saved.Version+1 {
		t.Fatalf("updated=%#v err=%v", updated, err)
	}
	operations, _ := triggerStore.GetContext(context.Background(), "org", "team", "operations", "daily")
	if operations.Instruction != "Check alerts." {
		t.Fatalf("other channel changed: %#v", operations)
	}
	if _, err := editor.Save(context.Background(), SaveRequest{Scope: scope, Kind: KindHeartbeat, ID: "daily", Instruction: "stale", Cron: "0 1 * * *", Timezone: "UTC", MinConfidence: .8, Enabled: true, Version: saved.Version, SourceID: "view-2"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale edit error=%v", err)
	}
}

func TestGlobalOperatorCanEditEveryEnrolledChannel(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	routineStore := routines.NewStore()
	triggerStore := triggers.NewStore(func() time.Time { return now })
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org", Name: "Org"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org", TeamID: "team", Name: "Team", Enabled: true})
	for _, channelID := range []string{"alerts", "operations"} {
		_, _ = scopes.PutChannel(context.Background(), orgconfig.ChannelPolicy{OrganizationID: "org", TeamID: "team", ChannelID: channelID, Name: channelID, Enrolled: true, ParticipationMode: types.ModeAssist, Cooldown: time.Second, MaxResponsesPerHour: 10, MaxConcurrentJobs: 2, ApproverUserIDs: []string{"channel-approver"}, MembershipRevision: "m1", MembershipRefreshedAt: now})
		if _, err := routineStore.PutContext(context.Background(), routines.Routine{ID: "daily", OrganizationID: "org", WorkspaceID: "team", ChannelID: channelID, SessionID: types.SessionID(channelID), Generation: 1, OwnerID: "owner", Input: "Review the channel.", Cron: "0 9 * * *", Timezone: "UTC", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	appender, _ := audit.NewMemoryAppender([]byte("01234567890123456789012345678901"))
	editor, err := NewEditor(routineStore, triggerStore, sessions.NewMemoryStore(func() time.Time { return now }), scopes, appender, []string{"global-operator"}, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	for _, channelID := range []string{"alerts", "operations"} {
		scope := Scope{OrganizationID: "org", WorkspaceID: "team", ChannelID: channelID, ActorID: "global-operator"}
		listed, err := editor.List(context.Background(), scope)
		if err != nil || len(listed.Tasks) != 1 || !listed.Editable || !listed.Tasks[0].Editable {
			t.Fatalf("channel=%s listed=%#v err=%v", channelID, listed, err)
		}
		loaded, err := editor.Load(context.Background(), scope, KindRoutine, "daily")
		if err != nil || !loaded.Editable {
			t.Fatalf("channel=%s loaded=%#v err=%v", channelID, loaded, err)
		}
		saved, err := editor.Save(context.Background(), SaveRequest{Scope: scope, Kind: KindRoutine, ID: "daily", Instruction: "Review this channel and summarize material changes.", Cron: "30 9 * * *", Timezone: "UTC", Enabled: true, Version: loaded.Version, SourceID: "view-" + channelID})
		if err != nil {
			t.Fatalf("channel=%s save error=%v", channelID, err)
		}
		if _, err := editor.Delete(context.Background(), DeleteRequest{Scope: scope, Kind: KindRoutine, ID: "daily", Version: loaded.Version, SourceID: "stale-delete-" + channelID}); !errors.Is(err, ErrConflict) {
			t.Fatalf("channel=%s stale delete error=%v", channelID, err)
		}
		deleted, err := editor.Delete(context.Background(), DeleteRequest{Scope: scope, Kind: KindRoutine, ID: "daily", Version: saved.Version, SourceID: "delete-" + channelID})
		if err != nil || deleted.ID != "daily" {
			t.Fatalf("channel=%s deleted=%#v err=%v", channelID, deleted, err)
		}
		if _, err := editor.Load(context.Background(), scope, KindRoutine, "daily"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("channel=%s deleted routine load error=%v", channelID, err)
		}
	}
}

func TestUnconfiguredUserGetsReadOnlyAutomationList(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	routineStore := routines.NewStore()
	triggerStore := triggers.NewStore(func() time.Time { return now })
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org", Name: "Org"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org", TeamID: "team", Name: "Team", Enabled: true})
	_, _ = scopes.PutChannel(context.Background(), orgconfig.ChannelPolicy{OrganizationID: "org", TeamID: "team", ChannelID: "alerts", Name: "alerts", Enrolled: true, ParticipationMode: types.ModeAssist, Cooldown: time.Second, MaxResponsesPerHour: 10, MaxConcurrentJobs: 2, MembershipRevision: "m1", MembershipRefreshedAt: now})
	if _, err := routineStore.PutContext(context.Background(), routines.Routine{ID: "daily", OrganizationID: "org", WorkspaceID: "team", ChannelID: "alerts", SessionID: "alerts", Generation: 1, OwnerID: "owner", Input: "Review the channel.", Cron: "0 9 * * *", Timezone: "UTC", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	appender, _ := audit.NewMemoryAppender([]byte("01234567890123456789012345678901"))
	editor, err := NewEditor(routineStore, triggerStore, sessions.NewMemoryStore(func() time.Time { return now }), scopes, appender, []string{"global-operator"}, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{OrganizationID: "org", WorkspaceID: "team", ChannelID: "alerts", ActorID: "viewer"}
	listed, err := editor.List(context.Background(), scope)
	if err != nil || len(listed.Tasks) != 1 || listed.Editable || listed.Tasks[0].Editable {
		t.Fatalf("listed=%#v err=%v", listed, err)
	}
	if _, err := editor.Load(context.Background(), scope, KindRoutine, "daily"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("read-only user load error=%v", err)
	}
	if _, err := editor.Delete(context.Background(), DeleteRequest{Scope: scope, Kind: KindRoutine, ID: "daily", Version: 1, SourceID: "view-delete"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("read-only user delete error=%v", err)
	}
}

func TestEditorCreatesAChannelBoundClassifierGatedAutomation(t *testing.T) {
	now := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	routineStore := routines.NewStore()
	triggerStore := triggers.NewStore(func() time.Time { return now })
	sessionStore := sessions.NewMemoryStore(func() time.Time { return now })
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org", Name: "Org"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org", TeamID: "team", Name: "Team", Enabled: true})
	_, _ = scopes.PutChannel(context.Background(), orgconfig.ChannelPolicy{OrganizationID: "org", TeamID: "team", ChannelID: "management", Name: "management", Enrolled: true, ParticipationMode: types.ModeAssist, Cooldown: time.Second, MaxResponsesPerHour: 10, MaxConcurrentJobs: 2, MembershipRevision: "m1", MembershipRefreshedAt: now})
	appender, _ := audit.NewMemoryAppender([]byte("01234567890123456789012345678901"))
	editor, err := NewEditor(routineStore, triggerStore, sessionStore, scopes, appender, []string{"global-operator"}, "America/Vancouver")
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{OrganizationID: "org", WorkspaceID: "team", ChannelID: "management", ActorID: "global-operator"}
	listed, err := editor.List(context.Background(), scope)
	if err != nil || !listed.Editable || len(listed.Tasks) != 0 {
		t.Fatalf("listed=%#v err=%v", listed, err)
	}
	created, err := editor.Save(context.Background(), SaveRequest{Scope: scope, Kind: KindHeartbeat, ID: "weekday-summary", Instruction: "Summarize material management updates.", Cron: "0 17 * * 1-5", Timezone: listed.DefaultTimezone, MinConfidence: .8, Enabled: true, SourceID: "view-new"})
	if err != nil || created.Version != 1 || created.Timezone != "America/Vancouver" {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	stored, err := triggerStore.GetContext(context.Background(), "org", "team", "management", "weekday-summary")
	if err != nil || !stored.ClassifierGate || stored.OwnerID != "global-operator" || stored.SessionID == "" || stored.Generation != 1 {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	if _, err := editor.Save(context.Background(), SaveRequest{Scope: scope, Kind: KindHeartbeat, ID: "weekday-summary", Instruction: "Duplicate.", Cron: "0 18 * * 1-5", Timezone: listed.DefaultTimezone, MinConfidence: .8, Enabled: true, SourceID: "view-duplicate"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate create error=%v", err)
	}
	deleted, err := editor.Delete(context.Background(), DeleteRequest{Scope: scope, Kind: KindHeartbeat, ID: created.ID, Version: created.Version, SourceID: "view-delete"})
	if err != nil || deleted.ID != created.ID {
		t.Fatalf("deleted=%#v err=%v", deleted, err)
	}
	if _, err := triggerStore.GetContext(context.Background(), "org", "team", "management", created.ID); err == nil {
		t.Fatal("deleted classifier-gated automation still exists")
	}
}
