package evals

import (
	"context"
	"fmt"
	"strings"
	"time"

	tagcore "github.com/telemetryos/tos-tag/core"
	"github.com/telemetryos/tos-tag/core/classifier"
	"github.com/telemetryos/tos-tag/core/config"
	"github.com/telemetryos/tos-tag/core/modelrouter"
	"github.com/telemetryos/tos-tag/types"
)

// RunLive evaluates the production classifier implementation against the same
// naturalistic fixtures as the deterministic gate. Expected outcomes live only
// in the scorer; OpenAI receives Target and ContextPackRevision data through the
// normal classifier contract and never receives fixture names or expectations.
func RunLive(ctx context.Context, cfg config.Config) (Score, error) {
	if cfg.Classifier.Provider != "openai" {
		return Score{}, fmt.Errorf("live eval requires classifier provider openai")
	}
	provider, err := classifier.NewOpenAIClassifier(classifier.OpenAIOptions{
		BaseURL:         cfg.Classifier.BaseURL,
		APIKey:          cfg.Classifier.OpenAIAPIKey,
		Model:           cfg.Classifier.Model,
		ReasoningEffort: cfg.Classifier.ReasoningEffort,
		Timeout:         cfg.Classifier.Timeout,
		MaxOutputTokens: cfg.Classifier.MaxOutputTokens,
		ReactionEmojis:  cfg.Classifier.ReactionEmojis,
		AgentProfiles:   evalProfileSource{snapshot: modelrouter.Snapshot{PolicyRevision: "live-eval/v1", DeploymentDefault: cfg.Models.DefaultProfile, Profiles: tagcore.DefaultResponseProfiles(cfg.Models)}},
	})
	if err != nil {
		return Score{}, err
	}
	recorder := &recordingClassifier{next: provider}
	gate, err := classifier.New(recorder, cfg.Classifier.Mode == "shadow", cfg.Classifier.AssistThreshold, cfg.Classifier.ChannelReplyThreshold)
	if err != nil {
		return Score{}, err
	}

	fixtures := Fixtures()
	score := Score{Suite: "cross-channel-behavior-live/v3", Total: len(fixtures) + 2, Threshold: 1, GeneratedAt: time.Now().UTC()}
	var speakOK, speakTotal, silenceOK, silenceTotal, evidenceOK, evidenceTotal, disclosureOK, disclosureTotal, placementOK, placementTotal int
	var routingOK, routingTotal, reactionOK, reactionTotal int
	var totalLatency time.Duration

	for index, fixture := range fixtures {
		if err := validateNaturalisticFixture(fixture); err != nil {
			return Score{}, err
		}
		target := fixture.Target
		target.ObservationID = fmt.Sprintf("live-eval-%02d", index+1)
		pack := fixture.Pack
		pack.TargetObservationID = target.ObservationID

		recorder.reset()
		started := time.Now()
		result := gate.Decide(ctx, target, pack)
		latency := time.Since(started)
		totalLatency += latency
		providerDecision, providerCalled, providerErr := recorder.observation()

		wantPredicted := fixture.LivePredicted
		if len(wantPredicted) == 0 {
			wantPredicted = []types.ClassificationOutcome{fixture.WantPredicted}
		}
		wantEffective := fixture.LiveEffective
		if len(wantEffective) == 0 {
			wantEffective = wantPredicted
		}

		passed := containsOutcome(wantPredicted, result.Predicted.Outcome) && containsOutcome(wantEffective, result.Effective.Outcome)
		var failures []string
		if !containsOutcome(wantPredicted, result.Predicted.Outcome) {
			failures = append(failures, fmt.Sprintf("predicted=%s", result.Predicted.Outcome))
		}
		if !containsOutcome(wantEffective, result.Effective.Outcome) {
			failures = append(failures, fmt.Sprintf("effective=%s", result.Effective.Outcome))
		}
		if fixture.SkipLiveProviderCall {
			if providerCalled {
				passed = false
				failures = append(failures, "provider_called=true")
			}
		} else {
			if !providerCalled || providerErr != nil {
				passed = false
				failure := "provider_call_failed"
				if providerErr != nil {
					failure += "=" + classifier.ErrorStage(providerErr) + "/" + classifier.ErrorCode(providerErr)
				}
				failures = append(failures, failure)
			} else {
				score.ProviderCalls++
			}
		}

		if fixture.WantReleasableEvidence {
			evidenceTotal++
			if len(result.Predicted.ReleasableEvidenceIDs) > 0 {
				evidenceOK++
			} else {
				passed = false
				failures = append(failures, "missing_releasable_evidence")
			}
		}
		if fixture.WantRestrictedSafeBlock {
			disclosureTotal++
			if result.Predicted.Outcome == types.OutcomeSilent && len(result.Predicted.ReleasableEvidenceIDs) == 0 {
				disclosureOK++
			} else {
				passed = false
				failures = append(failures, "restricted_disclosure_not_blocked")
			}
		}
		optionalNoReply := len(wantPredicted) > 1 && (result.Predicted.Outcome == types.OutcomeSilent || result.Predicted.Outcome == types.OutcomeReact)
		if fixture.WantDirectReply && !optionalNoReply && (result.Predicted.DirectReply == "" || result.Predicted.RequiresFullAgent) {
			passed = false
			failures = append(failures, "direct_reply_contract_failed")
		}
		if fixture.WantFullAgent && (!result.Predicted.RequiresFullAgent || result.Predicted.DirectReply != "") {
			passed = false
			failures = append(failures, "full_agent_contract_failed")
		}
		if fixture.WantSourceWriteRedirect && (!result.Predicted.SourceWriteRequested || !strings.Contains(result.Predicted.DirectReply, "Linear bug") || !strings.Contains(result.Predicted.DirectReply, "Linear feature")) {
			passed = false
			failures = append(failures, "source_write_redirect_contract_failed")
		}
		if fixture.WantProductRetrieval && (!result.Predicted.ProductRetrievalRequired || !result.Predicted.RequiresFullAgent) {
			passed = false
			failures = append(failures, "product_retrieval_contract_failed")
		}
		if fixture.ForbidSourceRedirect && result.Predicted.SourceWriteRequested {
			passed = false
			failures = append(failures, "read_only_analysis_misclassified_as_write")
		}

		if len(fixture.WantLiveReactions) != 0 && result.Predicted.Outcome != types.OutcomeSilent {
			reactionTotal++
			actual := result.Predicted.Reaction
			if providerCalled && providerDecision.Reaction != "" {
				actual = providerDecision.Reaction
			}
			if containsString(fixture.WantLiveReactions, actual) {
				reactionOK++
			} else {
				passed = false
				failures = append(failures, "reaction="+actual)
			}
		}
		if len(fixture.WantLiveRoutes) != 0 {
			routingTotal++
			if providerCalled && containsRoute(fixture.WantLiveRoutes, providerDecision.AgentModelStrength, providerDecision.AgentReasoningEffort) {
				routingOK++
			} else {
				passed = false
				failures = append(failures, fmt.Sprintf("routing=%s/%s", providerDecision.AgentModelStrength, providerDecision.AgentReasoningEffort))
			}
		}
		if providerCalled && providerDecision.Outcome == types.OutcomeStartBackgroundJob {
			routingTotal++
			if providerDecision.AgentModelStrength == "standard" || providerDecision.AgentModelStrength == "strong" {
				routingOK++
			} else {
				passed = false
				failures = append(failures, "background_job_routed_below_standard")
			}
		}

		optionalAction := containsOutcome(wantPredicted, types.OutcomeSilent) && !allOutcomesSilent(wantPredicted)
		if optionalAction {
			// A social acknowledgement is intentionally discretionary. Its chosen
			// action still has to satisfy the outcome, reaction, and direct-reply
			// contracts, but it is neither a speaking nor a silence recall case.
		} else if !allOutcomesSilent(wantPredicted) {
			speakTotal++
			if result.Predicted.Outcome != types.OutcomeSilent {
				speakOK++
			}
			if outcomesSpecifyPlacement(wantPredicted) {
				placementTotal++
				if containsOutcome(wantPredicted, result.Predicted.Outcome) {
					placementOK++
				}
			}
		} else {
			silenceTotal++
			if result.Predicted.Outcome == types.OutcomeSilent {
				silenceOK++
			}
		}

		if passed {
			score.Passed++
		}
		caseDecision := result.Predicted
		if providerCalled {
			caseDecision = providerDecision
		}
		score.Cases = append(score.Cases, CaseResult{
			Name: fixture.Name, Passed: passed, Detail: strings.Join(failures, ", "),
			Predicted: result.Predicted.Outcome, Effective: result.Effective.Outcome,
			Reaction: caseDecision.Reaction, AgentModelStrength: caseDecision.AgentModelStrength,
			AgentReasoningEffort:  caseDecision.AgentReasoningEffort,
			ClassifierInputTokens: caseDecision.ClassifierInputTokens, ClassifierOutputTokens: caseDecision.ClassifierOutputTokens,
			LatencyMilliseconds: latency.Milliseconds(),
			ReasonCodes:         append([]string(nil), result.Predicted.ReasonCodes...),
			ProviderReasonCodes: append([]string(nil), providerDecision.ReasonCodes...),
		})
	}

	capPassed := contextCap(cfg)
	if capPassed {
		score.Passed++
		score.ContextCap = 1
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
	score.ModelRouting = ratio(routingOK, routingTotal)
	score.ReactionSemantics = ratio(reactionOK, reactionTotal)
	if len(fixtures) > 0 {
		score.MeanLatencyMS = float64(totalLatency.Microseconds()) / 1000 / float64(len(fixtures))
	}
	score.ThresholdSatisfied = score.Passed == score.Total && score.SpeakPrecision >= score.Threshold && score.SilenceRecall >= score.Threshold && score.EvidenceGrounding >= score.Threshold && score.DisclosureSafety >= score.Threshold && score.ReplyPlacement >= score.Threshold && score.ModelRouting >= score.Threshold && score.ReactionSemantics >= score.Threshold && score.ContextCap >= score.Threshold && score.Dedupe >= score.Threshold
	return score, nil
}

type recordingClassifier struct {
	next     classifier.Classifier
	called   bool
	decision types.ClassificationDecision
	err      error
}

func (r *recordingClassifier) Decide(ctx context.Context, target classifier.Target, pack types.ContextPackRevision) (types.ClassificationDecision, error) {
	r.called = true
	r.decision, r.err = r.next.Decide(ctx, target, pack)
	return r.decision, r.err
}

func (r *recordingClassifier) reset() {
	r.called = false
	r.decision = types.ClassificationDecision{}
	r.err = nil
}

func (r *recordingClassifier) observation() (types.ClassificationDecision, bool, error) {
	return r.decision, r.called, r.err
}

type evalProfileSource struct{ snapshot modelrouter.Snapshot }

func (s evalProfileSource) Snapshot() modelrouter.Snapshot { return s.snapshot }

func containsOutcome(values []types.ClassificationOutcome, value types.ClassificationOutcome) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func containsRoute(values []LiveRoute, strength, effort string) bool {
	for _, candidate := range values {
		if candidate.Strength == strength && candidate.Effort == effort {
			return true
		}
	}
	return false
}

func allOutcomesSilent(values []types.ClassificationOutcome) bool {
	for _, value := range values {
		if value != types.OutcomeSilent {
			return false
		}
	}
	return true
}

func outcomesSpecifyPlacement(values []types.ClassificationOutcome) bool {
	for _, value := range values {
		if value == types.OutcomeReplyInThread || value == types.OutcomeReplyInChannel {
			return true
		}
	}
	return false
}
