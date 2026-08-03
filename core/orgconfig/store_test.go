package orgconfig

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

func TestMemoryChannelPolicy(t *testing.T) {
	store := NewMemory()
	policy := ChannelPolicy{OrganizationID: "org", TeamID: "team", ChannelID: "alerts", Enrolled: true, ParticipationMode: types.ModeAssist, Cooldown: time.Minute, MaxResponsesPerHour: 6, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: time.Now().UTC()}
	saved, err := store.PutChannel(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version != 1 {
		t.Fatal(saved.Version)
	}
	if saved.ContextHistoryMode != types.ContextHistoryDurable {
		t.Fatalf("default context history mode = %q", saved.ContextHistoryMode)
	}
	got, err := store.Resolve(context.Background(), "org", "team", "alerts")
	if err != nil || got.ParticipationMode != types.ModeAssist {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	if _, err := store.Resolve(context.Background(), "org", "team", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestMemoryListOrganizationsIsStable(t *testing.T) {
	store := NewMemory()
	for _, organization := range []models.Organization{{PublicID: "org-z", Name: "Zulu"}, {PublicID: "org-a", Name: "Alpha"}} {
		if _, err := store.PutOrganization(context.Background(), organization); err != nil {
			t.Fatal(err)
		}
	}
	organizations, err := store.ListOrganizations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(organizations) != 2 || organizations[0].PublicID != "org-a" || organizations[1].PublicID != "org-z" {
		t.Fatalf("organizations = %#v", organizations)
	}
}

func TestOrganizationAndWorkspaceKillSwitchesDenyResolvedChannel(t *testing.T) {
	for name, values := range map[string][2]bool{
		"organization": {true, true},
		"workspace":    {false, false},
	} {
		t.Run(name, func(t *testing.T) {
			organizationKilled, workspaceEnabled := values[0], values[1]
			store := NewMemory()
			_, _ = store.PutOrganization(context.Background(), models.Organization{PublicID: "org", KillSwitch: organizationKilled})
			_, _ = store.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org", TeamID: "team", Enabled: workspaceEnabled})
			_, err := store.PutChannel(context.Background(), ChannelPolicy{OrganizationID: "org", TeamID: "team", ChannelID: "alerts", Enrolled: true, ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 1, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: time.Now().UTC()})
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := store.Resolve(context.Background(), "org", "team", "alerts")
			if err != nil || !resolved.KillSwitch {
				t.Fatalf("resolved=%#v err=%v", resolved, err)
			}
		})
	}
}

func TestChannelPolicyValidation(t *testing.T) {
	err := ValidateChannel(ChannelPolicy{OrganizationID: "o", TeamID: "t", ChannelID: "c", ParticipationMode: "loud", MembershipRevision: "m", MembershipRefreshedAt: time.Now(), MaxResponsesPerHour: 1, MaxConcurrentJobs: 1})
	if err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestChannelPolicyRejectsInvalidContextHistoryMode(t *testing.T) {
	err := ValidateChannel(ChannelPolicy{OrganizationID: "o", TeamID: "t", ChannelID: "c", ParticipationMode: types.ModeObserve, ContextHistoryMode: "forever", MembershipRevision: "m", MembershipRefreshedAt: time.Now(), MaxResponsesPerHour: 1, MaxConcurrentJobs: 1})
	if err == nil {
		t.Fatal("invalid context history mode accepted")
	}
}

func TestUpsertContextChannelPreservesOperatorPolicyAndNeverUnrestricts(t *testing.T) {
	store := NewMemory()
	now := time.Now().UTC()
	existing := ChannelPolicy{
		OrganizationID: "org", TeamID: "team", ChannelID: "tos-tag", Name: "old",
		Enrolled: false, Restricted: true, ParticipationMode: types.ModeProactive,
		KillSwitch: true, Cooldown: time.Minute, MaxResponsesPerHour: 2, MaxConcurrentJobs: 3,
		DefaultModelProfile: "custom", ContextHistoryMode: types.ContextHistorySessionOnly, ApproverUserIDs: []string{"U_OPERATOR"}, MembershipRevision: "old", MembershipRefreshedAt: now.Add(-time.Hour),
	}
	if _, err := store.PutChannel(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	refreshed, err := store.UpsertContextChannel(context.Background(), ChannelPolicy{
		OrganizationID: "org", TeamID: "team", ChannelID: "tos-tag", Name: "renamed",
		Enrolled: true, Restricted: false, ParticipationMode: types.ModeObserve,
		Cooldown: 30 * time.Second, MaxResponsesPerHour: 6, MaxConcurrentJobs: 1,
		DefaultModelProfile: "default", MembershipRevision: "slack-user-context/v1", MembershipRefreshedAt: now,
		BotIsMember: true, BotMembershipKnown: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Enrolled || !refreshed.Restricted || refreshed.ParticipationMode != types.ModeProactive || !refreshed.KillSwitch || refreshed.Cooldown != time.Minute || refreshed.MaxResponsesPerHour != 2 || refreshed.MaxConcurrentJobs != 3 || refreshed.DefaultModelProfile != "custom" || refreshed.ContextHistoryMode != types.ContextHistorySessionOnly || len(refreshed.ApproverUserIDs) != 1 || refreshed.ApproverUserIDs[0] != "U_OPERATOR" {
		t.Fatalf("context refresh widened operator policy: %#v", refreshed)
	}
	if refreshed.Name != "renamed" || refreshed.MembershipRevision != "slack-user-context/v1" || !refreshed.MembershipRefreshedAt.Equal(now) {
		t.Fatalf("context metadata was not refreshed: %#v", refreshed)
	}
	if !refreshed.BotMembershipKnown || !refreshed.BotIsMember {
		t.Fatalf("bot membership metadata was not refreshed: %#v", refreshed)
	}
}

func TestUpsertContextChannelMembershipManagementOwnsOnlyParticipation(t *testing.T) {
	store := NewMemory()
	now := time.Now().UTC()
	existing := ChannelPolicy{
		OrganizationID: "org", TeamID: "team", ChannelID: "alerts", Enrolled: true,
		ParticipationMode: types.ModeProactive, KillSwitch: true, Cooldown: time.Minute,
		MaxResponsesPerHour: 2, MaxConcurrentJobs: 3, DefaultModelProfile: "custom",
		MembershipRevision: "old", MembershipRefreshedAt: now.Add(-time.Hour),
	}
	if _, err := store.PutChannel(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	refreshed, err := store.UpsertContextChannel(context.Background(), ChannelPolicy{
		OrganizationID: "org", TeamID: "team", ChannelID: "alerts", Enrolled: true,
		ParticipationMode: types.ModeAssist, ParticipationManagedByMembership: true,
		BotIsMember: true, BotMembershipKnown: true, Cooldown: 30 * time.Second,
		MaxResponsesPerHour: 120, MaxConcurrentJobs: 8,
		MembershipRevision: "slack-user+bot-context/v2", MembershipRefreshedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.ParticipationMode != types.ModeAssist || !refreshed.ParticipationManagedByMembership || !refreshed.BotIsMember || !refreshed.BotMembershipKnown {
		t.Fatalf("membership-managed participation = %#v", refreshed)
	}
	if !refreshed.KillSwitch || refreshed.Cooldown != time.Minute || refreshed.MaxResponsesPerHour != 2 || refreshed.MaxConcurrentJobs != 3 || refreshed.DefaultModelProfile != "custom" {
		t.Fatalf("membership refresh changed unrelated operator policy: %#v", refreshed)
	}
	disabled, err := store.UpsertContextChannel(context.Background(), ChannelPolicy{
		OrganizationID: "org", TeamID: "team", ChannelID: "alerts", Enrolled: true,
		ParticipationMode: types.ModeObserve, BotIsMember: true, BotMembershipKnown: true,
		Cooldown: 30 * time.Second, MaxResponsesPerHour: 120, MaxConcurrentJobs: 8,
		MembershipRevision: "slack-user+bot-context/v2", MembershipRefreshedAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if disabled.ParticipationMode != types.ModeObserve || disabled.ParticipationManagedByMembership {
		t.Fatalf("disabled membership management did not fail closed: %#v", disabled)
	}
}

func TestChannelPolicyRejectsDuplicateApprovers(t *testing.T) {
	err := ValidateChannel(ChannelPolicy{OrganizationID: "o", TeamID: "t", ChannelID: "c", ParticipationMode: types.ModeObserve, ApproverUserIDs: []string{"U1", "U1"}, MembershipRevision: "m", MembershipRefreshedAt: time.Now(), MaxResponsesPerHour: 1, MaxConcurrentJobs: 1})
	if err == nil {
		t.Fatal("duplicate approvers accepted")
	}
}
