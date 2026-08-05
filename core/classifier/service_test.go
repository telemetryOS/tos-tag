package classifier

import (
	"context"
	"errors"
	"slices"
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

func TestActiveThreadThirdPartyAddressIsSuppressedBeforeProvider(t *testing.T) {
	calls := 0
	service, err := New(classifierFunc(func(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
		calls++
		return types.ClassificationDecision{}, errors.New("provider should not be called")
	}), false, 0.9, 0.98)
	if err != nil {
		t.Fatal(err)
	}
	handoff := target("<@U03404W4Z> ^")
	handoff.ActiveThread = true
	if service.RequiresProviderCall(handoff) {
		t.Fatal("third-party handoff would consume a provider call")
	}
	result := service.Decide(context.Background(), handoff, types.ContextPackRevision{})
	if calls != 0 || result.Predicted.Outcome != types.OutcomeSilent || result.Effective.Outcome != types.OutcomeSilent || !slices.Contains(result.Effective.ReasonCodes, "suppress.third_party_address") {
		t.Fatalf("handoff result=%#v provider_calls=%d", result, calls)
	}
}

func TestParticipationRecheckRejectsThirdPartyAddressedTurn(t *testing.T) {
	decision := types.ClassificationDecision{
		Outcome:           types.OutcomeReplyInThread,
		Confidence:        0.99,
		ReasonCodes:       []string{"provider.thread_reply"},
		DisclosureClass:   types.DisclosureDestinationSafe,
		RequiresFullAgent: true,
		Reaction:          "speech_balloon",
	}
	target := Target{Mode: types.ModeAssist, ActiveThread: true, Envelope: types.SlackEnvelope{Text: "<@U03404W4Z> ^"}}
	result := EnforceParticipation(Result{Predicted: decision, Effective: decision}, target, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeSilent || !result.Shadowed || !slices.Contains(result.Effective.ReasonCodes, "policy.unsolicited_assist_work") {
		t.Fatalf("participation recheck allowed third-party address: %#v", result)
	}
}

func TestThirdPartyAddressGatePreservesActiveThreadIntent(t *testing.T) {
	for name, testCase := range map[string]struct {
		target Target
		want   bool
	}{
		"leading handoff marker":      {target: Target{ActiveThread: true, Envelope: types.SlackEnvelope{Text: "<@U03404W4Z> ^"}}, want: true},
		"labeled leading handoff":     {target: Target{ActiveThread: true, Envelope: types.SlackEnvelope{Text: "<@U03404W4Z|alex> ^"}}, want: true},
		"leading direct question":     {target: Target{ActiveThread: true, Envelope: types.SlackEnvelope{Text: " <@U03404W4Z>, can you check this?"}}, want: true},
		"recipient later in request":  {target: Target{ActiveThread: true, Envelope: types.SlackEnvelope{Text: "Summarize this for <@U03404W4Z>."}}, want: false},
		"Tag explicitly co-addressed": {target: Target{ActiveThread: true, Envelope: types.SlackEnvelope{Text: "<@U03404W4Z>, Tag, summarize this for both of us."}}, want: false},
		"Tag directly mentioned":      {target: Target{ActiveThread: true, Envelope: types.SlackEnvelope{Text: "<@U03404W4Z> compare these", IsMention: true}}, want: false},
		"direct message conversation": {target: Target{ActiveThread: true, Envelope: types.SlackEnvelope{ChannelKind: types.SlackChannelKindDirectMessage, Text: "<@U03404W4Z> is the owner"}}, want: false},
		"replayed direct message":     {target: Target{ActiveThread: true, Envelope: types.SlackEnvelope{ChannelID: "D03404W4Z", Text: "<@U03404W4Z> is the owner"}}, want: false},
		"inactive thread":             {target: Target{Envelope: types.SlackEnvelope{Text: "<@U03404W4Z> ^"}}, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := thirdPartyAddressedTurn(testCase.target); got != testCase.want {
				t.Fatalf("thirdPartyAddressedTurn()=%v want %v", got, testCase.want)
			}
		})
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
		"implementation plan":     "<@tos-tag> What would we need to change to add exponential backoff safely?",
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

func TestMentionStrongAgentRecommendationUsesThreadProgressSurface(t *testing.T) {
	service, err := New(classifierFunc(func(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
		return types.ClassificationDecision{
			Outcome:              types.OutcomeReplyInChannel,
			Confidence:           .99,
			ReasonCodes:          []string{"operational.synthesis"},
			DisclosureClass:      types.DisclosureDestinationSafe,
			RequiresFullAgent:    true,
			Reaction:             "thinking_face",
			AgentModelProfile:    "chatgpt-sol-medium",
			AgentModelStrength:   "strong",
			AgentReasoningEffort: "medium",
		}, nil
	}), false, .9, .95)
	if err != nil {
		t.Fatal(err)
	}
	got := target("<@tag> any operational issues?")
	got.Envelope.IsMention = true
	result := service.Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeReplyInThread || !strings.Contains(strings.Join(result.Effective.ReasonCodes, ","), "policy.substantial_agent_thread") {
		t.Fatalf("substantial mentioned work did not receive a threaded progress surface: %#v", result.Effective)
	}
}

func TestMentionStrongThreadRecommendationIsNotDownconvertedAsBrief(t *testing.T) {
	service, err := New(classifierFunc(func(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
		return types.ClassificationDecision{
			Outcome:              types.OutcomeReplyInThread,
			Confidence:           .99,
			ReasonCodes:          []string{"operational.synthesis"},
			DisclosureClass:      types.DisclosureDestinationSafe,
			RequiresFullAgent:    true,
			Reaction:             "rotating_light",
			AgentModelProfile:    "chatgpt-sol-medium",
			AgentModelStrength:   "strong",
			AgentReasoningEffort: "medium",
		}, nil
	}), false, .9, .95)
	if err != nil {
		t.Fatal(err)
	}
	got := target("<@tag> any operational issues?")
	got.Envelope.IsMention = true
	result := service.Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeReplyInThread || strings.Contains(strings.Join(result.Effective.ReasonCodes, ","), "policy.brief_surface_channel") {
		t.Fatalf("strong threaded work was down-converted as brief: %#v", result.Effective)
	}
}

func TestMentionExplicitChannelPlacementOverridesStrongThreadDefault(t *testing.T) {
	service, err := New(classifierFunc(func(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
		return types.ClassificationDecision{
			Outcome:              types.OutcomeReplyInThread,
			Confidence:           .99,
			ReasonCodes:          []string{"operational.synthesis"},
			DisclosureClass:      types.DisclosureDestinationSafe,
			RequiresFullAgent:    true,
			Reaction:             "thinking_face",
			AgentModelProfile:    "chatgpt-sol-medium",
			AgentModelStrength:   "strong",
			AgentReasoningEffort: "medium",
		}, nil
	}), false, .9, .95)
	if err != nil {
		t.Fatal(err)
	}
	got := target("<@tag> any operational issues? Reply in the channel.")
	got.Envelope.IsMention = true
	result := service.Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeReplyInChannel || !strings.Contains(strings.Join(result.Effective.ReasonCodes, ","), "hard.explicit_channel_request") {
		t.Fatalf("explicit channel placement was not preserved: %#v", result.Effective)
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

func TestDirectMentionUsesSafeFallbackForLowConfidenceSocialReply(t *testing.T) {
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
	if result.Effective.Outcome != types.OutcomeReplyInChannel || result.Effective.DirectReply != "You're welcome!" || result.Effective.RequiresFullAgent || result.Effective.ReasonCodes[0] != "policy.social_direct_reply_fallback" {
		t.Fatalf("low-confidence direct social reply did not use the safe fallback: %#v", result)
	}
}

func TestDirectSocialFallbackSurvivesClassifierFailure(t *testing.T) {
	service, err := New(classifierFunc(func(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
		return types.ClassificationDecision{}, errors.New("provider unavailable")
	}), false, .9, .98)
	if err != nil {
		t.Fatal(err)
	}
	got := target("<@tos-tag> nice work!")
	got.Envelope.IsMention = true
	result := service.Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeReplyInChannel || result.Effective.DirectReply != "Thanks!" || result.Effective.Reaction != "white_check_mark" || result.Effective.RequiresFullAgent {
		t.Fatalf("classifier failure dropped a safe social acknowledgement: %#v", result)
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

func TestSelfIntegrationAndKillSwitchSuppressBeforeClassifier(t *testing.T) {
	for name, mutate := range map[string]func(*Target){
		"self":                 func(target *Target) { target.SelfAuthored = true },
		"external bot":         func(target *Target) { target.Envelope.BotID = "B-claude" },
		"assistant app thread": func(target *Target) { target.Envelope.Subtype = types.SlackMessageSubtypeAssistantAppThread },
		"kill":                 func(target *Target) { target.KillSwitched = true },
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

func TestAssistModeBlocksUnsolicitedDeclarativeAgentWork(t *testing.T) {
	background := types.ClassificationDecision{
		Outcome: types.OutcomeStartBackgroundJob, Confidence: .99,
		ReasonCodes: []string{"active_incident"}, ResponseIntent: "investigate the incident",
		DisclosureClass: types.DisclosureDestinationSafe, RequiresFullAgent: true,
		Reaction: "rotating_light", AgentModelProfile: "strong", AgentModelStrength: "strong", AgentReasoningEffort: "medium",
	}
	service, err := New(classifierFunc(func(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error) {
		return background, nil
	}), false, .9, .98)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 2, 18, 30, 0, 0, time.UTC)
	got := target("The orange-cart staging checkout is currently unavailable; incident TEST-427 is active.")
	got.Envelope.ChannelID = "tos-tag"
	got.Envelope.MessageTS = "2.0"
	got.Envelope.UserID = "U_ALEX"
	got.Envelope.EventTime = now
	pack := types.ContextPackRevision{Sources: []types.ContextSource{
		{ID: "tos-tag/2.0", ChannelID: "tos-tag", AuthorID: "U_ALEX", Provenance: "human_message", Text: got.Envelope.Text, ObservedAt: now, DisclosureClass: types.DisclosureDestinationSafe},
		{ID: "tos-tag/1.0", ChannelID: "tos-tag", AuthorID: "U_TAG", Provenance: "agent_output_unverified", Text: "Previous Tag answer", ObservedAt: now.Add(-time.Minute), DisclosureClass: types.DisclosureDestinationSafe},
	}}
	if !likelyConversationallyAddressedToAgent(got, pack) {
		t.Fatal("fixture must prove that Tag speaking last is insufficient authority")
	}
	result := service.Decide(context.Background(), got, pack)
	if result.Predicted.Outcome != types.OutcomeStartBackgroundJob || result.Effective.Outcome != types.OutcomeSilent || !result.Shadowed || result.Effective.ReasonCodes[0] != "policy.unsolicited_assist_work" {
		t.Fatalf("unsolicited assist work was admitted: %#v", result)
	}

	// The pipeline calls the same gate again immediately before admission; the
	// check must be idempotent and preserve the original prediction.
	result = EnforceParticipation(result, got, pack)
	if result.Predicted.Outcome != types.OutcomeStartBackgroundJob || result.Effective.Outcome != types.OutcomeSilent || result.Effective.ReasonCodes[0] != "policy.unsolicited_assist_work" {
		t.Fatalf("participation backstop was not idempotent: %#v", result)
	}
}

func TestAssistModeBlocksUnsolicitedSourceWriteRedirect(t *testing.T) {
	redirect := types.ClassificationDecision{
		Outcome: types.OutcomeReplyInChannel, Confidence: .99,
		ReasonCodes: []string{"policy.source_write_to_linear"}, ResponseIntent: "redirect source writes to Linear",
		DirectReply: sourceWriteRedirectReply, SourceWriteRequested: true,
		DisclosureClass: types.DisclosureDestinationSafe, Reaction: "speech_balloon", AgentModelStrength: "none",
	}
	target := Target{Mode: types.ModeAssist, Envelope: types.SlackEnvelope{
		Text: "Done Today: [TOSF PWA] Report cache usage ENG-3175 — the browser player now reports its cache stats, so the Cache Information card shows hit rate, cache size, and image/video counts instead of blanks; background images are cached and counted as images. Also make sure the remote cache-clear command actually removed the stored media.",
	}}
	result := EnforceParticipation(Result{Predicted: redirect, Effective: redirect}, target, types.ContextPackRevision{})
	if result.Predicted.Outcome != types.OutcomeReplyInChannel || result.Effective.Outcome != types.OutcomeSilent || !result.Shadowed || !slices.Contains(result.Effective.ReasonCodes, "policy.unsolicited_assist_work") {
		t.Fatalf("unsolicited source-write redirect was admitted: %#v", result)
	}
}

func TestAssistInitiativeRequiresARealInvocationSignal(t *testing.T) {
	decision := types.ClassificationDecision{
		Outcome: types.OutcomeStartBackgroundJob, Confidence: .99,
		ReasonCodes: []string{"work"}, ResponseIntent: "do the requested work",
		DisclosureClass: types.DisclosureDestinationSafe, RequiresFullAgent: true, Reaction: "eyes",
	}
	baseResult := Result{Predicted: decision, Effective: decision}
	now := time.Date(2026, 8, 2, 18, 35, 0, 0, time.UTC)
	conversation := types.ContextPackRevision{Sources: []types.ContextSource{
		{ID: "tos-tag/2.0", ChannelID: "tos-tag", AuthorID: "U_ALEX", Provenance: "human_message", Text: "Investigate the checkout failures.", ObservedAt: now, DisclosureClass: types.DisclosureDestinationSafe},
		{ID: "tos-tag/1.0", ChannelID: "tos-tag", AuthorID: "U_TAG", Provenance: "agent_output_unverified", Text: "Previous Tag answer", ObservedAt: now.Add(-time.Minute), DisclosureClass: types.DisclosureDestinationSafe},
	}}
	for name, candidate := range map[string]Target{
		"direct mention": {
			Mode: types.ModeAssist, Envelope: types.SlackEnvelope{IsMention: true, Text: "<@tag> checkout is unavailable."},
		},
		"active thread": {
			Mode: types.ModeAssist, ActiveThread: true, Envelope: types.SlackEnvelope{Text: "Checkout is unavailable."},
		},
		"addressed declaration": {
			Mode: types.ModeAssist, Envelope: types.SlackEnvelope{Text: "Tag, checkout is unavailable."},
		},
		"conversational request": {
			Mode: types.ModeAssist, Envelope: types.SlackEnvelope{ChannelID: "tos-tag", MessageTS: "2.0", UserID: "U_ALEX", Text: "Investigate the checkout failures.", EventTime: now},
		},
		"authorized trigger": {
			Mode: types.ModeAssist, AuthorizedTrigger: true, Envelope: types.SlackEnvelope{Text: "Check whether checkout needs attention."},
		},
		"proactive declaration": {
			Mode: types.ModeProactive, Envelope: types.SlackEnvelope{Text: "Checkout is unavailable."},
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := EnforceParticipation(baseResult, candidate, conversation)
			if result.Effective.Outcome != types.OutcomeStartBackgroundJob {
				t.Fatalf("authorized initiative was suppressed: %#v", result)
			}
		})
	}

	product := decision
	product.ProductRetrievalRequired = true
	productTarget := Target{Mode: types.ModeAssist, Envelope: types.SlackEnvelope{Text: "What is the Premium Trial about"}}
	if result := EnforceParticipation(Result{Predicted: product, Effective: product}, productTarget, types.ContextPackRevision{}); result.Effective.Outcome != types.OutcomeStartBackgroundJob {
		t.Fatalf("ambient product question was suppressed: %#v", result)
	}
}

func TestQuestionGrantRejectsURLAndRepeatedPunctuation(t *testing.T) {
	for name, testCase := range map[string]struct {
		text string
		want bool
	}{
		"interrogative prefix":     {text: "Can you check checkout", want: true},
		"single terminal question": {text: "Checkout is unavailable?", want: true},
		"embedded question":        {text: "Can anyone help? Checkout is unavailable.", want: true},
		"bare declaration":         {text: "Checkout is unavailable.", want: false},
		"repeated punctuation":     {text: "Checkout is unavailable??", want: false},
		"plain URL query":          {text: "Checkout is unavailable; status: https://status.example/incidents?id=427", want: false},
		"Slack URL query":          {text: "Checkout is unavailable; status: <https://status.example/incidents?id=427|incident 427>", want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := looksLikeQuestion(testCase.text); got != testCase.want {
				t.Fatalf("looksLikeQuestion(%q)=%v want %v", testCase.text, got, testCase.want)
			}
		})
	}
}

func TestExplicitTagAddressRequiresVocativeUse(t *testing.T) {
	for name, testCase := range map[string]struct {
		text string
		want bool
	}{
		"leading punctuation":           {text: "Tag, checkout is unavailable.", want: true},
		"leading natural question":      {text: "Tag can you check checkout", want: true},
		"leading greeting":              {text: "Hey Tag: check checkout", want: true},
		"mid-sentence greeting":         {text: "Morning, Tag. Hope your queues are behaving.", want: true},
		"mid-sentence praise":           {text: "Nice work, Tag — thanks for sticking with it.", want: true},
		"co-address after user":         {text: "<@U03404W4Z>, Tag, summarize this for both of us.", want: true},
		"co-address after labeled user": {text: "<@U03404W4Z|alex>, Tag, summarize this for both of us.", want: true},
		"trailing vocative":             {text: "Checkout is unavailable, Tag!", want: true},
		"unpunctuated social address":   {text: "Thanks Tag!", want: true},
		"colon request":                 {text: "Tag: can you check checkout", want: true},
		"ordinary noun":                 {text: "Add the incident tag; checkout is unavailable.", want: false},
		"ordinary trailing noun":        {text: "Apply the incident tag.", want: false},
		"ordinary noun in list":         {text: "The required fields are owner, tag, priority.", want: false},
		"field label":                   {text: "Tag: incident", want: false},
		"embedded product name":         {text: "The tagging service is unavailable.", want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := explicitlyAddressesTag(testCase.text); got != testCase.want {
				t.Fatalf("explicitlyAddressesTag(%q)=%v want %v", testCase.text, got, testCase.want)
			}
		})
	}
}

func TestAssistRejectsMalformedSignalsThatProactiveMayActOn(t *testing.T) {
	decision := types.ClassificationDecision{
		Outcome: types.OutcomeStartBackgroundJob, Confidence: .99,
		ReasonCodes: []string{"active_incident"}, ResponseIntent: "investigate the incident",
		DisclosureClass: types.DisclosureDestinationSafe, RequiresFullAgent: true,
		Reaction: "rotating_light", AgentModelProfile: "standard", AgentModelStrength: "standard", AgentReasoningEffort: "medium",
	}
	for _, text := range []string{
		"Checkout is unavailable??",
		"Checkout is unavailable; status: https://status.example/incidents?id=427",
		"Checkout is unavailable; status: <https://status.example/incidents?id=427|incident 427>",
		"Add the incident tag; checkout is unavailable.",
	} {
		t.Run(text, func(t *testing.T) {
			assist := Target{Mode: types.ModeAssist, Envelope: types.SlackEnvelope{Text: text}}
			assistResult := EnforceParticipation(Result{Predicted: decision, Effective: decision}, assist, types.ContextPackRevision{})
			if assistResult.Effective.Outcome != types.OutcomeSilent || !assistResult.Shadowed || !slices.Contains(assistResult.Effective.ReasonCodes, "policy.unsolicited_assist_work") {
				t.Fatalf("malformed assist signal was admitted: %#v", assistResult)
			}

			proactive := assist
			proactive.Mode = types.ModeProactive
			proactiveResult := EnforceParticipation(Result{Predicted: decision, Effective: decision}, proactive, types.ContextPackRevision{})
			if proactiveResult.Effective.Outcome != types.OutcomeStartBackgroundJob || proactiveResult.Shadowed {
				t.Fatalf("proactive incident initiative was suppressed: %#v", proactiveResult)
			}
		})
	}
}

func TestTimeProximateConversationalFollowupRequestIsAdmitted(t *testing.T) {
	decision := types.ClassificationDecision{
		Outcome: types.OutcomeReplyInChannel, Confidence: .99,
		ReasonCodes: []string{"likely_addressed_to_agent"}, ResponseIntent: "look up the requested pricing source and answer",
		DisclosureClass: types.DisclosureDestinationSafe, RequiresFullAgent: true, Reaction: "speech_balloon",
		AgentModelProfile: "standard", AgentModelStrength: "standard", AgentReasoningEffort: "medium",
	}
	now := time.Date(2026, 8, 3, 21, 21, 15, 0, time.UTC)
	target := Target{Mode: types.ModeAssist, Envelope: types.SlackEnvelope{
		ChannelID: "tos-tag", MessageTS: "3.0", UserID: "U_ALEX",
		Text: "Take a look at OpenAI pricing page for Luna", EventTime: now,
	}}
	pack := types.ContextPackRevision{Sources: []types.ContextSource{
		{ID: "tos-tag/3.0", ChannelID: "tos-tag", AuthorID: "U_ALEX", Provenance: "human_message", Text: target.Envelope.Text, ObservedAt: now, DisclosureClass: types.DisclosureDestinationSafe},
		{ID: "tos-tag/2.0", ChannelID: "tos-tag", AuthorID: "U_TAG", Provenance: "agent_output_unverified", Text: "Share the applicable Luna rate and I can calculate it immediately.", ObservedAt: now.Add(-time.Minute), DisclosureClass: types.DisclosureDestinationSafe},
		{ID: "tos-tag/1.0", ChannelID: "tos-tag", AuthorID: "U_ALEX", Provenance: "human_message", Text: "<@U_TAG> what would 100k Luna low tokens cost?", ObservedAt: now.Add(-2 * time.Minute), DisclosureClass: types.DisclosureDestinationSafe},
	}}
	if !likelyConversationallyAddressedToAgent(target, pack) {
		t.Fatal("fixture must be a recent continuation of Tag's immediately preceding turn")
	}
	result := EnforceParticipation(Result{Predicted: decision, Effective: decision}, target, pack)
	if result.Effective.Outcome != types.OutcomeReplyInChannel || result.Shadowed || len(result.Effective.ReasonCodes) == 0 || result.Effective.ReasonCodes[0] != "likely_addressed_to_agent" {
		t.Fatalf("time-proximate follow-up request was suppressed: %#v", result)
	}
	stale := pack
	stale.Sources = append([]types.ContextSource(nil), pack.Sources...)
	stale.Sources[1].ObservedAt = now.Add(-16 * time.Minute)
	if likelyConversationallyAddressedToAgent(target, stale) {
		t.Fatal("stale Tag turn was treated as the same conversation")
	}
	staleResult := EnforceParticipation(Result{Predicted: decision, Effective: decision}, target, stale)
	if staleResult.Effective.Outcome != types.OutcomeSilent || !staleResult.Shadowed {
		t.Fatalf("stale follow-up request bypassed assist initiative policy: %#v", staleResult)
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

func TestServiceRequiresProviderCall(t *testing.T) {
	live := newService(t, false)
	if live.RequiresProviderCall(Target{Mode: types.ModeObserve}) {
		t.Fatal("live observe-only target should not call provider")
	}
	if !live.RequiresProviderCall(Target{Mode: types.ModeAssist}) {
		t.Fatal("assist target should call provider")
	}
	if live.RequiresProviderCall(Target{Mode: types.ModeAssist, SelfAuthored: true}) {
		t.Fatal("hard-suppressed target should not call provider")
	}
	if live.RequiresProviderCall(Target{Mode: types.ModeAssist, Envelope: types.SlackEnvelope{BotID: "B-claude"}}) {
		t.Fatal("external agent target should not call provider")
	}
	if live.RequiresProviderCall(Target{Mode: types.ModeAssist, Envelope: types.SlackEnvelope{Subtype: types.SlackMessageSubtypeAssistantAppThread}}) {
		t.Fatal("assistant app target should not call provider")
	}
	if live.RequiresProviderCall(Target{Mode: types.ModeAssist, AmbientLinkOnly: true}) {
		t.Fatal("ambient link-only target should not call provider")
	}
	shadow := newService(t, true)
	if !shadow.RequiresProviderCall(Target{Mode: types.ModeObserve}) {
		t.Fatal("shadow observe target should call provider for measurement")
	}
}
