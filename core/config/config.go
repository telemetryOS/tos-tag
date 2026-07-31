// Package config owns tos-tag configuration and fail-closed startup validation.
package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/RobertWHurst/orale"
)

type HTTPConfig struct {
	Addr              string        `config:"addr"`
	ReadHeaderTimeout time.Duration `config:"readHeaderTimeout"`
	ShutdownTimeout   time.Duration `config:"shutdownTimeout"`
}

type MongoConfig struct {
	URI      string        `config:"uri"`
	Database string        `config:"database"`
	Timeout  time.Duration `config:"timeout"`
}

type LoggingConfig struct {
	UseJSON      bool   `config:"useJson"`
	DisableColor bool   `config:"disableColor"`
	Level        string `config:"level"`
	NoSplash     bool   `config:"noSplash"`
}

type TelemetryConfig struct {
	ServiceName string `config:"serviceName"`
	OtelEnabled bool   `config:"otelEnabled"`
}

type AuthConfig struct {
	Enabled    bool   `config:"enabled"`
	AdminToken string `config:"adminToken"`
}

type SlackConfig struct {
	Mode           string `config:"mode"`
	LiveEnabled    bool   `config:"liveEnabled"`
	OrganizationID string `config:"organizationId"`
	TeamID         string `config:"teamId"`
	AppToken       string `config:"appToken"`
	BotToken       string `config:"botToken"`
	BotUserID      string `config:"botUserId"`
	StubQueueSize  int    `config:"stubQueueSize"`
}

type RetentionConfig struct {
	RawEnvelope time.Duration `config:"rawEnvelope"`
	Messages    time.Duration `config:"messages"`
	Prompt      time.Duration `config:"prompt"`
	Sweep       time.Duration `config:"sweep"`
}

type ContextPackConfig struct {
	MaxTokens int `config:"maxTokens"`
	System    int `config:"system"`
	Thread    int `config:"thread"`
	Channel   int `config:"channel"`
	RecentOrg int `config:"recentOrg"`
	Evidence  int `config:"evidence"`
	Situation int `config:"situation"`
	Headroom  int `config:"headroom"`
}

func (c ContextPackConfig) PartitionTotal() int {
	return c.System + c.Thread + c.Channel + c.RecentOrg + c.Evidence + c.Situation + c.Headroom
}

type GatingConfig struct {
	Mode                  string  `config:"mode"`
	AssistThreshold       float64 `config:"assistThreshold"`
	ChannelReplyThreshold float64 `config:"channelReplyThreshold"`
	MaxResponsesPerHour   int     `config:"maxResponsesPerHour"`
}

type JobsConfig struct {
	Lease       time.Duration `config:"lease"`
	Poll        time.Duration `config:"poll"`
	MaxAttempts int           `config:"maxAttempts"`
}

type OpenCodeConfig struct {
	Enabled    bool          `config:"enabled"`
	Mode       string        `config:"mode"`
	BaseURL    string        `config:"baseUrl"`
	Username   string        `config:"username"`
	Password   string        `config:"password"`
	Command    string        `config:"command"`
	WorkerRoot string        `config:"workerRoot"`
	Timeout    time.Duration `config:"timeout"`
}

type ModelConfig struct {
	DefaultProfile  string `config:"defaultProfile"`
	DefaultProvider string `config:"defaultProvider"`
	DefaultModel    string `config:"defaultModel"`
	DefaultVariant  string `config:"defaultVariant"`
}
type MarketplaceConfig struct {
	SkillRoot       string   `config:"skillRoot"`
	CatalogPath     string   `config:"catalogPath"`
	InjectedSkills  []string `config:"injectedSkills"`
	InjectedTools   []string `config:"injectedTools"`
	ToolRoot        string   `config:"toolRoot"`
	ToolCatalogPath string   `config:"toolCatalogPath"`
	ToolsEnabled    bool     `config:"toolsEnabled"`
}
type KeystoreConfig struct {
	Enabled   bool   `config:"enabled"`
	MasterKey string `config:"masterKey"`
}

type Config struct {
	Environment  string            `config:"environment"`
	HTTP         HTTPConfig        `config:"http"`
	Mongo        MongoConfig       `config:"mongo"`
	Logging      LoggingConfig     `config:"logging"`
	Telemetry    TelemetryConfig   `config:"telemetry"`
	Auth         AuthConfig        `config:"auth"`
	Slack        SlackConfig       `config:"slack"`
	Retention    RetentionConfig   `config:"retention"`
	ContextPacks ContextPackConfig `config:"contextPacks"`
	Gating       GatingConfig      `config:"gating"`
	Jobs         JobsConfig        `config:"jobs"`
	OpenCode     OpenCodeConfig    `config:"openCode"`
	Models       ModelConfig       `config:"models"`
	Marketplaces MarketplaceConfig `config:"marketplaces"`
	Keystore     KeystoreConfig    `config:"keystore"`
}

var DefaultConfiguration = Config{
	Environment: "development",
	HTTP: HTTPConfig{
		Addr:              "127.0.0.1:8090",
		ReadHeaderTimeout: 5 * time.Second,
		ShutdownTimeout:   10 * time.Second,
	},
	Mongo: MongoConfig{
		URI:      "mongodb://127.0.0.1:27017",
		Database: "tos_tag",
		Timeout:  5 * time.Second,
	},
	Logging:   LoggingConfig{Level: "info"},
	Telemetry: TelemetryConfig{ServiceName: "tos-tag"},
	Auth:      AuthConfig{Enabled: false},
	Slack:     SlackConfig{Mode: "stub", StubQueueSize: 256},
	Retention: RetentionConfig{
		RawEnvelope: 24 * time.Hour,
		Messages:    30 * 24 * time.Hour,
		Prompt:      24 * time.Hour,
		Sweep:       time.Minute,
	},
	ContextPacks: ContextPackConfig{
		MaxTokens: 100_000,
		System:    8_000,
		Thread:    20_000,
		Channel:   20_000,
		RecentOrg: 15_000,
		Evidence:  22_000,
		Situation: 10_000,
		Headroom:  5_000,
	},
	Gating: GatingConfig{
		Mode:                  "shadow",
		AssistThreshold:       0.90,
		ChannelReplyThreshold: 0.98,
		MaxResponsesPerHour:   6,
	},
	Jobs: JobsConfig{
		Lease:       30 * time.Second,
		Poll:        250 * time.Millisecond,
		MaxAttempts: 3,
	},
	OpenCode: OpenCodeConfig{Mode: "local_worker", BaseURL: "http://127.0.0.1:4096", Username: "opencode", Command: "opencode", WorkerRoot: "/tmp/tos-tag-workers", Timeout: 30 * time.Second},
	Models:   ModelConfig{DefaultProfile: "fake-default", DefaultProvider: "fake", DefaultModel: "deterministic"},
}

func Load() (*Config, error) {
	loader, err := orale.Load("tag")
	if err != nil {
		return nil, fmt.Errorf("initialize tag config loader: %w", err)
	}
	cfg := DefaultConfiguration
	if err := loader.GetAll(&cfg); err != nil {
		return nil, fmt.Errorf("read tag configuration: %w", err)
	}
	if env := strings.TrimSpace(os.Getenv("DEPLOYMENT_ENVIRONMENT")); env != "" {
		cfg.Environment = env
	}
	if err := Validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("configuration is required")
	}
	if strings.TrimSpace(cfg.HTTP.Addr) == "" {
		return fmt.Errorf("http.addr must not be empty")
	}
	host, _, err := net.SplitHostPort(cfg.HTTP.Addr)
	if err != nil {
		return fmt.Errorf("http.addr must be host:port: %w", err)
	}
	if !cfg.Auth.Enabled && !isLoopbackHost(host) {
		return fmt.Errorf("auth must be enabled for a non-loopback HTTP listener")
	}
	if cfg.Auth.Enabled && strings.TrimSpace(cfg.Auth.AdminToken) == "" {
		return fmt.Errorf("auth.adminToken is required when auth is enabled")
	}
	if cfg.HTTP.ReadHeaderTimeout <= 0 || cfg.HTTP.ShutdownTimeout <= 0 {
		return fmt.Errorf("HTTP timeouts must be positive")
	}
	if strings.TrimSpace(cfg.Mongo.URI) == "" || strings.TrimSpace(cfg.Mongo.Database) == "" {
		return fmt.Errorf("mongo URI and database are required")
	}
	if cfg.Mongo.Timeout <= 0 {
		return fmt.Errorf("mongo.timeout must be positive")
	}
	switch cfg.Slack.Mode {
	case "stub":
		if cfg.Slack.LiveEnabled {
			return fmt.Errorf("slack.liveEnabled cannot be true in stub mode")
		}
	case "socket_mode":
		if !cfg.Slack.LiveEnabled {
			return fmt.Errorf("slack.liveEnabled must be explicitly true for socket_mode")
		}
		if cfg.Slack.OrganizationID == "" || cfg.Slack.TeamID == "" {
			return fmt.Errorf("Slack organizationId and teamId are required for socket_mode")
		}
		if !strings.HasPrefix(cfg.Slack.AppToken, "xapp-") || !strings.HasPrefix(cfg.Slack.BotToken, "xoxb-") {
			return fmt.Errorf("Slack socket_mode requires xapp and xoxb tokens")
		}
	default:
		return fmt.Errorf("unsupported slack.mode %q", cfg.Slack.Mode)
	}
	if cfg.Slack.StubQueueSize <= 0 {
		return fmt.Errorf("slack.stubQueueSize must be positive")
	}
	if cfg.Retention.RawEnvelope <= 0 || cfg.Retention.Messages <= 0 || cfg.Retention.Prompt <= 0 || cfg.Retention.Sweep <= 0 {
		return fmt.Errorf("retention durations must be positive")
	}
	if cfg.Retention.RawEnvelope > cfg.Retention.Messages || cfg.Retention.Prompt > cfg.Retention.Messages {
		return fmt.Errorf("raw envelope and prompt retention cannot exceed message retention")
	}
	if cfg.ContextPacks.MaxTokens <= 0 || cfg.ContextPacks.PartitionTotal() != cfg.ContextPacks.MaxTokens {
		return fmt.Errorf("context pack partitions must exactly equal maxTokens")
	}
	if cfg.Gating.Mode != "shadow" {
		return fmt.Errorf("gating.mode %q is unsupported before live Slack evaluation; use shadow", cfg.Gating.Mode)
	}
	if cfg.Gating.AssistThreshold < 0 || cfg.Gating.AssistThreshold > 1 || cfg.Gating.ChannelReplyThreshold < cfg.Gating.AssistThreshold || cfg.Gating.ChannelReplyThreshold > 1 {
		return fmt.Errorf("invalid gating thresholds")
	}
	if cfg.Gating.MaxResponsesPerHour <= 0 || cfg.Jobs.Lease <= 0 || cfg.Jobs.Poll <= 0 || cfg.Jobs.MaxAttempts <= 0 {
		return fmt.Errorf("gating and job bounds must be positive")
	}
	if cfg.Models.DefaultProfile == "" || cfg.Models.DefaultProvider == "" || cfg.Models.DefaultModel == "" {
		return fmt.Errorf("default model profile, provider, and model are required")
	}
	if cfg.OpenCode.Enabled {
		if cfg.OpenCode.Timeout <= 0 {
			return fmt.Errorf("enabled OpenCode requires a positive timeout")
		}
		switch cfg.OpenCode.Mode {
		case "local_worker":
			if cfg.OpenCode.Command == "" || cfg.OpenCode.WorkerRoot == "" {
				return fmt.Errorf("local-worker OpenCode requires command and worker root")
			}
		case "external":
			if cfg.OpenCode.BaseURL == "" || cfg.OpenCode.Username == "" || cfg.OpenCode.Password == "" {
				return fmt.Errorf("external OpenCode requires base URL, username, and password")
			}
		default:
			return fmt.Errorf("unsupported OpenCode mode %q", cfg.OpenCode.Mode)
		}
		if cfg.Models.DefaultProvider == "fake" {
			return fmt.Errorf("enabled OpenCode requires a non-fake default provider")
		}
	}
	if (cfg.Marketplaces.SkillRoot == "") != (cfg.Marketplaces.CatalogPath == "") {
		return fmt.Errorf("marketplace skill root and catalog path must be configured together")
	}
	if (cfg.Marketplaces.ToolRoot == "") != (cfg.Marketplaces.ToolCatalogPath == "") {
		return fmt.Errorf("tool marketplace root and catalog path must be configured together")
	}
	if cfg.Marketplaces.ToolsEnabled && (!cfg.OpenCode.Enabled || cfg.OpenCode.Mode != "local_worker" || cfg.Marketplaces.ToolRoot == "" || !cfg.Keystore.Enabled || len(cfg.Marketplaces.InjectedTools) == 0) {
		return fmt.Errorf("enabled marketplace tools require local-worker OpenCode, a tool marketplace, the keystore, and an injected-tool allowlist")
	}
	if cfg.Keystore.Enabled {
		key, err := base64.StdEncoding.DecodeString(cfg.Keystore.MasterKey)
		if err != nil || len(key) != 32 {
			return fmt.Errorf("enabled keystore requires a base64-encoded 32-byte master key")
		}
	}
	return nil
}

func (c *Config) KeystoreKey() ([]byte, error) {
	if !c.Keystore.Enabled {
		return nil, fmt.Errorf("keystore is disabled")
	}
	key, err := base64.StdEncoding.DecodeString(c.Keystore.MasterKey)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("invalid keystore master key")
	}
	return key, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *Config) RedactedStatus() map[string]any {
	return map[string]any{
		"environment":                  c.Environment,
		"http_addr":                    c.HTTP.Addr,
		"mongo_database":               c.Mongo.Database,
		"slack_mode":                   c.Slack.Mode,
		"slack_live_enabled":           c.Slack.LiveEnabled,
		"gating_mode":                  c.Gating.Mode,
		"auth_enabled":                 c.Auth.Enabled,
		"message_retention":            c.Retention.Messages.String(),
		"context_max_tokens":           c.ContextPacks.MaxTokens,
		"opencode_enabled":             c.OpenCode.Enabled,
		"default_model_profile":        c.Models.DefaultProfile,
		"skill_marketplace_configured": c.Marketplaces.SkillRoot != "",
		"tool_marketplace_configured":  c.Marketplaces.ToolRoot != "",
		"marketplace_tools_enabled":    c.Marketplaces.ToolsEnabled,
		"keystore_enabled":             c.Keystore.Enabled,
	}
}
