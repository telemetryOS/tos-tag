// Package classifier implements tool-free response selection and deterministic
// admission. It cannot send Slack output or execute tools.
package classifier

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/telemetryos/tos-tag/types"
)

var ErrInvalidClassifierDecision = errors.New("invalid classifier decision")

type Classifier interface {
	Decide(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error)
}

type Target struct {
	ObservationID string
	Envelope      types.SlackEnvelope
	Mode          types.ParticipationMode
	ActiveThread  bool
	KillSwitched  bool
	WorkflowLoop  bool
	Unsupported   bool
	Deleted       bool
	SelfAuthored  bool
}

type Result struct {
	Predicted types.ClassificationDecision `json:"predicted"`
	Effective types.ClassificationDecision `json:"effective"`
	Shadowed  bool                         `json:"shadowed"`
}

type Service struct {
	classifier            Classifier
	shadow                bool
	assistThreshold       float64
	channelReplyThreshold float64
}

func New(classifier Classifier, shadow bool, assistThreshold, channelReplyThreshold float64) (*Service, error) {
	if classifier == nil {
		return nil, fmt.Errorf("classifier is required")
	}
	if assistThreshold < 0 || assistThreshold > 1 || channelReplyThreshold < assistThreshold || channelReplyThreshold > 1 {
		return nil, fmt.Errorf("invalid classifier thresholds")
	}
	return &Service{classifier: classifier, shadow: shadow, assistThreshold: assistThreshold, channelReplyThreshold: channelReplyThreshold}, nil
}

func (s *Service) Decide(ctx context.Context, target Target, pack types.ContextPackRevision) Result {
	if reason := hardSuppression(target); reason != "" {
		decision := silent(reason)
		return Result{Predicted: decision, Effective: decision}
	}
	// Observe mode is always an absolute no-output policy. With global shadow
	// classification enabled, evaluate the same candidate an assist channel would have
	// produced so operators can measure precision without expanding authority.
	if target.Mode == types.ModeObserve {
		if s.shadow {
			shadowTarget := target
			shadowTarget.Mode = types.ModeAssist
			predicted := s.predict(ctx, shadowTarget, pack)
			return Result{Predicted: predicted, Effective: silent("admission.channel_mode"), Shadowed: predicted.Outcome != types.OutcomeSilent}
		}
		decision := silent("admission.channel_mode")
		return Result{Predicted: decision, Effective: decision}
	}
	predicted := s.predict(ctx, target, pack)
	if target.Envelope.IsMention || target.ActiveThread || predicted.Outcome == types.OutcomeSilent {
		return Result{Predicted: predicted, Effective: predicted}
	}
	if s.shadow {
		return Result{Predicted: predicted, Effective: silent("admission.shadow_mode"), Shadowed: true}
	}
	return Result{Predicted: predicted, Effective: predicted}
}

func (s *Service) predict(ctx context.Context, target Target, pack types.ContextPackRevision) types.ClassificationDecision {
	if target.Envelope.IsMention || target.ActiveThread {
		reason := "hard.direct_mention"
		if target.ActiveThread && !target.Envelope.IsMention {
			reason = "hard.active_thread_reply"
		}
		decision := types.ClassificationDecision{
			Outcome:           types.OutcomeReplyInThread,
			Confidence:        1,
			ReasonCodes:       []string{reason},
			ResponseIntent:    "respond to the explicit request",
			DisclosureClass:   types.DisclosureDestinationSafe,
			RequiresFullAgent: true,
			Reaction:          "eyes",
		}
		return decision
	}
	if target.Mode == types.ModeMention {
		return silent("admission.channel_mode")
	}

	predicted, err := s.classifier.Decide(ctx, target, pack)
	if err != nil {
		return silent("classifier.error")
	}
	if err := validateDecision(predicted, pack); err != nil {
		return silent("classifier.invalid")
	}
	if predicted.Outcome != types.OutcomeSilent && predicted.Confidence < s.assistThreshold {
		predicted = silent("admission.low_confidence")
	}
	if predicted.Outcome == types.OutcomeReplyInChannel && predicted.Confidence < s.channelReplyThreshold {
		predicted.Outcome = types.OutcomeReplyInThread
		predicted.ReasonCodes = append(predicted.ReasonCodes, "admission.channel_reply_threshold")
	}
	if outcomeNeedsEvidence(predicted.Outcome) && len(predicted.ReleasableEvidenceIDs) == 0 && len(predicted.RestrictedSignalIDs) > 0 {
		predicted = silent("admission.destination_disclosure_denied")
	}
	return predicted
}

func hardSuppression(target Target) string {
	switch {
	case target.KillSwitched:
		return "suppress.kill_switch"
	case target.SelfAuthored:
		return "suppress.self_message"
	case target.WorkflowLoop || target.Envelope.OriginTag != "":
		return "suppress.workflow_loop"
	case target.Deleted || target.Envelope.Kind == types.SlackEventDelete:
		return "suppress.deleted"
	case target.Unsupported:
		return "suppress.unsupported_subtype"
	case target.Envelope.BotID != "":
		return "suppress.integration_message"
	default:
		return ""
	}
}

func validateDecision(decision types.ClassificationDecision, pack types.ContextPackRevision) error {
	if decision.Confidence < 0 || decision.Confidence > 1 {
		return fmt.Errorf("%w: confidence", ErrInvalidClassifierDecision)
	}
	switch decision.Outcome {
	case types.OutcomeSilent, types.OutcomeReact, types.OutcomeReplyInThread, types.OutcomeReplyInChannel, types.OutcomeStartBackgroundJob, types.OutcomeEscalateForApproval:
	default:
		return fmt.Errorf("%w: outcome", ErrInvalidClassifierDecision)
	}
	if len(decision.ReasonCodes) == 0 {
		return fmt.Errorf("%w: reason code", ErrInvalidClassifierDecision)
	}
	if decision.Outcome != types.OutcomeSilent && !validEmojiName(decision.Reaction) {
		return fmt.Errorf("%w: reaction", ErrInvalidClassifierDecision)
	}
	if outcomeNeedsAgent(decision.Outcome) && !decision.RequiresFullAgent {
		return fmt.Errorf("%w: full agent required", ErrInvalidClassifierDecision)
	}
	available := make(map[string]types.DisclosureClass, len(pack.Sources))
	for _, source := range pack.Sources {
		available[source.ID] = source.DisclosureClass
	}
	for _, id := range decision.ReleasableEvidenceIDs {
		if available[id] != types.DisclosureDestinationSafe {
			return fmt.Errorf("%w: releasable evidence %s", ErrInvalidClassifierDecision, id)
		}
	}
	for _, id := range decision.RestrictedSignalIDs {
		if available[id] != types.DisclosureRestrictedAwareness {
			return fmt.Errorf("%w: restricted signal %s", ErrInvalidClassifierDecision, id)
		}
	}
	return nil
}

func silent(reason string) types.ClassificationDecision {
	return types.ClassificationDecision{
		Outcome:         types.OutcomeSilent,
		Confidence:      1,
		ReasonCodes:     []string{reason},
		DisclosureClass: types.DisclosureDestinationSafe,
	}
}

// Suppress preserves the classifier prediction while applying an explainable
// hard admission denial to the effective action.
func Suppress(result Result, reason string) Result {
	result.Effective = silent(reason)
	result.Shadowed = result.Predicted.Outcome != types.OutcomeSilent
	return result
}

func outcomeNeedsEvidence(outcome types.ClassificationOutcome) bool {
	return outcome == types.OutcomeReplyInThread || outcome == types.OutcomeReplyInChannel || outcome == types.OutcomeStartBackgroundJob
}

func outcomeNeedsAgent(outcome types.ClassificationOutcome) bool {
	return outcome == types.OutcomeReplyInThread || outcome == types.OutcomeReplyInChannel || outcome == types.OutcomeStartBackgroundJob || outcome == types.OutcomeEscalateForApproval
}

type DeterministicClassifier struct{}

func (DeterministicClassifier) Decide(_ context.Context, target Target, pack types.ContextPackRevision) (types.ClassificationDecision, error) {
	lower := strings.ToLower(target.Envelope.Text)
	if target.Mode == types.ModeProactive && containsActionableSignal(lower) {
		return types.ClassificationDecision{
			Outcome:           types.OutcomeReplyInChannel,
			Confidence:        0.99,
			ReasonCodes:       []string{"ambient.proactive_actionable_signal"},
			ResponseIntent:    "offer help on the actionable channel signal",
			DisclosureClass:   types.DisclosureDestinationSafe,
			RequiresFullAgent: true,
			Reaction:          "eyes",
		}, nil
	}
	if strings.Contains(lower, "is the system down") || strings.Contains(lower, "is it down") {
		for _, source := range pack.Sources {
			if source.Partition != types.PartitionEvidence && source.Partition != types.PartitionSituation {
				continue
			}
			if source.DisclosureClass == types.DisclosureDestinationSafe && containsIncident(source.Text) {
				return types.ClassificationDecision{
					Outcome:               types.OutcomeReplyInThread,
					Confidence:            0.99,
					ReasonCodes:           []string{"ambient.cross_channel_incident_match"},
					ReleasableEvidenceIDs: []string{source.ID},
					ResponseIntent:        "answer with the current incident evidence",
					DisclosureClass:       types.DisclosureDestinationSafe,
					RequiresFullAgent:     true,
					Reaction:              "rotating_light",
				}, nil
			}
			if source.DisclosureClass == types.DisclosureRestrictedAwareness && containsIncident(source.Text) {
				return types.ClassificationDecision{
					Outcome:             types.OutcomeReplyInThread,
					Confidence:          0.99,
					ReasonCodes:         []string{"ambient.cross_channel_incident_match"},
					RestrictedSignalIDs: []string{source.ID},
					ResponseIntent:      "incident awareness exists but is not disclosable",
					DisclosureClass:     types.DisclosureRestrictedAwareness,
					RequiresFullAgent:   true,
					Reaction:            "warning",
				}, nil
			}
		}
	}
	if strings.HasSuffix(strings.TrimSpace(lower), "?") {
		return types.ClassificationDecision{
			Outcome:           types.OutcomeReplyInThread,
			Confidence:        0.91,
			ReasonCodes:       []string{"ambient.clear_unanswered_question"},
			ResponseIntent:    "answer the clear unresolved question",
			DisclosureClass:   types.DisclosureDestinationSafe,
			RequiresFullAgent: true,
			Reaction:          "thinking_face",
		}, nil
	}
	return silent("ambient.social_chatter"), nil
}

func containsActionableSignal(text string) bool {
	for _, signal := range []string{"incident", "outage", " is down", " failed", " failure", " error", " blocked", "needs attention"} {
		if strings.Contains(text, signal) {
			return true
		}
	}
	return false
}

func containsIncident(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "incident") || strings.Contains(lower, "outage") || strings.Contains(lower, "down")
}
