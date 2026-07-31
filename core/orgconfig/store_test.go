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
	got, err := store.Resolve(context.Background(), "org", "team", "alerts")
	if err != nil || got.ParticipationMode != types.ModeAssist {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	if _, err := store.Resolve(context.Background(), "org", "team", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
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
