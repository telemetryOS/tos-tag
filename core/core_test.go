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

func TestCompleteObjectGraphConstructionHasNoNetworkSideEffects(t *testing.T) {
	cfg := config.DefaultConfiguration
	cfg.Marketplaces.HeadlessRoot = filepath.Clean(filepath.Join("..", "..", "telemetryos-agent-skills"))
	cfg.Marketplaces.HeadlessCatalogPath = filepath.Join(".claude-plugin", "marketplace.json")
	cfg.Marketplaces.HeadlessPlugin = "telemetryos-automation"
	cfg.Marketplaces.BaseRoot = filepath.Clean(filepath.Join("..", "..", "tag-agent-skills"))
	cfg.Marketplaces.BaseCatalogPath = filepath.Join(".claude-plugin", "marketplace.json")
	cfg.Marketplaces.BasePlugin = "base"
	if _, err := New(&cfg, nil); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredBehavioralPluginsAreAutomaticallyInjected(t *testing.T) {
	cfg := config.MarketplaceConfig{
		HeadlessRoot:        filepath.Clean(filepath.Join("..", "..", "telemetryos-agent-skills")),
		HeadlessCatalogPath: filepath.Join(".claude-plugin", "marketplace.json"),
		HeadlessPlugin:      "telemetryos-automation",
		BaseRoot:            filepath.Clean(filepath.Join("..", "..", "tag-agent-skills")),
		BaseCatalogPath:     filepath.Join(".claude-plugin", "marketplace.json"),
		BasePlugin:          "base",
	}
	available, injected, err := loadBehavioralSkills(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(available) < 10 || len(injected) != len(available) {
		t.Fatalf("available=%d injected=%d", len(available), len(injected))
	}
	foundTagTriggers := false
	for _, snapshot := range injected {
		if snapshot.MarketplaceID != "telemetryos/telemetryos-automation" && snapshot.MarketplaceID != "tos-tag/base" {
			t.Fatalf("unexpected automatically injected source: %#v", snapshot)
		}
		if snapshot.MarketplaceID == "tos-tag/base" && snapshot.Name == "tag-triggers" {
			foundTagTriggers = true
		}
	}
	if !foundTagTriggers {
		t.Fatal("tag-triggers was not automatically injected from the base plugin")
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
