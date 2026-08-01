package classifier

import (
	"context"
	"testing"

	"github.com/telemetryos/tos-tag/types"
)

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

func TestActiveThreadSocialAcknowledgementDoesNotStartAgent(t *testing.T) {
	got := target("Thanks!")
	got.ActiveThread = true
	result := newService(t, true).Decide(context.Background(), got, types.ContextPackRevision{})
	if result.Effective.Outcome != types.OutcomeSilent || result.Effective.ReasonCodes[0] != "thread.social_acknowledgement" || result.Shadowed {
		t.Fatalf("social acknowledgement started work: %#v", result)
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
