package config

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDefaultConfigurationValid(t *testing.T) {
	cfg := DefaultConfiguration
	if err := Validate(&cfg); err != nil {
		t.Fatalf("default configuration invalid: %v", err)
	}
}

func TestValidateRequiresAuthOffLoopback(t *testing.T) {
	cfg := DefaultConfiguration
	cfg.HTTP.Addr = "0.0.0.0:8090"
	if err := Validate(&cfg); err == nil {
		t.Fatal("expected non-loopback listener without auth to fail")
	}
	cfg.Auth.Enabled = true
	cfg.Auth.AdminToken = "test-only-token"
	if err := Validate(&cfg); err != nil {
		t.Fatalf("authenticated non-loopback listener rejected: %v", err)
	}
}

func TestValidateRequiresTokenWhenAuthEnabled(t *testing.T) {
	cfg := DefaultConfiguration
	cfg.Auth.Enabled = true
	if err := Validate(&cfg); err == nil {
		t.Fatal("expected missing admin token to fail")
	}
}

func TestRedactedStatusDoesNotExposeSecrets(t *testing.T) {
	cfg := DefaultConfiguration
	cfg.Auth.AdminToken = "do-not-leak"
	status := fmt.Sprint(cfg.RedactedStatus())
	if strings.Contains(status, cfg.Auth.AdminToken) || strings.Contains(status, cfg.Mongo.URI) {
		t.Fatalf("redacted status leaked a secret-bearing value: %s", status)
	}
}

func TestValidateRetentionAndContextBounds(t *testing.T) {
	cfg := DefaultConfiguration
	cfg.Retention.Prompt = cfg.Retention.Messages + time.Second
	if err := Validate(&cfg); err == nil {
		t.Fatal("expected derived retention beyond messages to fail")
	}

	cfg = DefaultConfiguration
	cfg.ContextPacks.Headroom--
	if err := Validate(&cfg); err == nil {
		t.Fatal("expected mismatched context partitions to fail")
	}
}

func TestValidateRejectsLiveSlackAndGating(t *testing.T) {
	cfg := DefaultConfiguration
	cfg.Slack.Mode = "socket_mode"
	if err := Validate(&cfg); err == nil {
		t.Fatal("expected socket mode without explicit opt-in to be rejected")
	}
	cfg.Slack.LiveEnabled = true
	cfg.Slack.OrganizationID = "org"
	cfg.Slack.TeamID = "team"
	cfg.Slack.AppToken = "xapp-test"
	cfg.Slack.BotToken = "xoxb-test"
	if err := Validate(&cfg); err != nil {
		t.Fatalf("compiled socket mode configuration rejected: %v", err)
	}
	cfg = DefaultConfiguration
	cfg.Gating.Mode = "assist"
	if err := Validate(&cfg); err == nil {
		t.Fatal("expected live gating mode to be rejected")
	}
}

func TestRedactedStatusDoesNotExposeSlackTokens(t *testing.T) {
	cfg := DefaultConfiguration
	cfg.Slack.AppToken = "xapp-secret"
	cfg.Slack.BotToken = "xoxb-secret"
	status := fmt.Sprint(cfg.RedactedStatus())
	if strings.Contains(status, "xapp-secret") || strings.Contains(status, "xoxb-secret") {
		t.Fatalf("redacted status leaked Slack credentials: %s", status)
	}
}

func TestLoadInjectedSkillFromEnvironment(t *testing.T) {
	t.Setenv("TAG__MARKETPLACES__INJECTED_SKILLS", "linear")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if want := []string{"linear"}; !reflect.DeepEqual(cfg.Marketplaces.InjectedSkills, want) {
		t.Fatalf("injected skills = %#v, want %#v", cfg.Marketplaces.InjectedSkills, want)
	}
}

func TestValidateToolExecutionRequiresEverySafetyBoundary(t *testing.T) {
	cfg := DefaultConfiguration
	cfg.Marketplaces.ToolsEnabled = true
	if err := Validate(&cfg); err == nil {
		t.Fatal("tool execution enabled without its boundaries")
	}
}
