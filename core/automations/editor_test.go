package automations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/orgconfig"
	"github.com/telemetryos/tos-tag/core/routines"
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
	editor, err := NewEditor(routineStore, triggerStore, scopes, appender)
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{OrganizationID: "org", WorkspaceID: "team", ChannelID: "alerts", ActorID: "user"}
	listed, err := editor.List(context.Background(), scope)
	if err != nil || len(listed) != 1 || listed[0].Instruction != "Check alerts." {
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
