package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RobertWHurst/blackbox"

	"github.com/telemetryos/tos-tag/core/activity"
	"github.com/telemetryos/tos-tag/core/admission"
	"github.com/telemetryos/tos-tag/core/approvals"
	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/classifier"
	"github.com/telemetryos/tos-tag/core/config"
	"github.com/telemetryos/tos-tag/core/contextpacks"
	"github.com/telemetryos/tos-tag/core/deliveries"
	"github.com/telemetryos/tos-tag/core/flood"
	"github.com/telemetryos/tos-tag/core/harness"
	"github.com/telemetryos/tos-tag/core/jobs"
	agentmemory "github.com/telemetryos/tos-tag/core/memory"
	"github.com/telemetryos/tos-tag/core/observer"
	"github.com/telemetryos/tos-tag/core/orgconfig"
	"github.com/telemetryos/tos-tag/core/sessions"
	"github.com/telemetryos/tos-tag/core/slack"
	"github.com/telemetryos/tos-tag/core/triggers"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

func contextSyncConfig() config.Config {
	cfg := config.DefaultConfiguration
	cfg.Slack.Mode = "socket_mode"
	cfg.Slack.LiveEnabled = true
	cfg.Slack.OrganizationID = "org-test"
	cfg.Slack.TeamID = "team-test"
	cfg.Slack.ContextSyncEnabled = true
	return cfg
}

type recordingObservationStore struct {
	observer.Store
	recentChannels     []string
	recentSince        time.Time
	lateCandidateCalls int
}

type recordingAdmissions struct {
	completed []string
}

type failingApprovalRepository struct {
	approvals.Repository
	err error
}

func (r failingApprovalRepository) GetContext(context.Context, string, string) (approvals.Approval, error) {
	return approvals.Approval{}, r.err
}

type classifierFunc func(context.Context, classifier.Target, types.ContextPackRevision) (types.ClassificationDecision, error)

type blockingHarness struct {
	started chan struct{}
	release chan struct{}
}

func (*blockingHarness) Health(context.Context) error { return nil }
func (*blockingHarness) CreateSession(context.Context, string) (harness.Session, error) {
	return harness.Session{ID: string(types.NewID("harness")), CreatedAt: time.Now().UTC()}, nil
}
func (h *blockingHarness) Prompt(context.Context, string, harness.Prompt) error {
	h.started <- struct{}{}
	<-h.release
	return nil
}
func (*blockingHarness) Events(context.Context, string) (<-chan harness.Event, <-chan error) {
	events := make(chan harness.Event, 2)
	errs := make(chan error)
	events <- harness.Event{Type: "message.delta", Data: map[string]any{"text": `{"segments":[{"kind":"mrkdwn_text","text":"done"}]}`}}
	events <- harness.Event{Type: "session.idle"}
	close(events)
	close(errs)
	return events, errs
}
func (*blockingHarness) Permission(context.Context, string, harness.PermissionDecision) error {
	return nil
}
func (*blockingHarness) Abort(context.Context, string) error { return nil }

type footerHarness struct{}

func (*footerHarness) Health(context.Context) error { return nil }
func (*footerHarness) CreateSession(context.Context, string) (harness.Session, error) {
	return harness.Session{ID: "footer-session", CreatedAt: time.Now().UTC()}, nil
}
func (*footerHarness) Prompt(context.Context, string, harness.Prompt) error { return nil }
func (*footerHarness) Events(context.Context, string) (<-chan harness.Event, <-chan error) {
	events := make(chan harness.Event, 3)
	errs := make(chan error)
	events <- harness.Event{Type: "usage.updated", Data: map[string]any{"input_tokens": int64(18_000), "output_tokens": int64(2_400), "cached_input_tokens": int64(7_000), "reasoning_output_tokens": int64(800), "total_tokens": int64(20_400)}}
	events <- harness.Event{Type: "message.delta", Data: map[string]any{"text": `{"segments":[{"kind":"mrkdwn_text","text":"done"}]}`}}
	events <- harness.Event{Type: "session.idle"}
	close(events)
	close(errs)
	return events, errs
}
func (*footerHarness) Permission(context.Context, string, harness.PermissionDecision) error {
	return nil
}
func (*footerHarness) Abort(context.Context, string) error { return nil }

type duplicateToolProgressHarness struct{}

func (*duplicateToolProgressHarness) Health(context.Context) error { return nil }
func (*duplicateToolProgressHarness) CreateSession(context.Context, string) (harness.Session, error) {
	return harness.Session{ID: "duplicate-tool-progress-session", CreatedAt: time.Now().UTC()}, nil
}
func (*duplicateToolProgressHarness) Prompt(context.Context, string, harness.Prompt) error {
	return nil
}
func (*duplicateToolProgressHarness) Events(context.Context, string) (<-chan harness.Event, <-chan error) {
	events := make(chan harness.Event, 4)
	errs := make(chan error)
	toolEvent := harness.Event{Type: "tool.execution.completed", Data: map[string]any{"tool_id": "telemetryos.code", "operation_id": "read", "resource_action": "source"}}
	events <- toolEvent
	events <- toolEvent
	events <- harness.Event{Type: "message.delta", Data: map[string]any{"text": `{"segments":[{"kind":"mrkdwn_text","text":"done"}]}`}}
	events <- harness.Event{Type: "session.idle"}
	close(events)
	close(errs)
	return events, errs
}
func (*duplicateToolProgressHarness) Permission(context.Context, string, harness.PermissionDecision) error {
	return nil
}
func (*duplicateToolProgressHarness) Abort(context.Context, string) error { return nil }

type transparentProgressHarness struct{}

func (*transparentProgressHarness) Health(context.Context) error { return nil }
func (*transparentProgressHarness) CreateSession(context.Context, string) (harness.Session, error) {
	return harness.Session{ID: "transparent-progress-session", CreatedAt: time.Now().UTC()}, nil
}
func (*transparentProgressHarness) Prompt(context.Context, string, harness.Prompt) error { return nil }
func (*transparentProgressHarness) Events(context.Context, string) (<-chan harness.Event, <-chan error) {
	events := make(chan harness.Event, 6)
	errs := make(chan error)
	events <- harness.Event{Type: "skill.execution.started", Data: map[string]any{"skill_name": "product-knowledge", "call_id": "call-1"}}
	events <- harness.Event{Type: "tool.execution.started", Data: map[string]any{"call_id": "call-1", "tool_id": "telemetryos.wiki", "operation_id": "read", "resource_action": "get"}}
	events <- harness.Event{Type: "tool.execution.completed", Data: map[string]any{"call_id": "call-1", "tool_id": "telemetryos.wiki", "operation_id": "read", "resource_action": "get"}}
	events <- harness.Event{Type: "tool.execution.started", Data: map[string]any{"call_id": "call-2", "tool_id": "openai.web-search", "operation_id": "search"}}
	events <- harness.Event{Type: "tool.execution.failed", Data: map[string]any{"call_id": "call-2", "tool_id": "openai.web-search", "operation_id": "search"}}
	events <- harness.Event{Type: "message.delta", Data: map[string]any{"text": `{"segments":[{"kind":"mrkdwn_text","text":"done"}]}`}}
	close(events)
	close(errs)
	return events, errs
}
func (*transparentProgressHarness) Permission(context.Context, string, harness.PermissionDecision) error {
	return nil
}
func (*transparentProgressHarness) Abort(context.Context, string) error { return nil }

type wikiValidationRetryHarness struct{}

func (*wikiValidationRetryHarness) Health(context.Context) error { return nil }
func (*wikiValidationRetryHarness) CreateSession(context.Context, string) (harness.Session, error) {
	return harness.Session{ID: "wiki-validation-retry-session", CreatedAt: time.Now().UTC()}, nil
}
func (*wikiValidationRetryHarness) Prompt(context.Context, string, harness.Prompt) error { return nil }
func (*wikiValidationRetryHarness) Events(context.Context, string) (<-chan harness.Event, <-chan error) {
	events := make(chan harness.Event, 5)
	errs := make(chan error)
	events <- harness.Event{ID: "event-validation", Type: "tool.validation.failed", Data: map[string]any{"call_id": "call-1", "tool_id": "telemetryos.wiki", "operation_id": "write", "resource_action": "put", "validation_code": "wiki.body.required"}}
	events <- harness.Event{Type: "tool.execution.started", Data: map[string]any{"call_id": "call-2", "tool_id": "telemetryos.wiki", "operation_id": "write", "resource_action": "put"}}
	events <- harness.Event{Type: "tool.execution.completed", Data: map[string]any{"call_id": "call-2", "tool_id": "telemetryos.wiki", "operation_id": "write", "resource_action": "put"}}
	events <- harness.Event{Type: "message.delta", Data: map[string]any{"text": `{"segments":[{"kind":"mrkdwn_text","text":"done"}]}`}}
	close(events)
	close(errs)
	return events, errs
}
func (*wikiValidationRetryHarness) Permission(context.Context, string, harness.PermissionDecision) error {
	return nil
}
func (*wikiValidationRetryHarness) Abort(context.Context, string) error { return nil }

type contextFailureHarness struct{}

func (*contextFailureHarness) Health(context.Context) error { return nil }
func (*contextFailureHarness) CreateSession(ctx context.Context, _ string) (harness.Session, error) {
	return harness.Session{}, ctx.Err()
}
func (*contextFailureHarness) Prompt(context.Context, string, harness.Prompt) error { return nil }
func (*contextFailureHarness) Events(context.Context, string) (<-chan harness.Event, <-chan error) {
	return nil, nil
}
func (*contextFailureHarness) Permission(context.Context, string, harness.PermissionDecision) error {
	return nil
}
func (*contextFailureHarness) Abort(context.Context, string) error { return nil }

type recordingRequeueQueue struct {
	jobs.Queue
	contextErr error
}

func (q *recordingRequeueQueue) Requeue(ctx context.Context, id types.JobID, token, reason string, delay time.Duration) (jobs.Job, error) {
	q.contextErr = ctx.Err()
	return q.Queue.Requeue(ctx, id, token, reason, delay)
}

func (f classifierFunc) Decide(ctx context.Context, target classifier.Target, pack types.ContextPackRevision) (types.ClassificationDecision, error) {
	return f(ctx, target, pack)
}

func (*recordingAdmissions) Admit(context.Context, orgconfig.ChannelPolicy) (string, error) {
	return "", nil
}

func (r *recordingAdmissions) Complete(_ context.Context, id string) {
	r.completed = append(r.completed, id)
}

func (s *recordingObservationStore) Recent(ctx context.Context, organizationID string, channelIDs []string, since time.Time, limit int) ([]models.ChannelMessage, error) {
	s.recentChannels = append([]string(nil), channelIDs...)
	s.recentSince = since
	return s.Store.Recent(ctx, organizationID, channelIDs, since, limit)
}

func (s *recordingObservationStore) LateCandidates(ctx context.Context, organizationID string, since, before time.Time, limit int) ([]models.Observation, error) {
	s.lateCandidateCalls++
	return s.Store.LateCandidates(ctx, organizationID, since, before, limit)
}

type testSystem struct {
	pipeline   *Pipeline
	ingress    *slack.StubIngress
	transport  *slack.StubDelivery
	jobs       *jobs.MemoryQueue
	deliveries *deliveries.MemoryQueue
	decisions  *classifier.MemoryDecisionStore
	activity   *activity.Hub
}

func newTestSystem(t *testing.T) testSystem {
	t.Helper()
	cfg := config.DefaultConfiguration
	cfg.Jobs.Poll = 2 * time.Millisecond
	cfg.Jobs.Lease = time.Second
	builder, err := contextpacks.New(cfg.ContextPacks, contextpacks.WordTokenizer{})
	if err != nil {
		t.Fatal(err)
	}
	classificationService, err := classifier.New(classifier.DeterministicClassifier{}, true, cfg.Classifier.AssistThreshold, cfg.Classifier.ChannelReplyThreshold)
	if err != nil {
		t.Fatal(err)
	}
	ingress := slack.NewStubIngress(32)
	transport := slack.NewStubDelivery()
	jobQueue := jobs.NewMemoryQueue(nil)
	deliveryQueue := deliveries.NewMemoryQueue(nil)
	decisionStore := classifier.NewMemoryDecisionStore()
	activityFeed := activity.New(50)
	floodGate, err := flood.NewMemory(cfg.Classifier.FloodMaxMessages, cfg.Classifier.FloodWindow, nil)
	if err != nil {
		t.Fatal(err)
	}
	pipe, err := New(Dependencies{
		Config:          &cfg,
		Activity:        activityFeed,
		Ingress:         ingress,
		Transport:       transport,
		Observations:    observer.NewMemoryStore(cfg.Retention.Messages, nil),
		Sessions:        sessions.NewMemoryStore(nil),
		Jobs:            jobQueue,
		Decisions:       decisionStore,
		Deliveries:      deliveryQueue,
		ContextPacks:    builder,
		Classifier:      classificationService,
		FloodProtection: floodGate,
		Renderer:        deliveries.NewRenderer(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := pipe.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pipe.StartIngress(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := pipe.Stop(stopCtx); err != nil {
			t.Errorf("stop pipeline: %v", err)
		}
	})
	return testSystem{pipe, ingress, transport, jobQueue, deliveryQueue, decisionStore, activityFeed}
}

func envelope(eventID, channelID, messageTS, text string) types.SlackEnvelope {
	now := time.Now().UTC()
	return types.SlackEnvelope{
		OrganizationID: "org-test", EnvelopeID: "env-" + eventID, EventID: eventID,
		TeamID: "team-test", ChannelID: channelID, MessageTS: messageTS,
		UserID: "user-test", Kind: types.SlackEventMessage, Text: text,
		EventTime: now, ReceivedAt: now,
	}
}

func TestMentionRunsDurableStubJobAndDeliveryExactlyOnce(t *testing.T) {
	system := newTestSystem(t)
	message := envelope("mention-1", "product", "100.001", "<@tos-tag> summarize this")
	message.IsMention = true
	message.OriginTag = "slack_app_mention"

	ack, err := system.ingress.Inject(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Duplicate {
		t.Fatal("first envelope was marked duplicate")
	}
	waitFor(t, func() bool { return len(system.transport.Requests()) == 1 })
	if reactions := system.transport.ReactionRequests(); len(reactions) != 1 || reactions[0].Emoji == "" || reactions[0].ChannelID != message.ChannelID || reactions[0].MessageTS != message.MessageTS {
		t.Fatalf("admitted answer work did not immediately acknowledge the source message with a classifier reaction: %#v", reactions)
	}
	if starts := system.transport.ProgressStarts(); len(starts) != 1 || starts[0].ChannelID != message.ChannelID || starts[0].ThreadTS != message.MessageTS || starts[0].Step.Status != types.SlackProgressInProgress {
		t.Fatalf("Thinking Steps starts = %#v", starts)
	}
	activityRecords := system.activity.Snapshot(message.OrganizationID, 10)
	if len(activityRecords) != 1 || activityRecords[0].Category != "classifier" || activityRecords[0].Message != message.Text || activityRecords[0].Details["outcome"] != string(types.OutcomeReplyInThread) {
		t.Fatalf("classification activity = %#v", activityRecords)
	}

	jobList, err := system.jobs.List(context.Background())
	if err != nil || len(jobList) != 1 || jobList[0].State != jobs.StateSucceeded {
		t.Fatalf("jobs = %#v, err = %v", jobList, err)
	}
	deliveryList, err := system.deliveries.List(context.Background())
	if err != nil || len(deliveryList) != 1 || deliveryList[0].Status != deliveries.StatusDelivered {
		t.Fatalf("deliveries = %#v, err = %v", deliveryList, err)
	}
	request := system.transport.Requests()[0]
	if request.Destination.ChannelID != message.ChannelID || request.Destination.ThreadTS != message.MessageTS || request.Destination.StreamTS == "" {
		t.Fatalf("destination was not server-derived from the source: %#v", request.Destination)
	}
	if _, err := deliveries.NewRenderer().Render(request.Result); err != nil {
		t.Fatalf("delivered result violates Slack contract: %v", err)
	}

	duplicateAck, err := system.ingress.Inject(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicateAck.Duplicate {
		t.Fatal("duplicate envelope was not acknowledged as duplicate")
	}
	time.Sleep(10 * time.Millisecond)
	if got := len(system.transport.Requests()); got != 1 {
		t.Fatalf("duplicate caused %d deliveries", got)
	}
	if got := len(system.transport.ReactionRequests()); got != 1 {
		t.Fatalf("duplicate changed reaction count to %d", got)
	}
	if got := len(system.transport.ProgressStarts()); got != 1 {
		t.Fatalf("duplicate caused %d progress streams", got)
	}
}

func TestClassifierFloodProtectionDropsBeforeProviderOrAgent(t *testing.T) {
	system := newTestSystem(t)
	gate, err := flood.NewMemory(1, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	system.pipeline.deps.FloodProtection = gate
	var providerCalls atomic.Int64
	service, err := classifier.New(classifierFunc(func(context.Context, classifier.Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
		providerCalls.Add(1)
		return types.ClassificationDecision{
			Outcome: types.OutcomeSilent, Confidence: 1,
			ReasonCodes: []string{"test.silent"}, DisclosureClass: types.DisclosureDestinationSafe,
		}, nil
	}), false, .9, .98)
	if err != nil {
		t.Fatal(err)
	}
	system.pipeline.deps.Classifier = service

	for index, text := range []string{"What changed?", "Can you check again?"} {
		message := envelope(fmt.Sprintf("flood-%d", index), "tos-tag", fmt.Sprintf("200.%03d", index), text)
		if _, injectErr := system.ingress.Inject(context.Background(), message); injectErr != nil {
			t.Fatal(injectErr)
		}
	}
	waitFor(t, func() bool {
		decisions, listErr := system.decisions.List(context.Background())
		return listErr == nil && len(decisions) == 2
	})
	if got := providerCalls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
	decisions, err := system.decisions.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if decisions[1].Result.Effective.Outcome != types.OutcomeSilent || decisions[1].Result.Effective.ReasonCodes[0] != "safety.classifier_flood_limit" {
		t.Fatalf("flood decision = %#v", decisions[1])
	}
	if decisions[1].ContextPackRevisionID != "" {
		t.Fatalf("flooded message unexpectedly built context pack %q", decisions[1].ContextPackRevisionID)
	}
	if listed, listErr := system.jobs.List(context.Background()); listErr != nil || len(listed) != 0 {
		t.Fatalf("flooded messages created jobs: %#v err=%v", listed, listErr)
	}
	if len(system.transport.Requests()) != 0 || len(system.transport.ReactionRequests()) != 0 {
		t.Fatalf("flooded messages produced Slack output: requests=%#v reactions=%#v", system.transport.Requests(), system.transport.ReactionRequests())
	}
}

func TestExplicitInvocationAndActiveThreadBypassOnlyAdmissionCooldown(t *testing.T) {
	now := time.Now().UTC()
	clock := now
	base := orgconfig.ChannelPolicy{
		OrganizationID: "org-test", TeamID: "team-test", ChannelID: "development",
		Enrolled: true, ParticipationMode: types.ModeAssist, Cooldown: time.Minute,
		MaxResponsesPerHour: 10, MaxConcurrentJobs: 2,
		MembershipRevision: "member/v1", MembershipRefreshedAt: now,
	}

	ambient := admissionPolicyForTarget(base, classifier.Target{})
	if ambient.Cooldown != time.Minute {
		t.Fatalf("ambient cooldown = %s, want 1m", ambient.Cooldown)
	}
	controller := admission.NewMemory(func() time.Time { return clock })
	firstID, err := controller.Admit(context.Background(), ambient)
	if err != nil {
		t.Fatal(err)
	}
	controller.Complete(context.Background(), firstID)
	if _, err := controller.Admit(context.Background(), ambient); !errors.Is(err, admission.ErrCooldown) {
		t.Fatalf("ambient retry error = %v, want cooldown", err)
	}
	for name, target := range map[string]classifier.Target{
		"direct mention": {Envelope: types.SlackEnvelope{IsMention: true}},
		"active thread":  {ActiveThread: true},
	} {
		t.Run(name, func(t *testing.T) {
			policy := admissionPolicyForTarget(base, target)
			if policy.Cooldown != 0 {
				t.Fatalf("cooldown = %s, want zero", policy.Cooldown)
			}
			if policy.MaxResponsesPerHour != base.MaxResponsesPerHour || policy.MaxConcurrentJobs != base.MaxConcurrentJobs {
				t.Fatalf("safety bounds changed: %#v", policy)
			}
			id, err := controller.Admit(context.Background(), policy)
			if err != nil {
				t.Fatalf("explicit invocation was cooldown-blocked: %v", err)
			}
			controller.Complete(context.Background(), id)
		})
	}
	if base.Cooldown != time.Minute {
		t.Fatalf("source policy was mutated: %#v", base)
	}
}

func TestProactiveBackgroundWorkImmediatelyAcknowledgesSourceMessage(t *testing.T) {
	system := newTestSystem(t)
	classificationService, err := classifier.New(classifierFunc(func(context.Context, classifier.Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
		return types.ClassificationDecision{
			Outcome:              types.OutcomeStartBackgroundJob,
			Confidence:           .99,
			ReasonCodes:          []string{"test.proactive_incident"},
			ResponseIntent:       "investigate the reported failure",
			DisclosureClass:      types.DisclosureDestinationSafe,
			RequiresFullAgent:    true,
			Reaction:             "rotating_light",
			AgentModelStrength:   "standard",
			AgentReasoningEffort: "medium",
		}, nil
	}), false, config.DefaultConfiguration.Classifier.AssistThreshold, config.DefaultConfiguration.Classifier.ChannelReplyThreshold)
	if err != nil {
		t.Fatal(err)
	}
	system.pipeline.deps.Classifier = classificationService
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	_, _ = scopes.PutChannel(context.Background(), orgconfig.ChannelPolicy{
		OrganizationID: "org-test", TeamID: "team-test", ChannelID: "tos-tag", Enrolled: true,
		ParticipationMode: types.ModeProactive, MaxResponsesPerHour: 10, MaxConcurrentJobs: 1,
		MembershipRevision: "m1", MembershipRefreshedAt: time.Now().UTC(),
	})
	system.pipeline.deps.Scopes = scopes

	message := envelope("proactive-incident-1", "tos-tag", "101.001", "The lobby display has failed three times and needs attention.")
	if _, err := system.ingress.Inject(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(system.transport.ProgressStarts()) == 1 })
	if reactions := system.transport.ReactionRequests(); len(reactions) != 1 || reactions[0].Emoji != "rotating_light" || reactions[0].ChannelID != message.ChannelID || reactions[0].MessageTS != message.MessageTS {
		t.Fatalf("proactive background work did not immediately acknowledge its source message: %#v", reactions)
	}
}

func TestGroupDMEnvelopesAreIgnoredEntirely(t *testing.T) {
	system := newTestSystem(t)
	message := envelope("mpdm-1", "mpdm-group", "500.001", "<@tos-tag> can you help?")
	message.ChannelKind = types.SlackChannelKindGroupDM
	message.Restricted = true
	message.IsMention = true

	if _, err := system.ingress.Inject(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if len(system.transport.Requests()) != 0 || len(system.transport.ReactionRequests()) != 0 {
		t.Fatal("group DM produced Slack output")
	}
	if records, _ := system.decisions.List(context.Background()); len(records) != 0 {
		t.Fatalf("group DM was classified: %#v", records)
	}
	if records := system.activity.Snapshot(message.OrganizationID, 10); len(records) != 0 {
		t.Fatalf("group DM produced classification activity: %#v", records)
	}
}

func TestFullAgentHarnessAttachesMeasuredExecutionFooter(t *testing.T) {
	cfg := config.DefaultConfiguration
	cfg.Jobs.Lease = time.Minute
	p := &Pipeline{deps: Dependencies{Config: &cfg, Harness: &footerHarness{}}}
	result, err := p.runHarness(context.Background(), jobs.Job{
		ID: "job-footer", OrganizationID: "org-test", WorkspaceID: "team-test", ChannelID: "tos-tag",
		Input: `{"request":"investigate"}`, ResolvedModel: types.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5.6-luna", Variant: "max"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentFooter == nil || result.AgentFooter.ModelID != "gpt-5.6-luna" || result.AgentFooter.ReasoningEffort != "max" || result.AgentFooter.TotalTokens != 20_400 || result.AgentFooter.DurationMS < 0 {
		t.Fatalf("agent footer = %#v", result.AgentFooter)
	}
	payloads, err := deliveries.NewRenderer().Render(result)
	if err != nil || len(payloads) != 1 || len(payloads[0].Blocks) != 2 || payloads[0].Blocks[1]["type"] != "context" {
		t.Fatalf("rendered agent footer = %#v err=%v", payloads, err)
	}
}

func TestFullAgentHarnessDeduplicatesRepeatedToolProgress(t *testing.T) {
	cfg := config.DefaultConfiguration
	cfg.Jobs.Lease = time.Minute
	transport := slack.NewStubDelivery()
	p := &Pipeline{deps: Dependencies{Config: &cfg, Harness: &duplicateToolProgressHarness{}, Transport: transport}}
	_, err := p.runHarness(context.Background(), jobs.Job{
		ID: "job-progress", OrganizationID: "org-test", WorkspaceID: "team-test", ChannelID: "tos-tag", ProgressMessageTS: "progress.1",
		Input: `{"request":"inspect"}`, ResolvedModel: types.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5.6-luna", Variant: "medium"},
	})
	if err != nil {
		t.Fatal(err)
	}
	updates := transport.ProgressUpdates()
	if len(updates) != 2 || updates[0].Step.ID != "agent-work" || updates[1].Step.ID != "agent-work" {
		t.Fatalf("progress updates = %#v", updates)
	}
}

func TestFullAgentHarnessShowsSkillAndEveryToolLifecycle(t *testing.T) {
	cfg := config.DefaultConfiguration
	cfg.Jobs.Lease = time.Minute
	transport := slack.NewStubDelivery()
	p := &Pipeline{deps: Dependencies{Config: &cfg, Harness: &transparentProgressHarness{}, Transport: transport}}
	result, err := p.runHarness(context.Background(), jobs.Job{
		ID: "job-transparent-progress", OrganizationID: "org-test", WorkspaceID: "team-test", ChannelID: "tos-tag", ProgressMessageTS: "progress.1",
		Input: `{"request":"research"}`, ResolvedModel: types.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5.6-luna", Variant: "medium"},
	})
	if err != nil {
		t.Fatal(err)
	}
	updates := transport.ProgressUpdates()
	if len(updates) != 6 {
		t.Fatalf("progress updates = %#v", updates)
	}
	want := []struct {
		status types.SlackProgressStatus
		title  string
	}{
		{types.SlackProgressInProgress, "Using product knowledge skill"},
		{types.SlackProgressInProgress, "Reading Agent Wiki"},
		{types.SlackProgressInProgress, "Read Agent Wiki"},
		{types.SlackProgressInProgress, "Searching the web"},
		{types.SlackProgressInProgress, "Web search failed"},
		{types.SlackProgressComplete, "Completed agent work"},
	}
	for index, expected := range want {
		if updates[index].Step.Status != expected.status || updates[index].Step.Title != expected.title {
			t.Fatalf("update %d = %#v, want %q/%q", index, updates[index].Step, expected.status, expected.title)
		}
	}
	for _, update := range updates {
		if update.Step.ID != "agent-work" {
			t.Fatalf("progress did not reuse the single transient card: %#v", updates)
		}
	}
	for index, update := range updates[:len(updates)-1] {
		if update.Step.Status == types.SlackProgressComplete || update.Step.Status == types.SlackProgressError {
			t.Fatalf("intermediate progress update %d prematurely closed the stream: %#v", index, update.Step)
		}
	}
	if result.AgentFooter == nil || len(result.AgentFooter.Activities) != 1 || result.AgentFooter.Activities[0] != "wiki" {
		t.Fatalf("footer activities = %#v", result.AgentFooter)
	}
}

func TestFullAgentHarnessCollapsesSelfCorrectedWikiValidationAttempt(t *testing.T) {
	cfg := config.DefaultConfiguration
	cfg.Jobs.Lease = time.Minute
	transport := slack.NewStubDelivery()
	auditLog, err := audit.NewMemoryAppender([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{deps: Dependencies{Config: &cfg, Harness: &wikiValidationRetryHarness{}, Transport: transport, Audit: auditLog}}
	_, err = p.runHarness(context.Background(), jobs.Job{
		ID: "job-wiki-validation", OrganizationID: "org-test", WorkspaceID: "team-test", ChannelID: "tos-tag", ProgressMessageTS: "progress.1",
		Input: `{"request":"publish"}`, ResolvedModel: types.ResolvedModel{ProviderID: "openai", ModelID: "gpt-5.6-luna", Variant: "medium"},
	})
	if err != nil {
		t.Fatal(err)
	}
	updates := transport.ProgressUpdates()
	if len(updates) != 3 || updates[0].Step.Title != "Updating Agent Wiki" || updates[1].Step.Title != "Updated Agent Wiki" || updates[2].Step.Title != "Completed agent work" {
		t.Fatalf("collapsed progress updates = %#v", updates)
	}
	for _, update := range updates {
		if update.Step.Title == "Agent Wiki update failed" || update.Step.Title == "Retrying agent work" {
			t.Fatalf("self-corrected validation leaked as a separate failure: %#v", updates)
		}
	}
	receipts := auditLog.List("org-test")
	if len(receipts) != 1 || receipts[0].Type != "tool.validation.failed" || receipts[0].Metadata["validation_code"] != "wiki.body.required" {
		t.Fatalf("validation receipts = %#v", receipts)
	}
	encoded, _ := json.Marshal(receipts[0])
	if strings.Contains(string(encoded), "publish") || strings.Contains(string(encoded), "artifacts/") {
		t.Fatalf("validation receipt leaked request content: %s", encoded)
	}
}

func TestProductSourceProgressIdentifiesCorporateWebsite(t *testing.T) {
	tests := []struct {
		resourceAction string
		wantTitle      string
	}{
		{resourceAction: "corporate-full", wantTitle: "Read TelemetryOS corporate website"},
		{resourceAction: "docs-index", wantTitle: "Read TelemetryOS documentation index"},
		{resourceAction: "docs-page", wantTitle: "Read TelemetryOS documentation"},
	}
	for _, test := range tests {
		t.Run(test.resourceAction, func(t *testing.T) {
			step := safeToolProgressStep("telemetryos.product-docs", "read", test.resourceAction)
			if step.Title != test.wantTitle {
				t.Fatalf("title = %q, want %q", step.Title, test.wantTitle)
			}
		})
	}
}

func TestClassificationActivityHidesRestrictedMessageContent(t *testing.T) {
	activityFeed := activity.New(10)
	pipe := &Pipeline{deps: Dependencies{Activity: activityFeed}}
	message := envelope("restricted-activity", "private-channel", "101.001", "sensitive private message")
	message.Restricted = true
	pipe.publishClassificationActivity(message, classifier.DecisionRecord{
		ID: "decision", OrganizationID: message.OrganizationID, ObservationID: "observation",
		Result: classifier.Result{Effective: types.ClassificationDecision{Outcome: types.OutcomeSilent, Confidence: .99, DisclosureClass: types.DisclosureDestinationSafe}},
	})
	records := activityFeed.Snapshot(message.OrganizationID, 10)
	if len(records) != 1 || strings.Contains(records[0].Message, "sensitive") || records[0].Details["restricted"] != true {
		t.Fatalf("restricted activity = %#v", records)
	}
}

func TestBriefMentionDeliversInChannel(t *testing.T) {
	system := newTestSystem(t)
	message := envelope("mention-brief", "product", "100.002", "<@tos-tag> what is 2 + 2?")
	message.IsMention = true
	message.OriginTag = "slack_app_mention"

	if _, err := system.ingress.Inject(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(system.transport.Requests()) == 1 })

	requests := system.transport.Requests()
	if requests[0].Destination.ChannelID != message.ChannelID || requests[0].Destination.ThreadTS != "" {
		t.Fatalf("brief answer was not delivered in-channel: %#v", requests[0].Destination)
	}
	if starts := system.transport.ProgressStarts(); len(starts) != 0 {
		t.Fatalf("brief in-channel answer incorrectly started Thinking Steps: %#v", starts)
	}
	jobList, err := system.jobs.List(context.Background())
	if err != nil || len(jobList) != 1 || !jobList[0].ReplyInChannel {
		t.Fatalf("brief-answer job = %#v, err = %v", jobList, err)
	}
}

func TestMentionedThanksCreatesDirectDurableDeliveryWithoutAgentJob(t *testing.T) {
	system := newTestSystem(t)
	message := envelope("mention-thanks", "product", "100.003", "<@tos-tag> thanks!")
	message.IsMention = true
	message.OriginTag = "slack_app_mention"

	if _, err := system.ingress.Inject(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(system.transport.Requests()) == 1 })

	requests := system.transport.Requests()
	if requests[0].Destination.ChannelID != message.ChannelID || requests[0].Destination.ThreadTS != "" || requests[0].Result.Segments[0].Text != "You're welcome!" {
		t.Fatalf("direct social delivery = %#v", requests[0])
	}
	if requests[0].Result.AgentFooter != nil {
		t.Fatalf("classifier-only reply gained full-agent metadata: %#v", requests[0].Result.AgentFooter)
	}
	jobList, err := system.jobs.List(context.Background())
	if err != nil || len(jobList) != 0 {
		t.Fatalf("social reply launched an agent job: %#v, err=%v", jobList, err)
	}
	deliveryList, err := system.deliveries.List(context.Background())
	if err != nil || len(deliveryList) != 1 || deliveryList[0].DecisionID == "" || deliveryList[0].JobID != "" || deliveryList[0].Status != deliveries.StatusDelivered {
		t.Fatalf("direct decision delivery = %#v, err=%v", deliveryList, err)
	}
	if reactions := system.transport.ReactionRequests(); len(reactions) != 1 || reactions[0].Emoji != "white_check_mark" {
		t.Fatalf("social reply reactions = %#v", reactions)
	}
}

func TestSlackCallbackProvenanceIsNotAWorkflowLoop(t *testing.T) {
	for _, originTag := range []string{"", "slack_message", "slack_app_mention"} {
		if isWorkflowLoopOrigin(originTag) {
			t.Fatalf("normal Slack provenance %q was classified as a workflow loop", originTag)
		}
	}
	if !isWorkflowLoopOrigin("tos_tag_delivery") {
		t.Fatal("explicit generated origin was not classified as a workflow loop")
	}
}

func TestJobWorkersRunUpToConfiguredConcurrency(t *testing.T) {
	cfg := config.DefaultConfiguration
	cfg.Jobs.Poll = time.Millisecond
	cfg.Jobs.Lease = time.Second
	cfg.Jobs.WorkerConcurrency = 3
	queue := jobs.NewMemoryQueue(nil)
	for index := 0; index < cfg.Jobs.WorkerConcurrency; index++ {
		_, _, err := queue.Enqueue(context.Background(), jobs.Spec{
			OrganizationID: "org-test", WorkspaceID: "team-test", ChannelID: "channel-test", RootThreadTS: "100.1",
			SessionID: types.SessionID(fmt.Sprintf("session-test-%d", index)), Generation: 1, IdempotencyKey: fmt.Sprintf("concurrency/%d", index),
			Kind: "agent_response", Input: "inspect", MaxAttempts: 1, ExpiresAt: time.Now().UTC().Add(time.Minute),
			ResolvedModel: types.ResolvedModel{ProviderID: "openai", ModelID: "test", ProfileID: "test", Variant: "medium"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	blocking := &blockingHarness{started: make(chan struct{}, cfg.Jobs.WorkerConcurrency), release: make(chan struct{})}
	pipe := &Pipeline{deps: Dependencies{Config: &cfg, Logger: blackbox.New(), Jobs: queue, Deliveries: deliveries.NewMemoryQueue(nil), Renderer: deliveries.NewRenderer(), Harness: blocking}}
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	var releaseOnce sync.Once
	defer func() {
		releaseOnce.Do(func() { close(blocking.release) })
		cancel()
		workers.Wait()
	}()
	for worker := 1; worker <= cfg.Jobs.WorkerConcurrency; worker++ {
		workers.Add(1)
		go func(id int) {
			defer workers.Done()
			pipe.runJobs(ctx, types.WorkerID(fmt.Sprintf("test-worker-%d", id)))
		}(worker)
	}
	deadline := time.After(time.Second)
	for started := 0; started < cfg.Jobs.WorkerConcurrency; started++ {
		select {
		case <-blocking.started:
		case <-deadline:
			t.Fatalf("only %d of %d jobs started concurrently", started, cfg.Jobs.WorkerConcurrency)
		}
	}
	values, err := queue.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	owners := make(map[types.WorkerID]struct{})
	for _, job := range values {
		if job.State != jobs.StateRunning {
			t.Fatalf("job %s state = %s, want running", job.ID, job.State)
		}
		owners[job.Lease.Owner] = struct{}{}
	}
	if len(owners) != cfg.Jobs.WorkerConcurrency {
		t.Fatalf("worker owners = %v", owners)
	}
	releaseOnce.Do(func() { close(blocking.release) })
}

func TestReconcileJobsReleasesExpiredWorkerAdmission(t *testing.T) {
	now := time.Now().UTC()
	queue := jobs.NewMemoryQueue(func() time.Time { return now })
	job, _, err := queue.Enqueue(context.Background(), jobs.Spec{
		OrganizationID: "org-test", WorkspaceID: "team-test", ChannelID: "channel-test", RootThreadTS: "100.1",
		SessionID: "session-test", Generation: 1, IdempotencyKey: "message/test", Kind: "agent_response",
		Input: "test", MaxAttempts: 1, AdmissionReservationID: "admit-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = queue.Claim(context.Background(), "worker-test", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = queue.Transition(context.Background(), job.ID, job.Lease.Token, jobs.StateRunning, nil); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if _, err = queue.Claim(context.Background(), "worker-test", time.Second); !errors.Is(err, jobs.ErrNoRunnableJob) {
		t.Fatalf("expired running claim err = %v", err)
	}

	admissions := &recordingAdmissions{}
	pipe := &Pipeline{deps: Dependencies{Jobs: queue, Admissions: admissions}}
	pipe.reconcileJobs(context.Background())
	if len(admissions.completed) != 1 || admissions.completed[0] != "admit-test" {
		t.Fatalf("completed admissions = %v", admissions.completed)
	}
}

func TestCancelledWorkerPersistsDurableRetryOutsideCancelledContext(t *testing.T) {
	cfg := config.DefaultConfiguration
	cfg.Jobs.Poll = time.Millisecond
	cfg.Jobs.Lease = time.Second
	queue := jobs.NewMemoryQueue(nil)
	job, _, err := queue.Enqueue(context.Background(), jobs.Spec{
		OrganizationID: "org-test", WorkspaceID: "team-test", ChannelID: "channel-test", RootThreadTS: "100.1",
		SessionID: "session-test", Generation: 1, IdempotencyKey: "cancelled-worker-retry", Kind: "agent_response",
		Input: "test", MaxAttempts: 2, ExpiresAt: time.Now().UTC().Add(time.Minute),
		ResolvedModel: types.ResolvedModel{ProviderID: "openai", ModelID: "test", ProfileID: "test", Variant: "medium"},
	})
	if err != nil {
		t.Fatal(err)
	}
	recordingQueue := &recordingRequeueQueue{Queue: queue}
	pipe := &Pipeline{deps: Dependencies{Config: &cfg, Logger: blackbox.New(), Jobs: recordingQueue, Harness: &contextFailureHarness{}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !pipe.processOneJob(ctx, "worker-test") {
		t.Fatal("cancelled worker did not claim the queued job")
	}
	updated, err := queue.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recordingQueue.contextErr != nil || updated.State != jobs.StateRetryWait {
		t.Fatalf("durable retry used cancelled context or was not persisted: context_err=%v job=%#v", recordingQueue.contextErr, updated)
	}
}

func TestReconcileJobsKeepsWaitingApprovalAndReleasesConcurrencyOnTransientLookupFailure(t *testing.T) {
	ctx := context.Background()
	queue := jobs.NewMemoryQueue(nil)
	job, _, _ := queue.Enqueue(ctx, jobs.Spec{OrganizationID: "org-test", WorkspaceID: "team-test", ChannelID: "channel-test", RootThreadTS: "1", SessionID: "session", Generation: 1, IdempotencyKey: "approval-transient", Kind: "agent_response", MaxAttempts: 2, AdmissionReservationID: "admit-transient"})
	job, _ = queue.Claim(ctx, "worker", time.Minute)
	job, _ = queue.Transition(ctx, job.ID, job.Lease.Token, jobs.StateRunning, nil)
	job, _ = queue.SuspendForApproval(ctx, job.ID, job.Lease.Token, "approval-transient")
	admissions := &recordingAdmissions{}
	pipe := &Pipeline{deps: Dependencies{Logger: blackbox.New(), Jobs: queue, Approvals: failingApprovalRepository{err: errors.New("temporary Mongo timeout")}, Admissions: admissions}}
	pipe.reconcileJobs(ctx)
	unchanged, _ := queue.Get(ctx, job.ID)
	if unchanged.State != jobs.StateWaitingApproval || len(admissions.completed) != 1 || admissions.completed[0] != "admit-transient" {
		t.Fatalf("transient lookup failure cancelled waiting job: %#v completed=%v", unchanged, admissions.completed)
	}
}

func TestExpiredApprovalUpdatesDeliveredSlackCardInPlace(t *testing.T) {
	now := time.Now().UTC()
	deliveryQueue := deliveries.NewMemoryQueue(func() time.Time { return now })
	job := jobs.Job{ID: "job-test", OrganizationID: "org-test", WorkspaceID: "team-test", ChannelID: "channel-test", RootThreadTS: "100.1"}
	approval := approvals.Approval{
		ID: "approval-test", OrganizationID: job.OrganizationID, ActionHash: "sha256:abc",
		Action:    approvals.Action{OrganizationID: job.OrganizationID, JobID: string(job.ID), WorkspaceID: job.WorkspaceID, ChannelID: job.ChannelID, ThreadTS: job.RootThreadTS, ToolID: "tos-tag-triggers", OperationID: "put", Risk: "write", Destination: "team-test/channel-test", Arguments: map[string]any{"enabled": false}},
		ExpiresAt: now.Add(-time.Second), CleanupAt: now.Add(time.Hour),
	}
	record, _, err := deliveryQueue.Enqueue(context.Background(), deliveries.Spec{
		OrganizationID: job.OrganizationID, JobID: job.ID, IdempotencyKey: "approval/" + approval.ID + "/requested",
		Destination: types.SlackDestination{TeamID: job.WorkspaceID, ChannelID: job.ChannelID, ThreadTS: job.RootThreadTS},
		Result:      types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentApproval}}}, MaxAttempts: 3, ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err = deliveryQueue.Claim(context.Background(), "delivery-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = deliveryQueue.Complete(context.Background(), record.ID, record.Lease.Token, types.SlackDeliveryResult{MessageTS: "200.2", DeliveredAt: now}); err != nil {
		t.Fatal(err)
	}

	pipe := &Pipeline{deps: Dependencies{Logger: blackbox.New(), Deliveries: deliveryQueue}}
	pipe.enqueueExpiredApprovalUpdate(context.Background(), job, approval, now)
	records, err := deliveryQueue.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("delivery count = %d, want 2", len(records))
	}
	var update deliveries.Record
	for _, candidate := range records {
		if candidate.IdempotencyKey == "approval/"+approval.ID+"/expired" {
			update = candidate
			break
		}
	}
	segment := update.Result.Segments[0]
	if update.Destination.UpdateTS != "200.2" || segment.Approval == nil || segment.Approval.Status != "expired" || segment.Approval.ResolvedAt.IsZero() {
		t.Fatalf("expired approval update = %#v", update)
	}
}

func TestReconcileJobsEnqueuesSlackNativeNoticeWhenInteractiveRetriesExhaust(t *testing.T) {
	now := time.Now().UTC()
	queue := jobs.NewMemoryQueue(func() time.Time { return now })
	deliveryQueue := deliveries.NewMemoryQueue(func() time.Time { return now })
	job, _, err := queue.Enqueue(context.Background(), jobs.Spec{
		OrganizationID: "org-test", WorkspaceID: "team-test", ChannelID: "channel-test", RootThreadTS: "100.1",
		SessionID: "session-test", Generation: 1, IdempotencyKey: "message/failure", Kind: "agent_response",
		Input: "test", MaxAttempts: 1, AdmissionReservationID: "admit-test", ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = queue.Claim(context.Background(), "worker-test", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	job, err = queue.Transition(context.Background(), job.ID, job.Lease.Token, jobs.StateRunning, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = queue.Requeue(context.Background(), job.ID, job.Lease.Token, "provider failed", 0); err != nil {
		t.Fatal(err)
	}

	admissions := &recordingAdmissions{}
	pipe := &Pipeline{deps: Dependencies{Jobs: queue, Deliveries: deliveryQueue, Admissions: admissions}}
	pipe.reconcileJobs(context.Background())

	failed, _ := queue.Get(context.Background(), job.ID)
	if failed.State != jobs.StateFailed {
		t.Fatalf("job state = %q, want failed", failed.State)
	}
	records, _ := deliveryQueue.List(context.Background())
	if len(records) != 1 || records[0].Destination.ThreadTS != "100.1" || records[0].Result.Segments[0].Kind != types.SlackSegmentNotice || records[0].Result.Segments[0].Notice.Tone != "error" {
		t.Fatalf("terminal failure notice = %#v", records)
	}
	if _, err := deliveries.NewRenderer().Render(records[0].Result); err != nil {
		t.Fatalf("terminal failure notice did not render: %v", err)
	}
	if len(admissions.completed) != 1 || admissions.completed[0] != "admit-test" {
		t.Fatalf("completed admissions = %v", admissions.completed)
	}
}

func TestDeliveryDestinationHonorsReplyOutcome(t *testing.T) {
	job := jobs.Job{RootThreadTS: "100.1"}
	if got := deliveryThreadTS(job); got != "100.1" {
		t.Fatalf("thread reply destination = %q", got)
	}
	job.ReplyInChannel = true
	if got := deliveryThreadTS(job); got != "" {
		t.Fatalf("channel reply destination = %q", got)
	}
}

func TestSlackOutputChannelAllowlistFailsClosedOutsideConfiguredDestination(t *testing.T) {
	cfg := config.DefaultConfiguration
	cfg.Slack.OutputChannelIDs = []string{"tos-tag"}
	if !slackOutputChannelAllowed(&cfg, "tos-tag") {
		t.Fatal("configured output channel was denied")
	}
	if slackOutputChannelAllowed(&cfg, "private-channel") {
		t.Fatal("unconfigured output channel was allowed")
	}
	cfg.Slack.OutputChannelIDs = nil
	if !slackOutputChannelAllowed(&cfg, "policy-controlled") {
		t.Fatal("empty output allowlist did not defer to channel policy")
	}
}

func TestAmbientCrossChannelIncidentIsPredictedButShadowed(t *testing.T) {
	system := newTestSystem(t)
	alert := envelope("alert-1", "alerts", "200.001", "Production outage incident: API is down")
	if _, err := system.ingress.Inject(context.Background(), alert); err != nil {
		t.Fatal(err)
	}
	question := envelope("support-1", "support", "200.002", "is the system down?")
	if _, err := system.ingress.Inject(context.Background(), question); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { records, _ := system.decisions.List(context.Background()); return len(records) == 2 })

	records, err := system.decisions.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("decisions = %d, want 2", len(records))
	}
	decision := records[1].Result
	if decision.Predicted.Outcome != types.OutcomeReplyInThread || !decision.Shadowed || decision.Effective.Outcome != types.OutcomeSilent {
		t.Fatalf("unexpected cross-channel shadow result: %#v", decision)
	}
	if len(decision.Predicted.ReleasableEvidenceIDs) != 1 || len(decision.Predicted.RestrictedSignalIDs) != 0 {
		t.Fatalf("unexpected evidence admission: %#v", decision.Predicted)
	}
	if len(system.transport.Requests()) != 0 {
		t.Fatal("shadow decision produced a Slack delivery")
	}
	if len(system.transport.ReactionRequests()) != 0 {
		t.Fatal("shadow decision produced a Slack reaction")
	}
}

func TestRestrictedCrossChannelMessageIsExcludedFromAmbientContext(t *testing.T) {
	system := newTestSystem(t)
	alert := envelope("restricted-alert-1", "private-alerts", "300.001", "Confidential outage incident is down")
	alert.Restricted = true
	if _, err := system.ingress.Inject(context.Background(), alert); err != nil {
		t.Fatal(err)
	}
	question := envelope("support-2", "support", "300.002", "is the system down?")
	if _, err := system.ingress.Inject(context.Background(), question); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { records, _ := system.decisions.List(context.Background()); return len(records) == 2 })

	records, err := system.decisions.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	decision := records[len(records)-1].Result
	if decision.Effective.Outcome != types.OutcomeSilent {
		t.Fatalf("shadow decision produced an effective reply: %#v", decision)
	}
	if len(decision.Predicted.RestrictedSignalIDs) != 0 || len(decision.Predicted.ReleasableEvidenceIDs) != 0 {
		t.Fatalf("restricted cross-channel evidence entered the decision: %#v", decision.Predicted)
	}
}

func TestContextSyncAutoEnrollsObserveOnlyAndPreservesExistingOutputPolicy(t *testing.T) {
	cfg := contextSyncConfig()
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test", EnrollmentMode: "all_observable_channels"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	now := time.Now().UTC()
	_, err := scopes.PutChannel(context.Background(), orgconfig.ChannelPolicy{
		OrganizationID: "org-test", TeamID: "team-test", ChannelID: "tos-tag", Enrolled: true,
		ParticipationMode: types.ModeProactive, Cooldown: time.Minute, MaxResponsesPerHour: 2, MaxConcurrentJobs: 2,
		DefaultModelProfile: "custom", MembershipRevision: "manual/v1", MembershipRefreshedAt: now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{deps: Dependencies{Config: &cfg, Logger: blackbox.New(), Scopes: scopes}}
	if authorized, err := p.RegisterContextChannel(context.Background(), types.SlackContextChannel{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "public", Name: "public"}); err != nil || !authorized {
		t.Fatal(err)
	}
	if authorized, err := p.RegisterContextChannel(context.Background(), types.SlackContextChannel{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "private", Name: "private", Restricted: true}); err != nil || !authorized {
		t.Fatal(err)
	}
	if authorized, err := p.RegisterContextChannel(context.Background(), types.SlackContextChannel{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "tos-tag", Name: "tos-tag"}); err != nil || !authorized {
		t.Fatal(err)
	}
	public, _ := scopes.Resolve(context.Background(), "org-test", "team-test", "public")
	private, _ := scopes.Resolve(context.Background(), "org-test", "team-test", "private")
	testChannel, _ := scopes.Resolve(context.Background(), "org-test", "team-test", "tos-tag")
	if !public.Enrolled || public.Restricted || public.ParticipationMode != types.ModeObserve {
		t.Fatalf("public discovery policy = %#v", public)
	}
	if !private.Enrolled || !private.Restricted || private.ParticipationMode != types.ModeObserve {
		t.Fatalf("private discovery policy = %#v", private)
	}
	if testChannel.ParticipationMode != types.ModeProactive || testChannel.MaxResponsesPerHour != 2 || testChannel.MaxConcurrentJobs != 2 || testChannel.DefaultModelProfile != "custom" {
		t.Fatalf("existing output policy was overwritten: %#v", testChannel)
	}
}

func TestContextSyncAutomaticallyAssistsOnlyBotJoinedChannels(t *testing.T) {
	cfg := contextSyncConfig()
	cfg.Slack.AutoAssistJoinedChannels = true
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test", EnrollmentMode: "all_observable_channels"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	p := &Pipeline{deps: Dependencies{Config: &cfg, Logger: blackbox.New(), Scopes: scopes}}
	joined := types.SlackContextChannel{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "joined", Name: "joined", IsChannel: true, BotMembershipKnown: true, BotIsMember: true}
	observed := types.SlackContextChannel{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "observed", Name: "observed", IsChannel: true, BotMembershipKnown: true}
	if authorized, err := p.RegisterContextChannel(context.Background(), joined); err != nil || !authorized {
		t.Fatalf("register joined channel authorized=%v err=%v", authorized, err)
	}
	if authorized, err := p.RegisterContextChannel(context.Background(), observed); err != nil || !authorized {
		t.Fatalf("register observed channel authorized=%v err=%v", authorized, err)
	}
	joinedPolicy, _ := scopes.Resolve(context.Background(), "org-test", "team-test", "joined")
	observedPolicy, _ := scopes.Resolve(context.Background(), "org-test", "team-test", "observed")
	if joinedPolicy.ParticipationMode != types.ModeAssist || !joinedPolicy.ParticipationManagedByMembership || !joinedPolicy.BotIsMember {
		t.Fatalf("joined channel policy = %#v", joinedPolicy)
	}
	if observedPolicy.ParticipationMode != types.ModeObserve || !observedPolicy.ParticipationManagedByMembership || observedPolicy.BotIsMember {
		t.Fatalf("unjoined channel policy = %#v", observedPolicy)
	}
	if err := p.UpdateBotMembership(context.Background(), slack.BotMembershipChange{OrganizationID: "org-test", WorkspaceID: "team-test", ChannelID: "joined", EventID: "left", Joined: false}); err != nil {
		t.Fatal(err)
	}
	joinedPolicy, _ = scopes.Resolve(context.Background(), "org-test", "team-test", "joined")
	if joinedPolicy.ParticipationMode != types.ModeObserve || joinedPolicy.BotIsMember || authorizedOutputPolicy(joinedPolicy, time.Now().UTC()) {
		t.Fatalf("departed channel retained output authority: %#v", joinedPolicy)
	}
}

func TestContextSyncPreservesOperatorManagedProactiveMode(t *testing.T) {
	cfg := contextSyncConfig()
	cfg.Slack.AutoAssistJoinedChannels = true
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test", EnrollmentMode: "all_observable_channels"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	now := time.Now().UTC()
	_, err := scopes.PutChannel(context.Background(), orgconfig.ChannelPolicy{
		OrganizationID: "org-test", TeamID: "team-test", ChannelID: "tos-tag", Name: "tos-tag",
		Enrolled: true, ParticipationMode: types.ModeProactive, MaxResponsesPerHour: 120, MaxConcurrentJobs: 8,
		BotIsMember: true, BotMembershipKnown: true, ParticipationManagedByMembership: false,
		MembershipRevision: "operator/v1", MembershipRefreshedAt: now.Add(-7 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{deps: Dependencies{Config: &cfg, Logger: blackbox.New(), Scopes: scopes}}
	joined := types.SlackContextChannel{
		OrganizationID: "org-test", TeamID: "team-test", ChannelID: "tos-tag", Name: "tos-tag",
		RestrictionKnown: true, IsChannel: true, BotMembershipKnown: true, BotIsMember: true,
	}
	if authorized, err := p.RegisterContextChannel(context.Background(), joined); err != nil || !authorized {
		t.Fatalf("register proactive channel authorized=%v err=%v", authorized, err)
	}
	policy, err := scopes.Resolve(context.Background(), "org-test", "team-test", "tos-tag")
	if err != nil {
		t.Fatal(err)
	}
	if policy.ParticipationMode != types.ModeProactive || policy.ParticipationManagedByMembership {
		t.Fatalf("operator proactive policy was overwritten: %#v", policy)
	}
}

func TestUnknownAppMentionPrivacyIsDeferredBeforePersistence(t *testing.T) {
	cfg := contextSyncConfig()
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test", EnrollmentMode: "all_observable_channels"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	observations := observer.NewMemoryStore(cfg.Retention.Messages, nil)
	pipe := &Pipeline{deps: Dependencies{Config: &cfg, Logger: blackbox.New(), Scopes: scopes, Observations: observations}}
	message := envelope("unknown-app-mention", "unknown-channel", "391.001", "<@BOT> private or public is not yet known")
	message.IsMention = true
	message.OriginTag = "slack_app_mention"
	accepted, err := pipe.HandleEnvelope(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	if !accepted.Ignored {
		t.Fatalf("unknown app mention was admitted: %#v", accepted)
	}
	if _, err := scopes.Resolve(context.Background(), "org-test", "team-test", "unknown-channel"); !errors.Is(err, orgconfig.ErrNotFound) {
		t.Fatalf("unknown app mention minted a channel policy: %v", err)
	}
	if _, err := observations.CurrentMessage(context.Background(), "org-test", "team-test", "unknown-channel", "391.001"); !errors.Is(err, observer.ErrMessageNotFound) {
		t.Fatalf("unknown app mention content was retained: %v", err)
	}
}

func TestAgentAuthoredSlackOutputBecomesResolvedContextWithoutDecision(t *testing.T) {
	cfg := contextSyncConfig()
	cfg.Slack.BotUserID = "U-tag"
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test", EnrollmentMode: "all_observable_channels"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	_, err := scopes.PutChannel(context.Background(), orgconfig.ChannelPolicy{
		OrganizationID: "org-test", TeamID: "team-test", ChannelID: "tos-tag", Enrolled: true,
		ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 6, MaxConcurrentJobs: 2,
		MembershipRevision: "member/v1", MembershipRefreshedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	observations := observer.NewMemoryStore(cfg.Retention.Messages, nil)
	p := &Pipeline{deps: Dependencies{Config: &cfg, Logger: blackbox.New(), Scopes: scopes, Observations: observations}}

	message := envelope("message/team-test/tos-tag/392.001", "tos-tag", "392.001", "Tag delivery")
	message.UserID = "U-tag"
	message.BotID = "B-tag"
	accepted, err := p.HandleEnvelope(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	if !accepted.Ignored || !accepted.ResolvedContext || accepted.Duplicate {
		t.Fatalf("self-authored result = %#v", accepted)
	}
	current, err := observations.CurrentMessage(context.Background(), "org-test", "team-test", "tos-tag", "392.001")
	if err != nil || current.Text != "Tag delivery" {
		t.Fatalf("self-authored resolved context = %#v err=%v", current, err)
	}
	if _, err := observations.ClaimPending(context.Background(), "worker", time.Minute); !errors.Is(err, observer.ErrNoPendingObservation) {
		t.Fatalf("self-authored output entered decision queue: %v", err)
	}

	otherBot := envelope("message/team-test/tos-tag/392.002", "tos-tag", "392.002", "External integration update")
	otherBot.UserID = "U-other-bot"
	otherBot.BotID = "B-other"
	accepted, err = p.HandleEnvelope(context.Background(), otherBot)
	if err != nil || !accepted.Ignored || !accepted.ResolvedContext {
		t.Fatalf("other integration entered decision work: %#v err=%v", accepted, err)
	}
	current, err = observations.CurrentMessage(context.Background(), "org-test", "team-test", "tos-tag", "392.002")
	if err != nil || current.Text != "External integration update" || current.BotID != "B-other" {
		t.Fatalf("other integration was not retained as context: %#v err=%v", current, err)
	}

	assistant := envelope("message/team-test/tos-tag/392.003", "tos-tag", "392.003", "Assistant app update")
	assistant.UserID = "U-assistant"
	assistant.Subtype = types.SlackMessageSubtypeAssistantAppThread
	accepted, err = p.HandleEnvelope(context.Background(), assistant)
	if err != nil || !accepted.Ignored || !accepted.ResolvedContext {
		t.Fatalf("assistant app message entered decision work: %#v err=%v", accepted, err)
	}
	current, err = observations.CurrentMessage(context.Background(), "org-test", "team-test", "tos-tag", "392.003")
	if err != nil || current.Subtype != types.SlackMessageSubtypeAssistantAppThread {
		t.Fatalf("assistant app message was not retained with provenance: %#v err=%v", current, err)
	}
	if _, err := observations.ClaimPending(context.Background(), "worker", time.Minute); !errors.Is(err, observer.ErrNoPendingObservation) {
		t.Fatalf("agent-authored output entered decision queue: %v", err)
	}
}

func TestRecoveredAgentMentionRemainsContextOnly(t *testing.T) {
	cfg := contextSyncConfig()
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test", EnrollmentMode: "all_observable_channels"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	_, err := scopes.PutChannel(context.Background(), orgconfig.ChannelPolicy{
		OrganizationID: "org-test", TeamID: "team-test", ChannelID: "tos-tag", Enrolled: true,
		ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 6, MaxConcurrentJobs: 2,
		MembershipRevision: "member/v1", MembershipRefreshedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	observations := observer.NewMemoryStore(cfg.Retention.Messages, nil)
	p := &Pipeline{deps: Dependencies{Config: &cfg, Logger: blackbox.New(), Scopes: scopes, Observations: observations}}

	message := envelope("history/team-test/tos-tag/392.100", "tos-tag", "392.100", "<@U-tag> please continue")
	message.UserID = "U-claude"
	message.BotID = "B-claude"
	message.IsMention = true
	if err := p.RecoverContextEnvelope(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	current, err := observations.CurrentMessage(context.Background(), "org-test", "team-test", "tos-tag", "392.100")
	if err != nil || current.BotID != "B-claude" {
		t.Fatalf("recovered agent message was not retained as context: %#v err=%v", current, err)
	}
	if _, err := observations.ClaimPending(context.Background(), "worker", time.Minute); !errors.Is(err, observer.ErrNoPendingObservation) {
		t.Fatalf("recovered agent mention entered decision queue: %v", err)
	}
}

func TestContextSyncDoesNotExpandAllowlistEnrollment(t *testing.T) {
	cfg := contextSyncConfig()
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test", EnrollmentMode: "allowlist"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	p := &Pipeline{deps: Dependencies{Config: &cfg, Logger: blackbox.New(), Scopes: scopes}}
	if authorized, err := p.RegisterContextChannel(context.Background(), types.SlackContextChannel{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "not-allowed"}); err != nil || authorized {
		t.Fatal(err)
	}
	if _, err := scopes.Resolve(context.Background(), "org-test", "team-test", "not-allowed"); !errors.Is(err, orgconfig.ErrNotFound) {
		t.Fatalf("allowlist enrollment expanded: %v", err)
	}
	observations := observer.NewMemoryStore(cfg.Retention.Messages, nil)
	p.deps.Observations = observations
	message := envelope("not-allowed-event", "not-allowed", "390.001", "must not be retained")
	accepted, err := p.HandleEnvelope(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	if !accepted.Ignored || accepted.Duplicate {
		t.Fatalf("unenrolled user-event result = %#v", accepted)
	}
	if _, err := observations.CurrentMessage(context.Background(), "org-test", "team-test", "not-allowed", "390.001"); !errors.Is(err, observer.ErrMessageNotFound) {
		t.Fatalf("unenrolled user-event content was retained: %v", err)
	}
}

func TestContextHistoryImportCannotCreateDecisionAndRespectsExplicitUnenrollment(t *testing.T) {
	cfg := contextSyncConfig()
	observations := observer.NewMemoryStore(cfg.Retention.Messages, nil)
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	now := time.Now().UTC()
	for _, policy := range []orgconfig.ChannelPolicy{
		{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "public", Enrolled: true, ParticipationMode: types.ModeObserve, MaxResponsesPerHour: 6, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now},
		{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "opted-out", Enrolled: false, ParticipationMode: types.ModeObserve, MaxResponsesPerHour: 6, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now},
	} {
		if _, err := scopes.PutChannel(context.Background(), policy); err != nil {
			t.Fatal(err)
		}
	}
	p := &Pipeline{deps: Dependencies{Config: &cfg, Logger: blackbox.New(), Scopes: scopes, Observations: observations}}
	message := envelope("message/team-test/public/400.001", "public", "400.001", "retained context")
	if err := p.ImportContextEnvelope(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if _, err := observations.ClaimPending(context.Background(), "worker", time.Minute); !errors.Is(err, observer.ErrNoPendingObservation) {
		t.Fatalf("history import created a decision: %v", err)
	}
	optedOut := envelope("message/team-test/opted-out/400.002", "opted-out", "400.002", "must not retain")
	if err := p.ImportContextEnvelope(context.Background(), optedOut); err != nil {
		t.Fatal(err)
	}
	if _, err := observations.CurrentMessage(context.Background(), "org-test", "team-test", "opted-out", "400.002"); !errors.Is(err, observer.ErrMessageNotFound) {
		t.Fatalf("explicitly unenrolled history was retained: %v", err)
	}
}

func TestSessionOnlyChannelSkipsHistoricalImportAndOfflineRecovery(t *testing.T) {
	cfg := contextSyncConfig()
	observations := observer.NewMemoryStore(cfg.Retention.Messages, nil)
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	_, err := scopes.PutChannel(context.Background(), orgconfig.ChannelPolicy{
		OrganizationID: "org-test", TeamID: "team-test", ChannelID: "tos-tag", Enrolled: true,
		ParticipationMode: types.ModeAssist, ContextHistoryMode: types.ContextHistorySessionOnly,
		MaxResponsesPerHour: 6, MaxConcurrentJobs: 2, MembershipRevision: "member/v1", MembershipRefreshedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{deps: Dependencies{Config: &cfg, Logger: blackbox.New(), Scopes: scopes, Observations: observations, Sessions: sessions.NewMemoryStore(nil)}}
	history := envelope("message/team-test/tos-tag/ephemeral.001", "tos-tag", "ephemeral.001", "TEST-999 historical context")
	if err := p.ImportContextEnvelope(context.Background(), history); err != nil {
		t.Fatal(err)
	}
	recovered := envelope("message/team-test/tos-tag/ephemeral.002", "tos-tag", "ephemeral.002", "TEST-998 missed mention <@U-tag>")
	recovered.IsMention = true
	if err := p.RecoverContextEnvelope(context.Background(), recovered); err != nil {
		t.Fatal(err)
	}
	for _, ts := range []string{history.MessageTS, recovered.MessageTS} {
		if _, err := observations.CurrentMessage(context.Background(), "org-test", "team-test", "tos-tag", ts); !errors.Is(err, observer.ErrMessageNotFound) {
			t.Fatalf("session-only history %s was retained: %v", ts, err)
		}
	}
}

func TestSessionOnlyContextUsesOnlyDestinationMessagesFromCurrentProcess(t *testing.T) {
	cfg := config.DefaultConfiguration
	builder, err := contextpacks.New(cfg.ContextPacks, contextpacks.WordTokenizer{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	startedAt := now.Add(-time.Minute)
	base := observer.NewMemoryStore(cfg.Retention.Messages, func() time.Time { return now.Add(time.Minute) })
	recording := &recordingObservationStore{Store: base}
	for _, message := range []types.SlackEnvelope{
		envelope("old-test", "tos-tag", "ephemeral.101", "TEST-101 old test context"),
		envelope("current-test", "tos-tag", "ephemeral.102", "current session question"),
		envelope("other-channel", "public-status", "ephemeral.103", "current cross-channel report"),
	} {
		message.EventTime = now.Add(-2 * time.Hour)
		if message.EventID != "old-test" {
			message.EventTime = now
		}
		message.ReceivedAt = message.EventTime
		if _, err := base.Import(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	for _, policy := range []orgconfig.ChannelPolicy{
		{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "tos-tag", Enrolled: true, ParticipationMode: types.ModeAssist, ContextHistoryMode: types.ContextHistorySessionOnly, MaxResponsesPerHour: 10, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now},
		{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "public-status", Enrolled: true, ParticipationMode: types.ModeObserve, MaxResponsesPerHour: 10, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now},
	} {
		if _, err := scopes.PutChannel(context.Background(), policy); err != nil {
			t.Fatal(err)
		}
	}
	memoryStore := agentmemory.NewMemoryStore(func() time.Time { return now })
	if _, _, err := memoryStore.PutGenerated(context.Background(), agentmemory.Record{OrganizationID: "org-test", ChannelID: "tos-tag", Scope: agentmemory.ScopeChannel, ScopeKey: "tos-tag/channel", Text: "TEST-102 durable memory", Facts: []agentmemory.Fact{{Text: "TEST-102 fact", Confidence: .9, ExpiresAt: now.Add(time.Hour)}}, SourceHash: "old-test", Status: agentmemory.StatusActive, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{deps: Dependencies{Config: &cfg, Observations: recording, ContextPacks: builder, Scopes: scopes, Memory: memoryStore}, sessionStartedAt: startedAt}
	target := envelope("current-test", "tos-tag", "ephemeral.102", "current session question")
	target.EventTime, target.ReceivedAt = now, now
	pack, err := p.buildContextPack(context.Background(), target, "obs-current", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(recording.recentChannels) != 1 || recording.recentChannels[0] != "tos-tag" || !recording.recentSince.Equal(startedAt) {
		t.Fatalf("session-only query scope channels=%v since=%s", recording.recentChannels, recording.recentSince)
	}
	if !contextContains(pack, "current session question") || contextContains(pack, "TEST-101") || contextContains(pack, "TEST-102") || contextContains(pack, "cross-channel report") {
		t.Fatalf("session-only context was not isolated: %#v", pack.Sources)
	}
}

func TestOfflineCatchUpQueuesDirectMentionButKeepsAmbientHistoryResolved(t *testing.T) {
	cfg := contextSyncConfig()
	cfg.Slack.BotUserID = "U-tag"
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	_, err := scopes.PutChannel(context.Background(), orgconfig.ChannelPolicy{
		OrganizationID: "org-test", TeamID: "team-test", ChannelID: "tos-tag", Enrolled: true,
		ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 6, MaxConcurrentJobs: 2,
		MembershipRevision: "member/v1", MembershipRefreshedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	observations := observer.NewMemoryStore(cfg.Retention.Messages, nil)
	p := &Pipeline{deps: Dependencies{
		Config: &cfg, Logger: blackbox.New(), Scopes: scopes, Observations: observations,
		Sessions: sessions.NewMemoryStore(nil),
	}}

	ambient := envelope("message/team-test/tos-tag/500.001", "tos-tag", "500.001", "overnight deploy completed")
	if err := p.RecoverContextEnvelope(context.Background(), ambient); err != nil {
		t.Fatal(err)
	}
	if _, err := observations.ClaimPending(context.Background(), "worker", time.Minute); !errors.Is(err, observer.ErrNoPendingObservation) {
		t.Fatalf("ambient offline history became work: %v", err)
	}

	direct := envelope("message/team-test/tos-tag/500.002", "tos-tag", "500.002", "how did we do overnight <@U-tag>")
	direct.IsMention = true
	if err := p.RecoverContextEnvelope(context.Background(), direct); err != nil {
		t.Fatal(err)
	}
	claimed, err := observations.ClaimPending(context.Background(), "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.MessageTS != direct.MessageTS || !claimed.IsMention || claimed.DecisionState != "processing" {
		t.Fatalf("recovered direct mention = %#v", claimed)
	}
}

func TestOfflineCatchUpQueuesReplyOnlyForExistingTagThread(t *testing.T) {
	cfg := contextSyncConfig()
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	_, err := scopes.PutChannel(context.Background(), orgconfig.ChannelPolicy{
		OrganizationID: "org-test", TeamID: "team-test", ChannelID: "tos-tag", Enrolled: true,
		ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 6, MaxConcurrentJobs: 2,
		MembershipRevision: "member/v1", MembershipRefreshedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	observationStore := observer.NewMemoryStore(cfg.Retention.Messages, nil)
	sessionStore := sessions.NewMemoryStore(nil)
	if _, _, err := sessionStore.Resolve(context.Background(), "org-test", "team-test", "tos-tag", "root.001"); err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{deps: Dependencies{Config: &cfg, Logger: blackbox.New(), Scopes: scopes, Observations: observationStore, Sessions: sessionStore}}
	reply := envelope("message/team-test/tos-tag/500.003", "tos-tag", "500.003", "go ahead")
	reply.ThreadTS = "root.001"
	if err := p.RecoverContextEnvelope(context.Background(), reply); err != nil {
		t.Fatal(err)
	}
	claimed, err := observationStore.ClaimPending(context.Background(), "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.MessageTS != reply.MessageTS || claimed.RootThreadTS != "root.001" {
		t.Fatalf("recovered active-thread reply = %#v", claimed)
	}
}

func TestLiveSlackEnvelopeAdvancesDurableContextWatermark(t *testing.T) {
	cfg := contextSyncConfig()
	observations := observer.NewMemoryStore(cfg.Retention.Messages, nil)
	states := slack.NewMemoryContextSyncStateStore()
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	_, _ = scopes.PutChannel(context.Background(), orgconfig.ChannelPolicy{
		OrganizationID: "org-test", TeamID: "team-test", ChannelID: "public", Enrolled: true,
		ParticipationMode: types.ModeObserve, MaxResponsesPerHour: 6, MaxConcurrentJobs: 1,
		MembershipRevision: "m1", MembershipRefreshedAt: time.Now().UTC(),
	})
	p := &Pipeline{deps: Dependencies{Config: &cfg, Logger: blackbox.New(), Scopes: scopes, Observations: observations, ContextSyncState: states}}
	message := envelope("live-watermark", "public", "401.001", "live context")
	if _, err := p.HandleEnvelope(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	stored, err := states.List(context.Background(), "org-test", "team-test")
	if err != nil {
		t.Fatal(err)
	}
	state := stored["public"]
	if state.BootstrapCompleted || !state.SyncedThrough.Equal(message.EventTime) {
		t.Fatalf("live watermark = %#v", state)
	}
}

func TestContextQueryUsesOnlyAuthorizedChannelsAndPolicyRestriction(t *testing.T) {
	cfg := config.DefaultConfiguration
	builder, err := contextpacks.New(cfg.ContextPacks, contextpacks.WordTokenizer{})
	if err != nil {
		t.Fatal(err)
	}
	base := observer.NewMemoryStore(cfg.Retention.Messages, nil)
	recording := &recordingObservationStore{Store: base}
	now := time.Now().UTC()
	for _, message := range []types.SlackEnvelope{
		envelope("support-policy", "support", "350.001", "is the system down?"),
		envelope("public-policy", "public-status", "350.002", "Public status update"),
		envelope("private-policy", "private-alerts", "350.003", "Confidential customer incident is down"),
		envelope("management-policy", "management", "350.004", "Private management plan"),
		envelope("unenrolled-policy", "not-enrolled", "350.005", "Never query this channel"),
	} {
		message.EventTime, message.ReceivedAt = now, now
		if _, err := base.Accept(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	for _, policy := range []orgconfig.ChannelPolicy{
		{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "support", Name: "support", Enrolled: true, ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 10, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now},
		{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "public-status", Name: "public-status", Enrolled: true, ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 10, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now},
		{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "private-alerts", Enrolled: true, Restricted: true, ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 10, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now},
		{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "management", Enrolled: true, Restricted: true, ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 10, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now},
		{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "not-enrolled", Enrolled: false, ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 10, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now},
	} {
		if _, err := scopes.PutChannel(context.Background(), policy); err != nil {
			t.Fatal(err)
		}
	}
	memoryStore := agentmemory.NewMemoryStore(func() time.Time { return now })
	for _, record := range []agentmemory.Record{
		{OrganizationID: "org-test", ChannelID: "public-status", Scope: agentmemory.ScopeChannel, ScopeKey: "public-status/channel", Text: "Public durable memory", Facts: []agentmemory.Fact{{Text: "Public durable fact"}}, SourceHash: "public", Status: agentmemory.StatusActive, ExpiresAt: now.Add(time.Hour)},
		{OrganizationID: "org-test", ChannelID: "private-alerts", Restricted: true, Scope: agentmemory.ScopeChannel, ScopeKey: "private-alerts/channel", Text: "Other private durable memory", Facts: []agentmemory.Fact{{Text: "Other private durable fact"}}, SourceHash: "private", Status: agentmemory.StatusActive, ExpiresAt: now.Add(time.Hour)},
		{OrganizationID: "org-test", ChannelID: "management", Restricted: true, Scope: agentmemory.ScopeChannel, ScopeKey: "management/channel", Text: "Management durable memory", Facts: []agentmemory.Fact{{Text: "Management durable fact"}}, SourceHash: "management", Status: agentmemory.StatusActive, ExpiresAt: now.Add(time.Hour)},
	} {
		if _, _, err := memoryStore.PutGenerated(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	p := &Pipeline{deps: Dependencies{Config: &cfg, Observations: recording, ContextPacks: builder, Scopes: scopes, Memory: memoryStore}}
	target := envelope("target-policy", "support", "350.001", "is the system down?")
	pack, err := p.buildContextPack(context.Background(), target, "obs-target", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(recording.recentChannels) != 2 || !containsString(recording.recentChannels, "support") || !containsString(recording.recentChannels, "public-status") {
		t.Fatalf("pre-query channels=%v", recording.recentChannels)
	}
	for _, source := range pack.Sources {
		if source.ChannelID == "not-enrolled" || source.ChannelID == "private-alerts" || source.ChannelID == "management" {
			t.Fatalf("ineligible source leaked into public destination: %#v", source)
		}
		if source.ChannelID == "public-status" && source.Provenance == "human_message" && (source.ChannelName != "public-status" || source.AuthorID != "user-test" || source.ObservedAt.IsZero()) {
			t.Fatalf("public attribution metadata missing from context source: %#v", source)
		}
	}
	if !contextContains(pack, "Public durable memory") || contextContains(pack, "Other private durable memory") || contextContains(pack, "Management durable memory") {
		t.Fatalf("public memory disclosure was not destination safe: %#v", pack.Sources)
	}

	privateTarget := envelope("management-target", "management", "350.004", "Private management plan")
	privatePack, err := p.buildContextPack(context.Background(), privateTarget, "obs-management", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(recording.recentChannels) != 3 || !containsString(recording.recentChannels, "management") || !containsString(recording.recentChannels, "support") || !containsString(recording.recentChannels, "public-status") || containsString(recording.recentChannels, "private-alerts") {
		t.Fatalf("private-destination query channels=%v", recording.recentChannels)
	}
	var foundManagement bool
	for _, source := range privatePack.Sources {
		if source.ChannelID == "private-alerts" || source.ChannelID == "not-enrolled" {
			t.Fatalf("other private or unenrolled source leaked into management: %#v", source)
		}
		if source.ChannelID == "management" && source.Text == "Private management plan" {
			foundManagement = true
		}
	}
	if !foundManagement {
		t.Fatal("current private channel content was not available in its own context")
	}
	if !contextContains(privatePack, "Public durable memory") || !contextContains(privatePack, "Management durable memory") || contextContains(privatePack, "Other private durable memory") {
		t.Fatalf("private memory disclosure was not destination local: %#v", privatePack.Sources)
	}
}

func contextContains(pack types.ContextPackRevision, text string) bool {
	for _, source := range pack.Sources {
		if strings.Contains(source.Text, text) {
			return true
		}
	}
	return false
}

func TestHeartbeatClassifierUsesDestinationFilteredContextAndOutputPolicy(t *testing.T) {
	cfg := config.DefaultConfiguration
	builder, err := contextpacks.New(cfg.ContextPacks, contextpacks.WordTokenizer{})
	if err != nil {
		t.Fatal(err)
	}
	base := observer.NewMemoryStore(cfg.Retention.Messages, nil)
	recording := &recordingObservationStore{Store: base}
	now := time.Now().UTC()
	for _, message := range []types.SlackEnvelope{
		envelope("public-heartbeat", "public-status", "360.001", "Public incident update"),
		envelope("private-heartbeat", "private-alerts", "360.002", "Private incident detail"),
	} {
		message.EventTime, message.ReceivedAt = now, now
		if _, err := base.Import(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	for _, policy := range []orgconfig.ChannelPolicy{
		{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "tos-tag", Enrolled: true, ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 10, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now},
		{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "public-status", Enrolled: true, ParticipationMode: types.ModeObserve, MaxResponsesPerHour: 10, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now},
		{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "private-alerts", Enrolled: true, Restricted: true, ParticipationMode: types.ModeObserve, MaxResponsesPerHour: 10, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now},
	} {
		if _, err := scopes.PutChannel(context.Background(), policy); err != nil {
			t.Fatal(err)
		}
	}
	checked := false
	model := classifierFunc(func(_ context.Context, _ classifier.Target, pack types.ContextPackRevision) (types.ClassificationDecision, error) {
		checked = true
		var public bool
		for _, source := range pack.Sources {
			if source.ChannelID == "private-alerts" {
				t.Fatalf("private cross-channel source leaked into heartbeat: %#v", source)
			}
			if source.ChannelID == "public-status" {
				public = true
			}
		}
		if !public {
			t.Fatal("public cross-channel context was not available to heartbeat classifier")
		}
		return types.ClassificationDecision{Outcome: types.OutcomeStartBackgroundJob, Confidence: .9, ReasonCodes: []string{"heartbeat.actionable"}, ResponseIntent: "run heartbeat", DisclosureClass: types.DisclosureDestinationSafe, RequiresFullAgent: true, Reaction: "eyes"}, nil
	})
	service, err := classifier.New(model, false, cfg.Classifier.AssistThreshold, cfg.Classifier.ChannelReplyThreshold)
	if err != nil {
		t.Fatal(err)
	}
	floodGate, err := flood.NewMemory(cfg.Classifier.FloodMaxMessages, cfg.Classifier.FloodWindow, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{deps: Dependencies{Config: &cfg, Logger: blackbox.New(), Observations: recording, ContextPacks: builder, Scopes: scopes, Classifier: service, Decisions: classifier.NewMemoryDecisionStore(), FloodProtection: floodGate}}
	subscription := triggers.Subscription{ID: "heartbeat", OrganizationID: "org-test", WorkspaceID: "team-test", ChannelID: "tos-tag", SessionID: "session", Generation: 1, OwnerID: "owner", Kind: triggers.KindHeartbeat, Instruction: "Check if useful participation is needed.", Interval: time.Minute, NextRun: now, ClassifierGate: true, MinConfidence: .8, Enabled: true}
	decision, err := p.EvaluateHeartbeat(context.Background(), subscription, "window")
	if err != nil || !decision.Accepted || !checked {
		t.Fatalf("decision=%#v checked=%v err=%v", decision, checked, err)
	}
	if containsString(recording.recentChannels, "private-alerts") {
		t.Fatalf("private channel entered heartbeat observation query: %v", recording.recentChannels)
	}

	cfg.Slack.OutputChannelIDs = []string{"another-channel"}
	checked = false
	decision, err = p.EvaluateHeartbeat(context.Background(), subscription, "window-2")
	if err != nil || decision.Accepted || checked {
		t.Fatalf("output allowlist did not fail heartbeat silent: decision=%#v checked=%v err=%v", decision, checked, err)
	}
}

func TestHeartbeatSharesClassifierFloodProtectionBudget(t *testing.T) {
	system := newTestSystem(t)
	seed := envelope("heartbeat-context", "tos-tag", "370.001", "Recent channel context")
	if _, err := system.pipeline.deps.Observations.Import(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	gate, err := flood.NewMemory(1, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	system.pipeline.deps.FloodProtection = gate
	var providerCalls atomic.Int64
	service, err := classifier.New(classifierFunc(func(context.Context, classifier.Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
		providerCalls.Add(1)
		return types.ClassificationDecision{
			Outcome: types.OutcomeStartBackgroundJob, Confidence: .99,
			ReasonCodes: []string{"heartbeat.actionable"}, ResponseIntent: "run heartbeat",
			DisclosureClass: types.DisclosureDestinationSafe, RequiresFullAgent: true, Reaction: "eyes",
		}, nil
	}), false, .9, .98)
	if err != nil {
		t.Fatal(err)
	}
	system.pipeline.deps.Classifier = service
	subscription := triggers.Subscription{
		ID: "flood-heartbeat", OrganizationID: "org-test", WorkspaceID: "team-test", ChannelID: "tos-tag",
		OwnerID: "owner", Kind: triggers.KindHeartbeat, Instruction: "Review the current window.", ClassifierGate: true, Enabled: true,
	}
	first, err := system.pipeline.EvaluateHeartbeat(context.Background(), subscription, "window-1")
	if err != nil || !first.Accepted {
		t.Fatalf("first heartbeat = %#v err=%v", first, err)
	}
	second, err := system.pipeline.EvaluateHeartbeat(context.Background(), subscription, "window-2")
	if err != nil || second.Accepted || second.Decision.ReasonCodes[0] != "safety.classifier_flood_limit" {
		t.Fatalf("second heartbeat = %#v err=%v", second, err)
	}
	if got := providerCalls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
}

func TestBotAuthoredCrossChannelContextIsMarkedUnverified(t *testing.T) {
	cfg := config.DefaultConfiguration
	builder, err := contextpacks.New(cfg.ContextPacks, contextpacks.WordTokenizer{})
	if err != nil {
		t.Fatal(err)
	}
	observations := observer.NewMemoryStore(cfg.Retention.Messages, nil)
	now := time.Now().UTC()
	botMessage := envelope("bot-context", "status", "360.001", "generated status")
	botMessage.BotID = "B-external"
	target := envelope("target-context", "support", "360.002", "what is the status?")
	for _, message := range []types.SlackEnvelope{botMessage, target} {
		message.EventTime, message.ReceivedAt = now, now
		if _, err := observations.Import(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	for _, channel := range []string{"status", "support"} {
		if _, err := scopes.PutChannel(context.Background(), orgconfig.ChannelPolicy{OrganizationID: "org-test", TeamID: "team-test", ChannelID: channel, Enrolled: true, ParticipationMode: types.ModeObserve, MaxResponsesPerHour: 6, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	p := &Pipeline{deps: Dependencies{Config: &cfg, Observations: observations, ContextPacks: builder, Scopes: scopes}}
	pack, err := p.buildContextPack(context.Background(), target, "obs-target", 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range pack.Sources {
		if source.ChannelID == "status" {
			if source.Provenance != "agent_output_unverified" {
				t.Fatalf("bot source provenance = %q", source.Provenance)
			}
			return
		}
	}
	t.Fatal("bot-authored context source was not selected")
}

func TestRestrictedIncidentDoesNotReconsiderOtherChannels(t *testing.T) {
	cfg := config.DefaultConfiguration
	now := time.Now().UTC()
	recording := &recordingObservationStore{Store: observer.NewMemoryStore(cfg.Retention.Messages, nil)}
	scopes := orgconfig.NewMemory()
	_, _ = scopes.PutOrganization(context.Background(), models.Organization{PublicID: "org-test"})
	_, _ = scopes.PutWorkspace(context.Background(), models.Workspace{OrganizationID: "org-test", TeamID: "team-test", Enabled: true})
	_, err := scopes.PutChannel(context.Background(), orgconfig.ChannelPolicy{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "management", Enrolled: true, Restricted: true, ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 10, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{deps: Dependencies{Observations: recording, Scopes: scopes}}
	p.reconsiderLateQuestions(context.Background(), models.Observation{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "management", SlackEventTime: now})
	if recording.lateCandidateCalls != 0 {
		t.Fatalf("restricted incident triggered %d cross-channel reconsideration queries", recording.lateCandidateCalls)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestAgentInputContainsOnlyDestinationSafeContext(t *testing.T) {
	input := buildAgentInput(types.SlackEnvelope{ChannelID: "alerts", MessageTS: "2.0", Text: "Compare the classifier, worker, and delivery reconciler across responsibility and retry behavior."}, types.ContextPackRevision{Sources: []types.ContextSource{
		{ID: "system/classifier", Partition: types.PartitionSystem, Text: "internal classifier", DisclosureClass: types.DisclosureDestinationSafe},
		{ID: "directive/1", ChannelID: "alerts", Partition: types.PartitionSystem, Provenance: "operator_directive", Text: "Investigate every alert using OTel evidence.", DisclosureClass: types.DisclosureDestinationSafe},
		{ID: "alerts/1", ChannelID: "alerts", ChannelName: "development", AuthorID: "U_TOM", Partition: types.PartitionEvidence, Provenance: "human_message", Text: "Production incident active", ObservedAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC), DisclosureClass: types.DisclosureDestinationSafe},
		{ID: "private/2", ChannelID: "private", ChannelName: "leadership", AuthorID: "U_SECRET", Partition: types.PartitionSituation, Text: "restricted details", DisclosureClass: types.DisclosureRestrictedAwareness},
	}}, types.ClassificationDecision{ResponseIntent: "reconcile status", ReleasableEvidenceIDs: []string{"alerts/1"}, ProductRetrievalRequired: true})
	if !strings.Contains(input, "Production incident active") || !strings.Contains(input, "Investigate every alert using OTel evidence.") || !strings.Contains(input, `"response_intent":"reconcile status"`) || !strings.Contains(input, `"releasable_evidence_ids":["alerts/1"]`) || !strings.Contains(input, `"authoritative_product_retrieval_required":true`) || !strings.Contains(input, `"source_write_requested":false`) || !strings.Contains(input, `"presentation_requirements":["native_table"]`) || !strings.Contains(input, `"conversation_focus"`) || !strings.Contains(input, `"channel_name":"development"`) || !strings.Contains(input, `"author_id":"U_TOM"`) || !strings.Contains(input, `"observed_at":"2026-08-01T12:00:00Z"`) || strings.Contains(input, "restricted details") || strings.Contains(input, "leadership") || strings.Contains(input, "U_SECRET") || strings.Contains(input, "internal classifier") {
		t.Fatalf("unsafe agent input: %s", input)
	}
	allowed := trustedMentionAllowlist(input)
	if len(allowed.UserIDs) != 1 || allowed.UserIDs[0] != "U_TOM" || len(allowed.ChannelIDs) != 1 || allowed.ChannelIDs[0] != "alerts" {
		t.Fatalf("trusted mention allowlist = %#v", allowed)
	}
}

func TestPresentationRequirementsPreferNativeTablesForRepeatedComparisons(t *testing.T) {
	for name, testCase := range map[string]struct {
		request string
		want    bool
	}{
		"three-way repeated comparison": {request: "Compare the classifier, worker, and reconciler across authority, state, and retry behavior.", want: true},
		"comparison by repeated fields": {request: "Compare the classifier, worker, and reconciler by role, inputs, outputs, and failure behavior.", want: true},
		"difference phrasing":           {request: "What is the difference between the worker and the delivery reconciler?", want: true},
		"natural product choice":        {request: "What would make me choose a Node Pro instead of a Node Mini?", want: true},
		"pick one product over another": {request: "When would I pick Node Mini over Node Pro?", want: true},
		"explicit matrix":               {request: "Give me a rollout matrix for these checks.", want: true},
		"simple substitution":           {request: "Can I use Ethernet instead of Wi-Fi?", want: false},
		"ordinary explanation":          {request: "Explain why MongoDB owns durable state.", want: false},
	} {
		t.Run(name, func(t *testing.T) {
			got := presentationRequirements(testCase.request)
			if (len(got) > 0) != testCase.want {
				t.Fatalf("requirements = %#v, want table=%v", got, testCase.want)
			}
		})
	}
}

func TestTrustedMentionAllowlistCannotBeBroadenedOutsideSelectedEvidence(t *testing.T) {
	input := `{"releasable_evidence_ids":["public/1"],"authorized_context":[{"id":"public/1","channel_id":"C_PUBLIC","author_id":"U_PUBLIC"},{"id":"other/1","channel_id":"C_OTHER","author_id":"U_OTHER"}]}`
	allowed := trustedMentionAllowlist(input)
	if len(allowed.UserIDs) != 1 || allowed.UserIDs[0] != "U_PUBLIC" || len(allowed.ChannelIDs) != 1 || allowed.ChannelIDs[0] != "C_PUBLIC" {
		t.Fatalf("mention provenance widened: %#v", allowed)
	}
	if got := trustedMentionAllowlist(`{"releasable_evidence_ids":["missing"],"authorized_context":[]}`); len(got.UserIDs) != 0 || len(got.ChannelIDs) != 0 {
		t.Fatalf("missing evidence produced mentions: %#v", got)
	}
}

func TestCurrentAgentRuntimeContractOutranksHistoricalContext(t *testing.T) {
	for _, required := range []string{
		"direct, stateless, tool-free OpenAI Responses API call",
		"Codex App Server in a disposable worker",
		"TelemetryOS source access is permanently read-only",
		"bounded version workflow",
		"authoritative_product_retrieval_required",
		"marketing-messaging skill",
		"telemetryos-documentation skill",
		"read telemetryos.product-docs/read docs-index",
		"search and index results are discovery only",
		"Every product answer includes concise clickable links",
		"namespace/slug is an internal lookup identifier",
		"telemetryos.wiki/read get or url",
		"Treat any source that conflicts with these current facts as stale context",
	} {
		if !strings.Contains(currentAgentRuntimeContract, required) {
			t.Fatalf("current runtime contract is missing %q", required)
		}
	}
}

func TestAuthoritativeProductRetrievalRequiresReviewedOfficialReader(t *testing.T) {
	if authoritativeProductRetrievalCompleted(map[string]struct{}{}) || authoritativeProductRetrievalCompleted(map[string]struct{}{"agent.web/search": {}}) || authoritativeProductRetrievalCompleted(map[string]struct{}{"telemetryos.wiki/read/search": {}}) || authoritativeProductRetrievalCompleted(map[string]struct{}{"telemetryos.product-docs/read/docs-index": {}}) {
		t.Fatal("missing or arbitrary web retrieval satisfied product authority")
	}
	for _, operation := range []string{"telemetryos.wiki/read/get", "telemetryos.product-docs/read/docs-page", "telemetryos.product-docs/read/corporate-full"} {
		if !authoritativeProductRetrievalCompleted(map[string]struct{}{operation: {}}) {
			t.Fatalf("reviewed product reader %q did not satisfy retrieval", operation)
		}
	}
	flags := agentInputPolicyFlags(`{"source_write_requested":true,"authoritative_product_retrieval_required":true}`)
	if !flags.SourceWriteRequested || !flags.ProductRetrievalRequired {
		t.Fatalf("agent policy flags=%#v", flags)
	}
}

func TestSocialChatterStaysSilent(t *testing.T) {
	system := newTestSystem(t)
	if _, err := system.ingress.Inject(context.Background(), envelope("chatter-1", "general", "400.001", "good morning everyone")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { records, _ := system.decisions.List(context.Background()); return len(records) == 1 })
	records, err := system.decisions.List(context.Background())
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %#v, err = %v", records, err)
	}
	if records[0].Result.Effective.Outcome != types.OutcomeSilent || len(system.transport.Requests()) != 0 || len(system.transport.ReactionRequests()) != 0 {
		t.Fatal("social chatter produced output")
	}
}

func TestLateCrossChannelAlertReconsidersEarlierSupportQuestionOnce(t *testing.T) {
	system := newTestSystem(t)
	question := envelope("support-early", "support", "500.001", "is the system down?")
	question.EventTime = time.Now().UTC().Add(-time.Minute)
	if _, err := system.ingress.Inject(context.Background(), question); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { records, _ := system.decisions.List(context.Background()); return len(records) == 1 })
	alert := envelope("alert-late", "alerts", "500.002", "Production outage incident: API is down")
	if _, err := system.ingress.Inject(context.Background(), alert); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { records, _ := system.decisions.List(context.Background()); return len(records) == 3 })
	records, _ := system.decisions.List(context.Background())
	var reconsidered *classifier.DecisionRecord
	for index := range records {
		if records[index].ObservationID == records[0].ObservationID && records[index].DecisionRevision == 2 {
			reconsidered = &records[index]
		}
	}
	if reconsidered == nil || reconsidered.Result.Predicted.Outcome != types.OutcomeReplyInThread || !reconsidered.Result.Shadowed {
		t.Fatalf("late decision=%#v records=%#v", reconsidered, records)
	}
}

func TestResultSegmentKindsSupportsRedactedDeliveryDiagnostics(t *testing.T) {
	result := types.SlackResult{Segments: []types.SlackSegment{
		{Kind: types.SlackSegmentMRKDWN, Text: "sensitive text is intentionally not logged"},
		{Kind: types.SlackSegmentTable, Table: &types.SlackTable{}},
	}}
	got := resultSegmentKinds(result)
	if len(got) != 2 || got[0] != "mrkdwn_text" || got[1] != "table" {
		t.Fatalf("segment kinds = %#v", got)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not reached before deadline")
}
