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
	if cfg.Models.DefaultProfile != "chatgpt-luna-max" || cfg.Models.DefaultProvider != "openai" || cfg.Models.DefaultModel != "gpt-5.6-luna" || cfg.Models.DefaultVariant != "max" {
		t.Fatalf("unexpected default model configuration: %#v", cfg.Models)
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

func TestValidateLiveSlackAndClassifierModes(t *testing.T) {
	cfg := DefaultConfiguration
	cfg.Slack.Mode = "socket_mode"
	if err := Validate(&cfg); err == nil {
		t.Fatal("expected socket mode without explicit opt-in to be rejected")
	}
	cfg.Slack.LiveEnabled = true
	cfg.Slack.OrganizationID = "org"
	cfg.Slack.AppID = "A-test"
	cfg.Slack.TeamID = "team"
	cfg.Slack.AppLevelToken = "xapp-test"
	cfg.Slack.BotUserOAuthToken = "xoxb-test"
	if err := Validate(&cfg); err != nil {
		t.Fatalf("compiled socket mode configuration rejected: %v", err)
	}
	cfg.Classifier.Mode = "live"
	if err := Validate(&cfg); err != nil {
		t.Fatalf("explicit live classifier configuration rejected: %v", err)
	}
	cfg.Classifier.Mode = "assist"
	if err := Validate(&cfg); err == nil {
		t.Fatal("expected unknown global classifier mode to be rejected")
	}
}

func TestRedactedStatusDoesNotExposeSlackTokens(t *testing.T) {
	cfg := DefaultConfiguration
	cfg.Slack.AppLevelToken = "xapp-secret"
	cfg.Slack.UserOAuthToken = "xoxp-secret"
	cfg.Slack.BotUserOAuthToken = "xoxb-secret"
	status := fmt.Sprint(cfg.RedactedStatus())
	if strings.Contains(status, "xapp-secret") || strings.Contains(status, "xoxp-secret") || strings.Contains(status, "xoxb-secret") {
		t.Fatalf("redacted status leaked Slack credentials: %s", status)
	}
}

func TestLoadSlackTokenNamesMatchSlackLabels(t *testing.T) {
	t.Setenv("TAG__SLACK__APP_LEVEL_TOKEN", "xapp-test")
	t.Setenv("TAG__SLACK__USER_OAUTH_TOKEN", "xoxp-test")
	t.Setenv("TAG__SLACK__BOT_USER_OAUTH_TOKEN", "xoxb-test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if cfg.Slack.AppLevelToken != "xapp-test" || cfg.Slack.UserOAuthToken != "xoxp-test" || cfg.Slack.BotUserOAuthToken != "xoxb-test" {
		t.Fatalf("Slack token environment names did not map to their labeled fields")
	}
}

func TestLoadOpenCodeAndLiveClassifierEnvironment(t *testing.T) {
	t.Setenv("TAG__OPENCODE__ENABLED", "true")
	t.Setenv("TAG__OPENCODE__COMMAND", "/opt/opencode")
	t.Setenv("TAG__OPENCODE__TIMEOUT", "45s")
	t.Setenv("TAG__CLASSIFIER__MODE", "live")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if !cfg.OpenCode.Enabled || cfg.OpenCode.Command != "/opt/opencode" || cfg.OpenCode.Timeout != 45*time.Second || cfg.Classifier.Mode != "live" {
		t.Fatalf("OpenCode/classifier environment did not map: %#v %#v", cfg.OpenCode, cfg.Classifier)
	}
}

func TestLoadOpenAIClassifierEnvironment(t *testing.T) {
	t.Setenv("TAG__CLASSIFIER__PROVIDER", "openai")
	t.Setenv("TAG__CLASSIFIER__OPENAI_API_KEY", "test-openai-key")
	t.Setenv("TAG__CLASSIFIER__MODEL", "gpt-test")
	t.Setenv("TAG__CLASSIFIER__REASONING_EFFORT", "high")
	t.Setenv("TAG__CLASSIFIER__TIMEOUT", "12s")
	t.Setenv("TAG__CLASSIFIER__MAX_OUTPUT_TOKENS", "1234")
	t.Setenv("TAG__CLASSIFIER__REACTION_EMOJIS", "eyes, warning")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if cfg.Classifier.Provider != "openai" || cfg.Classifier.OpenAIAPIKey != "test-openai-key" || cfg.Classifier.Model != "gpt-test" || cfg.Classifier.ReasoningEffort != "high" || cfg.Classifier.Timeout != 12*time.Second || cfg.Classifier.MaxOutputTokens != 1234 || !reflect.DeepEqual(cfg.Classifier.ReactionEmojis, []string{"eyes", "warning"}) {
		t.Fatalf("OpenAI classifier environment did not map: %#v", cfg.Classifier)
	}
	status := fmt.Sprint(cfg.RedactedStatus())
	if strings.Contains(status, cfg.Classifier.OpenAIAPIKey) {
		t.Fatalf("redacted status leaked classifier API key: %s", status)
	}
}

func TestLoadRejectsInvalidDocumentedOpenCodeEnvironment(t *testing.T) {
	t.Setenv("TAG__OPENCODE__ENABLED", "sometimes")
	if _, err := Load(); err == nil {
		t.Fatal("invalid documented OpenCode boolean was accepted")
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

func TestValidateBehavioralPluginSourcesAreComplete(t *testing.T) {
	cfg := DefaultConfiguration
	cfg.Marketplaces.HeadlessRoot = "../telemetryos-agent-skills"
	if err := Validate(&cfg); err == nil {
		t.Fatal("partial headless plugin source was accepted")
	}
	cfg.Marketplaces.HeadlessCatalogPath = ".claude-plugin/marketplace.json"
	cfg.Marketplaces.HeadlessPlugin = "telemetryos-automation"
	cfg.Marketplaces.BaseRoot = "../tag-agent-skills"
	cfg.Marketplaces.BaseCatalogPath = ".claude-plugin/marketplace.json"
	cfg.Marketplaces.BasePlugin = "base"
	if err := Validate(&cfg); err != nil {
		t.Fatalf("complete behavioral plugin sources rejected: %v", err)
	}
}

func TestValidateToolExecutionRequiresEverySafetyBoundary(t *testing.T) {
	cfg := DefaultConfiguration
	cfg.Marketplaces.ToolsEnabled = true
	if err := Validate(&cfg); err == nil {
		t.Fatal("tool execution enabled without its boundaries")
	}
}
