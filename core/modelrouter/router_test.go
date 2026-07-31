package modelrouter

import (
	"context"
	"errors"
	"testing"

	"github.com/telemetryos/tos-tag/types"
)

func profiles() []types.ModelProfile {
	return []types.ModelProfile{
		{ID: "default", ProviderID: "local", ModelID: "small", Enabled: true, MaxInputTokens: 200000, RequiredCapabilities: []string{"structured"}, AllowedDataClasses: []string{"internal"}},
		{ID: "alerts", ProviderID: "anthropic", ModelID: "sonnet", Enabled: true, MaxInputTokens: 200000, FallbackProfileIDs: []string{"default"}, RequiredCapabilities: []string{"structured"}, AllowedDataClasses: []string{"internal"}},
		{ID: "deep", ProviderID: "openai", ModelID: "gpt", Variant: "xhigh", Enabled: true, MaxInputTokens: 200000, RequiredCapabilities: []string{"structured"}, AllowedDataClasses: []string{"internal"}},
	}
}

func TestRoutePrecedence(t *testing.T) {
	router, err := New(profiles(), []Rule{
		{ID: "channel", OrganizationID: "org", ChannelID: "alerts-channel", ProfileID: "alerts"},
		{ID: "phase", OrganizationID: "org", ChannelID: "alerts-channel", Phase: "review", ProfileID: "deep"},
	}, map[string]string{"org": "default"}, "default", "policy-1")
	if err != nil {
		t.Fatal(err)
	}
	got, trace, err := router.Resolve(context.Background(), types.ModelRouteContext{OrganizationID: "org", ChannelID: "alerts-channel", Phase: "review", DataClasses: []string{"internal"}, Capabilities: []string{"structured"}}, Constraints{})
	if err != nil {
		t.Fatal(err)
	}
	if got.ProfileID != "deep" || trace.MatchedRule != "phase" {
		t.Fatalf("bad route: %#v %#v", got, trace)
	}
}

func TestHardConstraintUsesOnlyApprovedFallback(t *testing.T) {
	router, _ := New(profiles(), []Rule{{ID: "channel", OrganizationID: "org", ChannelID: "alerts-channel", ProfileID: "alerts"}}, map[string]string{"org": "default"}, "default", "policy-1")
	got, trace, err := router.Resolve(context.Background(), types.ModelRouteContext{OrganizationID: "org", ChannelID: "alerts-channel", DataClasses: []string{"internal"}, Capabilities: []string{"structured"}}, Constraints{AllowedProviders: map[string]bool{"local": true}})
	if err != nil {
		t.Fatal(err)
	}
	if got.ProfileID != "default" || !trace.Fallback {
		t.Fatalf("fallback failed: %#v %#v", got, trace)
	}
}

func TestNoEligibleModelFailsClosed(t *testing.T) {
	router, _ := New(profiles(), nil, map[string]string{"org": "default"}, "default", "policy-1")
	_, _, err := router.Resolve(context.Background(), types.ModelRouteContext{OrganizationID: "org", DataClasses: []string{"restricted"}}, Constraints{})
	if !errors.Is(err, ErrNoEligibleModel) {
		t.Fatalf("got %v", err)
	}
}

func TestChannelDefaultIsNotAnExplicitOverride(t *testing.T) {
	router, _ := New(profiles(), []Rule{{ID: "review-phase", OrganizationID: "org", ChannelID: "product", Phase: "review", ProfileID: "deep"}}, map[string]string{"org": "default"}, "default", "policy-1")
	resolved, trace, err := router.Resolve(context.Background(), types.ModelRouteContext{OrganizationID: "org", ChannelID: "product", Phase: "review", ChannelDefault: "alerts", DataClasses: []string{"internal"}, Capabilities: []string{"structured"}}, Constraints{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ProfileID != "deep" || trace.MatchedRule != "review-phase" {
		t.Fatalf("phase rule did not outrank channel default: %#v %#v", resolved, trace)
	}
	resolved, trace, err = router.Resolve(context.Background(), types.ModelRouteContext{OrganizationID: "org", ChannelID: "support", Phase: "review", ChannelDefault: "alerts", DataClasses: []string{"internal"}, Capabilities: []string{"structured"}}, Constraints{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ProfileID != "alerts" || trace.MatchedRule != "channel_default" {
		t.Fatalf("channel default was not selected: %#v %#v", resolved, trace)
	}
}

func TestCompoundRuleRequiresEveryDeclaredScope(t *testing.T) {
	router, _ := New(profiles(), []Rule{{ID: "product-review", OrganizationID: "org", ChannelID: "product", Phase: "review", ProfileID: "deep"}}, map[string]string{"org": "default"}, "default", "policy-1")
	resolved, _, err := router.Resolve(context.Background(), types.ModelRouteContext{OrganizationID: "org", ChannelID: "support", Phase: "review", DataClasses: []string{"internal"}, Capabilities: []string{"structured"}}, Constraints{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ProfileID != "default" {
		t.Fatalf("compound rule leaked across channels: %#v", resolved)
	}
}
