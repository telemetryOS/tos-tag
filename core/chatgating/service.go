// Package chatgating implements tool-free response selection and deterministic
// admission. It cannot send Slack output or execute tools.
package chatgating

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/telemetryos/tos-tag/types"
)

var ErrInvalidClassifierDecision = errors.New("invalid classifier decision")

type Classifier interface {
	Decide(context.Context, Target, types.ContextPackRevision) (types.ChatGatingDecision, error)
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
	Predicted types.ChatGatingDecision `json:"predicted"`
	Effective types.ChatGatingDecision `json:"effective"`
	Shadowed  bool                     `json:"shadowed"`
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
		return nil, fmt.Errorf("invalid gating thresholds")
	}
	return &Service{classifier: classifier, shadow: shadow, assistThreshold: assistThreshold, channelReplyThreshold: channelReplyThreshold}, nil
}

func (s *Service) Decide(ctx context.Context, target Target, pack types.ContextPackRevision) Result {
	if reason := hardSuppression(target); reason != "" {
		decision := silent(reason)
		return Result{Predicted: decision, Effective: decision}
	}
	// Observe mode is an absolute no-output policy, including direct mentions
	// and replies in an otherwise active thread.
	if target.Mode == types.ModeObserve {
		decision := silent("admission.channel_mode")
		return Result{Predicted: decision, Effective: decision}
	}
	if target.Envelope.IsMention || target.ActiveThread {
		reason := "hard.direct_mention"
		if target.ActiveThread && !target.Envelope.IsMention {
			reason = "hard.active_thread_reply"
		}
		decision := types.ChatGatingDecision{
			Outcome:           types.OutcomeReplyInThread,
			Confidence:        1,
			ReasonCodes:       []string{reason},
			ResponseIntent:    "respond to the explicit request",
			DisclosureClass:   types.DisclosureDestinationSafe,
			RequiresFullAgent: true,
		}
		return Result{Predicted: decision, Effective: decision}
	}
	if target.Mode == types.ModeMention {
		decision := silent("admission.channel_mode")
		return Result{Predicted: decision, Effective: decision}
	}

	predicted, err := s.classifier.Decide(ctx, target, pack)
	if err != nil {
		decision := silent("classifier.error")
		return Result{Predicted: decision, Effective: decision}
	}
	if err := validateDecision(predicted, pack); err != nil {
		decision := silent("classifier.invalid")
		return Result{Predicted: decision, Effective: decision}
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
	if s.shadow && predicted.Outcome != types.OutcomeSilent {
		return Result{Predicted: predicted, Effective: silent("admission.shadow_mode"), Shadowed: true}
	}
	return Result{Predicted: predicted, Effective: predicted}
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

func validateDecision(decision types.ChatGatingDecision, pack types.ContextPackRevision) error {
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

func silent(reason string) types.ChatGatingDecision {
	return types.ChatGatingDecision{
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

func outcomeNeedsEvidence(outcome types.GatingOutcome) bool {
	return outcome == types.OutcomeReplyInThread || outcome == types.OutcomeReplyInChannel || outcome == types.OutcomeStartBackgroundJob
}

type DeterministicClassifier struct{}

func (DeterministicClassifier) Decide(_ context.Context, target Target, pack types.ContextPackRevision) (types.ChatGatingDecision, error) {
	lower := strings.ToLower(target.Envelope.Text)
	if strings.Contains(lower, "is the system down") || strings.Contains(lower, "is it down") {
		for _, source := range pack.Sources {
			if source.Partition != types.PartitionEvidence && source.Partition != types.PartitionSituation {
				continue
			}
			if source.DisclosureClass == types.DisclosureDestinationSafe && containsIncident(source.Text) {
				return types.ChatGatingDecision{
					Outcome:               types.OutcomeReplyInThread,
					Confidence:            0.99,
					ReasonCodes:           []string{"ambient.cross_channel_incident_match"},
					ReleasableEvidenceIDs: []string{source.ID},
					ResponseIntent:        "answer with the current incident evidence",
					DisclosureClass:       types.DisclosureDestinationSafe,
					RequiresFullAgent:     true,
				}, nil
			}
			if source.DisclosureClass == types.DisclosureRestrictedAwareness && containsIncident(source.Text) {
				return types.ChatGatingDecision{
					Outcome:             types.OutcomeReplyInThread,
					Confidence:          0.99,
					ReasonCodes:         []string{"ambient.cross_channel_incident_match"},
					RestrictedSignalIDs: []string{source.ID},
					ResponseIntent:      "incident awareness exists but is not disclosable",
					DisclosureClass:     types.DisclosureRestrictedAwareness,
					RequiresFullAgent:   true,
				}, nil
			}
		}
	}
	if strings.HasSuffix(strings.TrimSpace(lower), "?") {
		return types.ChatGatingDecision{
			Outcome:           types.OutcomeReplyInThread,
			Confidence:        0.91,
			ReasonCodes:       []string{"ambient.clear_unanswered_question"},
			ResponseIntent:    "answer the clear unresolved question",
			DisclosureClass:   types.DisclosureDestinationSafe,
			RequiresFullAgent: true,
		}, nil
	}
	return silent("ambient.social_chatter"), nil
}

func containsIncident(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "incident") || strings.Contains(lower, "outage") || strings.Contains(lower, "down")
}
