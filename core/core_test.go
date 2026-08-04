package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/RobertWHurst/blackbox"

	"github.com/telemetryos/tos-tag/core/classifier"
	"github.com/telemetryos/tos-tag/core/config"
	"github.com/telemetryos/tos-tag/types"
)

type failingClassifier struct{}

func (failingClassifier) Decide(context.Context, classifier.Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
	return types.ClassificationDecision{}, errors.New("provider unavailable")
}

func TestLoggedClassifierUsesConservativeDeterministicFallback(t *testing.T) {
	logged := loggedClassifier{next: failingClassifier{}, logger: blackbox.New()}
	decision, err := logged.Decide(context.Background(), classifier.Target{Envelope: types.SlackEnvelope{Text: "Can you help?"}, Mode: types.ModeAssist}, types.ContextPackRevision{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != types.OutcomeReplyInThread || len(decision.ReasonCodes) < 2 || decision.ReasonCodes[0] != "classifier.deterministic_fallback" {
		t.Fatalf("fallback decision = %#v", decision)
	}
}

func TestDefaultResponseProfilesExposeLunaLowMediumAndSolMedium(t *testing.T) {
	profiles := defaultResponseProfiles(config.DefaultConfiguration.Models)
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
	if len(available) != 14 || len(injected) != len(available) {
		t.Fatalf("available=%d injected=%d", len(available), len(injected))
	}
	wantSkills := map[string]bool{
		"bug": false, "code-change-intake": false, "codebase-read": false,
		"feature": false, "linear-issue-manager": false, "marketing-messaging": false, "product-knowledge": false, "telemetryos-documentation": false,
		"slack-message-design": false, "suitability": false, "tag-triggers": false,
		"team-alignment": false, "telemetry-otel-fetch": false, "wiki": false,
	}
	for _, snapshot := range injected {
		if snapshot.MarketplaceID != "tos-tag/base" {
			t.Fatalf("unexpected automatically injected source: %#v", snapshot)
		}
		if _, ok := wantSkills[snapshot.Name]; !ok {
			t.Fatalf("unexpected base skill: %s", snapshot.Name)
		}
		wantSkills[snapshot.Name] = true
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
	if _, err := New(&cfg, nil); err != nil {
		t.Fatal(err)
	}
}
