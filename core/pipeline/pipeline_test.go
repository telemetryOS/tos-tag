package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/classifier"
	"github.com/telemetryos/tos-tag/core/config"
	"github.com/telemetryos/tos-tag/core/contextpacks"
	"github.com/telemetryos/tos-tag/core/deliveries"
	"github.com/telemetryos/tos-tag/core/jobs"
	"github.com/telemetryos/tos-tag/core/observer"
	"github.com/telemetryos/tos-tag/core/orgconfig"
	"github.com/telemetryos/tos-tag/core/sessions"
	"github.com/telemetryos/tos-tag/core/slack"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

type recordingObservationStore struct {
	observer.Store
	recentChannels     []string
	lateCandidateCalls int
}

type recordingAdmissions struct {
	completed []string
}

func (*recordingAdmissions) Admit(context.Context, orgconfig.ChannelPolicy) (string, error) {
	return "", nil
}

func (r *recordingAdmissions) Complete(_ context.Context, id string) {
	r.completed = append(r.completed, id)
}

func (s *recordingObservationStore) Recent(ctx context.Context, organizationID string, channelIDs []string, since time.Time, limit int) ([]models.ChannelMessage, error) {
	s.recentChannels = append([]string(nil), channelIDs...)
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
	pipe, err := New(Dependencies{
		Config:       &cfg,
		Ingress:      ingress,
		Transport:    transport,
		Observations: observer.NewMemoryStore(cfg.Retention.Messages, nil),
		Sessions:     sessions.NewMemoryStore(nil),
		Jobs:         jobQueue,
		Decisions:    decisionStore,
		Deliveries:   deliveryQueue,
		ContextPacks: builder,
		Classifier:   classificationService,
		Renderer:     deliveries.NewRenderer(),
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
	return testSystem{pipe, ingress, transport, jobQueue, deliveryQueue, decisionStore}
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

	ack, err := system.ingress.Inject(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Duplicate {
		t.Fatal("first envelope was marked duplicate")
	}
	waitFor(t, func() bool { return len(system.transport.Requests()) == 1 })
	if reactions := system.transport.ReactionRequests(); len(reactions) != 1 || reactions[0].Emoji != "eyes" || reactions[0].ChannelID != message.ChannelID || reactions[0].MessageTS != message.MessageTS {
		t.Fatalf("classifier acknowledgement reactions = %#v", reactions)
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
	if request.Destination.ChannelID != message.ChannelID || request.Destination.ThreadTS != message.MessageTS {
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
		t.Fatalf("duplicate caused %d reactions", got)
	}
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
		{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "support", Enrolled: true, ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 10, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now},
		{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "public-status", Enrolled: true, ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 10, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now},
		{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "private-alerts", Enrolled: true, Restricted: true, ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 10, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now},
		{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "management", Enrolled: true, Restricted: true, ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 10, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now},
		{OrganizationID: "org-test", TeamID: "team-test", ChannelID: "not-enrolled", Enrolled: false, ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 10, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: now},
	} {
		if _, err := scopes.PutChannel(context.Background(), policy); err != nil {
			t.Fatal(err)
		}
	}
	p := &Pipeline{deps: Dependencies{Config: &cfg, Observations: recording, ContextPacks: builder, Scopes: scopes}}
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
	input := buildAgentInput("is it down?", types.ContextPackRevision{Sources: []types.ContextSource{
		{ID: "system/classifier", Partition: types.PartitionSystem, Text: "internal classifier", DisclosureClass: types.DisclosureDestinationSafe},
		{ID: "alerts/1", ChannelID: "alerts", Partition: types.PartitionEvidence, Text: "Production incident active", DisclosureClass: types.DisclosureDestinationSafe},
		{ID: "private/2", ChannelID: "private", Partition: types.PartitionSituation, Text: "restricted details", DisclosureClass: types.DisclosureRestrictedAwareness},
	}})
	if !strings.Contains(input, "Production incident active") || strings.Contains(input, "restricted details") || strings.Contains(input, "internal gate") {
		t.Fatalf("unsafe agent input: %s", input)
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
