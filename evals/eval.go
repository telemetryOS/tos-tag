// Package evals contains deterministic behavioral acceptance evaluations.
package evals

import (
	"context"
	"fmt"
	"time"

	"github.com/telemetryos/tos-tag/core/chatgating"
	"github.com/telemetryos/tos-tag/core/config"
	"github.com/telemetryos/tos-tag/core/contextpacks"
	"github.com/telemetryos/tos-tag/core/observer"
	"github.com/telemetryos/tos-tag/types"
)

type Fixture struct {
	Name                    string
	Target                  chatgating.Target
	Pack                    types.ContextPackRevision
	WantPredicted           types.GatingOutcome
	WantEffective           types.GatingOutcome
	WantReleasableEvidence  bool
	WantRestrictedSafeBlock bool
}

type CaseResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

type Score struct {
	Suite              string       `json:"suite"`
	Passed             int          `json:"passed"`
	Total              int          `json:"total"`
	SpeakPrecision     float64      `json:"speak_precision"`
	SilenceRecall      float64      `json:"silence_recall"`
	EvidenceGrounding  float64      `json:"evidence_grounding"`
	DisclosureSafety   float64      `json:"disclosure_safety"`
	ReplyPlacement     float64      `json:"reply_placement"`
	ContextCap         float64      `json:"context_cap"`
	Dedupe             float64      `json:"dedupe"`
	Threshold          float64      `json:"threshold"`
	ThresholdSatisfied bool         `json:"threshold_satisfied"`
	Cases              []CaseResult `json:"cases"`
	GeneratedAt        time.Time    `json:"generated_at"`
}

func Run() (Score, error) {
	cfg := config.DefaultConfiguration
	gate, err := chatgating.New(chatgating.DeterministicClassifier{}, true, cfg.Gating.AssistThreshold, cfg.Gating.ChannelReplyThreshold)
	if err != nil {
		return Score{}, err
	}
	fixtures := Fixtures()
	score := Score{Suite: "cross-channel-behavior/v2", Total: len(fixtures) + 2, Threshold: 1, GeneratedAt: time.Now().UTC()}
	var speakOK, speakTotal, silenceOK, silenceTotal, evidenceOK, evidenceTotal, disclosureOK, disclosureTotal, placementOK, placementTotal int
	for _, fixture := range fixtures {
		result := gate.Decide(context.Background(), fixture.Target, fixture.Pack)
		passed := result.Predicted.Outcome == fixture.WantPredicted && result.Effective.Outcome == fixture.WantEffective
		detail := ""
		if fixture.WantReleasableEvidence {
			evidenceTotal++
			grounded := len(result.Predicted.ReleasableEvidenceIDs) > 0
			if grounded {
				evidenceOK++
			} else {
				passed = false
			}
		}
		if fixture.WantRestrictedSafeBlock {
			disclosureTotal++
			blocked := result.Predicted.Outcome == types.OutcomeSilent && len(result.Predicted.ReleasableEvidenceIDs) == 0
			if blocked {
				disclosureOK++
			} else {
				passed = false
			}
		}
		wantSpeak := fixture.WantPredicted != types.OutcomeSilent
		if wantSpeak {
			speakTotal++
			if result.Predicted.Outcome != types.OutcomeSilent {
				speakOK++
			}
			placementTotal++
			if result.Predicted.Outcome == types.OutcomeReplyInThread {
				placementOK++
			}
		} else {
			silenceTotal++
			if result.Predicted.Outcome == types.OutcomeSilent {
				silenceOK++
			}
		}
		if !passed {
			detail = fmt.Sprintf("predicted=%s effective=%s", result.Predicted.Outcome, result.Effective.Outcome)
		} else {
			score.Passed++
		}
		score.Cases = append(score.Cases, CaseResult{Name: fixture.Name, Passed: passed, Detail: detail})
	}

	capPassed := contextCap(cfg)
	if capPassed {
		score.Passed++
	}
	score.Cases = append(score.Cases, CaseResult{Name: "context_hard_cap", Passed: capPassed})
	dedupePassed := dedupeCheck()
	if dedupePassed {
		score.Passed++
		score.Dedupe = 1
	}
	score.Cases = append(score.Cases, CaseResult{Name: "observation_dedupe", Passed: dedupePassed})
	score.SpeakPrecision = ratio(speakOK, speakTotal)
	score.SilenceRecall = ratio(silenceOK, silenceTotal)
	score.EvidenceGrounding = ratio(evidenceOK, evidenceTotal)
	score.DisclosureSafety = ratio(disclosureOK, disclosureTotal)
	score.ReplyPlacement = ratio(placementOK, placementTotal)
	if capPassed {
		score.ContextCap = 1
	}
	score.ThresholdSatisfied = score.Passed == score.Total && score.SpeakPrecision >= score.Threshold && score.SilenceRecall >= score.Threshold && score.EvidenceGrounding >= score.Threshold && score.DisclosureSafety >= score.Threshold && score.ReplyPlacement >= score.Threshold && score.ContextCap >= score.Threshold && score.Dedupe >= score.Threshold
	return score, nil
}

func dedupeCheck() bool {
	store := observer.NewMemoryStore(30*24*time.Hour, nil)
	now := time.Now().UTC()
	envelope := types.SlackEnvelope{OrganizationID: "org", TeamID: "team", ChannelID: "channel", EnvelopeID: "envelope", EventID: "event", MessageTS: "1", UserID: "user", Kind: types.SlackEventMessage, Text: "hello", EventTime: now}
	first, err := store.Accept(context.Background(), envelope)
	if err != nil {
		return false
	}
	second, err := store.Accept(context.Background(), envelope)
	return err == nil && !first.Duplicate && second.Duplicate && first.Observation.PublicID == second.Observation.PublicID
}

func Fixtures() []Fixture {
	base := func(text string) chatgating.Target {
		return chatgating.Target{ObservationID: "obs-target", Mode: types.ModeAssist, Envelope: types.SlackEnvelope{OrganizationID: "org", TeamID: "team", ChannelID: "support", MessageTS: "2.0", Text: text, Kind: types.SlackEventMessage}}
	}
	pack := func(sources ...types.ContextSource) types.ContextPackRevision {
		return types.ContextPackRevision{OrganizationID: "org", TargetObservationID: "obs-target", Sources: sources}
	}
	mention := base("<@tos-tag> help")
	mention.Envelope.IsMention = true
	thread := base("one more thing")
	thread.ActiveThread = true
	bot := base("automated message")
	bot.Envelope.BotID = "another-bot"
	return []Fixture{
		{Name: "social_chatter_silent", Target: base("good morning team"), Pack: pack(), WantPredicted: types.OutcomeSilent, WantEffective: types.OutcomeSilent},
		{Name: "direct_mention_thread_reply", Target: mention, Pack: pack(), WantPredicted: types.OutcomeReplyInThread, WantEffective: types.OutcomeReplyInThread},
		{Name: "active_thread_reply", Target: thread, Pack: pack(), WantPredicted: types.OutcomeReplyInThread, WantEffective: types.OutcomeReplyInThread},
		{Name: "ambient_question_shadowed", Target: base("what changed?"), Pack: pack(), WantPredicted: types.OutcomeReplyInThread, WantEffective: types.OutcomeSilent},
		{Name: "alert_to_support", Target: base("is the system down?"), Pack: pack(types.ContextSource{ID: "alerts/1", Partition: types.PartitionEvidence, Text: "Production outage incident is down", DisclosureClass: types.DisclosureDestinationSafe}), WantPredicted: types.OutcomeReplyInThread, WantEffective: types.OutcomeSilent, WantReleasableEvidence: true},
		{Name: "late_alert_reconsideration_context", Target: base("is the system down?"), Pack: pack(types.ContextSource{ID: "alerts/late", Partition: types.PartitionEvidence, Text: "Late arriving incident confirms outage", DisclosureClass: types.DisclosureDestinationSafe}), WantPredicted: types.OutcomeReplyInThread, WantEffective: types.OutcomeSilent, WantReleasableEvidence: true},
		{Name: "restricted_incident_blocked", Target: base("is the system down?"), Pack: pack(types.ContextSource{ID: "private/1", Partition: types.PartitionSituation, Text: "active_incident: true", DisclosureClass: types.DisclosureRestrictedAwareness}), WantPredicted: types.OutcomeSilent, WantEffective: types.OutcomeSilent, WantRestrictedSafeBlock: true},
		{Name: "integration_message_suppressed", Target: bot, Pack: pack(), WantPredicted: types.OutcomeSilent, WantEffective: types.OutcomeSilent},
	}
}

func contextCap(cfg config.Config) bool {
	builder, err := contextpacks.New(cfg.ContextPacks, contextpacks.WordTokenizer{})
	if err != nil {
		return false
	}
	candidates := make([]types.ContextCandidate, 0, 2000)
	for i := 0; i < 2000; i++ {
		candidates = append(candidates, types.ContextCandidate{ID: fmt.Sprintf("source-%d", i), Version: 1, OrganizationID: "org", ChannelID: fmt.Sprintf("channel-%d", i%20), Partition: types.PartitionRecentOrg, Text: "one two three four five six seven eight nine ten", Priority: i, DisclosureClass: types.DisclosureDestinationSafe})
	}
	pack, err := builder.Build(contextpacks.Request{OrganizationID: "org", TargetObservationID: "obs-target", Candidates: candidates})
	return err == nil && pack.TotalTokens <= cfg.ContextPacks.MaxTokens-cfg.ContextPacks.Headroom && pack.PartitionTokens[types.PartitionRecentOrg] <= cfg.ContextPacks.RecentOrg
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 1
	}
	return float64(numerator) / float64(denominator)
}
