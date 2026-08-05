// Package classifier implements tool-free response selection and deterministic
// admission. It cannot send Slack output or execute tools.
package classifier

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/telemetryos/tos-tag/types"
)

var ErrInvalidClassifierDecision = errors.New("invalid classifier decision")

var leadingSlackUserAddressPattern = regexp.MustCompile(`^\s*<@U[A-Z0-9]+>(?:[\s,;:!?.]|$)`)

type Classifier interface {
	Decide(context.Context, Target, types.ContextPackRevision) (types.ClassificationDecision, error)
}

type Target struct {
	ObservationID string
	Envelope      types.SlackEnvelope
	Mode          types.ParticipationMode
	ActiveThread  bool
	// AuthorizedTrigger distinguishes an operator-created heartbeat or trigger
	// subscription from ordinary ambient Slack traffic. A trigger may request
	// classifier-gated work without pretending that its instruction was a
	// mention or widening the channel's participation mode.
	AuthorizedTrigger bool
	KillSwitched      bool
	WorkflowLoop      bool
	Unsupported       bool
	Deleted           bool
	// AmbientLinkOnly identifies an unaddressed top-level channel message made
	// entirely of one or more links. These messages are useful as context, but
	// are not requests for Tag to classify or answer.
	AmbientLinkOnly bool
	SelfAuthored    bool
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
	return EnforceParticipation(Result{Predicted: predicted, Effective: predicted}, target, pack)
}

// RequiresProviderCall reports whether Decide would reach the configured
// classifier implementation for this target. The pipeline uses it to apply the
// durable flood budget only to messages that would otherwise consume a model
// classification, while deterministic hard suppressions remain available.
func (s *Service) RequiresProviderCall(target Target) bool {
	if hardSuppression(target) != "" {
		return false
	}
	return target.Mode != types.ModeObserve || s.shadow
}

// EnforceParticipation is the model-independent initiative boundary. It is
// intentionally safe to call more than once: Service applies it after ambient
// classification, and the pipeline applies it again immediately before
// admission so a future classifier implementation cannot bypass the rule.
func EnforceParticipation(result Result, target Target, pack types.ContextPackRevision) Result {
	effective := result.Effective
	if target.Mode != types.ModeAssist || !outcomeNeedsAgent(effective.Outcome) {
		return result
	}
	if assistInitiativeAuthorized(target, pack, effective) {
		return result
	}
	return Suppress(result, "policy.unsolicited_assist_work")
}

func assistInitiativeAuthorized(target Target, pack types.ContextPackRevision, decision types.ClassificationDecision) bool {
	if thirdPartyAddressedTurn(target) {
		return false
	}
	if target.Envelope.IsMention || target.ActiveThread || target.AuthorizedTrigger || explicitlyAddressesTag(target.Envelope.Text) {
		return true
	}
	question := looksLikeQuestion(target.Envelope.Text)
	questionOrRequest := question || looksLikeExplicitRequest(target.Envelope.Text)
	// Assist mode is allowed to answer a clear question; the classifier still
	// decides whether the particular ambient question is useful enough to answer.
	if question {
		return true
	}
	if likelyConversationallyAddressedToAgent(target, pack) && questionOrRequest {
		return true
	}
	// Product questions are an intentional ambient assist surface, including
	// natural questions that omit a final question mark.
	if decision.ProductRetrievalRequired && questionOrRequest {
		return true
	}
	// A destination-safe operational question or a deterministic public
	// alignment conflict is also an intentional assist-mode intervention.
	if asksOperationalStatus(target.Envelope.Text) && len(decision.ReleasableEvidenceIDs) > 0 {
		return true
	}
	_, aligned := alignmentConflict(target, pack)
	return aligned
}

func looksLikeQuestion(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return false
	}
	if strings.Contains(normalized, "?") {
		return true
	}
	for _, prefix := range []string{
		"what ", "why ", "how ", "when ", "where ", "who ", "which ",
		"is ", "are ", "am ", "do ", "does ", "did ", "can ", "could ",
		"would ", "should ", "will ", "has ", "have ",
	} {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

func looksLikeExplicitRequest(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	for _, prefix := range []string{
		"please ",
		"tell me", "show me", "give me", "help me", "check ", "investigate ",
		"look into ", "look at ", "take a look", "have a look", "review ", "explain ", "compare ", "summarize ", "find ",
		"create ", "update ", "edit ", "add ", "remove ", "delete ", "run ",
	} {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

func (s *Service) predict(ctx context.Context, target Target, pack types.ContextPackRevision) types.ClassificationDecision {
	if target.ActiveThread {
		predicted, err := s.classifier.Decide(ctx, target, pack)
		predicted = sanitizeEvidenceReferences(predicted, pack)
		predicted = enforceDirectReplyPlacement(predicted, target)
		if err == nil && validateDecision(predicted, pack) == nil && predicted.Outcome != types.OutcomeSilent && predicted.Outcome != types.OutcomeReact {
			if predicted.DirectReply != "" {
				if predicted.Confidence >= s.assistThreshold && validateDirectReplyForTarget(predicted, target, pack) == nil {
					return predicted
				}
			} else {
				if predicted.Outcome == types.OutcomeReplyInChannel {
					predicted.Outcome = types.OutcomeReplyInThread
					predicted.ReasonCodes = append(predicted.ReasonCodes, "policy.active_thread_placement")
				}
				return predicted
			}
		}
		// An active thread is a hard participation trigger, but the direct
		// classifier still gets the first chance to recognize natural social
		// language and answer without starting a worker. If that decision is
		// unavailable, use a fixed destination-safe acknowledgement rather than
		// dropping an unmistakably social turn.
		if isDirectSocialCandidate(target.Envelope.Text) {
			return directSocialFallback(target.Envelope.Text, true)
		}
		return types.ClassificationDecision{
			Outcome:           types.OutcomeReplyInThread,
			Confidence:        1,
			ReasonCodes:       []string{"hard.active_thread_reply"},
			ResponseIntent:    "continue the active thread",
			DisclosureClass:   types.DisclosureDestinationSafe,
			RequiresFullAgent: true,
			Reaction:          "eyes",
		}
	}
	if target.Envelope.IsMention {
		predicted, err := s.classifier.Decide(ctx, target, pack)
		predicted = sanitizeEvidenceReferences(predicted, pack)
		predicted = enforceDirectReplyPlacement(predicted, target)
		// Placement is part of the model-independent contract, so canonicalize it
		// before validation. A provider can legitimately recommend strong work
		// while initially choosing the channel; rejecting that otherwise useful
		// recommendation would drop into the generic direct-mention fallback and
		// lose the threaded Thinking Steps surface.
		predicted = enforceDirectMentionPlacement(predicted, target.Envelope.Text)
		validationErr := validateDecision(predicted, pack)
		// A direct mention is already a hard participation trigger. The ambient
		// confidence threshold must not discard an otherwise valid placement and
		// model-routing recommendation, or simple mentioned questions fall back to
		// the deployment-default (usually max-effort) profile.
		if err == nil && validationErr == nil && predicted.Outcome != types.OutcomeSilent && predicted.Outcome != types.OutcomeReact && (predicted.DirectReply == "" || predicted.Confidence >= s.channelReplyThreshold) {
			if predicted.DirectReply == "" || validateDirectReplyForTarget(predicted, target, pack) == nil {
				return predicted
			}
		}
		if isDirectSocialCandidate(target.Envelope.Text) {
			return directSocialFallback(target.Envelope.Text, false)
		}
		fallback := directMentionFallback(target.Envelope.Text)
		switch {
		case err != nil:
			fallback.ReasonCodes = append(fallback.ReasonCodes, "classifier.provider_error_fallback")
		case validationErr != nil:
			fallback.ReasonCodes = append(fallback.ReasonCodes, decisionValidationReason(validationErr))
		case predicted.DirectReply != "" && predicted.Confidence < s.channelReplyThreshold:
			fallback.ReasonCodes = append(fallback.ReasonCodes, "classifier.direct_reply_threshold_fallback")
		case predicted.DirectReply != "" && validateDirectReplyForTarget(predicted, target, pack) != nil:
			fallback.ReasonCodes = append(fallback.ReasonCodes, "classifier.invalid_direct_reply_fallback")
		default:
			fallback.ReasonCodes = append(fallback.ReasonCodes, "classifier.non_action_fallback")
		}
		return fallback
	}
	if target.Mode == types.ModeMention {
		return silent("admission.channel_mode")
	}

	predicted, err := s.classifier.Decide(ctx, target, pack)
	if err != nil {
		return silent("classifier.error")
	}
	predicted = sanitizeEvidenceReferences(predicted, pack)
	predicted = enforceDirectReplyPlacement(predicted, target)
	if err := validateDecision(predicted, pack); err != nil {
		return silent("classifier.invalid")
	}
	if predicted.DirectReply != "" && validateDirectReplyForTarget(predicted, target, pack) != nil {
		return silent("classifier.invalid_direct_reply")
	}
	if predicted.Outcome != types.OutcomeSilent && predicted.Confidence < s.assistThreshold {
		predicted = silent("admission.low_confidence")
	}
	if predicted.Outcome == types.OutcomeReplyInChannel && predicted.DirectReply != "" && predicted.Confidence < s.channelReplyThreshold {
		predicted = silent("admission.direct_reply_threshold")
	} else if predicted.Outcome == types.OutcomeReplyInChannel && predicted.Confidence < s.channelReplyThreshold {
		predicted.Outcome = types.OutcomeReplyInThread
		predicted.ReasonCodes = append(predicted.ReasonCodes, "admission.channel_reply_threshold")
	}
	if predicted.DirectReply == "" && outcomeNeedsEvidence(predicted.Outcome) && len(predicted.ReleasableEvidenceIDs) == 0 && len(predicted.RestrictedSignalIDs) > 0 {
		predicted = silent("admission.destination_disclosure_denied")
	}
	return predicted
}

func decisionValidationReason(err error) string {
	message := err.Error()
	for _, candidate := range []string{
		"confidence", "outcome", "reason code", "reaction", "direct reply outcome",
		"direct reply agent recommendation", "direct reply text", "full agent required",
		"releasable evidence", "restricted signal",
	} {
		if strings.Contains(message, candidate) {
			return "classifier.invalid_" + strings.ReplaceAll(candidate, " ", "_") + "_fallback"
		}
	}
	return "classifier.invalid_decision_fallback"
}

// sanitizeEvidenceReferences preserves an otherwise valid participation
// decision while removing source IDs that the immutable context pack does not
// authorize for the requested disclosure class. The worker can still retrieve
// fresh product or web evidence through reviewed capabilities; an invented or
// misclassified Slack source must never turn into context authority or make a
// useful standalone question disappear.
func sanitizeEvidenceReferences(decision types.ClassificationDecision, pack types.ContextPackRevision) types.ClassificationDecision {
	available := make(map[string]types.DisclosureClass, len(pack.Sources))
	for _, source := range pack.Sources {
		available[source.ID] = source.DisclosureClass
	}
	pruned := false
	releasable := make([]string, 0, len(decision.ReleasableEvidenceIDs))
	for _, id := range decision.ReleasableEvidenceIDs {
		if available[id] == types.DisclosureDestinationSafe {
			releasable = append(releasable, id)
		} else {
			pruned = true
		}
	}
	restricted := make([]string, 0, len(decision.RestrictedSignalIDs))
	for _, id := range decision.RestrictedSignalIDs {
		if available[id] == types.DisclosureRestrictedAwareness {
			restricted = append(restricted, id)
		} else {
			pruned = true
		}
	}
	decision.ReleasableEvidenceIDs = releasable
	decision.RestrictedSignalIDs = restricted
	if pruned {
		decision.ReasonCodes = append(decision.ReasonCodes, "policy.invalid_evidence_pruned")
	}
	return decision
}

func enforceDirectReplyPlacement(decision types.ClassificationDecision, target Target) types.ClassificationDecision {
	if decision.DirectReply == "" {
		return decision
	}
	want := types.OutcomeReplyInChannel
	if target.ActiveThread {
		want = types.OutcomeReplyInThread
	}
	if decision.Outcome != want {
		decision.Outcome = want
		decision.ReasonCodes = append(decision.ReasonCodes, "policy.direct_reply_placement")
	}
	return decision
}

func directMentionFallback(text string) types.ClassificationDecision {
	outcome := types.OutcomeReplyInThread
	reason := "hard.direct_mention_deeper_reply"
	intent := "respond to the explicit request in a focused thread"
	if requested, requestedReason, ok := requestedReplyPlacement(text); ok {
		outcome = requested
		reason = requestedReason
		if requested == types.OutcomeReplyInChannel {
			intent = "honor the explicit request for a channel-level answer"
		} else {
			intent = "honor the explicit request for a threaded answer"
		}
	} else if briefSelfContainedMention(text) {
		outcome = types.OutcomeReplyInChannel
		reason = "hard.direct_mention_brief_reply"
		intent = "give a brief self-contained answer in the channel"
	}
	return types.ClassificationDecision{
		Outcome:           outcome,
		Confidence:        1,
		ReasonCodes:       []string{reason},
		ResponseIntent:    intent,
		DisclosureClass:   types.DisclosureDestinationSafe,
		RequiresFullAgent: true,
		Reaction:          "eyes",
	}
}

func enforceDirectMentionPlacement(decision types.ClassificationDecision, text string) types.ClassificationDecision {
	if requested, reason, ok := requestedReplyPlacement(text); ok {
		if decision.Outcome != requested {
			decision.Outcome = requested
			decision.ReasonCodes = append(decision.ReasonCodes, reason)
		}
		return decision
	}
	// A strong/high-effort recommendation means the classifier expects
	// substantial work rather than a brief self-contained channel answer. Keep
	// that work in a thread so it has a focused conversation surface and Slack
	// can show Thinking Steps while the worker runs. An explicit channel request
	// was already honored above.
	if substantialAgentRecommendation(decision) && (decision.Outcome == types.OutcomeReplyInChannel || decision.Outcome == types.OutcomeReplyInThread) {
		if decision.Outcome != types.OutcomeReplyInThread {
			decision.Outcome = types.OutcomeReplyInThread
			decision.ReasonCodes = append(decision.ReasonCodes, "policy.substantial_agent_thread")
		}
		return decision
	}
	if decision.SourceWriteRequested {
		return decision
	}
	if decision.ProductRetrievalRequired {
		return decision
	}
	if decision.Outcome == types.OutcomeReplyInThread && briefSelfContainedMention(text) {
		decision.Outcome = types.OutcomeReplyInChannel
		decision.ReasonCodes = append(decision.ReasonCodes, "policy.brief_surface_channel")
		return decision
	}
	if decision.Outcome == types.OutcomeReplyInChannel && requiresThreadSurface(text) {
		decision.Outcome = types.OutcomeReplyInThread
		decision.ReasonCodes = append(decision.ReasonCodes, "policy.deep_surface_thread")
	}
	return decision
}

func substantialAgentRecommendation(decision types.ClassificationDecision) bool {
	if !decision.RequiresFullAgent {
		return false
	}
	if decision.AgentModelStrength == "strong" {
		return true
	}
	switch decision.AgentReasoningEffort {
	case "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func requestedReplyPlacement(text string) (types.ClassificationOutcome, string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(text))
	threadRequested := containsAny(normalized,
		"reply in a thread", "reply in thread", "answer in a thread", "answer in thread",
		"respond in a thread", "respond in thread", "put the answer in a thread", "start a thread",
	)
	channelRequested := containsAny(normalized,
		"reply in the channel", "reply in channel", "answer in the channel", "answer in channel",
		"respond in the channel", "respond in channel", "post in the channel", "post in channel",
		"reply at channel level", "answer at channel level", "in-channel", "channel-level",
	)
	channelRequested = channelRequested || (containsAny(normalized, "in the channel", "in channel") && containsAny(normalized, "not a thread", "rather than a thread", "instead of a thread"))
	if threadRequested == channelRequested {
		return "", "", false
	}
	if threadRequested {
		return types.OutcomeReplyInThread, "hard.explicit_thread_request", true
	}
	return types.OutcomeReplyInChannel, "hard.explicit_channel_request", true
}

func briefSelfContainedMention(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if len([]rune(normalized)) > 180 || strings.Count(normalized, "?") > 1 || requiresThreadSurface(normalized) {
		return false
	}
	return strings.Count(normalized, "?") == 1 || containsAny(normalized, "tell me", "give me", "name the", "state the", "identify the", "confirm the", "convert ", "define ")
}

func requiresThreadSurface(text string) bool {
	normalized := strings.ToLower(text)
	return containsAny(normalized,
		"analyze", "debug", "deep dive", "explain in detail", "figure out", "implement",
		"investigate", "look into", "review", "step by step", "walk me through",
		"what would we need to change", "what do we need to change", "what changes would be needed",
		"how would we change", "how should we change", "how would we implement", "what would it take to",
		"implementation plan", "migration plan",
		"compare", "comparison", "native table", "table with", "structured report", "artifact",
		"research", "multiple sections", "code sample", "code block", "long-form",
		"architecture document", "design document", "reference guide", "white paper",
	)
}

func isImplementationPlanningRequest(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	return containsAny(normalized,
		"what would we need to change", "what do we need to change", "what changes would be needed",
		"how would we change", "how should we change", "how would we implement", "what would it take to",
		"implementation plan", "migration plan",
	)
}

func containsAny(text string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func isSocialAcknowledgement(text string) bool {
	normalized := normalizedSocialText(text)
	switch normalized {
	case "thanks", "thanks again", "thank you", "thank you!", "thx", "ty",
		"perfect", "great", "awesome", "cool", "nice", "cheers",
		"appreciate it", "much appreciated", "sounds good", "makes sense",
		"got it", "all good", ":thumbsup:", ":+1:", "👍", "🙏":
		return true
	default:
		return false
	}
}

func isDirectSocialCandidate(text string) bool {
	if strings.ContainsRune(text, '?') {
		return false
	}
	normalized := normalizedSocialText(text)
	if normalized == "" || utf8.RuneCountInString(normalized) > 120 {
		return false
	}
	if isSocialAcknowledgement(normalized) {
		return true
	}
	words := strings.Fields(strings.Map(func(character rune) rune {
		switch character {
		case ',', '.', '!', ';', ':', '-', '—', '–':
			return ' '
		default:
			return character
		}
	}, normalized))
	natural := strings.Join(words, " ")
	for _, prefix := range []string{"morning tag", "good morning tag", "hello tag", "hi tag", "hey tag", "afternoon tag", "good afternoon tag", "evening tag", "good evening tag"} {
		if strings.HasPrefix(natural, prefix) && !hasSubstantiveSocialTail(natural) {
			return true
		}
	}
	for _, prefix := range []string{"thanks ", "thank you ", "appreciate ", "great work ", "nice work ", "good work ", "well done "} {
		if strings.HasPrefix(natural, prefix) && !hasSubstantiveSocialTail(natural) {
			return true
		}
	}
	switch normalized {
	case "hi", "hello", "hey", "hiya", "morning", "good morning", "afternoon", "good afternoon",
		"evening", "good evening", "how are you", "how are you doing", "how's it going", "whats up", "what's up",
		"bye", "goodbye", "see you", "see you later", "later", "good bot", "nice work", "well done",
		"you're great", "you are great", "love it", "lol", "lmao", "haha", "hahaha", "😂", "😄":
		return true
	default:
		return false
	}
}

func directSocialFallback(text string, activeThread bool) types.ClassificationDecision {
	normalized := normalizedSocialText(text)
	reply := "Happy to help!"
	reaction := "white_check_mark"
	switch {
	case containsAny(normalized, "morning"):
		reply, reaction = "Morning!", "speech_balloon"
	case containsAny(normalized, "afternoon"):
		reply, reaction = "Good afternoon!", "speech_balloon"
	case containsAny(normalized, "evening"):
		reply, reaction = "Good evening!", "speech_balloon"
	case containsAny(normalized, "hello", "hey", "hiya", "how are you", "how's it going", "whats up", "what's up"):
		reply, reaction = "Hey!", "speech_balloon"
	case containsAny(normalized, "bye", "goodbye", "see you", "later"):
		reply, reaction = "See you!", "speech_balloon"
	case containsAny(normalized, "nice work", "great work", "good work", "well done", "good bot", "you're great", "you are great"):
		reply = "Thanks!"
	case containsAny(normalized, "lol", "lmao", "haha", "😂", "😄"):
		reply, reaction = "😄", "speech_balloon"
	case containsAny(normalized, "thanks", "thank you", "thx", "ty", "appreciate", "cheers"):
		reply = "You're welcome!"
	}
	outcome := types.OutcomeReplyInChannel
	if activeThread {
		outcome = types.OutcomeReplyInThread
	}
	return types.ClassificationDecision{
		Outcome:            outcome,
		Confidence:         1,
		ReasonCodes:        []string{"policy.social_direct_reply_fallback"},
		ResponseIntent:     "brief social acknowledgement",
		DirectReply:        reply,
		DisclosureClass:    types.DisclosureDestinationSafe,
		Reaction:           reaction,
		AgentModelStrength: "none",
	}
}

func hasSubstantiveSocialTail(text string) bool {
	tail := strings.TrimSpace(text)
	for _, prefix := range []string{
		"good afternoon tag", "good morning tag", "good evening tag",
		"afternoon tag", "morning tag", "evening tag", "hello tag", "hey tag", "hi tag",
		"thanks again", "thank you", "thanks", "appreciate", "great work", "nice work", "good work", "well done",
	} {
		if tail == prefix {
			return false
		}
		if strings.HasPrefix(tail, prefix+" ") {
			tail = strings.TrimSpace(strings.TrimPrefix(tail, prefix))
			break
		}
	}
	if tail == "tag" {
		return false
	}
	if strings.HasPrefix(tail, "tag ") {
		tail = strings.TrimSpace(strings.TrimPrefix(tail, "tag"))
	}
	for _, prefix := range []string{
		"please ", "tell me", "give me", "name the", "state the", "identify the", "confirm the",
		"can you", "could you", "would you", "check ", "investigate", "update ", "create ", "delete ",
		"run ", "compare ", "explain ", "summarize ", "what ", "why ", "how ", "which ", "when ", "where ",
	} {
		if strings.HasPrefix(tail, prefix) {
			return true
		}
	}
	return containsAny(tail,
		" is down", " error", " failed", " failing", " outage", " incident", " exposed", " leaked", " broken", " blocked", " need attention", " needs attention",
	)
}

func normalizedSocialText(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	for {
		start := strings.Index(text, "<@")
		if start < 0 {
			break
		}
		end := strings.Index(text[start:], ">")
		if end < 0 {
			break
		}
		text = text[:start] + " " + text[start+end+1:]
	}
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "tag"))
	return strings.Trim(text, " \t\r\n.!?,;:")
}

func validateDirectReplyForTarget(decision types.ClassificationDecision, target Target, pack types.ContextPackRevision) error {
	if decision.DirectReply == "" {
		return errors.New("direct reply is empty")
	}
	policyRedirect := decision.SourceWriteRequested && decision.DirectReply == sourceWriteRedirectReply
	boundedClarification := likelyConversationallyAddressedToAgent(target, pack) && isMissingLocationWeatherQuestion(target.Envelope.Text) && decision.DirectReply == weatherLocationClarificationReply
	if !policyRedirect && !boundedClarification && !isDirectSocialCandidate(target.Envelope.Text) {
		return errors.New("target is not an allowlisted direct-reply message")
	}
	if decision.RequiresFullAgent || len(decision.ReleasableEvidenceIDs) != 0 || len(decision.RestrictedSignalIDs) != 0 {
		return errors.New("direct reply requested agent or evidence")
	}
	if decision.DisclosureClass != types.DisclosureDestinationSafe {
		return errors.New("direct reply is not destination safe")
	}
	if target.ActiveThread && decision.Outcome != types.OutcomeReplyInThread {
		return errors.New("active-thread direct reply escaped its thread")
	}
	if !target.ActiveThread && decision.Outcome != types.OutcomeReplyInChannel {
		return errors.New("top-level direct reply did not stay in channel")
	}
	return validateDirectReplyText(decision.DirectReply)
}

func validateDirectReplyText(reply string) error {
	if reply != strings.TrimSpace(reply) || reply == "" || utf8.RuneCountInString(reply) > 240 || strings.ContainsAny(reply, "\r\n\t") {
		return errors.New("direct reply must be one trimmed line of at most 240 characters")
	}
	lower := strings.ToLower(reply)
	for _, forbidden := range []string{"http://", "https://", "www.", "<", ">", "`", "*", "_", "~", "@", "#"} {
		if strings.Contains(lower, forbidden) {
			return errors.New("direct reply contains disallowed formatting or addressing")
		}
	}
	return nil
}

func hardSuppression(target Target) string {
	switch {
	case target.KillSwitched:
		return "suppress.kill_switch"
	case target.SelfAuthored:
		return "suppress.self_message"
	case target.WorkflowLoop:
		return "suppress.workflow_loop"
	case target.Deleted || target.Envelope.Kind == types.SlackEventDelete:
		return "suppress.deleted"
	case target.AmbientLinkOnly:
		return "suppress.ambient_link_only"
	case target.Unsupported:
		return "suppress.unsupported_subtype"
	case target.Envelope.IntegrationAuthored():
		return "suppress.integration_message"
	case thirdPartyAddressedTurn(target):
		return "suppress.third_party_address"
	default:
		return ""
	}
}

// thirdPartyAddressedTurn recognizes a narrow human-to-human handoff inside a
// Tag-owned thread. Active-thread state remains a hard participation trigger
// for normal continuations, but a leading Slack user address belongs to that
// person unless the turn also mentions or explicitly addresses Tag. Mentions
// used as the object of a request (for example, "summarize this for <@U123>")
// do not match this gate.
func thirdPartyAddressedTurn(target Target) bool {
	if !target.ActiveThread || target.Envelope.IsMention || target.Envelope.ChannelKind == types.SlackChannelKindDirectMessage {
		return false
	}
	if explicitlyAddressesTag(target.Envelope.Text) {
		return false
	}
	return leadingSlackUserAddressPattern.MatchString(target.Envelope.Text)
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
	if decision.DirectReply != "" {
		if decision.Outcome != types.OutcomeReplyInThread && decision.Outcome != types.OutcomeReplyInChannel {
			return fmt.Errorf("%w: direct reply outcome", ErrInvalidClassifierDecision)
		}
		if decision.RequiresFullAgent || decision.AgentModelProfile != "" || (decision.AgentModelStrength != "" && decision.AgentModelStrength != "none") || decision.AgentReasoningEffort != "" {
			return fmt.Errorf("%w: direct reply agent recommendation", ErrInvalidClassifierDecision)
		}
		if err := validateDirectReplyText(decision.DirectReply); err != nil {
			return fmt.Errorf("%w: direct reply text", ErrInvalidClassifierDecision)
		}
	} else if outcomeNeedsAgent(decision.Outcome) && !decision.RequiresFullAgent {
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

// SilentResult constructs a model-independent, auditable no-output decision.
// It is used by control-plane safety gates that run before classification.
func SilentResult(reason string) Result {
	decision := silent(reason)
	return Result{Predicted: decision, Effective: decision}
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
	if corrected := withConversationalAddressPolicyCorrections(types.ClassificationDecision{Outcome: types.OutcomeSilent, ReasonCodes: []string{"deterministic.ambient"}}, target, pack); corrected.Outcome != types.OutcomeSilent {
		return corrected, nil
	}
	if corrected := withConversationalReferencePolicyCorrections(types.ClassificationDecision{Outcome: types.OutcomeSilent, ReasonCodes: []string{"deterministic.ambient"}}, target, pack, nil); corrected.Outcome != types.OutcomeSilent {
		return corrected, nil
	}
	if corrected := withClarificationFollowupPolicyCorrections(types.ClassificationDecision{Outcome: types.OutcomeSilent, ReasonCodes: []string{"deterministic.ambient"}}, target, pack, nil); corrected.Outcome != types.OutcomeSilent {
		return corrected, nil
	}
	if isObviousWikiPageCRUDRequest(target.Envelope.Text) {
		return types.ClassificationDecision{
			Outcome:              types.OutcomeReplyInThread,
			Confidence:           0.99,
			ReasonCodes:          []string{"deterministic.wiki_page_crud"},
			ResponseIntent:       "perform the requested Agent Wiki page CRUD through the reviewed Wiki capability",
			DisclosureClass:      types.DisclosureDestinationSafe,
			RequiresFullAgent:    true,
			Reaction:             "eyes",
			AgentModelStrength:   "standard",
			AgentReasoningEffort: "medium",
		}, nil
	}
	if isObviousSourceWriteRequest(target.Envelope.Text) {
		return withSourceWritePolicyCorrections(types.ClassificationDecision{ReasonCodes: []string{"deterministic.source_write"}}, target), nil
	}
	if isObviousProductKnowledgeQuestion(target.Envelope.Text, types.ClassificationDecision{}) {
		return types.ClassificationDecision{
			Outcome:                  types.OutcomeReplyInThread,
			Confidence:               0.99,
			ReasonCodes:              []string{"deterministic.product_knowledge"},
			ResponseIntent:           "retrieve authoritative TelemetryOS product evidence before answering",
			ProductRetrievalRequired: true,
			DisclosureClass:          types.DisclosureDestinationSafe,
			RequiresFullAgent:        true,
			Reaction:                 "thinking_face",
		}, nil
	}
	if isDirectSocialCandidate(target.Envelope.Text) {
		outcome := types.OutcomeReplyInChannel
		if target.ActiveThread {
			outcome = types.OutcomeReplyInThread
		}
		return types.ClassificationDecision{
			Outcome:         outcome,
			Confidence:      0.99,
			ReasonCodes:     []string{"social.direct_reply"},
			ResponseIntent:  "brief social acknowledgement",
			DirectReply:     "You're welcome!",
			DisclosureClass: types.DisclosureDestinationSafe,
			Reaction:        "white_check_mark",
		}, nil
	}
	if target.Envelope.IsMention {
		return directMentionFallback(target.Envelope.Text), nil
	}
	if target.Mode == types.ModeAssist || target.Mode == types.ModeProactive {
		if isStableNonUrgentMetricObservation(target.Envelope.Text) {
			return types.ClassificationDecision{
				Outcome:            types.OutcomeReact,
				Confidence:         0.99,
				ReasonCodes:        []string{"ambient.stable_metric_reaction_only"},
				DisclosureClass:    types.DisclosureDestinationSafe,
				Reaction:           "warning",
				AgentModelStrength: "none",
			}, nil
		}
		if source, ok := alignmentConflict(target, pack); ok {
			reaction := "speech_balloon"
			if operationalStance(source.Text) < 0 || operationalStance(target.Envelope.Text) < 0 {
				reaction = "warning"
			}
			return types.ClassificationDecision{
				Outcome:               types.OutcomeReplyInChannel,
				Confidence:            0.995,
				ReasonCodes:           []string{"ambient.cross_channel_alignment_conflict"},
				ReleasableEvidenceIDs: []string{source.ID},
				ResponseIntent:        fmt.Sprintf("briefly surface and reconcile the conflicting public report from <@%s> in <#%s> without assigning blame or treating the report as verified fact", source.AuthorID, source.ChannelID),
				DisclosureClass:       types.DisclosureDestinationSafe,
				RequiresFullAgent:     true,
				Reaction:              reaction,
			}, nil
		}
	}
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
	if asksOperationalStatus(lower) {
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

func alignmentConflict(target Target, pack types.ContextPackRevision) (types.ContextSource, bool) {
	currentStance := operationalStance(target.Envelope.Text)
	if currentStance == 0 || target.Envelope.ChannelID == "" || target.Envelope.UserID == "" {
		return types.ContextSource{}, false
	}
	now := target.Envelope.EventTime
	for _, source := range pack.Sources {
		if source.DisclosureClass != types.DisclosureDestinationSafe || source.Provenance != "human_message" || source.AuthorID == "" || source.AuthorID == target.Envelope.UserID || source.ChannelID == "" || source.ChannelID == target.Envelope.ChannelID {
			continue
		}
		if !now.IsZero() && !source.ObservedAt.IsZero() && source.ObservedAt.Before(now.Add(-48*time.Hour)) {
			continue
		}
		if sourceAuthorRecentlyParticipated(source.AuthorID, target.Envelope.ChannelID, pack.Sources) {
			continue
		}
		if sourceStance := operationalStance(source.Text); sourceStance == 0 || sourceStance == currentStance {
			continue
		}
		if !sharesOperationalSubject(target.Envelope.Text, source.Text) {
			continue
		}
		return source, true
	}
	return types.ContextSource{}, false
}

func sourceAuthorRecentlyParticipated(authorID, channelID string, sources []types.ContextSource) bool {
	for _, source := range sources {
		if source.ChannelID == channelID && source.AuthorID == authorID && source.Provenance == "human_message" {
			return true
		}
	}
	return false
}

func operationalStance(text string) int {
	lower := strings.ToLower(text)
	if containsAny(lower, "healthy", "working again", "back up", "recovered", "resolved", "all clear", "stable again", "fine now") {
		return 1
	}
	if containsAny(lower, "incident", "outage", " is down", " unavailable", "degraded", "failing", " failed", "timeout", "timing out", " errors") {
		return -1
	}
	return 0
}

func sharesOperationalSubject(left, right string) bool {
	left, right = strings.ToLower(left), strings.ToLower(right)
	for _, subject := range []string{"checkout", "login", "sign-in", "signin", "api", "server", "gateway", "database", "deploy", "production", "prod", "staging", "build"} {
		if strings.Contains(left, subject) && strings.Contains(right, subject) {
			return true
		}
	}
	return false
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
	return containsAny(lower,
		"incident", "outage", " down", "failure", "failing", "failed",
		"error", "degraded", "unavailable", "timeout",
	)
}

func asksOperationalStatus(text string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(text))
	if !strings.HasSuffix(trimmed, "?") {
		return false
	}
	return containsAny(trimmed,
		"is the system down", "is it down", "is it working", "is this working",
		"still able to", "anyone else seeing", "anybody else seeing",
		"seeing errors", "seeing failures", "seeing timeouts", "having trouble",
		"is production healthy", "is prod healthy", "is the api healthy",
		"is checkout failing", "is login failing", "is sign-in failing",
		"is signin failing", "is it offline", "is it unavailable",
	)
}
