package classifier

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

type classifierFunc func(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error)

func (f classifierFunc) Decide(ctx context.Context, target Target, pack types.ContextPackRevision) (types.ClassificationDecision, error) {
	return f(ctx, target, pack)
}

func newService(t *testing.T, shadow bool) *Service {
	t.Helper()
	service, err := New(DeterministicClassifier{}, shadow, 0.9, 0.98)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func target(text string) Target {
	return Target{ObservationID: "obs-1", Mode: types.ModeAssist, Envelope: types.SlackEnvelope{Kind: types.SlackEventMessage, Text: text}}
}

func TestHardMentionSurvivesShadowMode(t *testing.T) {
	got := target("help")
	got.Envelope.IsMention = true
	result := newService(t, true).Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeReplyInThread || result.Shadowed {
		t.Fatalf("hard mention was shadowed: %#v", result)
	}
}

func TestDirectMentionPlacementDistinguishesBriefFromDeeperReplies(t *testing.T) {
	for name, testCase := range map[string]struct {
		text string
		want types.ClassificationOutcome
	}{
		"brief channel answer": {text: "<@tos-tag> what is 2 + 2?", want: types.OutcomeReplyInChannel},
		"deeper thread answer": {text: "<@tos-tag> investigate why the deployment failed and walk me through the fix", want: types.OutcomeReplyInThread},
	} {
		t.Run(name, func(t *testing.T) {
			got := target(testCase.text)
			got.Envelope.IsMention = true
			result := newService(t, true).Decide(context.Background(), got, types.ContextPackRevision{})
			if result.Predicted.Outcome != testCase.want || result.Effective.Outcome != testCase.want || result.Shadowed {
				t.Fatalf("placement = %#v, want %s", result, testCase.want)
			}
		})
	}
}

func TestDirectMentionPlacementPolicyCorrectsUnsafeClassifierPlacement(t *testing.T) {
	channelDecision := types.ClassificationDecision{
		Outcome:           types.OutcomeReplyInChannel,
		Confidence:        0.99,
		ReasonCodes:       []string{"classifier.channel"},
		ResponseIntent:    "answer",
		DisclosureClass:   types.DisclosureDestinationSafe,
		RequiresFullAgent: true,
		Reaction:          "speech_balloon",
	}
	service, err := New(classifierFunc(func(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
		return channelDecision, nil
	}), true, 0.9, 0.98)
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"explicit thread request": "<@tos-tag> Reply in a thread. What is 2 + 2?",
		"deep table surface":      "<@tos-tag> Compare the two approaches in a native table with three rows.",
		"document surface":        "<@tos-tag> Write a detailed architecture document explaining how the control plane works.",
	} {
		t.Run(name, func(t *testing.T) {
			got := target(text)
			got.Envelope.IsMention = true
			result := service.Decide(context.Background(), got, types.ContextPackRevision{})
			if result.Predicted.Outcome != types.OutcomeReplyInThread || result.Effective.Outcome != types.OutcomeReplyInThread {
				t.Fatalf("deep request escaped into channel: %#v", result)
			}
		})
	}
}

func TestDirectMentionKeepsValidLowConfidenceRoutingRecommendation(t *testing.T) {
	service, err := New(classifierFunc(func(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
		return types.ClassificationDecision{
			Outcome: types.OutcomeReplyInChannel, Confidence: .72, ReasonCodes: []string{"brief_question"},
			ResponseIntent: "answer briefly", DisclosureClass: types.DisclosureDestinationSafe,
			RequiresFullAgent: true, Reaction: "speech_balloon", AgentModelProfile: "chatgpt-luna-low",
			AgentModelStrength: "light", AgentReasoningEffort: "low",
		}, nil
	}), false, .9, .98)
	if err != nil {
		t.Fatal(err)
	}
	got := target("<@tos-tag> which part runs before the full agent?")
	got.Envelope.IsMention = true
	result := service.Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeReplyInChannel || result.Effective.AgentModelProfile != "chatgpt-luna-low" || result.Effective.AgentReasoningEffort != "low" {
		t.Fatalf("direct mention routing recommendation was discarded: %#v", result)
	}
}

func TestDirectMentionStillRequiresHighConfidenceForClassifierDirectReply(t *testing.T) {
	service, err := New(classifierFunc(func(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
		return types.ClassificationDecision{
			Outcome: types.OutcomeReplyInChannel, Confidence: .72, ReasonCodes: []string{"social"},
			ResponseIntent: "acknowledge thanks", DirectReply: "You're welcome!",
			DisclosureClass: types.DisclosureDestinationSafe, Reaction: "white_check_mark",
		}, nil
	}), false, .9, .98)
	if err != nil {
		t.Fatal(err)
	}
	got := target("<@tos-tag> thanks!")
	got.Envelope.IsMention = true
	result := service.Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeSilent || result.Effective.ReasonCodes[0] != "classifier.direct_reply_unavailable" {
		t.Fatalf("low-confidence direct social reply was admitted: %#v", result)
	}
}

func TestExplicitChannelRequestOverridesClassifierThreadPlacement(t *testing.T) {
	service, err := New(classifierFunc(func(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
		return types.ClassificationDecision{Outcome: types.OutcomeReplyInThread, Confidence: 0.99, ReasonCodes: []string{"classifier.thread"}, DisclosureClass: types.DisclosureDestinationSafe, RequiresFullAgent: true, Reaction: "eyes"}, nil
	}), true, 0.9, 0.98)
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"natural language": "<@tos-tag> Reply in the channel with one short sentence.",
		"hyphenated":       "<@tos-tag> Give an in-channel status in one short sentence.",
		"channel level":    "<@tos-tag> Give a channel-level status in one short sentence.",
		"negated thread":   "<@tos-tag> Post a compact native table in the channel, not a thread.",
	} {
		t.Run(name, func(t *testing.T) {
			got := target(text)
			got.Envelope.IsMention = true
			result := service.Decide(context.Background(), got, types.ContextPackRevision{})
			if result.Effective.Outcome != types.OutcomeReplyInChannel {
				t.Fatalf("explicit channel placement was not honored: %#v", result)
			}
		})
	}
}

func TestActiveThreadSocialAcknowledgementUsesDirectReplyWithoutAgent(t *testing.T) {
	got := target("Thanks!")
	got.ActiveThread = true
	result := newService(t, true).Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeReplyInThread || result.Effective.DirectReply != "You're welcome!" || result.Effective.RequiresFullAgent || result.Shadowed {
		t.Fatalf("social acknowledgement did not use a direct thread reply: %#v", result)
	}
}

func TestLikelyAddressedWeatherQuestionUsesDirectClassifierClarification(t *testing.T) {
	now := time.Date(2026, 8, 2, 16, 8, 54, 0, time.UTC)
	service, err := New(DeterministicClassifier{}, false, .9, .98)
	if err != nil {
		t.Fatal(err)
	}
	got := target("what's the weather like today?")
	got.Envelope.ChannelID = "tos-tag"
	got.Envelope.MessageTS = "2.0"
	got.Envelope.UserID = "U_ALEX"
	got.Envelope.EventTime = now
	pack := types.ContextPackRevision{Sources: []types.ContextSource{
		{ID: "tos-tag/2.0", ChannelID: "tos-tag", AuthorID: "U_ALEX", Provenance: "human_message", Text: got.Envelope.Text, ObservedAt: now, DisclosureClass: types.DisclosureDestinationSafe},
		{ID: "tos-tag/1.0", ChannelID: "tos-tag", AuthorID: "U_TAG", Provenance: "agent_output_unverified", Text: "Previous Tag answer", ObservedAt: now.Add(-time.Minute), DisclosureClass: types.DisclosureDestinationSafe},
	}}
	result := service.Decide(context.Background(), got, pack)
	if result.Effective.Outcome != types.OutcomeReplyInChannel || result.Effective.DirectReply != weatherLocationClarificationReply || result.Effective.RequiresFullAgent || result.Effective.Reaction != "speech_balloon" {
		t.Fatalf("weather clarification did not stay in classifier: %#v", result)
	}
}

func TestActiveThreadNaturalSocialReplyIsDecidedByClassifierWithoutAgent(t *testing.T) {
	calls := 0
	service, err := New(classifierFunc(func(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
		calls++
		return types.ClassificationDecision{
			Outcome: types.OutcomeReplyInThread, Confidence: .99, ReasonCodes: []string{"social.natural_acknowledgement"},
			ResponseIntent: "acknowledge naturally", DirectReply: "Glad that helped!",
			DisclosureClass: types.DisclosureDestinationSafe, Reaction: "white_check_mark",
		}, nil
	}), false, .9, .98)
	if err != nil {
		t.Fatal(err)
	}
	got := target("Thanks, Tag — that's exactly what I needed.")
	got.ActiveThread = true
	result := service.Decide(context.Background(), got, types.ContextPackRevision{})
	if calls != 1 || result.Effective.Outcome != types.OutcomeReplyInThread || result.Effective.DirectReply != "Glad that helped!" || result.Effective.RequiresFullAgent {
		t.Fatalf("natural social acknowledgement did not use the direct classifier: calls=%d result=%#v", calls, result)
	}
}

func TestDirectSocialReplyPlacementIsNormalizedToItsSlackSurface(t *testing.T) {
	for name, activeThread := range map[string]bool{"top level": false, "active thread": true} {
		t.Run(name, func(t *testing.T) {
			service, err := New(classifierFunc(func(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
				wrong := types.OutcomeReplyInThread
				if activeThread {
					wrong = types.OutcomeReplyInChannel
				}
				return types.ClassificationDecision{
					Outcome: wrong, Confidence: .99, ReasonCodes: []string{"social.natural_acknowledgement"},
					ResponseIntent: "acknowledge naturally", DirectReply: "Happy to help!",
					DisclosureClass: types.DisclosureDestinationSafe, Reaction: "white_check_mark",
				}, nil
			}), false, .9, .98)
			if err != nil {
				t.Fatal(err)
			}
			got := target("Nice work, Tag — thanks for sticking with it.")
			got.ActiveThread = activeThread
			result := service.Decide(context.Background(), got, types.ContextPackRevision{})
			want := types.OutcomeReplyInChannel
			if activeThread {
				want = types.OutcomeReplyInThread
			}
			if result.Effective.Outcome != want || result.Effective.DirectReply != "Happy to help!" || result.Effective.RequiresFullAgent {
				t.Fatalf("direct social placement = %#v, want %s", result, want)
			}
		})
	}
}

func TestActiveThreadSubstantiveReplyUsesClassifierRouting(t *testing.T) {
	calls := 0
	service, err := New(classifierFunc(func(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
		calls++
		return types.ClassificationDecision{
			Outcome: types.OutcomeReplyInChannel, Confidence: .99, ReasonCodes: []string{"action.continue"},
			ResponseIntent: "continue the work", DisclosureClass: types.DisclosureDestinationSafe,
			RequiresFullAgent: true, Reaction: "hammer_and_wrench", AgentModelProfile: "chatgpt-luna-medium",
			AgentModelStrength: "standard", AgentReasoningEffort: "medium",
		}, nil
	}), false, .9, .98)
	if err != nil {
		t.Fatal(err)
	}
	got := target("Please update the configuration and verify it.")
	got.ActiveThread = true
	result := service.Decide(context.Background(), got, types.ContextPackRevision{})
	if calls != 1 || result.Effective.Outcome != types.OutcomeReplyInThread || result.Effective.AgentModelProfile != "chatgpt-luna-medium" || !result.Effective.RequiresFullAgent {
		t.Fatalf("substantive active-thread reply bypassed classifier routing: calls=%d result=%#v", calls, result)
	}
}

func TestMentionedSocialAcknowledgementUsesDirectChannelReply(t *testing.T) {
	got := target("<@U123> thanks!")
	got.Envelope.IsMention = true
	result := newService(t, true).Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeReplyInChannel || result.Effective.DirectReply != "You're welcome!" || result.Effective.RequiresFullAgent || result.Shadowed {
		t.Fatalf("mentioned thanks did not use a direct channel reply: %#v", result)
	}
}

func TestAddressedGreetingUsesDirectChannelReply(t *testing.T) {
	got := target("Morning, Tag. Hope your queues are behaving.")
	result := newService(t, false).Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeReplyInChannel || result.Effective.DirectReply == "" || result.Effective.RequiresFullAgent {
		t.Fatalf("addressed greeting did not use a direct channel reply: %#v", result)
	}
}

func TestStableAmbientMetricUsesReactionOnly(t *testing.T) {
	got := target("Worker memory has held around 84% for the last hour without any errors.")
	result := newService(t, false).Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeReact || result.Effective.Reaction != "warning" || result.Effective.RequiresFullAgent {
		t.Fatalf("stable ambient metric did not stay reaction-only: %#v", result)
	}
}

func TestMixedSocialAndSubstantiveQuestionUsesFullAgent(t *testing.T) {
	got := target("<@U123> Thanks for all the careful work — which durable record should I inspect first when a Slack reply appears twice?")
	got.Envelope.IsMention = true
	result := newService(t, false).Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeReplyInChannel || !result.Effective.RequiresFullAgent || result.Effective.DirectReply != "" {
		t.Fatalf("substantive question used social shortcut: %#v", result)
	}
}

func TestMixedSocialAndSubstantiveImperativeUsesFullAgent(t *testing.T) {
	got := target("<@U123> Thanks again, Tag — tell me which store owns delivery idempotency.")
	got.Envelope.IsMention = true
	result := newService(t, false).Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeReplyInChannel || !result.Effective.RequiresFullAgent || result.Effective.DirectReply != "" {
		t.Fatalf("substantive imperative used social shortcut: %#v", result)
	}
}

func TestDirectReplyCannotAnswerSubstantiveQuestion(t *testing.T) {
	service, err := New(classifierFunc(func(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
		return types.ClassificationDecision{
			Outcome: types.OutcomeReplyInChannel, Confidence: .99, ReasonCodes: []string{"forged.social"}, ResponseIntent: "answer",
			DirectReply: "Everything is operational.", DisclosureClass: types.DisclosureDestinationSafe, Reaction: "speech_balloon",
		}, nil
	}), false, .9, .98)
	if err != nil {
		t.Fatal(err)
	}
	result := service.Decide(context.Background(), target("is production healthy?"), types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeSilent || result.Effective.ReasonCodes[0] != "classifier.invalid_direct_reply" {
		t.Fatalf("substantive question received a classifier-direct reply: %#v", result)
	}
}

func TestStandaloneProductComparisonSurvivesInvalidEvidenceReference(t *testing.T) {
	service, err := New(classifierFunc(func(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
		return types.ClassificationDecision{
			Outcome: types.OutcomeReplyInThread, Confidence: .93, ReasonCodes: []string{"product.comparison"},
			ReleasableEvidenceIDs: []string{"invented/product-source"}, ResponseIntent: "retrieve and compare the named plans",
			DisclosureClass: types.DisclosureDestinationSafe, RequiresFullAgent: true, Reaction: "thinking_face",
			AgentModelProfile: "chatgpt-luna-medium", AgentModelStrength: "standard", AgentReasoningEffort: "medium",
		}, nil
	}), false, .9, .98)
	if err != nil {
		t.Fatal(err)
	}
	result := service.Decide(context.Background(), target("What is the difference between the enterprise and premium billing plans?"), types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeReplyInThread || !result.Effective.RequiresFullAgent || len(result.Effective.ReleasableEvidenceIDs) != 0 || result.Effective.AgentModelProfile != "chatgpt-luna-medium" {
		t.Fatalf("standalone product comparison was discarded: %#v", result)
	}
	if !strings.Contains(strings.Join(result.Effective.ReasonCodes, ","), "policy.invalid_evidence_pruned") {
		t.Fatalf("evidence pruning was not auditable: %#v", result.Effective.ReasonCodes)
	}
}

func TestEvidenceSanitizerPreservesOnlyAuthorizedClasses(t *testing.T) {
	decision := types.ClassificationDecision{
		ReasonCodes:           []string{"test"},
		ReleasableEvidenceIDs: []string{"public/1", "private/1", "missing/1"},
		RestrictedSignalIDs:   []string{"private/1", "public/1", "missing/2"},
	}
	pack := types.ContextPackRevision{Sources: []types.ContextSource{
		{ID: "public/1", DisclosureClass: types.DisclosureDestinationSafe},
		{ID: "private/1", DisclosureClass: types.DisclosureRestrictedAwareness},
	}}
	got := sanitizeEvidenceReferences(decision, pack)
	if len(got.ReleasableEvidenceIDs) != 1 || got.ReleasableEvidenceIDs[0] != "public/1" || len(got.RestrictedSignalIDs) != 1 || got.RestrictedSignalIDs[0] != "private/1" {
		t.Fatalf("sanitized evidence = %#v", got)
	}
}

func TestDirectReplyRejectsSlackAddressingAndLinks(t *testing.T) {
	for _, reply := range []string{"Thanks <@U123>!", "See https://example.com", "*You're welcome!*", "line one\nline two"} {
		if err := validateDirectReplyText(reply); err == nil {
			t.Fatalf("unsafe direct reply was accepted: %q", reply)
		}
	}
}

func TestActiveThreadActionStillStartsAgent(t *testing.T) {
	got := target("Yes, please update the configuration now.")
	got.ActiveThread = true
	result := newService(t, true).Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeReplyInThread || !result.Effective.RequiresFullAgent {
		t.Fatalf("actionable thread reply was suppressed: %#v", result)
	}
}

func TestSelfMessageAndKillSwitchSuppressBeforeClassifier(t *testing.T) {
	for name, mutate := range map[string]func(*Target){
		"self": func(target *Target) { target.SelfAuthored = true },
		"kill": func(target *Target) { target.KillSwitched = true },
	} {
		t.Run(name, func(t *testing.T) {
			got := target("is the system down?")
			mutate(&got)
			result := newService(t, false).Decide(context.Background(), got, types.ContextPackRevision{})
			if result.Effective.Outcome != types.OutcomeSilent {
				t.Fatalf("suppression failed: %#v", result)
			}
		})
	}
}

func TestCrossChannelIncidentIsShadowedButInspectable(t *testing.T) {
	pack := types.ContextPackRevision{Sources: []types.ContextSource{{ID: "alert-1", Partition: types.PartitionEvidence, Text: "active outage", DisclosureClass: types.DisclosureDestinationSafe}}}
	result := newService(t, true).Decide(context.Background(), target("is the system down?"), pack)
	if result.Predicted.Outcome != types.OutcomeReplyInThread || result.Effective.Outcome != types.OutcomeSilent || !result.Shadowed {
		t.Fatalf("bad shadow result: %#v", result)
	}
	if len(result.Predicted.ReleasableEvidenceIDs) != 1 || result.Predicted.ReleasableEvidenceIDs[0] != "alert-1" {
		t.Fatalf("evidence missing: %#v", result)
	}
}

func TestNaturalOperationalLanguageUsesRelevantIncidentContext(t *testing.T) {
	pack := types.ContextPackRevision{Sources: []types.ContextSource{{ID: "alert-1", Partition: types.PartitionEvidence, Text: "Checkout requests are failing with timeouts", DisclosureClass: types.DisclosureDestinationSafe}}}
	for _, message := range []string{
		"Is anyone else seeing checkout fail?",
		"Are customers still able to check out?",
		"Is the API healthy?",
	} {
		result := newService(t, true).Decide(context.Background(), target(message), pack)
		if result.Predicted.Outcome != types.OutcomeReplyInThread || len(result.Predicted.ReleasableEvidenceIDs) != 1 || result.Predicted.ReleasableEvidenceIDs[0] != "alert-1" {
			t.Fatalf("natural operational question %q did not use incident context: %#v", message, result)
		}
	}
}

func TestCrossChannelHumanConflictProducesBriefAlignmentIntervention(t *testing.T) {
	now := time.Now().UTC()
	got := target("Checkout is healthy again.")
	got.Envelope.ChannelID = "support"
	got.Envelope.UserID = "U_ALEX"
	got.Envelope.EventTime = now
	pack := types.ContextPackRevision{Sources: []types.ContextSource{{
		ID: "development/1", ChannelID: "development", ChannelName: "development", AuthorID: "U_TOM",
		Partition: types.PartitionRecentOrg, Provenance: "human_message", Text: "Checkout is still timing out.",
		ObservedAt: now.Add(-time.Minute), DisclosureClass: types.DisclosureDestinationSafe,
	}}}
	result := newService(t, false).Decide(context.Background(), got, pack)
	if result.Effective.Outcome != types.OutcomeReplyInChannel || result.Effective.Reaction != "warning" || len(result.Effective.ReleasableEvidenceIDs) != 1 || result.Effective.ReleasableEvidenceIDs[0] != "development/1" || !strings.Contains(result.Effective.ResponseIntent, "<@U_TOM>") || !strings.Contains(result.Effective.ResponseIntent, "<#development>") {
		t.Fatalf("alignment conflict was not surfaced safely: %#v", result)
	}
}

func TestCrossChannelAlignmentSkipsPresentStaleRestrictedAndOpinionSources(t *testing.T) {
	now := time.Now().UTC()
	got := target("Checkout is healthy again.")
	got.Envelope.ChannelID = "support"
	got.Envelope.UserID = "U_ALEX"
	got.Envelope.EventTime = now
	base := types.ContextSource{ID: "development/1", ChannelID: "development", AuthorID: "U_TOM", Partition: types.PartitionRecentOrg, Provenance: "human_message", Text: "Checkout is still timing out.", ObservedAt: now.Add(-time.Minute), DisclosureClass: types.DisclosureDestinationSafe}
	for name, sources := range map[string][]types.ContextSource{
		"author recently participated here": {base, {ID: "support/tom", ChannelID: "support", AuthorID: "U_TOM", Provenance: "human_message", Text: "I am here", DisclosureClass: types.DisclosureDestinationSafe}},
		"stale report":                      {func() types.ContextSource { item := base; item.ObservedAt = now.Add(-72 * time.Hour); return item }()},
		"restricted report": {func() types.ContextSource {
			item := base
			item.DisclosureClass = types.DisclosureRestrictedAwareness
			return item
		}()},
		"different subject": {func() types.ContextSource { item := base; item.Text = "Login is still timing out."; return item }()},
		"opinion":           {func() types.ContextSource { item := base; item.Text = "I dislike the checkout design."; return item }()},
	} {
		t.Run(name, func(t *testing.T) {
			result := newService(t, false).Decide(context.Background(), got, types.ContextPackRevision{Sources: sources})
			if result.Effective.Outcome != types.OutcomeSilent {
				t.Fatalf("unsafe or unhelpful alignment intervention: %#v", result)
			}
		})
	}
}

func TestRestrictedSignalCannotGroundResponse(t *testing.T) {
	pack := types.ContextPackRevision{Sources: []types.ContextSource{{ID: "secret-alert", Partition: types.PartitionSituation, Text: "active incident", DisclosureClass: types.DisclosureRestrictedAwareness}}}
	result := newService(t, false).Decide(context.Background(), target("is the system down?"), pack)
	if result.Effective.Outcome != types.OutcomeSilent || result.Effective.ReasonCodes[0] != "admission.destination_disclosure_denied" {
		t.Fatalf("restricted source grounded a response: %#v", result)
	}
}

func TestMentionModeSilencesAmbientQuestion(t *testing.T) {
	got := target("can anyone help?")
	got.Mode = types.ModeMention
	result := newService(t, false).Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeSilent {
		t.Fatalf("mention mode spoke ambiently: %#v", result)
	}
}

func TestProactiveModeActsOnActionableStatementInChannel(t *testing.T) {
	got := target("the deployment failed and needs attention")
	got.Mode = types.ModeProactive
	result := newService(t, false).Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeReplyInChannel || result.Effective.Confidence < 0.98 {
		t.Fatalf("proactive statement was not admitted in-channel: %#v", result)
	}
}

func TestObserveModeSilencesDirectMentionAndActiveThread(t *testing.T) {
	for _, activeThread := range []bool{false, true} {
		got := target("@tag help")
		got.Mode = types.ModeObserve
		got.Envelope.IsMention = true
		got.ActiveThread = activeThread
		result := newService(t, false).Decide(context.Background(), got, types.ContextPackRevision{})
		if result.Effective.Outcome != types.OutcomeSilent {
			t.Fatalf("observe mode produced output (active_thread=%v): %#v", activeThread, result)
		}
	}
}

func TestObserveModeRecordsAssistPredictionOnlyInGlobalShadow(t *testing.T) {
	got := target("can anyone help?")
	got.Mode = types.ModeObserve
	result := newService(t, true).Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Predicted.Outcome != types.OutcomeReplyInThread || result.Effective.Outcome != types.OutcomeSilent || !result.Shadowed {
		t.Fatalf("observe shadow prediction was not safely recorded: %#v", result)
	}
	if result.Effective.ReasonCodes[0] != "admission.channel_mode" {
		t.Fatalf("observe authority was not preserved: %#v", result)
	}
}

func TestObserveShadowStillSuppressesSelfAuthoredMessagesBeforeClassification(t *testing.T) {
	got := target("can anyone help?")
	got.Mode = types.ModeObserve
	got.SelfAuthored = true
	result := newService(t, true).Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Predicted.Outcome != types.OutcomeSilent || result.Shadowed || result.Effective.ReasonCodes[0] != "suppress.self_message" {
		t.Fatalf("self-authored observe event reached shadow classification: %#v", result)
	}
}
