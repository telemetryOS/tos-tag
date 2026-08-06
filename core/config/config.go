// Package config owns tos-tag configuration and fail-closed startup validation.
package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/RobertWHurst/orale"
)

type HTTPConfig struct {
	Addr                 string        `config:"addr"`
	AllowUnauthenticated bool          `config:"allowUnauthenticated"`
	ReadHeaderTimeout    time.Duration `config:"readHeaderTimeout"`
	ShutdownTimeout      time.Duration `config:"shutdownTimeout"`
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
	FilePath     string `config:"filePath"`
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
	Mode                          string        `config:"mode"`
	LiveEnabled                   bool          `config:"liveEnabled"`
	OrganizationID                string        `config:"organizationId"`
	AppID                         string        `config:"appId"`
	TeamID                        string        `config:"teamId"`
	AppLevelToken                 string        `config:"appLevelToken"`
	UserOAuthToken                string        `config:"userOauthToken"`
	BotUserOAuthToken             string        `config:"botUserOauthToken"`
	BotUserID                     string        `config:"botUserId"`
	ContextSyncEnabled            bool          `config:"contextSyncEnabled"`
	ContextSyncLookback           time.Duration `config:"contextSyncLookback"`
	ContextSyncTimeout            time.Duration `config:"contextSyncTimeout"`
	ContextSyncRefresh            time.Duration `config:"contextSyncRefresh"`
	ContextSyncRequestInterval    time.Duration `config:"contextSyncRequestInterval"`
	ContextSyncMaxChannels        int           `config:"contextSyncMaxChannels"`
	ContextSyncMaxMessages        int           `config:"contextSyncMaxMessages"`
	ContextSyncMessagesPerChannel int           `config:"contextSyncMessagesPerChannel"`
	AutoAssistJoinedChannels      bool          `config:"autoAssistJoinedChannels"`
	AutomationOperatorUserIDs     []string      `config:"automationOperatorUserIds"`
	OutputChannelIDs              []string      `config:"outputChannelIds"`
	StubQueueSize                 int           `config:"stubQueueSize"`
}

type RetentionConfig struct {
	RawEnvelope time.Duration `config:"rawEnvelope"`
	// Messages remains only so a retired TTL setting fails explicitly.
	// Normalized messages are durable and do not expire.
	Messages time.Duration `config:"messages"`
	Prompt   time.Duration `config:"prompt"`
	Sweep    time.Duration `config:"sweep"`
}

type ContextPackConfig struct {
	Lookback  time.Duration `config:"lookback"`
	MaxTokens int           `config:"maxTokens"`
	System    int           `config:"system"`
	Thread    int           `config:"thread"`
	Channel   int           `config:"channel"`
	RecentOrg int           `config:"recentOrg"`
	Evidence  int           `config:"evidence"`
	Situation int           `config:"situation"`
	Headroom  int           `config:"headroom"`
}

func (c ContextPackConfig) PartitionTotal() int {
	return c.System + c.Thread + c.Channel + c.RecentOrg + c.Evidence + c.Situation + c.Headroom
}

type ClassifierConfig struct {
	Mode                   string        `config:"mode"`
	Provider               string        `config:"provider"`
	BaseURL                string        `config:"baseUrl"`
	OpenAIAPIKey           string        `config:"openAiApiKey"`
	Model                  string        `config:"model"`
	ReasoningEffort        string        `config:"reasoningEffort"`
	Timeout                time.Duration `config:"timeout"`
	MaxOutputTokens        int           `config:"maxOutputTokens"`
	ReactionEmojis         []string      `config:"reactionEmojis"`
	AssistThreshold        float64       `config:"assistThreshold"`
	ChannelReplyThreshold  float64       `config:"channelReplyThreshold"`
	MaxResponsesPerHour    int           `config:"maxResponsesPerHour"`
	MaxConcurrentJobs      int           `config:"maxConcurrentJobs"`
	FloodProtectionEnabled bool          `config:"floodProtectionEnabled"`
	FloodMaxMessages       int           `config:"floodMaxMessages"`
	FloodWindow            time.Duration `config:"floodWindow"`
}

type MemoryConfig struct {
	Enabled         bool          `config:"enabled"`
	BaseURL         string        `config:"baseUrl"`
	OpenAIAPIKey    string        `config:"openAiApiKey"`
	Model           string        `config:"model"`
	ReasoningEffort string        `config:"reasoningEffort"`
	Timeout         time.Duration `config:"timeout"`
	Interval        time.Duration `config:"interval"`
	Lookback        time.Duration `config:"lookback"`
	MinMessages     int           `config:"minMessages"`
	MaxMessages     int           `config:"maxMessages"`
	MaxScopesPerRun int           `config:"maxScopesPerRun"`
	MaxOutputTokens int           `config:"maxOutputTokens"`
	MinConfidence   float64       `config:"minConfidence"`
}

type JobsConfig struct {
	Lease             time.Duration `config:"lease"`
	Poll              time.Duration `config:"poll"`
	MaxAttempts       int           `config:"maxAttempts"`
	WorkerConcurrency int           `config:"workerConcurrency"`
}

type CodexConfig struct {
	Enabled       bool          `config:"enabled"`
	Command       string        `config:"command"`
	Home          string        `config:"home"`
	WorkerRoot    string        `config:"workerRoot"`
	WebSearchMode string        `config:"webSearchMode"`
	Timeout       time.Duration `config:"timeout"`
}

type ModelConfig struct {
	DefaultProfile  string `config:"defaultProfile"`
	DefaultProvider string `config:"defaultProvider"`
	DefaultModel    string `config:"defaultModel"`
	DefaultVariant  string `config:"defaultVariant"`
	FastProfileBase string `config:"fastProfileBase"`
	FastModel       string `config:"fastModel"`
}
type MarketplaceConfig struct {
	SkillRoot       string   `config:"skillRoot"`
	CatalogPath     string   `config:"catalogPath"`
	InjectedSkills  []string `config:"injectedSkills"`
	BaseRoot        string   `config:"baseRoot"`
	BaseCatalogPath string   `config:"baseCatalogPath"`
	BasePlugin      string   `config:"basePlugin"`
	InjectedTools   []string `config:"injectedTools"`
	ToolRoot        string   `config:"toolRoot"`
	ToolCatalogPath string   `config:"toolCatalogPath"`
	ToolPath        string   `config:"toolPath"`
	ToolsEnabled    bool     `config:"toolsEnabled"`
}
type KeystoreConfig struct {
	Enabled   bool   `config:"enabled"`
	MasterKey string `config:"masterKey"`
}
type AuditConfig struct {
	CommitmentKey string `config:"commitmentKey"`
}

const developmentAuditCommitmentKey = "dG9zLXRhZy1kZXZlbG9wbWVudC1hdWRpdC1rZXkhISE="

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
	Classifier   ClassifierConfig  `config:"classifier"`
	Memory       MemoryConfig      `config:"memory"`
	Jobs         JobsConfig        `config:"jobs"`
	Codex        CodexConfig       `config:"codex"`
	Models       ModelConfig       `config:"models"`
	Marketplaces MarketplaceConfig `config:"marketplaces"`
	Keystore     KeystoreConfig    `config:"keystore"`
	Audit        AuditConfig       `config:"audit"`
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
	Audit:     AuditConfig{CommitmentKey: developmentAuditCommitmentKey},
	Slack: SlackConfig{
		Mode:                          "stub",
		ContextSyncLookback:           7 * 24 * time.Hour,
		ContextSyncTimeout:            2 * time.Hour,
		ContextSyncRefresh:            5 * time.Minute,
		ContextSyncRequestInterval:    1200 * time.Millisecond,
		ContextSyncMaxChannels:        500,
		ContextSyncMaxMessages:        5_000,
		ContextSyncMessagesPerChannel: 100,
		StubQueueSize:                 256,
	},
	Retention: RetentionConfig{
		RawEnvelope: 24 * time.Hour,
		Prompt:      24 * time.Hour,
		Sweep:       time.Minute,
	},
	ContextPacks: ContextPackConfig{
		Lookback:  30 * 24 * time.Hour,
		MaxTokens: 100_000,
		System:    8_000,
		Thread:    20_000,
		Channel:   20_000,
		RecentOrg: 15_000,
		Evidence:  22_000,
		Situation: 10_000,
		Headroom:  5_000,
	},
	Classifier: ClassifierConfig{
		Mode:                   "shadow",
		Provider:               "deterministic",
		BaseURL:                "https://api.openai.com/v1",
		Model:                  "gpt-5.6-luna",
		ReasoningEffort:        "none",
		Timeout:                60 * time.Second,
		MaxOutputTokens:        2048,
		ReactionEmojis:         []string{"eyes", "thinking_face", "white_check_mark", "warning", "rotating_light", "hammer_and_wrench", "speech_balloon"},
		AssistThreshold:        0.90,
		ChannelReplyThreshold:  0.98,
		MaxResponsesPerHour:    120,
		MaxConcurrentJobs:      8,
		FloodProtectionEnabled: true,
		FloodMaxMessages:       1000,
		FloodWindow:            time.Hour,
	},
	Memory: MemoryConfig{
		BaseURL:         "https://api.openai.com/v1",
		Model:           "gpt-5.6-luna",
		ReasoningEffort: "medium",
		Timeout:         90 * time.Second,
		Interval:        10 * time.Minute,
		Lookback:        7 * 24 * time.Hour,
		MinMessages:     6,
		MaxMessages:     80,
		MaxScopesPerRun: 8,
		MaxOutputTokens: 3000,
		MinConfidence:   0.78,
	},
	Jobs: JobsConfig{
		Lease:             30 * time.Second,
		Poll:              250 * time.Millisecond,
		MaxAttempts:       3,
		WorkerConcurrency: 8,
	},
	Codex: CodexConfig{Command: "codex", WorkerRoot: "/tmp/tos-tag-workers", WebSearchMode: "disabled", Timeout: 5 * time.Minute},
	Models: ModelConfig{
		DefaultProfile:  "chatgpt-sol-medium",
		DefaultProvider: "openai",
		DefaultModel:    "gpt-5.6-sol",
		DefaultVariant:  "medium",
		FastProfileBase: "chatgpt-luna",
		FastModel:       "gpt-5.6-luna",
	},
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
	if err := applyCodexEnvironment(&cfg.Codex); err != nil {
		return nil, err
	}
	if err := applySlackEnvironment(&cfg.Slack); err != nil {
		return nil, err
	}
	applyMarketplaceEnvironment(&cfg.Marketplaces)
	if err := applyClassifierEnvironment(&cfg.Classifier); err != nil {
		return nil, err
	}
	if err := applyJobsEnvironment(&cfg.Jobs); err != nil {
		return nil, err
	}
	if env := strings.TrimSpace(os.Getenv("DEPLOYMENT_ENVIRONMENT")); env != "" {
		cfg.Environment = env
	}
	if err := Validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyMarketplaceEnvironment(cfg *MarketplaceConfig) {
	if raw, ok := os.LookupEnv("TAG__MARKETPLACES__INJECTED_SKILLS"); ok {
		cfg.InjectedSkills = splitNonEmpty(raw)
	}
	if raw, ok := os.LookupEnv("TAG__MARKETPLACES__INJECTED_TOOLS"); ok {
		cfg.InjectedTools = splitNonEmpty(raw)
	}
	if raw, ok := os.LookupEnv("TAG__MARKETPLACES__TOOL_PATH"); ok {
		cfg.ToolPath = strings.TrimSpace(raw)
	}
}

func applySlackEnvironment(cfg *SlackConfig) error {
	if raw, ok := os.LookupEnv("TAG__SLACK__AUTO_ASSIST_JOINED_CHANNELS"); ok {
		value, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("TAG__SLACK__AUTO_ASSIST_JOINED_CHANNELS must be a boolean: %w", err)
		}
		cfg.AutoAssistJoinedChannels = value
	}
	if raw, ok := os.LookupEnv("TAG__SLACK__OUTPUT_CHANNEL_IDS"); ok {
		cfg.OutputChannelIDs = splitNonEmpty(raw)
	}
	if raw, ok := os.LookupEnv("TAG__SLACK__AUTOMATION_OPERATOR_USER_IDS"); ok {
		cfg.AutomationOperatorUserIDs = splitNonEmpty(raw)
	}
	return nil
}

func applyCodexEnvironment(cfg *CodexConfig) error {
	if raw, ok := os.LookupEnv("TAG__CODEX__ENABLED"); ok {
		value, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("TAG__CODEX__ENABLED must be a boolean: %w", err)
		}
		cfg.Enabled = value
	}
	for name, target := range map[string]*string{
		"TAG__CODEX__COMMAND":         &cfg.Command,
		"TAG__CODEX__HOME":            &cfg.Home,
		"TAG__CODEX__WORKER_ROOT":     &cfg.WorkerRoot,
		"TAG__CODEX__WEB_SEARCH_MODE": &cfg.WebSearchMode,
	} {
		if value, ok := os.LookupEnv(name); ok {
			*target = strings.TrimSpace(value)
		}
	}
	if raw, ok := os.LookupEnv("TAG__CODEX__TIMEOUT"); ok {
		value, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("TAG__CODEX__TIMEOUT must be a duration: %w", err)
		}
		cfg.Timeout = value
	}
	if cfg.Home == "" {
		cfg.Home = strings.TrimSpace(os.Getenv("CODEX_HOME"))
	}
	if cfg.Home == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve Codex home: %w", err)
		}
		cfg.Home = filepath.Join(home, ".codex")
	}
	return nil
}

// Explicit mapping keeps the public OpenAI credential name unambiguous and
// independent from struct-field tokenization details in the generic loader.
func applyClassifierEnvironment(cfg *ClassifierConfig) error {
	for name, target := range map[string]*string{
		"TAG__CLASSIFIER__MODE":             &cfg.Mode,
		"TAG__CLASSIFIER__PROVIDER":         &cfg.Provider,
		"TAG__CLASSIFIER__BASE_URL":         &cfg.BaseURL,
		"TAG__CLASSIFIER__OPENAI_API_KEY":   &cfg.OpenAIAPIKey,
		"TAG__CLASSIFIER__MODEL":            &cfg.Model,
		"TAG__CLASSIFIER__REASONING_EFFORT": &cfg.ReasoningEffort,
	} {
		if value, ok := os.LookupEnv(name); ok {
			*target = strings.TrimSpace(value)
		}
	}
	if _, explicit := os.LookupEnv("TAG__CLASSIFIER__OPENAI_API_KEY"); !explicit && cfg.OpenAIAPIKey == "" {
		cfg.OpenAIAPIKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	}
	if raw, ok := os.LookupEnv("TAG__CLASSIFIER__TIMEOUT"); ok {
		value, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("TAG__CLASSIFIER__TIMEOUT must be a duration: %w", err)
		}
		cfg.Timeout = value
	}
	if raw, ok := os.LookupEnv("TAG__CLASSIFIER__MAX_OUTPUT_TOKENS"); ok {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("TAG__CLASSIFIER__MAX_OUTPUT_TOKENS must be an integer: %w", err)
		}
		cfg.MaxOutputTokens = value
	}
	if raw, ok := os.LookupEnv("TAG__CLASSIFIER__REACTION_EMOJIS"); ok {
		cfg.ReactionEmojis = splitNonEmpty(raw)
	}
	return nil
}

func applyJobsEnvironment(cfg *JobsConfig) error {
	if raw, ok := os.LookupEnv("TAG__JOBS__WORKER_CONCURRENCY"); ok {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("TAG__JOBS__WORKER_CONCURRENCY must be an integer: %w", err)
		}
		cfg.WorkerConcurrency = value
	}
	return nil
}

func splitNonEmpty(raw string) []string {
	var values []string
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values
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
	if !cfg.Auth.Enabled && !isLoopbackHost(host) && !cfg.HTTP.AllowUnauthenticated {
		return fmt.Errorf("auth must be enabled for a non-loopback HTTP listener unless http.allowUnauthenticated is explicitly true")
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
		if cfg.Slack.OrganizationID == "" || !strings.HasPrefix(cfg.Slack.AppID, "A") || cfg.Slack.TeamID == "" {
			return fmt.Errorf("Slack organizationId, appId, and teamId are required for socket_mode")
		}
		if !strings.HasPrefix(cfg.Slack.AppLevelToken, "xapp-") || !strings.HasPrefix(cfg.Slack.BotUserOAuthToken, "xoxb-") {
			return fmt.Errorf("Slack socket_mode requires an app-level xapp token and bot-user OAuth xoxb token")
		}
		if !strings.HasPrefix(cfg.Slack.BotUserID, "U") && !strings.HasPrefix(cfg.Slack.BotUserID, "W") {
			return fmt.Errorf("Slack socket_mode requires a U- or W-prefixed botUserId")
		}
		if cfg.Slack.UserOAuthToken != "" && !strings.HasPrefix(cfg.Slack.UserOAuthToken, "xoxp-") {
			return fmt.Errorf("Slack user OAuth token must use the xoxp prefix")
		}
	default:
		return fmt.Errorf("unsupported slack.mode %q", cfg.Slack.Mode)
	}
	if cfg.Slack.StubQueueSize <= 0 {
		return fmt.Errorf("slack.stubQueueSize must be positive")
	}
	if cfg.Slack.ContextSyncLookback <= 0 || cfg.Slack.ContextSyncTimeout <= 0 || cfg.Slack.ContextSyncRefresh <= 0 || cfg.Slack.ContextSyncRequestInterval < 0 || cfg.Slack.ContextSyncMaxChannels <= 0 || cfg.Slack.ContextSyncMaxMessages <= 0 || cfg.Slack.ContextSyncMessagesPerChannel <= 0 {
		return fmt.Errorf("Slack context-sync bounds must be positive")
	}
	if cfg.Slack.ContextSyncEnabled {
		if cfg.Slack.Mode != "socket_mode" || !cfg.Slack.LiveEnabled {
			return fmt.Errorf("Slack context sync requires explicitly enabled socket_mode")
		}
		if !strings.HasPrefix(cfg.Slack.UserOAuthToken, "xoxp-") {
			return fmt.Errorf("Slack context sync requires a User OAuth xoxp token")
		}
	}
	if cfg.Slack.AutoAssistJoinedChannels && !cfg.Slack.ContextSyncEnabled {
		return fmt.Errorf("automatic assist for joined channels requires Slack context sync")
	}
	seenOutputChannels := make(map[string]struct{}, len(cfg.Slack.OutputChannelIDs))
	for _, channelID := range cfg.Slack.OutputChannelIDs {
		if strings.TrimSpace(channelID) == "" {
			return fmt.Errorf("Slack output channel IDs must not be empty")
		}
		if _, duplicate := seenOutputChannels[channelID]; duplicate {
			return fmt.Errorf("Slack output channel IDs must be unique")
		}
		seenOutputChannels[channelID] = struct{}{}
	}
	seenAutomationOperators := make(map[string]struct{}, len(cfg.Slack.AutomationOperatorUserIDs))
	for _, userID := range cfg.Slack.AutomationOperatorUserIDs {
		if !strings.HasPrefix(userID, "U") && !strings.HasPrefix(userID, "W") {
			return fmt.Errorf("Slack automation operator user IDs must use the U or W prefix")
		}
		if _, duplicate := seenAutomationOperators[userID]; duplicate {
			return fmt.Errorf("Slack automation operator user IDs must be unique")
		}
		seenAutomationOperators[userID] = struct{}{}
	}
	if cfg.Retention.RawEnvelope <= 0 || cfg.Retention.Prompt <= 0 || cfg.Retention.Sweep <= 0 {
		return fmt.Errorf("retention durations must be positive")
	}
	if cfg.Retention.Messages != 0 {
		return fmt.Errorf("retention.messages is retired; normalized messages are retained indefinitely and contextPacks.lookback controls context inclusion")
	}
	if cfg.ContextPacks.Lookback <= 0 || cfg.ContextPacks.MaxTokens <= 0 || cfg.ContextPacks.PartitionTotal() != cfg.ContextPacks.MaxTokens {
		return fmt.Errorf("context pack partitions must exactly equal maxTokens")
	}
	if cfg.Classifier.Mode != "shadow" && cfg.Classifier.Mode != "live" {
		return fmt.Errorf("unsupported classifier.mode %q; use shadow or live", cfg.Classifier.Mode)
	}
	if cfg.Classifier.AssistThreshold < 0 || cfg.Classifier.AssistThreshold > 1 || cfg.Classifier.ChannelReplyThreshold < cfg.Classifier.AssistThreshold || cfg.Classifier.ChannelReplyThreshold > 1 {
		return fmt.Errorf("invalid classifier thresholds")
	}
	if cfg.Classifier.MaxResponsesPerHour <= 0 || cfg.Classifier.MaxConcurrentJobs <= 0 || cfg.Jobs.Lease <= 0 || cfg.Jobs.Poll <= 0 || cfg.Jobs.MaxAttempts <= 0 || cfg.Jobs.WorkerConcurrency <= 0 || cfg.Jobs.WorkerConcurrency > 64 {
		return fmt.Errorf("classifier and job bounds must be positive and worker concurrency must not exceed 64")
	}
	if cfg.Classifier.FloodProtectionEnabled && (cfg.Classifier.FloodMaxMessages <= 0 || cfg.Classifier.FloodWindow <= 0) {
		return fmt.Errorf("enabled classifier flood protection requires a positive message limit and window")
	}
	if cfg.Classifier.Timeout <= 0 || cfg.Classifier.MaxOutputTokens <= 0 || len(cfg.Classifier.ReactionEmojis) == 0 {
		return fmt.Errorf("classifier timeout, output bound, and reaction allowlist are required")
	}
	switch cfg.Classifier.Provider {
	case "deterministic":
	case "openai":
		if cfg.Classifier.OpenAIAPIKey == "" || cfg.Classifier.BaseURL == "" || cfg.Classifier.Model == "" || cfg.Classifier.ReasoningEffort == "" {
			return fmt.Errorf("OpenAI classifier requires base URL, API key, model, and reasoning effort")
		}
	default:
		return fmt.Errorf("unsupported classifier.provider %q; use deterministic or openai", cfg.Classifier.Provider)
	}
	if cfg.Memory.Enabled {
		if cfg.Memory.BaseURL == "" || cfg.Memory.Model == "" || cfg.Memory.ReasoningEffort == "" || (cfg.Memory.OpenAIAPIKey == "" && cfg.Classifier.OpenAIAPIKey == "") {
			return fmt.Errorf("enabled memory curation requires an OpenAI base URL, API key, Luna model, and reasoning effort")
		}
		if cfg.Memory.Model != "gpt-5.6-luna" {
			return fmt.Errorf("memory curation must use gpt-5.6-luna")
		}
		if cfg.Memory.Timeout <= 0 || cfg.Memory.Interval <= 0 || cfg.Memory.Lookback <= 0 || cfg.Memory.Lookback > cfg.ContextPacks.Lookback || cfg.Memory.MinMessages < 2 || cfg.Memory.MaxMessages < cfg.Memory.MinMessages || cfg.Memory.MaxScopesPerRun <= 0 || cfg.Memory.MaxOutputTokens <= 0 || cfg.Memory.MinConfidence < 0 || cfg.Memory.MinConfidence > 1 {
			return fmt.Errorf("invalid memory curation bounds")
		}
	}
	if cfg.Models.DefaultProfile == "" || cfg.Models.DefaultProvider == "" || cfg.Models.DefaultModel == "" || cfg.Models.DefaultVariant == "" || cfg.Models.FastProfileBase == "" || cfg.Models.FastModel == "" {
		return fmt.Errorf("default and fast model profile configuration is required")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Codex.WebSearchMode)) {
	case "disabled", "cached", "indexed", "live":
	default:
		return fmt.Errorf("unsupported codex.webSearchMode %q; use disabled, cached, indexed, or live", cfg.Codex.WebSearchMode)
	}
	if cfg.Codex.Enabled {
		if cfg.Codex.Timeout <= 0 || strings.TrimSpace(cfg.Codex.Command) == "" || strings.TrimSpace(cfg.Codex.Home) == "" || strings.TrimSpace(cfg.Codex.WorkerRoot) == "" {
			return fmt.Errorf("enabled Codex App Server requires command, home, worker root, and a positive timeout")
		}
		if cfg.Models.DefaultProvider != "openai" {
			return fmt.Errorf("enabled Codex App Server requires the OpenAI model provider")
		}
	}
	if (cfg.Marketplaces.SkillRoot == "") != (cfg.Marketplaces.CatalogPath == "") {
		return fmt.Errorf("marketplace skill root and catalog path must be configured together")
	}
	if err := validatePluginSource("base", cfg.Marketplaces.BaseRoot, cfg.Marketplaces.BaseCatalogPath, cfg.Marketplaces.BasePlugin); err != nil {
		return err
	}
	if (cfg.Marketplaces.ToolRoot == "") != (cfg.Marketplaces.ToolCatalogPath == "") {
		return fmt.Errorf("tool marketplace root and catalog path must be configured together")
	}
	if cfg.Marketplaces.ToolsEnabled && (!cfg.Codex.Enabled || cfg.Marketplaces.ToolRoot == "" || !cfg.Keystore.Enabled || len(cfg.Marketplaces.InjectedTools) == 0) {
		return fmt.Errorf("enabled marketplace tools require Codex App Server, a tool marketplace, the keystore, and an injected-tool allowlist")
	}
	if cfg.Marketplaces.ToolsEnabled && strings.TrimSpace(cfg.Marketplaces.ToolPath) == "" {
		return fmt.Errorf("enabled marketplace tools require a deterministic executable PATH")
	}
	if cfg.Marketplaces.ToolsEnabled && strings.TrimSpace(cfg.Slack.OrganizationID) == "" {
		return fmt.Errorf("enabled marketplace tools require a Slack organization ID for credential scoping")
	}
	if cfg.Keystore.Enabled {
		key, err := base64.StdEncoding.DecodeString(cfg.Keystore.MasterKey)
		if err != nil || len(key) != 32 {
			return fmt.Errorf("enabled keystore requires a base64-encoded 32-byte master key")
		}
	}
	if _, err := cfg.AuditCommitmentKey(); err != nil {
		return err
	}
	if cfg.Environment != "development" && cfg.Audit.CommitmentKey == developmentAuditCommitmentKey {
		return fmt.Errorf("non-development environments require a non-default audit commitment key")
	}
	return nil
}

func validatePluginSource(label, root, catalogPath, plugin string) error {
	configured := 0
	for _, value := range []string{root, catalogPath, plugin} {
		if strings.TrimSpace(value) != "" {
			configured++
		}
	}
	if configured != 0 && configured != 3 {
		return fmt.Errorf("%s behavioral plugin root, catalog path, and plugin name must be configured together", label)
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

func (c *Config) AuditCommitmentKey() ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(c.Audit.CommitmentKey)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("audit.commitmentKey must be a base64-encoded 32-byte key")
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
		"environment":                         c.Environment,
		"http_addr":                           c.HTTP.Addr,
		"http_allow_unauthenticated":          c.HTTP.AllowUnauthenticated,
		"mongo_database":                      c.Mongo.Database,
		"slack_mode":                          c.Slack.Mode,
		"slack_live_enabled":                  c.Slack.LiveEnabled,
		"slack_context_sync_enabled":          c.Slack.ContextSyncEnabled,
		"slack_context_sync_lookback":         c.Slack.ContextSyncLookback.String(),
		"slack_context_sync_refresh":          c.Slack.ContextSyncRefresh.String(),
		"slack_context_sync_request_interval": c.Slack.ContextSyncRequestInterval.String(),
		"slack_context_sync_max_channels":     c.Slack.ContextSyncMaxChannels,
		"slack_context_sync_max_messages":     c.Slack.ContextSyncMaxMessages,
		"slack_auto_assist_joined_channels":   c.Slack.AutoAssistJoinedChannels,
		"slack_automation_operator_count":     len(c.Slack.AutomationOperatorUserIDs),
		"slack_output_channel_count":          len(c.Slack.OutputChannelIDs),
		"classifier_mode":                     c.Classifier.Mode,
		"classifier_provider":                 c.Classifier.Provider,
		"classifier_model":                    c.Classifier.Model,
		"classifier_reasoning_effort":         c.Classifier.ReasoningEffort,
		"classifier_max_responses_hour":       c.Classifier.MaxResponsesPerHour,
		"classifier_max_concurrent_jobs":      c.Classifier.MaxConcurrentJobs,
		"classifier_flood_protection_enabled": c.Classifier.FloodProtectionEnabled,
		"classifier_flood_max_messages":       c.Classifier.FloodMaxMessages,
		"classifier_flood_window":             c.Classifier.FloodWindow.String(),
		"memory_enabled":                      c.Memory.Enabled,
		"memory_model":                        c.Memory.Model,
		"memory_reasoning_effort":             c.Memory.ReasoningEffort,
		"memory_interval":                     c.Memory.Interval.String(),
		"memory_lookback":                     c.Memory.Lookback.String(),
		"job_worker_concurrency":              c.Jobs.WorkerConcurrency,
		"auth_enabled":                        c.Auth.Enabled,
		"log_file_enabled":                    c.Logging.FilePath != "",
		"raw_observation_retention":           c.Retention.RawEnvelope.String(),
		"message_retention":                   "indefinite",
		"context_lookback":                    c.ContextPacks.Lookback.String(),
		"context_max_tokens":                  c.ContextPacks.MaxTokens,
		"codex_app_server_enabled":            c.Codex.Enabled,
		"codex_web_search_mode":               c.Codex.WebSearchMode,
		"default_model_profile":               c.Models.DefaultProfile,
		"default_model":                       c.Models.DefaultModel,
		"default_model_variant":               c.Models.DefaultVariant,
		"fast_model":                          c.Models.FastModel,
		"skill_marketplace_configured":        c.Marketplaces.SkillRoot != "" || c.Marketplaces.BaseRoot != "",
		"base_plugin":                         c.Marketplaces.BasePlugin,
		"tool_marketplace_configured":         c.Marketplaces.ToolRoot != "",
		"marketplace_tools_enabled":           c.Marketplaces.ToolsEnabled,
		"keystore_enabled":                    c.Keystore.Enabled,
	}
}
