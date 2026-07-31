package core

import (
	"path/filepath"
	"testing"

	"github.com/telemetryos/tos-tag/core/config"
)

func TestCompleteObjectGraphConstructionHasNoNetworkSideEffects(t *testing.T) {
	cfg := config.DefaultConfiguration
	cfg.Marketplaces.SkillRoot = filepath.Clean(filepath.Join("..", "..", "telemetryos-agent-skills"))
	cfg.Marketplaces.CatalogPath = filepath.Join(".claude-plugin", "marketplace.json")
	if _, err := New(&cfg, nil); err != nil {
		t.Fatal(err)
	}
}
func TestLiveSlackObjectGraphConstructionDoesNotConnect(t *testing.T) {
	cfg := config.DefaultConfiguration
	cfg.Slack.Mode = "socket_mode"
	cfg.Slack.LiveEnabled = true
	cfg.Slack.OrganizationID = "org"
	cfg.Slack.TeamID = "team"
	cfg.Slack.AppToken = "xapp-test"
	cfg.Slack.BotToken = "xoxb-test"
	if _, err := New(&cfg, nil); err != nil {
		t.Fatal(err)
	}
}
