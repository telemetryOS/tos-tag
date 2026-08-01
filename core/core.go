// Package core owns construction and ordered lifecycle for tos-tag.
package core

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/RobertWHurst/blackbox"

	"github.com/telemetryos/tos-tag/core/admission"
	"github.com/telemetryos/tos-tag/core/approvals"
	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/channelconfig"
	"github.com/telemetryos/tos-tag/core/classifier"
	"github.com/telemetryos/tos-tag/core/config"
	"github.com/telemetryos/tos-tag/core/contextpacks"
	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/core/deliveries"
	"github.com/telemetryos/tos-tag/core/harness"
	"github.com/telemetryos/tos-tag/core/intelligence"
	"github.com/telemetryos/tos-tag/core/jobs"
	"github.com/telemetryos/tos-tag/core/keystore"
	"github.com/telemetryos/tos-tag/core/management"
	"github.com/telemetryos/tos-tag/core/marketplace"
	"github.com/telemetryos/tos-tag/core/modelrouter"
	"github.com/telemetryos/tos-tag/core/observer"
	"github.com/telemetryos/tos-tag/core/orgconfig"
	"github.com/telemetryos/tos-tag/core/pipeline"
	"github.com/telemetryos/tos-tag/core/retention"
	"github.com/telemetryos/tos-tag/core/routines"
	"github.com/telemetryos/tos-tag/core/server"
	"github.com/telemetryos/tos-tag/core/sessions"
	"github.com/telemetryos/tos-tag/core/slack"
	"github.com/telemetryos/tos-tag/core/tools"
	"github.com/telemetryos/tos-tag/core/triggers"
	"github.com/telemetryos/tos-tag/core/usage"
	"github.com/telemetryos/tos-tag/core/workers"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

const Version = "0.1.0-dev"

type Core struct {
	Config *config.Config
	Logger *blackbox.Logger

	database    *database.Database
	pipeline    *pipeline.Pipeline
	server      *server.Server
	retention   *retention.Janitor
	router      *modelrouter.Registry
	toolBridge  *tools.Bridge
	routines    *routines.Service
	triggers    *triggers.Service
	contextSync *slack.ContextSyncer
	contextRun  *slack.ContextSyncRun
	contextStop context.CancelFunc
	contextDone chan struct{}
}

// New builds the full object graph but performs no network I/O.
func New(cfg *config.Config, logger *blackbox.Logger) (*Core, error) {
	if err := config.Validate(cfg); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = blackbox.New()
	}
	db := database.New(cfg, logger)
	observations := observer.NewMongoStore(db, cfg.Retention.Messages)
	sessionStore := sessions.NewMongoStore(db)
	jobQueue := jobs.NewMongoQueue(db)
	deliveryQueue := deliveries.NewMongoQueue(db)
	decisionStore := classifier.NewMongoDecisionStore(db)
	contextBuilder, err := contextpacks.New(cfg.ContextPacks, contextpacks.WordTokenizer{})
	if err != nil {
		return nil, err
	}
	contextStore := contextpacks.NewMongoStore(db)
	renderer := deliveries.NewRenderer()
	organizationStore := orgconfig.NewMongo(db)
	intelligenceProjector := intelligence.NewMongo(db)
	retentionJanitor, err := retention.New(db, cfg.Retention.Sweep)
	if err != nil {
		return nil, err
	}
	var scopeResolver interface {
		orgconfig.Resolver
		GetOrganization(context.Context, string) (models.Organization, error)
		GetWorkspace(context.Context, string, string) (models.Workspace, error)
		UpsertContextChannel(context.Context, orgconfig.ChannelPolicy) (orgconfig.ChannelPolicy, error)
		ListChannels(context.Context, string) ([]orgconfig.ChannelPolicy, error)
	}
	if cfg.Slack.LiveEnabled {
		scopeResolver = organizationStore
	}
	admissionController := admission.NewMongo(db)
	usageRecorder := usage.NewMongo(db)
	channelConfiguration := channelconfig.NewMongoStore(db)
	managementRecords := management.NewMongo(db)
	routineStore := routines.NewMongoStore(db)
	triggerStore := triggers.NewMongoStore(db)
	routineScheduler := routines.NewScheduler(routineStore, jobQueue, routines.AuthorizerFunc(func(ctx context.Context, routine routines.Routine) error {
		policy, err := organizationStore.Resolve(ctx, routine.OrganizationID, routine.WorkspaceID, routine.ChannelID)
		if err != nil || !policy.Enrolled || policy.KillSwitch || !policy.MembershipRefreshedAt.After(time.Now().UTC().Add(-24*time.Hour)) {
			return fmt.Errorf("routine scope denied")
		}
		return nil
	}))
	routineService := routines.NewService(routineScheduler, cfg.Jobs.Poll)
	auditKey := make([]byte, 32)
	if _, err := rand.Read(auditKey); err != nil {
		return nil, fmt.Errorf("create audit commitment key: %w", err)
	}
	auditChain, err := audit.NewMongoChain(db, auditKey)
	if err != nil {
		return nil, fmt.Errorf("construct audit chain: %w", err)
	}
	var secretStore keystore.Repository
	if cfg.Keystore.Enabled {
		masterKey, keyErr := cfg.KeystoreKey()
		if keyErr != nil {
			return nil, keyErr
		}
		secretStore, err = keystore.NewMongoStore(db, masterKey)
		if err != nil {
			return nil, fmt.Errorf("construct keystore: %w", err)
		}
	}
	skillSnapshots, injectedSkillSnapshots, err := loadBehavioralSkills(cfg.Marketplaces)
	if err != nil {
		return nil, err
	}
	marketplaceRegistry, err := marketplace.NewRegistry(skillSnapshots)
	if err != nil {
		return nil, err
	}
	var toolMarketplaceRegistry *tools.Registry
	var allowedToolIDs []string
	if cfg.Marketplaces.ToolRoot != "" {
		toolMarketplaceRegistry, err = tools.LoadMarketplace(cfg.Marketplaces.ToolRoot, cfg.Marketplaces.ToolCatalogPath)
		if err != nil {
			return nil, fmt.Errorf("load tool marketplace: %w", err)
		}
	}
	if cfg.Marketplaces.ToolsEnabled {
		toolSkills, selectedToolIDs, selectErr := toolMarketplaceRegistry.Select(cfg.Marketplaces.InjectedTools)
		if selectErr != nil {
			return nil, selectErr
		}
		injectedSkillSnapshots = append(injectedSkillSnapshots, toolSkills...)
		allowedToolIDs = selectedToolIDs
	}
	approvalStore := approvals.NewMongoStore(db)
	approvalAuthorizer := approvals.ApproverAuthorizerFunc(func(ctx context.Context, organizationID, workspaceID, channelID, userID string) error {
		policy, resolveErr := organizationStore.Resolve(ctx, organizationID, workspaceID, channelID)
		if resolveErr != nil || !policy.Enrolled || policy.KillSwitch || !policy.WorkspaceEnabled || !policy.MembershipRefreshedAt.After(time.Now().UTC().Add(-24*time.Hour)) {
			return fmt.Errorf("approval channel policy denied")
		}
		for _, allowedUserID := range policy.ApproverUserIDs {
			if allowedUserID == userID {
				return nil
			}
		}
		return fmt.Errorf("user is not in the channel approver set")
	})
	approvalCoordinator, err := approvals.NewCoordinator(approvalStore, jobQueue, deliveryQueue, auditChain, approvalAuthorizer, admissionController)
	if err != nil {
		return nil, fmt.Errorf("construct approval coordinator: %w", err)
	}
	toolBridge, err := tools.NewBridge(tools.Gateway{Registry: toolMarketplaceRegistry, Secrets: secretStore, Executor: tools.Executor{Enabled: cfg.Marketplaces.ToolsEnabled, Usage: usageRecorder}}, jobQueue, approvalStore, auditChain, approvalCoordinator)
	if err != nil {
		return nil, fmt.Errorf("construct tool bridge: %w", err)
	}
	toolBridge.SetTriggerRepository(triggerStore)
	var responseHarness harness.Harness = harness.NewFake()
	if cfg.OpenCode.Enabled {
		switch cfg.OpenCode.Mode {
		case "local_worker":
			workerManager, workerErr := workers.NewLocalWithDependencies(cfg.OpenCode.WorkerRoot, "", toolBridge, usageRecorder)
			if workerErr != nil {
				return nil, fmt.Errorf("construct OpenCode worker manager: %w", workerErr)
			}
			var providerGateway *harness.ProviderGateway
			if cfg.Models.DefaultProvider != "opencode" {
				providerGateway, err = harness.NewProviderGateway(harness.ProviderGatewayOptions{ProviderID: cfg.Models.DefaultProvider, BaseURL: cfg.Classifier.BaseURL, APIKey: cfg.Classifier.OpenAIAPIKey, Timeout: cfg.OpenCode.Timeout, Jobs: jobQueue})
				if err != nil {
					return nil, fmt.Errorf("construct local model gateway: %w", err)
				}
			}
			responseHarness, err = harness.NewWorkerOpenCode(harness.WorkerOpenCodeOptions{Manager: workerManager, Command: cfg.OpenCode.Command, Skills: injectedSkillSnapshots, Timeout: cfg.OpenCode.Timeout, ToolBridge: toolBridge, ToolIDs: allowedToolIDs, Provider: providerGateway})
		case "external":
			responseHarness, err = harness.NewOpenCode(harness.OpenCodeOptions{Enabled: true, BaseURL: cfg.OpenCode.BaseURL, Username: cfg.OpenCode.Username, Password: cfg.OpenCode.Password, Timeout: cfg.OpenCode.Timeout})
		}
		if err != nil {
			return nil, err
		}
	}
	responseRouter, err := modelrouter.NewRegistry([]types.ModelProfile{{ID: cfg.Models.DefaultProfile, ProviderID: cfg.Models.DefaultProvider, ModelID: cfg.Models.DefaultModel, Variant: cfg.Models.DefaultVariant, ProviderOptions: map[string]any{"strength": "strong"}, RequiredCapabilities: []string{"structured"}, AllowedDataClasses: []string{"internal"}, MaxInputTokens: 200000, MaxOutputTokens: 16000, Enabled: true}}, nil, nil, cfg.Models.DefaultProfile, "routing/v1")
	if err != nil {
		return nil, err
	}
	responseRouter.AttachStore(modelrouter.NewMongoStore(db))
	var ambientClassifier classifier.Classifier = classifier.DeterministicClassifier{}
	if cfg.Classifier.Provider == "openai" {
		ambientClassifier, err = classifier.NewOpenAIClassifier(classifier.OpenAIOptions{
			BaseURL: cfg.Classifier.BaseURL, APIKey: cfg.Classifier.OpenAIAPIKey,
			Model: cfg.Classifier.Model, ReasoningEffort: cfg.Classifier.ReasoningEffort,
			Timeout: cfg.Classifier.Timeout, MaxOutputTokens: cfg.Classifier.MaxOutputTokens,
			ReactionEmojis: cfg.Classifier.ReactionEmojis, AgentProfiles: responseRouter,
		})
		if err != nil {
			return nil, err
		}
		ambientClassifier = loggedClassifier{next: ambientClassifier, logger: logger, usage: usageRecorder, providerID: "openai", modelID: cfg.Classifier.Model}
	}
	classificationService, err := classifier.New(ambientClassifier, cfg.Classifier.Mode == "shadow", cfg.Classifier.AssistThreshold, cfg.Classifier.ChannelReplyThreshold)
	if err != nil {
		return nil, err
	}
	var ingress slack.Ingress
	var transport slack.Delivery
	var stubIngress *slack.StubIngress
	var stubTransport *slack.StubDelivery
	var slackContextSync *slack.ContextSyncer
	var liveIngress *slack.LiveIngress
	if cfg.Slack.Mode == "stub" {
		stubIngress = slack.NewStubIngress(cfg.Slack.StubQueueSize)
		stubTransport = slack.NewStubDelivery()
		ingress, transport = stubIngress, stubTransport
	} else {
		var liveDelivery *slack.LiveDelivery
		var liveErr error
		liveIngress, liveDelivery, liveErr = slack.NewLive(slack.LiveOptions{
			OrganizationID:    cfg.Slack.OrganizationID,
			AppID:             cfg.Slack.AppID,
			TeamID:            cfg.Slack.TeamID,
			AppLevelToken:     cfg.Slack.AppLevelToken,
			BotUserOAuthToken: cfg.Slack.BotUserOAuthToken,
			BotUserID:         cfg.Slack.BotUserID,
			Logger:            logger,
		}, renderer)
		if liveErr != nil {
			return nil, fmt.Errorf("construct live Slack adapters: %w", liveErr)
		}
		if cfg.Slack.ContextSyncEnabled {
			slackContextSync, liveErr = slack.NewContextSyncer(slack.ContextSyncOptions{
				OrganizationID:     cfg.Slack.OrganizationID,
				TeamID:             cfg.Slack.TeamID,
				UserOAuthToken:     cfg.Slack.UserOAuthToken,
				BotUserID:          cfg.Slack.BotUserID,
				Lookback:           cfg.Slack.ContextSyncLookback,
				Timeout:            cfg.Slack.ContextSyncTimeout,
				MaxChannels:        cfg.Slack.ContextSyncMaxChannels,
				MaxMessages:        cfg.Slack.ContextSyncMaxMessages,
				MessagesPerChannel: cfg.Slack.ContextSyncMessagesPerChannel,
				Logger:             logger,
			})
			if liveErr != nil {
				return nil, fmt.Errorf("construct Slack user context sync: %w", liveErr)
			}
		}
		ingress, transport = liveIngress, liveDelivery
		liveIngress.SetApprovalInteractionHandler(func(ctx context.Context, interaction slack.ApprovalInteraction) error {
			return approvalCoordinator.HandleSlackDecision(ctx, approvals.SlackDecision{OrganizationID: interaction.OrganizationID, WorkspaceID: interaction.WorkspaceID, ChannelID: interaction.ChannelID, UserID: interaction.UserID, ApprovalID: interaction.ApprovalID, MessageTS: interaction.MessageTS, Approve: interaction.Approve})
		})
	}
	pipe, err := pipeline.New(pipeline.Dependencies{
		Config: cfg, Logger: logger, Ingress: ingress, Transport: transport,
		Observations: observations, Sessions: sessionStore, Jobs: jobQueue,
		Decisions: decisionStore, Deliveries: deliveryQueue, ContextPacks: contextBuilder, ContextStore: contextStore,
		Classifier: classificationService, Renderer: renderer, Scopes: scopeResolver, Intelligence: intelligenceProjector, Admissions: admissionController, ModelRouter: responseRouter, Harness: responseHarness, Usage: usageRecorder, ChannelConfig: channelConfiguration, Audit: auditChain, Approvals: approvalStore,
	})
	if err != nil {
		return nil, err
	}
	triggerScheduler, err := triggers.NewScheduler(triggerStore, jobQueue, triggers.GateFunc(pipe.EvaluateHeartbeat), triggers.AuthorizerFunc(func(ctx context.Context, subscription triggers.Subscription) error {
		policy, resolveErr := organizationStore.Resolve(ctx, subscription.OrganizationID, subscription.WorkspaceID, subscription.ChannelID)
		if resolveErr != nil || !policy.Enrolled || policy.KillSwitch || !policy.MembershipRefreshedAt.After(time.Now().UTC().Add(-24*time.Hour)) || !slackOutputChannelAllowedConfig(cfg, subscription.ChannelID) {
			return fmt.Errorf("trigger scope denied")
		}
		return nil
	}))
	if err != nil {
		return nil, fmt.Errorf("construct trigger scheduler: %w", err)
	}
	triggerService := triggers.NewService(triggerScheduler, cfg.Jobs.Poll)
	var statusIngress server.StubIngress
	var statusTransport server.StubDelivery
	if stubIngress != nil {
		statusIngress = stubIngress
	}
	if stubTransport != nil {
		statusTransport = stubTransport
	}
	srv, err := server.New(server.Dependencies{
		Config: cfg, Logger: logger, Health: db, Ingress: statusIngress, Transport: statusTransport,
		Jobs: jobQueue, Deliveries: deliveryQueue, Decisions: decisionStore, Version: Version,
		Routes: responseRouter, Organizations: organizationStore, Retention: retentionJanitor, Records: managementRecords, ChannelConfig: channelConfiguration, Marketplaces: marketplaceRegistry, ToolMarketplaces: toolMarketplaceRegistry, Intelligence: intelligenceProjector, Secrets: secretStore, Audit: auditChain, Approvals: approvalStore, ApprovalCoordinator: approvalCoordinator, Routines: routineStore, Triggers: triggerStore,
	})
	if err != nil {
		return nil, err
	}
	return &Core{Config: cfg, Logger: logger, database: db, pipeline: pipe, server: srv, retention: retentionJanitor, router: responseRouter, toolBridge: toolBridge, routines: routineService, triggers: triggerService, contextSync: slackContextSync}, nil
}

func slackOutputChannelAllowedConfig(cfg *config.Config, channelID string) bool {
	if cfg == nil || len(cfg.Slack.OutputChannelIDs) == 0 {
		return true
	}
	for _, allowed := range cfg.Slack.OutputChannelIDs {
		if allowed == channelID {
			return true
		}
	}
	return false
}

type loggedClassifier struct {
	next       classifier.Classifier
	logger     *blackbox.Logger
	usage      usage.Recorder
	providerID string
	modelID    string
}

func (c loggedClassifier) Decide(ctx context.Context, target classifier.Target, pack types.ContextPackRevision) (types.ClassificationDecision, error) {
	started := time.Now()
	c.logger.WithCtx(blackbox.Ctx{
		"organization_id": target.Envelope.OrganizationID,
		"channel_id":      target.Envelope.ChannelID,
		"observation_id":  target.ObservationID,
		"context_pack_id": pack.ID,
		"context_tokens":  pack.TotalTokens,
		"context_sources": len(pack.Sources),
		"provider_id":     c.providerID,
		"model_id":        c.modelID,
	}).Info("OpenAI classifier request started")
	decision, err := c.next.Decide(ctx, target, pack)
	if err != nil {
		duration := time.Since(started)
		c.logger.WithCtx(blackbox.Ctx{
			"organization_id":  target.Envelope.OrganizationID,
			"channel_id":       target.Envelope.ChannelID,
			"observation_id":   target.ObservationID,
			"classifier_stage": classifier.ErrorStage(err),
			"classifier_code":  classifier.ErrorCode(err),
			"error_type":       fmt.Sprintf("%T", err),
			"duration_ms":      duration.Milliseconds(),
		}).Warn("OpenAI classifier failed; deterministic fallback selected")
		if c.usage != nil {
			_ = c.usage.Record(ctx, usage.Event{OrganizationID: target.Envelope.OrganizationID, Category: "classifier", ProviderID: c.providerID, ModelID: c.modelID, Calls: 1, DurationMS: duration.Milliseconds()})
		}
		fallback, fallbackErr := (classifier.DeterministicClassifier{}).Decide(ctx, target, pack)
		if fallbackErr != nil {
			return types.ClassificationDecision{}, err
		}
		fallback.ReasonCodes = append([]string{"classifier.deterministic_fallback"}, fallback.ReasonCodes...)
		return fallback, nil
	}
	duration := time.Since(started)
	c.logger.WithCtx(blackbox.Ctx{
		"organization_id":             target.Envelope.OrganizationID,
		"channel_id":                  target.Envelope.ChannelID,
		"observation_id":              target.ObservationID,
		"recommended_outcome":         decision.Outcome,
		"recommended_confidence":      decision.Confidence,
		"recommended_reaction":        decision.Reaction,
		"recommended_agent_profile":   decision.AgentModelProfile,
		"recommended_agent_strength":  decision.AgentModelStrength,
		"recommended_agent_effort":    decision.AgentReasoningEffort,
		"classifier_response_id":      decision.ClassifierResponseID,
		"classifier_model":            decision.ClassifierModel,
		"classifier_reasoning_effort": decision.ClassifierReasoningEffort,
		"input_tokens":                decision.ClassifierInputTokens,
		"output_tokens":               decision.ClassifierOutputTokens,
		"duration_ms":                 duration.Milliseconds(),
	}).Info("OpenAI classifier request completed")
	if c.usage != nil {
		_ = c.usage.Record(ctx, usage.Event{OrganizationID: target.Envelope.OrganizationID, Category: "classifier", ProviderID: c.providerID, ModelID: c.modelID, InputTokens: decision.ClassifierInputTokens, OutputTokens: decision.ClassifierOutputTokens, Calls: 1, DurationMS: duration.Milliseconds()})
	}
	return decision, err
}

func loadBehavioralSkills(cfg config.MarketplaceConfig) ([]marketplace.SkillSnapshot, []marketplace.SkillSnapshot, error) {
	var available []marketplace.SkillSnapshot
	var automaticallyInjected []marketplace.SkillSnapshot
	if cfg.SkillRoot != "" {
		loaded, err := marketplace.LoadPluginMarketplace(cfg.SkillRoot, cfg.CatalogPath)
		if err != nil {
			return nil, nil, fmt.Errorf("load skill marketplace: %w", err)
		}
		available = append(available, loaded...)
	}
	for _, source := range []struct {
		label       string
		root        string
		catalogPath string
		plugin      string
	}{
		{label: "headless", root: cfg.HeadlessRoot, catalogPath: cfg.HeadlessCatalogPath, plugin: cfg.HeadlessPlugin},
		{label: "base", root: cfg.BaseRoot, catalogPath: cfg.BaseCatalogPath, plugin: cfg.BasePlugin},
	} {
		if source.root == "" {
			continue
		}
		loaded, err := marketplace.LoadPlugin(source.root, source.catalogPath, source.plugin)
		if err != nil {
			return nil, nil, fmt.Errorf("load %s behavioral plugin: %w", source.label, err)
		}
		available = append(available, loaded...)
		automaticallyInjected = append(automaticallyInjected, loaded...)
	}
	selected, err := selectSkillSnapshots(available, cfg.InjectedSkills)
	if err != nil {
		return nil, nil, err
	}
	selected = append(selected, automaticallyInjected...)
	if len(selected) > 0 {
		registry, err := marketplace.NewRegistry(selected)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve injected behavioral plugins: %w", err)
		}
		selected = registry.List()
	}
	return available, selected, nil
}

func selectSkillSnapshots(available []marketplace.SkillSnapshot, allowlist []string) ([]marketplace.SkillSnapshot, error) {
	if len(allowlist) == 0 {
		return nil, nil
	}
	byName := make(map[string]marketplace.SkillSnapshot, len(available))
	for _, snapshot := range available {
		byName[snapshot.Name] = snapshot
	}
	selected := make([]marketplace.SkillSnapshot, 0, len(allowlist))
	seen := make(map[string]bool, len(allowlist))
	for _, name := range allowlist {
		if seen[name] {
			continue
		}
		snapshot, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("injected skill %q is not present in the configured marketplace", name)
		}
		seen[name] = true
		selected = append(selected, snapshot)
	}
	return selected, nil
}

// Start connects persistence, starts workers, binds management HTTP, and only
// then enables Slack ingress so acknowledgements cannot outrun durability.
func (c *Core) Start(ctx context.Context) error {
	if err := c.database.Connect(ctx); err != nil {
		return fmt.Errorf("connect MongoDB: %w", err)
	}
	if err := c.router.Load(ctx); err != nil {
		_ = c.database.Disconnect(context.Background())
		return fmt.Errorf("load model routing policy: %w", err)
	}
	if c.contextSync != nil {
		run, err := c.contextSync.Discover(ctx, c.pipeline.RegisterContextChannel)
		if err != nil {
			_ = c.database.Disconnect(context.Background())
			return fmt.Errorf("discover Slack user context: %w", err)
		}
		c.contextRun = run
	}
	if c.toolBridge != nil {
		if err := c.toolBridge.Start(); err != nil {
			_ = c.database.Disconnect(context.Background())
			return fmt.Errorf("start tool bridge: %w", err)
		}
	}
	if err := c.pipeline.StartWorkers(ctx); err != nil {
		if c.toolBridge != nil {
			_ = c.toolBridge.Stop(context.Background())
		}
		_ = c.database.Disconnect(context.Background())
		return fmt.Errorf("start workers: %w", err)
	}
	c.retention.Start(ctx)
	c.routines.Start(ctx)
	c.triggers.Start(ctx)
	if err := c.server.Listen(); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), c.Config.HTTP.ShutdownTimeout)
		defer cancel()
		_ = c.pipeline.Stop(stopCtx)
		_ = c.retention.Stop(stopCtx)
		_ = c.routines.Stop(stopCtx)
		_ = c.triggers.Stop(stopCtx)
		if c.toolBridge != nil {
			_ = c.toolBridge.Stop(stopCtx)
		}
		_ = c.database.Disconnect(stopCtx)
		return err
	}
	if err := c.pipeline.StartIngress(ctx); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), c.Config.HTTP.ShutdownTimeout)
		defer cancel()
		_ = c.server.Shutdown(stopCtx)
		_ = c.pipeline.Stop(stopCtx)
		_ = c.retention.Stop(stopCtx)
		_ = c.routines.Stop(stopCtx)
		_ = c.triggers.Stop(stopCtx)
		if c.toolBridge != nil {
			_ = c.toolBridge.Stop(stopCtx)
		}
		_ = c.database.Disconnect(stopCtx)
		return fmt.Errorf("start Slack ingress: %w", err)
	}
	c.startContextBackfill(ctx)
	c.Logger.Infof("tos-tag started at %s with Slack mode %s and %s %s classifier", c.server.Addr(), c.Config.Slack.Mode, c.Config.Classifier.Mode, c.Config.Classifier.Provider)
	return nil
}

func (c *Core) startContextBackfill(parent context.Context) {
	if c.contextSync == nil || c.contextRun == nil || c.contextStop != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	c.contextStop = cancel
	c.contextDone = make(chan struct{})
	run := c.contextRun
	go func() {
		defer close(c.contextDone)
		if _, err := c.contextSync.Backfill(ctx, run, c.pipeline.ImportContextEnvelope); err != nil && ctx.Err() == nil {
			c.Logger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", err), "error": err.Error()}).Error("Slack user context backfill stopped before completion")
		}
	}()
}

func (c *Core) stopContextBackfill(ctx context.Context) error {
	if c.contextStop == nil {
		return nil
	}
	c.contextStop()
	select {
	case <-c.contextDone:
		c.contextStop = nil
		c.contextDone = nil
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop closes ingress and workers first, then HTTP, then MongoDB.
func (c *Core) Stop(ctx context.Context) error {
	var first error
	if err := c.pipeline.Stop(ctx); err != nil && first == nil {
		first = err
	}
	if err := c.stopContextBackfill(ctx); err != nil && first == nil {
		first = err
	}
	if c.toolBridge != nil {
		if err := c.toolBridge.Stop(ctx); err != nil && first == nil {
			first = err
		}
	}
	if err := c.retention.Stop(ctx); err != nil && first == nil {
		first = err
	}
	if err := c.routines.Stop(ctx); err != nil && first == nil {
		first = err
	}
	if err := c.triggers.Stop(ctx); err != nil && first == nil {
		first = err
	}
	if err := c.server.Shutdown(ctx); err != nil && first == nil {
		first = err
	}
	if err := c.database.Disconnect(ctx); err != nil && first == nil {
		first = err
	}
	return first
}
