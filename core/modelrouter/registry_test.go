package modelrouter

import (
	"context"
	"github.com/telemetryos/tos-tag/types"
	"testing"
)

func TestRegistryUpdatesRoutesDynamically(t *testing.T) {
	profiles := profiles()
	registry, err := NewRegistry(profiles, nil, map[string]string{"org": "default"}, "default", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.PutRule(Rule{ID: "alerts", OrganizationID: "org", ChannelID: "alerts", ProfileID: "alerts"}); err != nil {
		t.Fatal(err)
	}
	resolved, _, err := registry.Resolve(context.Background(), types.ModelRouteContext{OrganizationID: "org", ChannelID: "alerts", DataClasses: []string{"internal"}, Capabilities: []string{"structured"}}, Constraints{})
	if err != nil || resolved.ProfileID != "alerts" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	if len(registry.Snapshot().Rules) != 1 {
		t.Fatal("rule not visible")
	}
}
