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
	"github.com/telemetryos/tos-tag/core/chatgating"
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
	"github.com/telemetryos/tos-tag/core/usage"
	"github.com/telemetryos/tos-tag/core/workers"
	"github.com/telemetryos/tos-tag/types"
)

const Version = "0.1.0-dev"

type Core struct {
	Config *config.Config
	Logger *blackbox.Logger

	database   *database.Database
	pipeline   *pipeline.Pipeline
	server     *server.Server
	retention  *retention.Janitor
	router     *modelrouter.Registry
	toolBridge *tools.Bridge
	routines   *routines.Service
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
	decisionStore := chatgating.NewMongoDecisionStore(db)
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
	var skillSnapshots []marketplace.SkillSnapshot
	if cfg.Marketplaces.SkillRoot != "" {
		skillSnapshots, err = marketplace.LoadPluginMarketplace(cfg.Marketplaces.SkillRoot, cfg.Marketplaces.CatalogPath)
		if err != nil {
			return nil, fmt.Errorf("load skill marketplace: %w", err)
		}
	}
	marketplaceRegistry, err := marketplace.NewRegistry(skillSnapshots)
	if err != nil {
		return nil, err
	}
	injectedSkillSnapshots, err := selectSkillSnapshots(skillSnapshots, cfg.Marketplaces.InjectedSkills)
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
	var toolBridge *tools.Bridge
	if cfg.Marketplaces.ToolsEnabled {
		toolBridge, err = tools.NewBridge(tools.Gateway{Registry: toolMarketplaceRegistry, Secrets: secretStore, Executor: tools.Executor{Enabled: true, Usage: usageRecorder}}, jobQueue, approvalStore, auditChain)
		if err != nil {
			return nil, fmt.Errorf("construct tool bridge: %w", err)
		}
	}
	var responseHarness harness.Harness = harness.NewFake()
	var gatingHarness harness.Harness = responseHarness
	if cfg.OpenCode.Enabled {
		switch cfg.OpenCode.Mode {
		case "local_worker":
			workerManager, workerErr := workers.NewLocalWithDependencies(cfg.OpenCode.WorkerRoot, "", toolBridge, usageRecorder)
			if workerErr != nil {
				return nil, fmt.Errorf("construct OpenCode worker manager: %w", workerErr)
			}
			responseHarness, err = harness.NewWorkerOpenCode(harness.WorkerOpenCodeOptions{Manager: workerManager, Command: cfg.OpenCode.Command, Skills: injectedSkillSnapshots, Timeout: cfg.OpenCode.Timeout, ToolBridge: toolBridge, ToolIDs: allowedToolIDs})
			if err == nil {
				gatingHarness, err = harness.NewWorkerOpenCode(harness.WorkerOpenCodeOptions{Manager: workerManager, Command: cfg.OpenCode.Command, Timeout: cfg.OpenCode.Timeout})
			}
		case "external":
			responseHarness, err = harness.NewOpenCode(harness.OpenCodeOptions{Enabled: true, BaseURL: cfg.OpenCode.BaseURL, Username: cfg.OpenCode.Username, Password: cfg.OpenCode.Password, Timeout: cfg.OpenCode.Timeout})
			gatingHarness = responseHarness
		}
		if err != nil {
			return nil, err
		}
	}
	responseRouter, err := modelrouter.NewRegistry([]types.ModelProfile{{ID: cfg.Models.DefaultProfile, ProviderID: cfg.Models.DefaultProvider, ModelID: cfg.Models.DefaultModel, Variant: cfg.Models.DefaultVariant, RequiredCapabilities: []string{"structured"}, AllowedDataClasses: []string{"internal"}, MaxInputTokens: 200000, MaxOutputTokens: 16000, Enabled: true}}, nil, nil, cfg.Models.DefaultProfile, "routing/v1")
	if err != nil {
		return nil, err
	}
	responseRouter.AttachStore(modelrouter.NewMongoStore(db))
	var gateClassifier chatgating.Classifier = chatgating.DeterministicClassifier{}
	if cfg.OpenCode.Enabled {
		gateClassifier, err = chatgating.NewHarnessClassifier(gatingHarness, responseRouter)
		if err != nil {
			return nil, err
		}
	}
	gate, err := chatgating.New(gateClassifier, true, cfg.Gating.AssistThreshold, cfg.Gating.ChannelReplyThreshold)
	if err != nil {
		return nil, err
	}
	var ingress slack.Ingress
	var transport slack.Delivery
	var stubIngress *slack.StubIngress
	var stubTransport *slack.StubDelivery
	if cfg.Slack.Mode == "stub" {
		stubIngress = slack.NewStubIngress(cfg.Slack.StubQueueSize)
		stubTransport = slack.NewStubDelivery()
		ingress, transport = stubIngress, stubTransport
	} else {
		liveIngress, liveDelivery, liveErr := slack.NewLive(slack.LiveOptions{
			OrganizationID: cfg.Slack.OrganizationID,
			TeamID:         cfg.Slack.TeamID,
			AppToken:       cfg.Slack.AppToken,
			BotToken:       cfg.Slack.BotToken,
			BotUserID:      cfg.Slack.BotUserID,
		}, renderer)
		if liveErr != nil {
			return nil, fmt.Errorf("construct live Slack adapters: %w", liveErr)
		}
		ingress, transport = liveIngress, liveDelivery
	}
	pipe, err := pipeline.New(pipeline.Dependencies{
		Config: cfg, Logger: logger, Ingress: ingress, Transport: transport,
		Observations: observations, Sessions: sessionStore, Jobs: jobQueue,
		Decisions: decisionStore, Deliveries: deliveryQueue, ContextPacks: contextBuilder, ContextStore: contextStore,
		Gate: gate, Renderer: renderer, Scopes: scopeResolver, Intelligence: intelligenceProjector, Admissions: admissionController, ModelRouter: responseRouter, Harness: responseHarness, Usage: usageRecorder, ChannelConfig: channelConfiguration, Audit: auditChain,
	})
	if err != nil {
		return nil, err
	}
	srv, err := server.New(server.Dependencies{
		Config: cfg, Logger: logger, Health: db, Ingress: stubIngress, Transport: stubTransport,
		Jobs: jobQueue, Deliveries: deliveryQueue, Decisions: decisionStore, Version: Version,
		Routes: responseRouter, Organizations: organizationStore, Retention: retentionJanitor, Records: managementRecords, ChannelConfig: channelConfiguration, Marketplaces: marketplaceRegistry, ToolMarketplaces: toolMarketplaceRegistry, Intelligence: intelligenceProjector, Secrets: secretStore, Audit: auditChain, Approvals: approvalStore, Routines: routineStore,
	})
	if err != nil {
		return nil, err
	}
	return &Core{Config: cfg, Logger: logger, database: db, pipeline: pipe, server: srv, retention: retentionJanitor, router: responseRouter, toolBridge: toolBridge, routines: routineService}, nil
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
	if err := c.server.Listen(); err != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), c.Config.HTTP.ShutdownTimeout)
		defer cancel()
		_ = c.pipeline.Stop(stopCtx)
		_ = c.retention.Stop(stopCtx)
		_ = c.routines.Stop(stopCtx)
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
		if c.toolBridge != nil {
			_ = c.toolBridge.Stop(stopCtx)
		}
		_ = c.database.Disconnect(stopCtx)
		return fmt.Errorf("start Slack ingress: %w", err)
	}
	c.Logger.Infof("tos-tag started at %s with Slack mode %s and shadow gating", c.server.Addr(), c.Config.Slack.Mode)
	return nil
}

// Stop closes ingress and workers first, then HTTP, then MongoDB.
func (c *Core) Stop(ctx context.Context) error {
	var first error
	if err := c.pipeline.Stop(ctx); err != nil && first == nil {
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
	if err := c.server.Shutdown(ctx); err != nil && first == nil {
		first = err
	}
	if err := c.database.Disconnect(ctx); err != nil && first == nil {
		first = err
	}
	return first
}
