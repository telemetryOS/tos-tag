package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/RobertWHurst/blackbox"

	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/classifier"
	"github.com/telemetryos/tos-tag/core/config"
	"github.com/telemetryos/tos-tag/core/orgconfig"
	"github.com/telemetryos/tos-tag/core/slack"
	"github.com/telemetryos/tos-tag/core/usage"
	"github.com/telemetryos/tos-tag/types"
)

func TestValidParticipationModeRejectsUnknownValues(t *testing.T) {
	for _, mode := range []string{"observe", "mention", "assist", "proactive"} {
		if !validParticipationMode(mode) {
			t.Fatalf("valid mode %q was rejected", mode)
		}
	}
	for _, mode := range []string{"", "active", "admin", "PROACTIVE"} {
		if validParticipationMode(mode) {
			t.Fatalf("unsupported mode %q was accepted", mode)
		}
	}
}

func TestModeChangeAuditHasRetentionAndDeterministicIdempotency(t *testing.T) {
	appender, err := audit.NewMemoryAppender([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	request := slack.ModeChangeRequest{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", UserID: "operator", Mode: "proactive"}
	saved := orgconfig.ChannelPolicy{Version: 7, ParticipationMode: types.ModeProactive}
	if err := appendModeChangeAudit(context.Background(), appender, request, saved, "assist"); err != nil {
		t.Fatal(err)
	}
	if err := appendModeChangeAudit(context.Background(), appender, request, saved, "assist"); err != nil {
		t.Fatal(err)
	}
	receipts := appender.List("org")
	if len(receipts) != 1 || receipts[0].RetentionEpoch == "" || receipts[0].IdempotencyKey != "channel-mode/channel/7" || receipts[0].Type != "channel_policy.mode_command" {
		t.Fatalf("mode-change receipts = %#v", receipts)
	}
}

func TestBackgroundScopeAuthorizationUsesSharedParticipationMembershipAndOutputPolicy(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	policy := orgconfig.ChannelPolicy{
		Enrolled: true, ParticipationMode: types.ModeAssist,
		ParticipationManagedByMembership: true, BotMembershipKnown: true, BotIsMember: true,
		MembershipRefreshedAt: now,
	}
	if !authorizedBackgroundScope(policy, now, true) {
		t.Fatal("valid background scope was denied")
	}
	policy.ParticipationMode = types.ModeObserve
	if authorizedBackgroundScope(policy, now, true) {
		t.Fatal("observe-only scope admitted background work")
	}
	policy.ParticipationMode = types.ModeAssist
	policy.BotIsMember = false
	if authorizedBackgroundScope(policy, now, true) {
		t.Fatal("membership-managed non-member scope admitted background work")
	}
	policy.BotIsMember = true
	if authorizedBackgroundScope(policy, now, false) {
		t.Fatal("output-disallowed scope admitted background work")
	}
	policy.MembershipRefreshedAt = now.Add(-membershipPolicyFreshness)
	if authorizedFreshScope(policy, now) {
		t.Fatal("stale membership scope was authorized")
	}
}

type failingClassifier struct{}

func (failingClassifier) Decide(context.Context, classifier.Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
	return types.ClassificationDecision{}, errors.New("provider unavailable")
}

func TestLoggedClassifierUsesConservativeDeterministicFallback(t *testing.T) {
	usageStore := usage.NewMemory()
	logged := loggedClassifier{next: failingClassifier{}, logger: blackbox.New(), usage: usageStore, providerID: "openai", modelID: "classifier-test"}
	decision, err := logged.Decide(context.Background(), classifier.Target{Envelope: types.SlackEnvelope{OrganizationID: "org", Text: "Can you help?"}, Mode: types.ModeAssist}, types.ContextPackRevision{TotalTokens: 42})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != types.OutcomeReplyInThread || len(decision.ReasonCodes) < 2 || decision.ReasonCodes[0] != "classifier.deterministic_fallback" {
		t.Fatalf("fallback decision = %#v", decision)
	}
	events, err := usageStore.List(context.Background(), "org", 10)
	if err != nil || len(events) != 1 || events[0].EfficiencyAccountingVersion != usage.ClassifierEfficiencyAccountingVersion || events[0].Calls != 1 || events[0].FailedCalls != 1 || events[0].ContextPackTokens != 42 || events[0].Outcome != "provider_error" {
		t.Fatalf("failure usage events=%#v err=%v", events, err)
	}
}

type successfulClassifier struct{}

func (successfulClassifier) Decide(context.Context, classifier.Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
	return types.ClassificationDecision{Outcome: types.OutcomeSilent, ReasonCodes: []string{"ambient.no_action"}, ClassifierInputTokens: 125, ClassifierOutputTokens: 7}, nil
}

func TestLoggedClassifierRecordsTokenEfficiencyDimensions(t *testing.T) {
	usageStore := usage.NewMemory()
	logged := loggedClassifier{next: successfulClassifier{}, logger: blackbox.New(), usage: usageStore, providerID: "openai", modelID: "classifier-test"}
	if _, err := logged.Decide(context.Background(), classifier.Target{Envelope: types.SlackEnvelope{OrganizationID: "org"}}, types.ContextPackRevision{TotalTokens: 25}); err != nil {
		t.Fatal(err)
	}
	events, err := usageStore.List(context.Background(), "org", 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("usage events=%#v err=%v", events, err)
	}
	event := events[0]
	if event.Category != usage.CategoryClassifier || event.EfficiencyAccountingVersion != usage.ClassifierEfficiencyAccountingVersion || event.Calls != 1 || event.FailedCalls != 0 || event.InputTokens != 125 || event.OutputTokens != 7 || event.ContextPackTokens != 25 || event.Outcome != "silent" || event.ReasonCode != "" {
		t.Fatalf("usage event = %#v", event)
	}
}

func TestDefaultResponseProfilesExposeLunaLowMediumAndSolMedium(t *testing.T) {
	profiles := DefaultResponseProfiles(config.DefaultConfiguration.Models)
	if len(profiles) != 3 {
		t.Fatalf("profile count = %d", len(profiles))
	}
	want := map[string]string{
		"chatgpt-luna-low":    "light",
		"chatgpt-luna-medium": "standard",
		"chatgpt-sol-medium":  "strong",
	}
	for _, profile := range profiles {
		strength, _ := profile.ProviderOptions["strength"].(string)
		if want[profile.ID] != strength || profile.Variant == "" || !profile.Enabled {
			t.Fatalf("unexpected profile: %#v", profile)
		}
		delete(want, profile.ID)
	}
	if strong := profiles[2]; strong.ModelID != "gpt-5.6-sol" || strong.Variant != "medium" {
		t.Fatalf("strong profile must use Sol at medium effort: %#v", strong)
	}
	if len(want) != 0 {
		t.Fatalf("missing profiles: %#v", want)
	}
}

func TestCompleteObjectGraphConstructionHasNoNetworkSideEffects(t *testing.T) {
	cfg := config.DefaultConfiguration
	cfg.Marketplaces.BaseRoot = filepath.Clean(filepath.Join("..", "..", "tag-agent-skills"))
	cfg.Marketplaces.BaseCatalogPath = filepath.Join(".claude-plugin", "marketplace.json")
	cfg.Marketplaces.BasePlugin = "base"
	if _, err := New(&cfg, nil); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredBehavioralPluginsAreAutomaticallyInjected(t *testing.T) {
	cfg := config.MarketplaceConfig{
		BaseRoot:        filepath.Clean(filepath.Join("..", "..", "tag-agent-skills")),
		BaseCatalogPath: filepath.Join(".claude-plugin", "marketplace.json"),
		BasePlugin:      "base",
	}
	available, injected, err := loadBehavioralSkills(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(available) == 0 || len(injected) != len(available) {
		t.Fatalf("available=%d injected=%d", len(available), len(injected))
	}
	wantSkills := map[string]bool{
		"bug": false, "code-change-intake": false, "codebase-read": false,
		"feature": false, "linear-issue-manager": false,
		"marketing-account-journey": false, "marketing-funnel-chain": false, "marketing-funnel-review": false,
		"marketing-messaging": false, "marketing-unstall-draft": false, "product-knowledge": false, "telemetryos-documentation": false,
		"slack-message-design": false, "suitability": false, "tag-triggers": false,
		"team-alignment": false, "telemetry-otel-fetch": false, "wiki": false,
	}
	for _, snapshot := range injected {
		if snapshot.MarketplaceID != "tos-tag/base" {
			t.Fatalf("unexpected automatically injected source: %#v", snapshot)
		}
		if _, required := wantSkills[snapshot.Name]; required {
			wantSkills[snapshot.Name] = true
		}
	}
	for name, found := range wantSkills {
		if !found {
			t.Fatalf("base skill %s was not automatically injected", name)
		}
	}

	cfg.BasePlugin = "missing"
	if _, _, err := loadBehavioralSkills(cfg); err == nil {
		t.Fatal("missing configured base plugin was accepted")
	}
}
func TestLiveSlackObjectGraphConstructionDoesNotConnect(t *testing.T) {
	cfg := config.DefaultConfiguration
	cfg.Slack.Mode = "socket_mode"
	cfg.Slack.LiveEnabled = true
	cfg.Slack.OrganizationID = "org"
	cfg.Slack.AppID = "A-test"
	cfg.Slack.TeamID = "team"
	cfg.Slack.AppLevelToken = "xapp-test"
	cfg.Slack.BotUserOAuthToken = "xoxb-test"
	cfg.Slack.BotUserID = "U-tag"
	if _, err := New(&cfg, nil); err != nil {
		t.Fatal(err)
	}
}
