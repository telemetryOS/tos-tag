package classifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/telemetryos/tos-tag/core/modelrouter"
	"github.com/telemetryos/tos-tag/types"
)

// AgentProfileSource exposes the current, policy-controlled model profiles the
// classifier may recommend for admitted Codex agent work.
type AgentProfileSource interface {
	Snapshot() modelrouter.Snapshot
}

type OpenAIOptions struct {
	BaseURL         string
	APIKey          string
	Model           string
	ReasoningEffort string
	Timeout         time.Duration
	MaxOutputTokens int
	ReactionEmojis  []string
	AgentProfiles   AgentProfileSource
	HTTPClient      *http.Client
}

// OpenAIClassifier performs one tool-free, stateless Responses API call. It
// never creates a Codex session and never exposes the API key to a worker.
type OpenAIClassifier struct {
	endpoint        string
	apiKey          string
	model           string
	reasoningEffort string
	maxOutputTokens int
	reactionEmojis  []string
	agentProfiles   AgentProfileSource
	httpClient      *http.Client
}

// ClassifierError identifies the failed stage without retaining provider
// output, Slack content, or credentials in the error value.
type ClassifierError struct {
	Stage string
	Code  string
	Err   error
}

func (e *ClassifierError) Error() string {
	return "classifier " + e.Stage + ": " + e.Err.Error()
}

func (e *ClassifierError) Unwrap() error          { return e.Err }
func (e *ClassifierError) DiagnosticCode() string { return e.Code }

func ErrorStage(err error) string {
	var failure *ClassifierError
	if errors.As(err, &failure) {
		return failure.Stage
	}
	return "unknown"
}

func ErrorCode(err error) string {
	var coded interface{ DiagnosticCode() string }
	if errors.As(err, &coded) {
		return coded.DiagnosticCode()
	}
	return "unknown"
}

func classifierError(stage, code string, err error) error {
	return &ClassifierError{Stage: stage, Code: code, Err: err}
}

func NewOpenAIClassifier(options OpenAIOptions) (*OpenAIClassifier, error) {
	if strings.TrimSpace(options.APIKey) == "" || strings.TrimSpace(options.Model) == "" || strings.TrimSpace(options.ReasoningEffort) == "" {
		return nil, errors.New("classifier OpenAI API key, model, and reasoning effort are required")
	}
	if options.Timeout <= 0 || options.MaxOutputTokens <= 0 {
		return nil, errors.New("classifier timeout and max output tokens must be positive")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(options.BaseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !isLoopbackURL(parsed)) {
		return nil, errors.New("classifier base URL must use HTTPS or a loopback HTTP address")
	}
	if len(options.ReactionEmojis) == 0 {
		return nil, errors.New("classifier reaction emoji allowlist is required")
	}
	for _, emoji := range options.ReactionEmojis {
		if !validEmojiName(emoji) {
			return nil, fmt.Errorf("invalid classifier reaction emoji %q", emoji)
		}
	}
	if options.AgentProfiles == nil {
		return nil, errors.New("classifier agent profile source is required")
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: options.Timeout}
	}
	return &OpenAIClassifier{
		endpoint:        baseURL + "/responses",
		apiKey:          strings.TrimSpace(options.APIKey),
		model:           strings.TrimSpace(options.Model),
		reasoningEffort: strings.TrimSpace(options.ReasoningEffort),
		maxOutputTokens: options.MaxOutputTokens,
		reactionEmojis:  append([]string(nil), options.ReactionEmojis...),
		agentProfiles:   options.AgentProfiles,
		httpClient:      client,
	}, nil
}

func (c *OpenAIClassifier) Decide(ctx context.Context, target Target, pack types.ContextPackRevision) (types.ClassificationDecision, error) {
	profiles := advertisedProfiles(c.agentProfiles.Snapshot())
	if len(profiles) == 0 {
		return types.ClassificationDecision{}, classifierError("profiles", "no_agent_profiles", errors.New("no enabled agent model profiles"))
	}
	recentParticipantIDs := destinationRecentParticipantIDs(target.Envelope.ChannelID, pack.Sources)
	conversationFocus := destinationConversationFocus(target, pack.Sources, 8)
	payload := classifierInput{
		Message:                             target.Envelope.Text,
		MessageAuthorID:                     target.Envelope.UserID,
		DestinationChannelID:                target.Envelope.ChannelID,
		Mode:                                target.Mode,
		DirectMention:                       target.Envelope.IsMention,
		ActiveThread:                        target.ActiveThread,
		Sources:                             pack.Sources,
		DestinationRecentParticipantIDs:     recentParticipantIDs,
		DestinationRecentHumanCount:         len(recentParticipantIDs),
		PreviousDestinationMessageFromAgent: previousDestinationMessageFromAgent(target, pack),
		LikelyAddressedToAgent:              likelyConversationallyAddressedToAgent(target, pack),
		ConversationFocus:                   conversationFocus,
		AgentProfiles:                       profiles,
		ReactionEmojis:                      c.reactionEmojis,
	}
	encodedInput, err := json.Marshal(payload)
	if err != nil {
		return types.ClassificationDecision{}, classifierError("encode", "input_encode", err)
	}
	requestBody := responsesRequest{
		Model:        c.model,
		Instructions: classifierInstructions,
		Input: []responsesInput{{
			Role:    "user",
			Content: []responsesInputContent{{Type: "input_text", Text: string(encodedInput)}},
		}},
		Reasoning:       responsesReasoning{Effort: c.reasoningEffort},
		Text:            responsesText{Format: classifierSchema(profiles, c.reactionEmojis)},
		MaxOutputTokens: c.maxOutputTokens,
		Store:           false,
	}
	encodedRequest, err := json.Marshal(requestBody)
	if err != nil {
		return types.ClassificationDecision{}, classifierError("encode", "request_encode", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(encodedRequest))
	if err != nil {
		return types.ClassificationDecision{}, classifierError("request", "request_create", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Client-Request-Id", "classifier-"+target.ObservationID)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return types.ClassificationDecision{}, classifierError("transport", "request_failed", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return types.ClassificationDecision{}, classifierError("response", "response_read", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return types.ClassificationDecision{}, classifierError("response", fmt.Sprintf("http_%d", response.StatusCode), fmt.Errorf("OpenAI returned HTTP %d", response.StatusCode))
	}
	var providerResponse responsesResponse
	if err := json.Unmarshal(body, &providerResponse); err != nil {
		return types.ClassificationDecision{}, classifierError("response", "response_decode", err)
	}
	if providerResponse.Status != "completed" {
		code := "response_incomplete"
		if providerResponse.IncompleteDetails != nil && providerResponse.IncompleteDetails.Reason != "" {
			code += "_" + providerResponse.IncompleteDetails.Reason
		}
		return types.ClassificationDecision{}, classifierError("response", code, errors.New("OpenAI response did not complete"))
	}
	raw := providerResponse.outputText()
	if raw == "" {
		return types.ClassificationDecision{}, classifierError("decode_empty", "empty_output", errors.New("OpenAI returned no structured output"))
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var decision types.ClassificationDecision
	if err := decoder.Decode(&decision); err != nil {
		return types.ClassificationDecision{}, classifierError(decodeErrorStage(raw, err), "invalid_structured_output", err)
	}
	providerReaction := decision.Reaction
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("structured output contained trailing JSON")
		}
		return types.ClassificationDecision{}, classifierError("decode_trailing", "invalid_structured_output", err)
	}
	decision = withDirectMentionPolicyCorrections(decision, target, profiles)
	decision = withAddressedSocialPolicyCorrections(decision, target)
	decision = withConversationalAddressPolicyCorrections(decision, target, pack)
	decision = withConversationalReferencePolicyCorrections(decision, target, pack, profiles)
	decision = withClarificationFollowupPolicyCorrections(decision, target, pack, profiles)
	decision = withProductKnowledgePolicyCorrections(decision, target, profiles)
	decision = withSourceWritePolicyCorrections(decision, target)
	decision = withImplementationPlanningPolicyCorrections(decision, target, profiles)
	decision = withReadOnlyCodeAnalysisPolicyCorrections(decision, target, profiles)
	decision = withOperationalSynthesisPolicyCorrections(decision, target, pack, profiles)
	decision = withHighIntelligenceProfileCorrections(decision, target, profiles)
	decision = withBriefMentionPolicyCorrections(decision, target, profiles)
	decision = withAmbientPolicyCorrections(decision, target, pack, profiles)
	decision = withCanonicalAgentProfile(decision, profiles)
	decision = withBackgroundProfileFloor(decision, target, profiles)
	decision = withPolicyReactionAllowlist(decision, providerReaction, c.reactionEmojis)
	decision = withDefaultReaction(decision, c.reactionEmojis)
	decision = withConsistentAuditReasonCodes(decision, target)
	if err := validateRecommendation(decision, profiles, c.reactionEmojis); err != nil {
		return types.ClassificationDecision{}, classifierError("recommendation", recommendationErrorCode(err), err)
	}
	decision.ClassifierModel = c.model
	decision.ClassifierReasoningEffort = c.reasoningEffort
	decision.ClassifierResponseID = providerResponse.ID
	decision.ClassifierInputTokens = providerResponse.Usage.InputTokens
	decision.ClassifierOutputTokens = providerResponse.Usage.OutputTokens
	return decision, nil
}

func withConsistentAuditReasonCodes(decision types.ClassificationDecision, target Target) types.ClassificationDecision {
	removed := false
	decision.ReasonCodes = slices.DeleteFunc(decision.ReasonCodes, func(reason string) bool {
		normalized := strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(reason)))
		negativeMention := containsAny(normalized,
			"no_direct_mention", "not_directed_to_agent", "not_likely_addressed", "no_likely_address",
			"not_addressed_to_agent", "no_address_signal", "likely_addressed_false", "direct_mention_false",
		)
		positiveMention := !negativeMention && (normalized == "direct_mention" || strings.HasSuffix(normalized, "_direct_mention") || normalized == "likely_addressed_to_agent")
		negativeThread := containsAny(normalized, "no_active_thread", "not_active_thread", "active_thread_false")
		positiveThread := !negativeThread && (normalized == "active_thread" || strings.HasSuffix(normalized, "_active_thread") || normalized == "active_thread_true")
		ambientMention := target.Envelope.IsMention && strings.HasPrefix(normalized, "ambient_")
		contradicts := ambientMention || (target.Envelope.IsMention && negativeMention) || (!target.Envelope.IsMention && positiveMention) ||
			(target.ActiveThread && negativeThread) || (!target.ActiveThread && positiveThread)
		removed = removed || contradicts
		return contradicts
	})
	if removed {
		decision.ReasonCodes = appendUnique(decision.ReasonCodes, "policy.audit_reason_codes_corrected")
	}
	return decision
}

type advertisedAgentProfile struct {
	ID              string `json:"id"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	Strength        string `json:"strength"`
	ReasoningEffort string `json:"reasoning_effort"`
}

func advertisedProfiles(snapshot modelrouter.Snapshot) []advertisedAgentProfile {
	canonical := make(map[string]advertisedAgentProfile, 3)
	for _, profile := range snapshot.Profiles {
		if !profile.Enabled {
			continue
		}
		strength := modelStrength(profile)
		candidate := advertisedAgentProfile{ID: profile.ID, Provider: profile.ProviderID, Model: profile.ModelID, Strength: strength, ReasoningEffort: profile.Variant}
		if _, exists := canonical[strength]; !exists || profile.ID == snapshot.DeploymentDefault {
			canonical[strength] = candidate
		}
	}
	result := make([]advertisedAgentProfile, 0, len(canonical))
	for _, strength := range []string{"light", "standard", "strong"} {
		if profile, exists := canonical[strength]; exists {
			result = append(result, profile)
		}
	}
	return result
}

func modelStrength(profile types.ModelProfile) string {
	if configured, ok := profile.ProviderOptions["strength"].(string); ok {
		switch configured {
		case "light", "standard", "strong":
			return configured
		}
	}
	switch profile.Variant {
	case "none", "minimal", "low":
		return "light"
	case "medium":
		return "standard"
	default:
		return "strong"
	}
}

type classifierInput struct {
	Message                             string                   `json:"message"`
	MessageAuthorID                     string                   `json:"message_author_id,omitempty"`
	DestinationChannelID                string                   `json:"destination_channel_id,omitempty"`
	Mode                                types.ParticipationMode  `json:"mode"`
	DirectMention                       bool                     `json:"direct_mention"`
	ActiveThread                        bool                     `json:"active_thread"`
	Sources                             []types.ContextSource    `json:"sources"`
	DestinationRecentParticipantIDs     []string                 `json:"destination_recent_participant_ids,omitempty"`
	DestinationRecentHumanCount         int                      `json:"destination_recent_human_count"`
	PreviousDestinationMessageFromAgent bool                     `json:"previous_destination_message_from_agent"`
	LikelyAddressedToAgent              bool                     `json:"likely_addressed_to_agent"`
	ConversationFocus                   []types.ContextSource    `json:"conversation_focus,omitempty"`
	AgentProfiles                       []advertisedAgentProfile `json:"available_agent_profiles"`
	ReactionEmojis                      []string                 `json:"allowed_reaction_emojis"`
}

func destinationRecentParticipantIDs(channelID string, sources []types.ContextSource) []string {
	seen := make(map[string]struct{})
	participants := make([]string, 0)
	for _, source := range sources {
		if source.ChannelID != channelID || source.AuthorID == "" || source.Provenance != "human_message" {
			continue
		}
		if _, exists := seen[source.AuthorID]; exists {
			continue
		}
		seen[source.AuthorID] = struct{}{}
		participants = append(participants, source.AuthorID)
	}
	return participants
}

const weatherLocationClarificationReply = "I can check—what location should I use?"

func destinationConversationFocus(target Target, sources []types.ContextSource, limit int) []types.ContextSource {
	if target.Envelope.ChannelID == "" || limit <= 0 {
		return nil
	}
	targetID := target.Envelope.ChannelID + "/" + target.Envelope.MessageTS
	focus := make([]types.ContextSource, 0, limit)
	for _, source := range sources {
		// An active thread is its own conversational surface. Channel-wide
		// messages can still remain in the larger source set for material facts,
		// but they must not become the chronological conversation focus or
		// resolve a thread follow-up by accident.
		if target.ActiveThread && source.Partition != types.PartitionThread {
			continue
		}
		if source.ChannelID != target.Envelope.ChannelID || source.ID == targetID || source.DisclosureClass != types.DisclosureDestinationSafe || (source.Provenance != "human_message" && source.Provenance != "agent_output_unverified") {
			continue
		}
		focus = append(focus, source)
	}
	sort.SliceStable(focus, func(i, j int) bool { return focus[i].ObservedAt.Before(focus[j].ObservedAt) })
	if len(focus) > limit {
		focus = focus[len(focus)-limit:]
	}
	return focus
}

func previousDestinationConversationSource(target Target, pack types.ContextPackRevision) (types.ContextSource, bool) {
	focus := destinationConversationFocus(target, pack.Sources, 1)
	if len(focus) != 1 {
		return types.ContextSource{}, false
	}
	return focus[0], true
}

func withConversationalAddressPolicyCorrections(decision types.ClassificationDecision, target Target, pack types.ContextPackRevision) types.ClassificationDecision {
	if target.ActiveThread || target.Envelope.IsMention || !likelyConversationallyAddressedToAgent(target, pack) || !isMissingLocationWeatherQuestion(target.Envelope.Text) {
		return decision
	}
	return types.ClassificationDecision{
		Outcome:            types.OutcomeReplyInChannel,
		Confidence:         max(decision.Confidence, 0.99),
		ReasonCodes:        append(decision.ReasonCodes, "policy.conversational_address", "policy.weather_location_clarification"),
		ResponseIntent:     "ask one brief location clarification before checking current weather",
		DirectReply:        weatherLocationClarificationReply,
		DisclosureClass:    types.DisclosureDestinationSafe,
		Reaction:           "speech_balloon",
		AgentModelStrength: "none",
	}
}

func withConversationalReferencePolicyCorrections(decision types.ClassificationDecision, target Target, pack types.ContextPackRevision, profiles []advertisedAgentProfile) types.ClassificationDecision {
	if target.ActiveThread || target.Envelope.IsMention || !likelyConversationallyAddressedToAgent(target, pack) || !isConversationalReferenceQuestion(target.Envelope.Text) {
		return decision
	}
	previous, ok := previousDestinationConversationSource(target, pack)
	if !ok || previous.Provenance != "agent_output_unverified" {
		return decision
	}
	corrected := types.ClassificationDecision{
		Outcome:              types.OutcomeReplyInChannel,
		Confidence:           max(decision.Confidence, 0.99),
		ReasonCodes:          append(decision.ReasonCodes, "policy.conversational_reference", "policy.prior_tag_turn_focus"),
		ResponseIntent:       fmt.Sprintf("Resolve the current message's reference against the immediately preceding Tag turn in conversation_focus source %q, then answer the underlying question itself. Do not merely describe the referent or ask what it means when that turn supplies one clear referent. If the question asks whether we use a version or technology, follow the injected codebase-read skill's bounded version workflow: inspect the repository manifest/toolchain, container/build pins, and relevant CI or deployment configuration before comparing current usage with the referenced version. Do not infer that a patch is unpinned from the manifest alone.", previous.ID),
		DisclosureClass:      types.DisclosureDestinationSafe,
		RequiresFullAgent:    true,
		Reaction:             "thinking_face",
		AgentModelStrength:   "standard",
		AgentReasoningEffort: "medium",
	}
	return withCanonicalAgentProfile(corrected, profiles)
}

func isConversationalReferenceQuestion(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" || !strings.Contains(normalized, "?") || len(strings.Fields(normalized)) > 14 {
		return false
	}
	hasReference := containsAny(normalized, " it?", " that?", " this?", " those?", " them?", " on it", " using it", " use it", " using that", " use that")
	hasQuestionShape := containsAny(normalized, "are we ", "do we ", "did we ", "have we ", "can we ", "should we ", "is it ", "is that ", "does it ", "does that ", "what about ", "how about ")
	return hasReference && hasQuestionShape
}

func withClarificationFollowupPolicyCorrections(decision types.ClassificationDecision, target Target, pack types.ContextPackRevision, profiles []advertisedAgentProfile) types.ClassificationDecision {
	if !target.ActiveThread || len(strings.Fields(strings.TrimSpace(target.Envelope.Text))) > 12 {
		return decision
	}
	focus := destinationConversationFocus(target, pack.Sources, 8)
	if len(focus) < 2 {
		return decision
	}
	previous := focus[len(focus)-1]
	if previous.Provenance != "agent_output_unverified" || !looksLikeClarificationQuestion(previous.Text) {
		return decision
	}
	var unresolved types.ContextSource
	for index := len(focus) - 2; index >= 0; index-- {
		if focus[index].Provenance == "human_message" {
			unresolved = focus[index]
			break
		}
	}
	if unresolved.ID == "" {
		return decision
	}
	corrected := decision
	corrected.Outcome = types.OutcomeReplyInThread
	corrected.Confidence = max(decision.Confidence, 0.99)
	corrected.ReasonCodes = append(decision.ReasonCodes, "policy.clarification_followup_composition")
	corrected.ResponseIntent = fmt.Sprintf("Treat the current short message as the answer to Tag's clarification in conversation_focus source %q. Apply it to the unresolved human request in source %q and answer that composed request; do not answer or explain the clarification fragment by itself.", previous.ID, unresolved.ID)
	corrected.DirectReply = ""
	corrected.RequiresFullAgent = true
	corrected.Reaction = "thinking_face"
	corrected.AgentModelProfile = ""
	corrected.AgentModelStrength = "standard"
	corrected.AgentReasoningEffort = "medium"
	return withCanonicalAgentProfile(corrected, profiles)
}

func looksLikeClarificationQuestion(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	return strings.Contains(normalized, "?") && containsAny(normalized, "what does", "what do you mean", "which ", "do you mean", "could you clarify", "can you clarify", "what is “", "what is \"")
}

func likelyConversationallyAddressedToAgent(target Target, pack types.ContextPackRevision) bool {
	if target.Envelope.UserID == "" || target.Envelope.ChannelID == "" {
		return false
	}
	participants := destinationRecentParticipantIDs(target.Envelope.ChannelID, pack.Sources)
	return len(participants) == 1 && participants[0] == target.Envelope.UserID && previousDestinationMessageFromAgent(target, pack)
}

func previousDestinationMessageFromAgent(target Target, pack types.ContextPackRevision) bool {
	targetSourceID := ""
	if target.Envelope.ChannelID != "" && target.Envelope.MessageTS != "" {
		targetSourceID = target.Envelope.ChannelID + "/" + target.Envelope.MessageTS
	}
	// Some test and imported source IDs do not use channel/timestamp. Locate the
	// newest exact human-message match as a defensive fallback so it is not
	// mistaken for the preceding conversational turn.
	fallbackTargetID := ""
	var fallbackTargetAt time.Time
	for _, source := range pack.Sources {
		if source.ChannelID == target.Envelope.ChannelID && source.Provenance == "human_message" && source.AuthorID == target.Envelope.UserID && strings.TrimSpace(source.Text) == strings.TrimSpace(target.Envelope.Text) && (fallbackTargetID == "" || source.ObservedAt.After(fallbackTargetAt)) {
			fallbackTargetID, fallbackTargetAt = source.ID, source.ObservedAt
		}
	}
	var previous *types.ContextSource
	for index := range pack.Sources {
		source := &pack.Sources[index]
		if source.ChannelID != target.Envelope.ChannelID || (source.Provenance != "human_message" && source.Provenance != "agent_output_unverified") || source.ID == targetSourceID || source.ID == fallbackTargetID {
			continue
		}
		if previous == nil || source.ObservedAt.After(previous.ObservedAt) {
			previous = source
		}
	}
	if previous == nil || previous.Provenance != "agent_output_unverified" {
		return false
	}
	targetAt := target.Envelope.EventTime
	if targetAt.IsZero() {
		targetAt = target.Envelope.ReceivedAt
	}
	if !targetAt.IsZero() && !previous.ObservedAt.IsZero() {
		age := targetAt.Sub(previous.ObservedAt)
		if age < 0 || age > 15*time.Minute {
			return false
		}
	}
	return true
}

func isMissingLocationWeatherQuestion(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if !strings.Contains(normalized, "?") || !containsAny(normalized, "weather", "forecast", "temperature") {
		return false
	}
	if containsAny(normalized, " in ", " at ", " near ", " around ") {
		return false
	}
	if index := strings.Index(normalized, " for "); index >= 0 {
		tail := strings.Trim(normalized[index+5:], " .?!")
		if tail != "" && !containsAny(tail, "today", "tomorrow", "tonight", "this morning", "this afternoon", "this evening", "this week", "now", "currently") {
			return false
		}
	}
	return true
}

const classifierInstructions = `You are tos-tag's stateless, tool-free Slack classifier. Using only the immutable input, decide whether to remain silent, react only, answer in the current thread, answer in the channel, start background agent work, or request approval. Choose the least disruptive placement. A direct mention is a hard participation trigger but not automatically a thread: choose reply_in_channel when the expected answer is brief, self-contained, useful to the channel, and unlikely to invite follow-up; choose reply_in_thread when the answer itself is likely to become a deeper dive, needs multiple explanatory steps, code, a table, an artifact, concerns a narrow side topic, or is likely to continue as a conversation. Internal retrieval or tool use alone does not force a thread: judge placement by the expected final Slack message. A requested table, structured report, code sample, artifact, research result, multi-part comparison, implementation plan, migration plan, or question asking what would need to change is not a brief answer and must use reply_in_thread with at least standard/medium routing even when it can be self-contained. Honor an explicit request to reply in the channel or in a thread. When active_thread is true, keep any substantive response in that thread. For ambient messages, default to silent on ambiguity, repetition, or an already-answered question.

likely_addressed_to_agent is a deterministic conversational signal, not a direct mention: it is true only when the current author is the sole recent human participant represented in destination context and the immediately preceding destination message came from Tag. Treat a clear question or imperative request with this signal as likely directed to Tag even when it omits a mention. conversation_focus is a short chronological view of the latest destination-local human and Tag turns; use it before the larger source set to resolve conversational pronouns and short follow-ups. In an active thread, conversation_focus contains only thread-partition turns: do not reinterpret a short thread follow-up using an unrelated channel or other-thread message from the larger source set. When a question such as "are we using it?" has one clear referent in the immediately preceding Tag turn, answer the underlying question instead of asking what "it" means or merely describing the referent. A time-proximate follow-up such as "Take a look at the OpenAI pricing page" continues the same conversation: admit full-agent retrieval of the named public source, and do not ask the user to provide a URL when that authoritative source is readily discoverable with web search. When a short active-thread message answers a Tag clarification, compose it with the earlier unresolved human request and answer that composed request. Do not claim the channel has only one human member; recent context is not a complete membership roster. If such a weather or forecast question omits the location needed to answer, reply directly in the channel with one short location clarification rather than staying silent or starting a worker.

An active Tag thread is a strong continuation signal, not permission to intrude on a turn addressed to another person. A human-authored thread reply that begins with another Slack user mention and neither mentions nor explicitly addresses Tag is a human-to-human handoff and is deterministically suppressed before this classifier. Mentions later in a request, such as "summarize this for <@U123>", remain valid requested recipients and do not suppress Tag.

Set source_write_requested true when the message asks tos-tag to edit, implement, fix, patch, refactor, commit, push, merge, deploy, or otherwise mutate TelemetryOS source code or a TelemetryOS repository. Set it false for read-only investigation, code explanation or review, code samples or pseudocode, Linear issue CRUD, Wiki page CRUD, Slack configuration, and non-source actions. Requested Wiki page text may itself mention code, source writes, source-write redirection, regressions, or implementation; those are document contents, not source-mutation instructions. Source access is permanently read-only: a source-write request must receive only the control-plane redirect to create a Linear bug for broken existing behavior or a Linear feature for new or changed behavior. Do not propose or recommend a source mutation workflow.

Set authoritative_product_retrieval_required true for any question or requested claim about a named TelemetryOS product, hardware model, plan, trial, pricing tier, feature, limit, compatibility rule, setup procedure, API, security/compliance property, or positioning. Also set it true for every request to create or revise TelemetryOS marketing copy, including campaigns, landing pages, sales collateral, customer announcements, social posts, headlines, taglines, and CTAs; the worker must use the marketing-messaging skill and retrieve the full corporate source before drafting. This includes apparently simple questions such as what the Premium Trial is about. Set it false for generic technology questions, social messages, source-write redirects, and questions whose complete authoritative excerpt is already supplied in the current message. When true, admit a full agent and require it to retrieve the Agent Wiki Primer and/or official TelemetryOS product documentation before answering; Slack context and generic model memory are not authoritative product evidence. Authoritative retrieval alone does not force a thread: a short definitional what-is product, plan, or trial question and another simple factual product answer that should fit in one short message belong in the channel, while a comparison, table, caveated explanation, or likely follow-up belongs in a thread. Product retrieval requires at least standard/medium routing in either placement because the worker must reliably use the knowledge tools and fetch full authoritative content.

Use destination-safe sources when they materially resolve the current message, and list every source used in releasable_evidence_ids. Leave releasable_evidence_ids empty when the full agent must retrieve the answer from product knowledge, the Wiki, public documentation, or the web; source IDs are only for exact immutable context sources supplied in the input. A clear unresolved comparison about the organization's products, billing plans, pricing tiers, or device tiers in assist mode is useful ambient work even without a direct mention: admit a full agent so it can retrieve authoritative knowledge, using a thread and standard/medium routing. Do not generalize this rule to every ambient factual question. A clear operational-status question with current destination-safe incident evidence requires an answer rather than react-only; prefer a thread when the status is likely to need supporting detail or follow-up. Never answer from restricted-awareness-only material. Participation mode controls initiative: assist may answer a clear unresolved ambient question when useful, but it must not start full-agent work merely because an unmentioned top-level declarative status says that something failed, is unavailable, or needs attention. The likely_addressed_to_agent signal authorizes a clear question or request, not a bare declaration. Proactive may react to or answer a clear actionable failure, risk, or incident signal; mention and observe restrictions are enforced independently by the admission service. Prefer react-only for a top-level ambient, non-urgent metric or risk observation that asks for no work and needs no explanation. In particular, an elevated metric explicitly described as stable or steady with no errors or failures should normally receive only warning, not a reply or worker job, unless it is marked urgent, critical, paging, failing, or needing attention.

An ambient alignment intervention is appropriate when a current human statement materially conflicts with a recent, destination-safe factual report from a different human in another public channel, or with a clear destination-safe fact, and surfacing that conflict would prevent confusion, duplicated work, a bad operational decision, or a missed incident. Default to silent for opinions, preferences, predictions, minor wording differences, stale reports, ambiguous entities, weak inferences, or facts that do not change what this channel should know or do. A source author absent from destination_recent_participant_ids is a stronger reason to help, but that list means recent visible participation only: never claim the person is not a channel member. Attribute human reports without blame and without converting them into verified truth—for example, response_intent may say to note that <@AUTHOR_ID> reported the server down in <#CHANNEL_ID> and ask the worker to reconcile or verify the current state. Use only author_id, channel_id, channel_name, observed_at, and text supplied by a cited source; never invent a person, channel, time, or fact. A single clear conflict needing only a brief attributed status note must use reply_in_channel even though someone might follow up; use a thread or background work only when the response itself must reconcile evidence, investigate, or explain multiple facts. Use speech_balloon for ordinary alignment, warning or rotating_light for material operational risk. Never perform an alignment intervention from agent_output_unverified or any restricted-awareness source, and never reveal even the existence of another private channel, DM, or group DM. If a direct mention asks to quote, paste, show, share, or summarize private-channel, DM, or group-DM content outside its destination, use a light/low full agent for one brief reply_in_channel refusal. Cite no evidence and reveal no awareness, names, channels, or content.

For an unmistakably social message that needs no factual answer, tools, action, private context, or follow-up—such as thanks, a greeting, a farewell, light praise, or brief friendly banter—you may answer directly without a full agent. Set reply_in_channel for a top-level message or reply_in_thread for an active thread, set requires_full_agent false, leave all agent recommendation and evidence fields empty, and place one natural reply of at most 240 characters in direct_reply. Keep it to one plain-text line. It must not contain facts, advice, commitments, status claims, links, mentions, identifiers, markdown, or claims derived from context. Use white_check_mark for thanks or praise and speech_balloon for a greeting or banter. Examples include "You're welcome!", "Happy to help!", and "Morning!". The only non-social direct-reply exception is one bounded clarification for a likely-addressed weather or forecast question missing its location; ask which location to check, make no weather claim, and use speech_balloon. Otherwise leave direct_reply empty. Never use direct_reply to answer a substantive question, summarize content, perform a transformation, report work, or avoid full-agent admission.

For every non-silent action select exactly one allowed Slack reaction. Apply these meanings consistently: eyes acknowledges newly requested work; thinking_face marks a question or decision that needs consideration; white_check_mark marks a completed, confirmed, or immediately resolved acknowledgement; warning marks a non-urgent risk or degraded condition; rotating_light marks an active urgent incident; hammer_and_wrench marks implementation, repair, or build work; speech_balloon marks a conversational explanation or discussion. Select only from the allowed list and choose the closest semantic match.

Every reply, background action, or approval request other than a validated direct_reply is executed by the full agent, so those outcomes must set requires_full_agent true and select exactly one available agent profile plus its advertised strength and reasoning effort. Route brief factual answers and simple transformations to an available light/low profile. Route comparisons, bounded plans, requested tables, structured formatting, and moderate analysis to an available standard/medium profile unless the underlying work itself is unusually difficult or consequential. Route authoring durable documents, deep multi-step analysis, tricky debugging or root-cause analysis, incident or security work, complex use of multiple tools, high-consequence decisions, and genuinely document-sized synthesis or long-form expository work that will probably need an Agent Wiki artifact to the available strong profile. The strong profile is ChatGPT 5.6 Sol at medium reasoning effort: choose it for greater model intelligence, not merely for more reasoning effort. A start_background_job action must use at least standard/medium; use strong/medium when it investigates an active production incident or security concern. In proactive mode, an explicit current failure, outage, or needs-attention statement is a high-confidence actionable signal; do not stay silent merely because it is not phrased as a question. Mechanical reformatting and formatting alone never justify the strong profile. Choose the least costly profile that safely fits; do not use the strong profile merely because it is the deployment default. Reason codes are compact audit labels and must agree with the immutable booleans: never claim a direct mention when direct_mention is false, never claim an active thread when active_thread is false, and never claim either is absent when its value is true. React-only and direct-reply decisions must set requires_full_agent false and leave the agent recommendation fields empty or none as required by the schema. Never invent profile IDs, evidence IDs, emoji names, or channel facts. Restricted-awareness sources may appear only in restricted_signal_ids and may never ground final prose. Sources with provenance agent_output_unverified are prior generated prose for continuity, not factual evidence, and must not justify confidence or releasable evidence. Do not use tools and do not reveal chain-of-thought.`

type responsesRequest struct {
	Model           string             `json:"model"`
	Instructions    string             `json:"instructions"`
	Input           []responsesInput   `json:"input"`
	Reasoning       responsesReasoning `json:"reasoning"`
	Text            responsesText      `json:"text"`
	MaxOutputTokens int                `json:"max_output_tokens"`
	Store           bool               `json:"store"`
}

type responsesInput struct {
	Role    string                  `json:"role"`
	Content []responsesInputContent `json:"content"`
}

type responsesInputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesReasoning struct {
	Effort string `json:"effort"`
}

type responsesText struct {
	Format map[string]any `json:"format"`
}

type responsesResponse struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

func (r responsesResponse) outputText() string {
	var output strings.Builder
	for _, item := range r.Output {
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			if content.Type == "output_text" {
				output.WriteString(content.Text)
			}
		}
	}
	return strings.TrimSpace(output.String())
}

func classifierSchema(profiles []advertisedAgentProfile, reactions []string) map[string]any {
	profileIDs := []string{""}
	strengths := []string{"none"}
	efforts := []string{""}
	for _, profile := range profiles {
		if !slices.Contains(profileIDs, profile.ID) {
			profileIDs = append(profileIDs, profile.ID)
		}
		if !slices.Contains(strengths, profile.Strength) {
			strengths = append(strengths, profile.Strength)
		}
		if !slices.Contains(efforts, profile.ReasoningEffort) {
			efforts = append(efforts, profile.ReasoningEffort)
		}
	}
	reactionValues := append([]string{""}, reactions...)
	properties := map[string]any{
		"outcome":                 map[string]any{"type": "string", "enum": []string{"silent", "react", "reply_in_thread", "reply_in_channel", "start_background_job", "escalate_for_approval"}},
		"confidence":              map[string]any{"type": "number", "minimum": 0, "maximum": 1},
		"reason_codes":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1},
		"topic_ids":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"releasable_evidence_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"restricted_signal_ids":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"response_intent":         map[string]any{"type": "string"},
		"direct_reply":            map[string]any{"type": "string", "maxLength": 240},
		"source_write_requested":  map[string]any{"type": "boolean"},
		"authoritative_product_retrieval_required": map[string]any{"type": "boolean"},
		"disclosure_class":                         map[string]any{"type": "string", "enum": []string{"destination_safe", "restricted_awareness_only"}},
		"requires_full_agent":                      map[string]any{"type": "boolean"},
		"reaction":                                 map[string]any{"type": "string", "enum": reactionValues},
		"agent_model_profile":                      map[string]any{"type": "string", "enum": profileIDs},
		"agent_model_strength":                     map[string]any{"type": "string", "enum": strengths},
		"agent_reasoning_effort":                   map[string]any{"type": "string", "enum": efforts},
	}
	required := []string{"outcome", "confidence", "reason_codes", "topic_ids", "releasable_evidence_ids", "restricted_signal_ids", "response_intent", "direct_reply", "source_write_requested", "authoritative_product_retrieval_required", "disclosure_class", "requires_full_agent", "reaction", "agent_model_profile", "agent_model_strength", "agent_reasoning_effort"}
	return map[string]any{
		"type":        "json_schema",
		"name":        "tos_tag_classification",
		"description": "A safe Slack participation and full-agent admission decision.",
		"strict":      true,
		"schema": map[string]any{
			"type":                 "object",
			"properties":           properties,
			"required":             required,
			"additionalProperties": false,
		},
	}
}

func validateRecommendation(decision types.ClassificationDecision, profiles []advertisedAgentProfile, reactions []string) error {
	if decision.Outcome == types.OutcomeSilent {
		if decision.Reaction != "" || decision.DirectReply != "" || decision.RequiresFullAgent || decision.AgentModelProfile != "" || decision.AgentModelStrength != "none" || decision.AgentReasoningEffort != "" {
			return errors.New("silent classification included an action recommendation")
		}
		return nil
	}
	if !slices.Contains(reactions, decision.Reaction) {
		return errors.New("classification reaction is not allowlisted")
	}
	if decision.DirectReply != "" {
		if decision.Outcome != types.OutcomeReplyInChannel && decision.Outcome != types.OutcomeReplyInThread {
			return errors.New("direct reply selected an invalid outcome")
		}
		if decision.RequiresFullAgent || decision.AgentModelProfile != "" || decision.AgentModelStrength != "none" || decision.AgentReasoningEffort != "" {
			return errors.New("direct reply included an agent recommendation")
		}
		return nil
	}
	if outcomeNeedsAgent(decision.Outcome) && !decision.RequiresFullAgent {
		return errors.New("classification outcome requires full agent work")
	}
	if !decision.RequiresFullAgent {
		if decision.AgentModelProfile != "" || decision.AgentModelStrength != "none" || decision.AgentReasoningEffort != "" {
			return errors.New("non-agent classification included an agent model recommendation")
		}
		return nil
	}
	for _, profile := range profiles {
		if profile.ID == decision.AgentModelProfile && profile.Strength == decision.AgentModelStrength && profile.ReasoningEffort == decision.AgentReasoningEffort {
			return nil
		}
	}
	return errors.New("classification selected an unavailable agent profile")
}

func withDefaultReaction(decision types.ClassificationDecision, reactions []string) types.ClassificationDecision {
	if decision.Outcome == types.OutcomeSilent || decision.Reaction != "" || len(reactions) == 0 {
		return decision
	}
	preferred := []string{"speech_balloon", "eyes"}
	if decision.SourceWriteRequested {
		preferred = []string{"speech_balloon", "eyes"}
	} else if decision.DirectReply != "" {
		preferred = []string{"white_check_mark", "speech_balloon"}
	} else if decision.Outcome == types.OutcomeStartBackgroundJob || decision.Outcome == types.OutcomeEscalateForApproval {
		preferred = []string{"eyes", "thinking_face"}
	}
	for _, reaction := range preferred {
		if slices.Contains(reactions, reaction) {
			decision.Reaction = reaction
			decision.ReasonCodes = append(decision.ReasonCodes, "policy.default_reaction")
			return decision
		}
	}
	decision.Reaction = reactions[0]
	decision.ReasonCodes = append(decision.ReasonCodes, "policy.default_reaction")
	return decision
}

func withPolicyReactionAllowlist(decision types.ClassificationDecision, providerReaction string, reactions []string) types.ClassificationDecision {
	if decision.Outcome == types.OutcomeSilent || decision.Reaction == "" || decision.Reaction == providerReaction || slices.Contains(reactions, decision.Reaction) {
		return decision
	}
	decision.Reaction = ""
	decision.ReasonCodes = append(decision.ReasonCodes, "policy.reaction_allowlist_fallback")
	return decision
}

const sourceWriteRedirectReply = "TelemetryOS source is read-only here. Please file broken existing behavior as a Linear bug and new or changed behavior as a Linear feature, or ask me to create the issue for you."

func withSourceWritePolicyCorrections(decision types.ClassificationDecision, target Target) types.ClassificationDecision {
	if isObviousWikiPageCRUDRequest(target.Envelope.Text) && !isExplicitSeparateSourceMutationRequest(target.Envelope.Text) {
		decision.Outcome = types.OutcomeReplyInThread
		decision.Confidence = max(decision.Confidence, 0.99)
		decision.ReasonCodes = append(decision.ReasonCodes, "policy.wiki_page_crud_not_source_write")
		decision.ResponseIntent = strings.TrimSpace(decision.ResponseIntent + " Perform the requested Agent Wiki page CRUD through the reviewed Wiki capability; this is not a TelemetryOS source-code write.")
		decision.DirectReply = ""
		decision.SourceWriteRequested = false
		decision.RequiresFullAgent = true
		decision.Reaction = "eyes"
		if decision.AgentModelStrength != "standard" && decision.AgentModelStrength != "strong" {
			decision.AgentModelProfile = ""
			decision.AgentModelStrength = "standard"
			decision.AgentReasoningEffort = "medium"
		}
		return decision
	}
	// The provider's source_write_requested flag is advisory. Confirm it against
	// the user's actual text before replacing a classifier result with the Linear
	// redirect. This prevents report titles and other declarative references such
	// as "GitHub commit volume" from being treated as mutation requests.
	if !isObviousSourceWriteRequest(target.Envelope.Text) {
		if decision.SourceWriteRequested {
			decision.SourceWriteRequested = false
			decision.ReasonCodes = append(decision.ReasonCodes, "policy.unconfirmed_source_write_ignored")
			if decision.DirectReply == sourceWriteRedirectReply {
				decision.DirectReply = ""
			}
		}
		return decision
	}
	outcome := types.OutcomeReplyInChannel
	if target.ActiveThread {
		outcome = types.OutcomeReplyInThread
	}
	return types.ClassificationDecision{
		Outcome:                  outcome,
		Confidence:               max(decision.Confidence, 0.99),
		ReasonCodes:              append(decision.ReasonCodes, "policy.source_write_to_linear"),
		ResponseIntent:           "direct the requester to Linear issue intake because TelemetryOS source access is read-only",
		DirectReply:              sourceWriteRedirectReply,
		SourceWriteRequested:     true,
		ProductRetrievalRequired: false,
		DisclosureClass:          types.DisclosureDestinationSafe,
		Reaction:                 "speech_balloon",
		AgentModelStrength:       "none",
	}
}

func isExplicitSeparateSourceMutationRequest(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	return containsAny(lower,
		"then edit the code", "and edit the code", "then change the code", "and change the code",
		"then modify the code", "and modify the code", "then patch the source", "and patch the source",
		"then implement the code", "and implement the code", "then commit", "and commit",
		"then push", "and push", "then merge", "and merge", "then deploy", "and deploy",
	)
}

func isObviousWikiPageCRUDRequest(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	wikiSurface := containsAny(lower,
		"agent wiki", " wiki ", "wiki page", "wiki artifact", "architecture reference",
		"durable document", "durable documentation", "document you just published",
		"page you just published", "reference you just published", "published artifact",
	)
	pageAction := containsAny(lower,
		"create ", "write ", "publish ", "edit ", "update ", "append ", "add ",
		"revise ", "change ", "delete ", "remove ",
	)
	return wikiSurface && pageAction
}

func isObviousSourceWriteRequest(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	if containsAny(lower, "read the code", "inspect the code", "review the code", "explain the code", "show me the code", "code sample", "code example", "pseudocode") &&
		!containsAny(lower, "then fix", "and fix", "then change", "and change", "then implement", "and implement") {
		return false
	}
	explicit := containsAny(lower,
		"edit the code", "change the code", "modify the code", "update the code", "write the code",
		"edit the source", "change the source", "modify the source", "patch the source",
		"commit the ", "commit this", "please commit ",
		"push the ", "push this", "please push ", "open a pull request", "open a pr",
		"merge the ", "merge this", "please merge ",
		"deploy the ", "deploy this", "please deploy ", "ship the ", "ship this",
	)
	engineeringAction := containsAny(lower, "implement ", "refactor ", "patch ", "fix the bug", "fix this bug", "fix that bug", "fix the regression", "build the feature", "add support for ", "remove support for ")
	// Match source nouns as whole words. Loose prefixes such as " repo" and
	// " pr" also match ordinary status-report words such as "reports" and
	// product names such as "Premium".
	sourceSurface := containsAnyWholeWord(lower, "code", "codebase", "source", "repo", "repository", "branch", "commit") || strings.Contains(lower, "pull request")
	// tos-tag is itself a repository/source surface. Match the mutation verb and
	// name together so a normal <@tos-tag> mention is not mistaken for a write.
	namedRepoMutation := containsAny(lower,
		"change tos-tag", "edit tos-tag", "modify tos-tag", "update tos-tag", "write tos-tag",
		"fix tos-tag", "patch tos-tag", "refactor tos-tag", "add to tos-tag", "remove from tos-tag",
	)
	// Commit, push, merge, and deploy can also be nouns in report titles. They
	// are handled only by the explicit request phrases above; do not let one noun
	// satisfy both the source-surface and mutation-action sides of this check.
	return explicit || engineeringAction || namedRepoMutation || (sourceSurface && containsAny(lower, "edit", "change", "modify", "update", "write", "fix", "add", "remove", "rename", "delete"))
}

func containsAnyWholeWord(text string, words ...string) bool {
	tokens := strings.FieldsFunc(text, func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsNumber(character)
	})
	for _, word := range words {
		if slices.Contains(tokens, word) {
			return true
		}
	}
	return false
}

func withReadOnlyCodeAnalysisPolicyCorrections(decision types.ClassificationDecision, target Target, profiles []advertisedAgentProfile) types.ClassificationDecision {
	// The request text is authoritative for this boundary. A provider may mark a
	// code review or explanation as a source write, but explicit read-only
	// analysis wording must recover the reviewed read path instead of redirecting
	// the user to Linear.
	if !isObviousReadOnlyCodeAnalysisRequest(target.Envelope.Text) {
		return decision
	}
	decision.Outcome = types.OutcomeReplyInThread
	decision.Confidence = max(decision.Confidence, 0.99)
	decision.ReasonCodes = append(decision.ReasonCodes, "policy.read_only_code_analysis_floor")
	decision.DirectReply = ""
	decision.SourceWriteRequested = false
	decision.RequiresFullAgent = true
	decision.AgentModelProfile = ""
	if isSecuritySensitiveCodeAnalysis(target.Envelope.Text) {
		decision.AgentModelStrength = "strong"
		decision.AgentReasoningEffort = "medium"
		decision.ResponseIntent = strings.TrimSpace(decision.ResponseIntent + " Inspect the relevant implementation with the reviewed read-only source capability before reaching security or privacy conclusions; distinguish source evidence from unverified Slack summaries.")
	} else {
		decision.AgentModelStrength = "standard"
		decision.AgentReasoningEffort = "medium"
	}
	return withCanonicalAgentProfile(decision, profiles)
}

func withHighIntelligenceProfileCorrections(decision types.ClassificationDecision, target Target, profiles []advertisedAgentProfile) types.ClassificationDecision {
	if !decision.RequiresFullAgent || !outcomeNeedsAgent(decision.Outcome) {
		return decision
	}
	lower := strings.ToLower(target.Envelope.Text)
	documentAuthoringIntent := containsAny(lower,
		"write a ", "write the ", "write comprehensive", "author a ", "author the ", "author comprehensive",
		"create a ", "create the ", "publish a ", "publish the ", "produce a ", "produce the ",
		"draft a ", "draft the ", "update the ", "revise the ",
	)
	durableDocument := containsAny(lower, "architecture reference", "architecture document", "design document", "design doc", "operator runbook", "operational runbook", "durable documentation", "comprehensive document") &&
		documentAuthoringIntent &&
		!containsAny(lower, "short section", "brief section", "one sentence", "single sentence", "small edit", "minor edit", "append one")
	trickyDebugging := containsAny(lower, "root cause", "root-cause", "race condition", "deadlock", "intermittent", "heisenbug", "privacy leak", "security boundary") &&
		containsAny(lower, "debug", "investigate", "diagnose", "trace", "find", "determine", "review")
	complexTools := containsAny(lower, "cross-reference", "cross reference", "correlate") &&
		containsAny(lower, "logs", "traces", "telemetry", "source", "wiki", "linear", "documentation", "web")
	if !durableDocument && !trickyDebugging && !complexTools {
		return decision
	}
	decision.AgentModelProfile = ""
	decision.AgentModelStrength = "strong"
	decision.AgentReasoningEffort = "medium"
	decision.ReasonCodes = appendUnique(decision.ReasonCodes, "policy.high_intelligence_profile")
	return withCanonicalAgentProfile(decision, profiles)
}

func withOperationalSynthesisPolicyCorrections(decision types.ClassificationDecision, target Target, pack types.ContextPackRevision, profiles []advertisedAgentProfile) types.ClassificationDecision {
	if !target.Envelope.IsMention || target.ActiveThread || !isBroadOperationalStatusQuestion(target.Envelope.Text) {
		return decision
	}
	evidenceIDs := operationalSynthesisEvidenceIDs(pack)
	if len(evidenceIDs) < 2 {
		return decision
	}
	decision.Outcome = types.OutcomeReplyInThread
	if requested, _, ok := requestedReplyPlacement(target.Envelope.Text); ok {
		decision.Outcome = requested
	}
	decision.Confidence = max(decision.Confidence, 0.99)
	decision.ReasonCodes = appendUnique(decision.ReasonCodes, "policy.multi_issue_operational_synthesis")
	decision.ResponseIntent = "synthesize the multiple current destination-safe human operational reports in a focused response; attribute them as reports, distinguish separate issues, and do not present unverified status or root cause as confirmed"
	decision.DirectReply = ""
	decision.ProductRetrievalRequired = false
	decision.RequiresFullAgent = true
	decision.ReleasableEvidenceIDs = nil
	for _, id := range evidenceIDs {
		decision.ReleasableEvidenceIDs = appendUnique(decision.ReleasableEvidenceIDs, id)
	}
	if decision.Reaction != "warning" && decision.Reaction != "rotating_light" {
		decision.Reaction = "thinking_face"
	}
	decision.AgentModelProfile = ""
	decision.AgentModelStrength = "strong"
	decision.AgentReasoningEffort = "medium"
	return withCanonicalAgentProfile(decision, profiles)
}

func isBroadOperationalStatusQuestion(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if !strings.Contains(lower, "?") {
		return false
	}
	return containsAny(lower,
		"operational issues", "operational concerns", "any incidents", "current incidents", "open incidents",
		"anything broken", "what is broken", "what's broken",
	)
}

func operationalSynthesisEvidenceIDs(pack types.ContextPackRevision) []string {
	channels := make(map[string]bool)
	ids := make([]string, 0, len(pack.Sources))
	for _, source := range pack.Sources {
		if source.ID == "" || source.ChannelID == "" || channels[source.ChannelID] || source.Provenance != "human_message" || source.DisclosureClass != types.DisclosureDestinationSafe {
			continue
		}
		lower := strings.ToLower(source.Text)
		if !containsIncident(lower) && !containsAny(lower, " blocked", " blocking", "regression", "needs triage", "root cause", "unconfirmed") {
			continue
		}
		channels[source.ChannelID] = true
		ids = append(ids, source.ID)
	}
	return ids
}

func withImplementationPlanningPolicyCorrections(decision types.ClassificationDecision, target Target, profiles []advertisedAgentProfile) types.ClassificationDecision {
	if !target.Envelope.IsMention || !isImplementationPlanningRequest(target.Envelope.Text) {
		return decision
	}
	decision.Outcome = types.OutcomeReplyInThread
	decision.Confidence = max(decision.Confidence, 0.99)
	decision.ReasonCodes = slices.DeleteFunc(decision.ReasonCodes, func(reason string) bool {
		return reason == "policy.source_write_to_linear"
	})
	decision.ReasonCodes = appendUnique(decision.ReasonCodes, "policy.implementation_plan_thread")
	decision.ResponseIntent = "provide a bounded implementation plan without modifying source; explain the required changes, safety constraints, and verification"
	decision.DirectReply = ""
	decision.SourceWriteRequested = false
	decision.RequiresFullAgent = true
	decision.AgentModelProfile = ""
	decision.AgentModelStrength = "standard"
	decision.AgentReasoningEffort = "medium"
	return withCanonicalAgentProfile(decision, profiles)
}

func isObviousReadOnlyCodeAnalysisRequest(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" || isObviousSourceWriteRequest(lower) {
		return false
	}
	readOnlyAction := containsAny(lower, "read ", "inspect ", "review ", "explain ", "analyze ", "investigate", "trace ", "show me ")
	codeSurface := containsAny(lower, " code", "codebase", " source", " repository", " repo", "package", "module", "component", "service", "authentication", "handler", "implementation", "classifier", "classification", "admission gate", "context boundary", "context construction", "privacy boundary", "security boundary", "delivery reconciliation")
	ownershipQuestion := containsAny(lower,
		"which package", "what package", "which module", "what module", "which component", "what component",
		"which repository", "what repository", "which repo", "what repo", "where in the code",
	) || (containsAny(lower, " owns ", "owned by", "responsibilities") && codeSurface)
	return (readOnlyAction && codeSurface) || ownershipQuestion
}

func isSecuritySensitiveCodeAnalysis(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return isObviousReadOnlyCodeAnalysisRequest(lower) && containsAny(lower, "security", "privacy", " private ", "leak", "threat", "attack", "exposure", "cross-channel", "cross channel")
}

func withBriefMentionPolicyCorrections(decision types.ClassificationDecision, target Target, profiles []advertisedAgentProfile) types.ClassificationDecision {
	if target.Envelope.IsMention && decision.DirectReply != "" && !decision.SourceWriteRequested && !isDirectSocialCandidate(target.Envelope.Text) {
		decision.DirectReply = ""
		decision.RequiresFullAgent = true
		decision.AgentModelProfile = ""
		decision.AgentModelStrength = ""
		decision.AgentReasoningEffort = ""
		decision.Reaction = "thinking_face"
		decision.ReasonCodes = append(decision.ReasonCodes, "policy.substantive_direct_reply_rejected")
	}
	if !target.Envelope.IsMention || target.ActiveThread || decision.SourceWriteRequested || decision.ProductRetrievalRequired || decision.DirectReply != "" || len(decision.ReleasableEvidenceIDs) != 0 || decision.Reaction == "warning" || decision.Reaction == "rotating_light" || !briefSelfContainedMention(target.Envelope.Text) {
		return decision
	}
	decision.Outcome = types.OutcomeReplyInChannel
	decision.Confidence = max(decision.Confidence, 0.99)
	decision.ReasonCodes = append(decision.ReasonCodes, "policy.brief_surface_channel")
	decision.RequiresFullAgent = true
	return withLightestProfile(decision, profiles)
}

func withProductKnowledgePolicyCorrections(decision types.ClassificationDecision, target Target, profiles []advertisedAgentProfile) types.ClassificationDecision {
	// Product-topic labels such as "pricing" are not sufficient on their own:
	// a request that explicitly names an external authoritative source belongs
	// to public-web retrieval, not the TelemetryOS Primer/product-doc path.
	if isClearlyExternalPublicSourceQuestion(target.Envelope.Text) {
		if decision.ProductRetrievalRequired || isObviousProductKnowledgeQuestion(target.Envelope.Text, decision) {
			decision.ProductRetrievalRequired = false
			decision.ReasonCodes = append(decision.ReasonCodes, "policy.external_public_source")
		}
		return decision
	}
	// The request text is authoritative here. Context about a feature regression
	// can legitimately produce a "feature" topic, but that must not turn an
	// unmistakable operational-status question into product-document retrieval.
	if isClearlyOperationalSchedulingQuestion(target.Envelope.Text) {
		if decision.ProductRetrievalRequired || isObviousProductKnowledgeQuestion(target.Envelope.Text, decision) {
			decision.ProductRetrievalRequired = false
			decision.ReasonCodes = append(decision.ReasonCodes, "policy.non_product_operational_question")
		}
		return decision
	}
	productQuestion := decision.ProductRetrievalRequired || isObviousProductKnowledgeQuestion(target.Envelope.Text, decision)
	providerSourceWrite := decision.SourceWriteRequested
	// The provider can over-index on ordinary product-state verbs such as
	// "changes" or "moves" and incorrectly mark a plan comparison as a source
	// mutation. Product questions only lose their retrieval path when the text
	// itself contains an unambiguous source-write request.
	if decision.SourceWriteRequested && productQuestion && !isObviousSourceWriteRequest(target.Envelope.Text) {
		decision.SourceWriteRequested = false
		decision.ReasonCodes = append(decision.ReasonCodes, "policy.product_question_not_source_write")
	}
	if decision.SourceWriteRequested || !productQuestion {
		return decision
	}
	corrected := types.ClassificationDecision{
		Outcome:                  types.OutcomeReplyInThread,
		Confidence:               max(decision.Confidence, 0.99),
		ReasonCodes:              append(decision.ReasonCodes, "policy.authoritative_product_retrieval"),
		TopicIDs:                 append([]string(nil), decision.TopicIDs...),
		ReleasableEvidenceIDs:    append([]string(nil), decision.ReleasableEvidenceIDs...),
		ResponseIntent:           "retrieve authoritative TelemetryOS product evidence from the Agent Wiki Primer and/or official product documentation before answering; do not answer from model memory or Slack context alone",
		ProductRetrievalRequired: true,
		DisclosureClass:          types.DisclosureDestinationSafe,
		RequiresFullAgent:        true,
		Reaction:                 "thinking_face",
		AgentModelStrength:       "standard",
		AgentReasoningEffort:     "medium",
	}
	if target.ActiveThread {
		corrected.Outcome = types.OutcomeReplyInThread
	} else if !providerSourceWrite && (decision.Outcome == types.OutcomeReplyInChannel || isBriefProductDefinitionQuestion(target.Envelope.Text)) {
		corrected.Outcome = types.OutcomeReplyInChannel
	}
	return withCanonicalAgentProfile(corrected, profiles)
}

func isClearlyExternalPublicSourceQuestion(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if containsAny(lower,
		"telemetryos", "telemetry os", "premium trial", "premium plan", "enterprise plan", "billing plan",
		"subscription plan", "subscription tier", "node pro", "node mini", "tos node", "device tier", "product tier",
	) {
		return false
	}
	tokens := strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && r != '.' && r != '-'
	})
	commercial := map[string]bool{"pricing": true, "price": true, "prices": true, "cost": true, "costs": true, "billing": true, "rate": true, "rates": true}
	generic := map[string]bool{
		"a": true, "about": true, "an": true, "and": true, "are": true, "at": true, "can": true, "current": true, "do": true, "does": true,
		"for": true, "from": true, "how": true, "i": true, "in": true, "is": true, "latest": true, "look": true, "me": true, "of": true,
		"official": true, "on": true, "our": true, "page": true, "pages": true, "plan": true, "plans": true, "pricing": true, "price": true,
		"prices": true, "cost": true, "costs": true, "billing": true, "rate": true, "rates": true, "option": true, "options": true, "product": true, "provider": true,
		"public": true, "site": true, "subscription": true, "the": true, "their": true, "this": true, "token": true, "tokens": true,
		"vendor": true, "web": true, "website": true, "what": true, "where": true, "which": true, "with": true, "your": true,
	}
	for index, token := range tokens {
		if !commercial[token] {
			continue
		}
		start, end := max(0, index-4), min(len(tokens), index+5)
		for _, candidate := range tokens[start:end] {
			if !generic[candidate] && len(candidate) > 1 {
				return true
			}
		}
	}
	return false
}

func isBriefProductDefinitionQuestion(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" || len(strings.Fields(lower)) > 18 || strings.ContainsAny(lower, "\n;") {
		return false
	}
	if containsAny(lower, "compare", "comparison", "difference", " versus ", " vs ", "trade-off", "tradeoff", "what changes", "how does", "how do", "why ", "which ") {
		return false
	}
	return strings.HasPrefix(lower, "what is ") || strings.HasPrefix(lower, "what's ") || strings.HasPrefix(lower, "does ") || strings.HasPrefix(lower, "can ") || strings.HasPrefix(lower, "is ")
}

func isClearlyOperationalSchedulingQuestion(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if containsAny(lower, "premium", "enterprise", "billing", "pricing", "trial", "node pro", "node mini", "telemetryos", "telemetry os", "product", "subscription", "device tier") {
		return false
	}
	return containsAny(lower,
		"deploy window", "deployment window", "release window", "maintenance window",
		"operational issues", "operational concerns", "any incidents", "current incidents", "open incidents",
		"anything broken", "what is broken", "what's broken",
	)
}

func isObviousProductKnowledgeQuestion(text string, decision types.ClassificationDecision) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	question := strings.Contains(lower, "?") || containsAny(lower, "what is", "what's", "what are", "how does", "how do", "does the", "can the", "tell me about", "explain the", "compare ", "difference between")
	if !question {
		return false
	}
	for _, topic := range decision.TopicIDs {
		if containsAny(strings.ToLower(topic), "product", "pricing", "billing", "trial", "plan", "hardware", "device", "compatibility", "feature") {
			return true
		}
	}
	if containsAny(strings.ToLower(decision.ResponseIntent), "authoritative product", "primer wiki", "product documentation", "public documentation") {
		return true
	}
	if strings.Contains(lower, "premium") && strings.Contains(lower, "enterprise") {
		return true
	}
	return containsAny(lower,
		"telemetryos", "telemetry os", "premium trial", "premium plan", "enterprise plan", "billing plan", "pricing plan",
		"subscription plan", "subscription tier", "node pro", "node mini", "tos node", "device tier", "product tier",
	)
}

func withDirectMentionPolicyCorrections(decision types.ClassificationDecision, target Target, profiles []advertisedAgentProfile) types.ClassificationDecision {
	if !target.Envelope.IsMention || target.ActiveThread || !requestsCrossDestinationPrivateDisclosure(target.Envelope.Text) {
		return decision
	}
	corrected := types.ClassificationDecision{
		Outcome:           types.OutcomeReplyInChannel,
		Confidence:        max(decision.Confidence, 0.99),
		ReasonCodes:       append(decision.ReasonCodes, "policy.cross_destination_private_refusal"),
		ResponseIntent:    "briefly refuse to disclose private-channel, DM, or group-DM content because it is destination-local; reveal no awareness, source, channel, person, or content",
		DisclosureClass:   types.DisclosureDestinationSafe,
		RequiresFullAgent: true,
		Reaction:          "warning",
	}
	return withLightestProfile(corrected, profiles)
}

func requestsCrossDestinationPrivateDisclosure(text string) bool {
	lower := strings.ToLower(text)
	privateSurface := containsAny(lower, "private channel", "private-channel", "private conversation", "direct message", "group dm", "group-dm", " dm ", " dms ")
	disclosureVerb := containsAny(lower, "quote", "paste", "show", "share", "summarize", "repeat", "copy", "tell me what", "what did")
	return privateSurface && disclosureVerb
}

func withAddressedSocialPolicyCorrections(decision types.ClassificationDecision, target Target) types.ClassificationDecision {
	if target.ActiveThread && isDirectSocialCandidate(target.Envelope.Text) {
		fallback := directSocialFallback(target.Envelope.Text, true)
		fallback.Confidence = max(decision.Confidence, 0.99)
		fallback.ReasonCodes = append(decision.ReasonCodes, "policy.thread_social_reply", "policy.social_direct_reply_fallback")
		return fallback
	}
	if target.ActiveThread || target.Envelope.IsMention || !explicitlyAddressesTag(target.Envelope.Text) || !isDirectSocialCandidate(target.Envelope.Text) {
		return decision
	}
	fallback := directSocialFallback(target.Envelope.Text, false)
	fallback.Confidence = max(decision.Confidence, 0.99)
	fallback.ReasonCodes = append(decision.ReasonCodes, "policy.addressed_social_reply", "policy.social_direct_reply_fallback")
	return fallback
}

func explicitlyAddressesTag(text string) bool {
	words := strings.Fields(strings.Map(func(character rune) rune {
		switch character {
		case ',', '.', '!', '?', ';', ':', '-', '—', '–':
			return ' '
		default:
			return character
		}
	}, strings.ToLower(text)))
	return slices.Contains(words, "tag")
}

func withAmbientPolicyCorrections(decision types.ClassificationDecision, target Target, pack types.ContextPackRevision, profiles []advertisedAgentProfile) types.ClassificationDecision {
	if decision.DirectReply != "" || target.Envelope.IsMention || target.ActiveThread {
		return decision
	}
	if target.Mode == types.ModeAssist && isUndirectedAmbientQuestion(target.Envelope.Text) && !decision.ProductRetrievalRequired && len(decision.ReleasableEvidenceIDs) == 0 && len(decision.RestrictedSignalIDs) == 0 {
		return types.ClassificationDecision{
			Outcome:            types.OutcomeSilent,
			Confidence:         max(decision.Confidence, 0.99),
			ReasonCodes:        append(decision.ReasonCodes, "policy.undirected_ambient_question"),
			DisclosureClass:    types.DisclosureDestinationSafe,
			AgentModelStrength: "none",
		}
	}
	if decision.Outcome == types.OutcomeReact && isUndirectedGroupGreeting(target.Envelope.Text) {
		return types.ClassificationDecision{
			Outcome:            types.OutcomeSilent,
			Confidence:         max(decision.Confidence, 0.99),
			ReasonCodes:        append(decision.ReasonCodes, "policy.undirected_group_greeting"),
			DisclosureClass:    types.DisclosureDestinationSafe,
			AgentModelStrength: "none",
		}
	}
	if asksOperationalStatus(target.Envelope.Text) {
		for _, source := range pack.Sources {
			if source.DisclosureClass != types.DisclosureDestinationSafe || (source.Partition != types.PartitionEvidence && source.Partition != types.PartitionSituation) || !containsIncident(source.Text) {
				continue
			}
			switch decision.Outcome {
			case types.OutcomeSilent, types.OutcomeReact:
				decision.Outcome = types.OutcomeReplyInThread
			case types.OutcomeReplyInChannel, types.OutcomeReplyInThread:
				// Preserve the provider's useful placement choice.
			default:
				return decision
			}
			decision.RequiresFullAgent = true
			decision.ReleasableEvidenceIDs = appendUnique(decision.ReleasableEvidenceIDs, source.ID)
			if decision.AgentModelProfile == "" || decision.AgentModelStrength == "" || decision.AgentModelStrength == "none" {
				decision = withLightestProfile(decision, profiles)
			}
			decision.ReasonCodes = append(decision.ReasonCodes, "policy.operational_question_requires_answer")
			break
		}
	}
	if source, ok := alignmentConflict(target, pack); ok {
		if decision.Reaction != "speech_balloon" && decision.Reaction != "warning" && decision.Reaction != "rotating_light" {
			decision.Reaction = "speech_balloon"
			decision.ReasonCodes = append(decision.ReasonCodes, "policy.alignment_reaction")
		}
		switch decision.Outcome {
		case types.OutcomeSilent:
			decision.Outcome = types.OutcomeReplyInChannel
			decision.Confidence = max(decision.Confidence, 0.99)
			decision.RequiresFullAgent = true
			decision.ReleasableEvidenceIDs = appendUnique(decision.ReleasableEvidenceIDs, source.ID)
			decision.ResponseIntent = fmt.Sprintf("briefly surface and reconcile the conflicting public report from <@%s> in <#%s> without assigning blame or treating the report as verified fact", source.AuthorID, source.ChannelID)
			decision = withLightestProfile(decision, profiles)
			decision.ReasonCodes = append(decision.ReasonCodes, "policy.alignment_requires_message")
		case types.OutcomeReplyInChannel:
			decision.Confidence = max(decision.Confidence, 0.99)
			decision.ReasonCodes = append(decision.ReasonCodes, "policy.brief_alignment_in_channel")
		case types.OutcomeReplyInThread:
			decision.Outcome = types.OutcomeReplyInChannel
			decision.Confidence = max(decision.Confidence, 0.99)
			decision.ReasonCodes = append(decision.ReasonCodes, "policy.brief_alignment_in_channel")
		case types.OutcomeReact:
			decision.Outcome = types.OutcomeReplyInChannel
			decision.Confidence = max(decision.Confidence, 0.99)
			decision.RequiresFullAgent = true
			decision.ReleasableEvidenceIDs = appendUnique(decision.ReleasableEvidenceIDs, source.ID)
			decision = withLightestProfile(decision, profiles)
			decision.ReasonCodes = append(decision.ReasonCodes, "policy.alignment_requires_message")
		}
		return decision
	}
	if decision.Outcome == types.OutcomeSilent {
		return decision
	}
	if isStableNonUrgentMetricObservation(target.Envelope.Text) && decision.Outcome != types.OutcomeReact {
		return types.ClassificationDecision{
			Outcome:            types.OutcomeReact,
			Confidence:         max(decision.Confidence, 0.99),
			ReasonCodes:        append(decision.ReasonCodes, "policy.stable_metric_reaction_only"),
			DisclosureClass:    types.DisclosureDestinationSafe,
			Reaction:           "warning",
			AgentModelStrength: "none",
		}
	}
	return decision
}

func isUndirectedAmbientQuestion(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	return strings.Contains(normalized, "?") && (strings.HasPrefix(normalized, "does anyone know") || strings.HasPrefix(normalized, "does anybody know") || strings.HasPrefix(normalized, "anyone know") || strings.HasPrefix(normalized, "anybody know") || strings.HasPrefix(normalized, "can anyone tell") || strings.HasPrefix(normalized, "can anybody tell"))
}

func isStableNonUrgentMetricObservation(text string) bool {
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "%") || !containsAny(lower, "stable", "steady", "held", "holding") || !containsAny(lower, "no errors", "without errors", "without any errors", "no failures", "without failures", "without any failures", "nothing failing") {
		return false
	}
	return !containsAny(lower, "urgent", "critical", "paging", "page fired", "failing", "failed", "outage", "needs attention", "investigate")
}

func isUndirectedGroupGreeting(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return containsAny(lower, "everyone", "everybody", "team", "folks") && containsAny(lower, "morning", "afternoon", "evening", "hello", "hi ", "hey ") && !strings.Contains(lower, "tag") && !strings.Contains(lower, "?")
}

func withLightestProfile(decision types.ClassificationDecision, profiles []advertisedAgentProfile) types.ClassificationDecision {
	if len(profiles) == 0 {
		return decision
	}
	selected := profiles[0]
	for _, profile := range profiles {
		if profile.Strength == "light" {
			selected = profile
			break
		}
	}
	decision.AgentModelProfile = selected.ID
	decision.AgentModelStrength = selected.Strength
	decision.AgentReasoningEffort = selected.ReasoningEffort
	return decision
}

func withCanonicalAgentProfile(decision types.ClassificationDecision, profiles []advertisedAgentProfile) types.ClassificationDecision {
	if decision.Outcome == types.OutcomeSilent {
		if decision.Reaction != "" || decision.DirectReply != "" || decision.RequiresFullAgent || decision.AgentModelProfile != "" || (decision.AgentModelStrength != "" && decision.AgentModelStrength != "none") || decision.AgentReasoningEffort != "" || len(decision.ReleasableEvidenceIDs) != 0 || len(decision.RestrictedSignalIDs) != 0 {
			decision.ReasonCodes = append(decision.ReasonCodes, "policy.silent_action_cleared")
		}
		decision.Reaction = ""
		decision.DirectReply = ""
		decision.RequiresFullAgent = false
		decision.AgentModelProfile = ""
		decision.AgentModelStrength = "none"
		decision.AgentReasoningEffort = ""
		decision.ReleasableEvidenceIDs = nil
		decision.RestrictedSignalIDs = nil
		return decision
	}
	if outcomeNeedsAgent(decision.Outcome) && decision.DirectReply == "" && !decision.RequiresFullAgent {
		decision.RequiresFullAgent = true
		decision.ReasonCodes = append(decision.ReasonCodes, "policy.agent_requirement_inferred")
	}
	if !decision.RequiresFullAgent {
		if decision.Outcome == types.OutcomeReact || decision.DirectReply != "" {
			if decision.AgentModelProfile != "" || (decision.AgentModelStrength != "" && decision.AgentModelStrength != "none") || decision.AgentReasoningEffort != "" {
				decision.ReasonCodes = append(decision.ReasonCodes, "policy.non_agent_profile_cleared")
			}
			decision.AgentModelProfile = ""
			decision.AgentModelStrength = "none"
			decision.AgentReasoningEffort = ""
		}
		return decision
	}
	if decision.DirectReply != "" {
		decision.DirectReply = ""
		decision.ReasonCodes = append(decision.ReasonCodes, "policy.agent_direct_reply_cleared")
	}
	for _, profile := range profiles {
		if profile.ID == decision.AgentModelProfile && profile.Strength == decision.AgentModelStrength && profile.ReasoningEffort == decision.AgentReasoningEffort {
			return decision
		}
	}
	wantStrength := decision.AgentModelStrength
	if wantStrength != "light" && wantStrength != "standard" && wantStrength != "strong" {
		switch decision.AgentReasoningEffort {
		case "max", "high", "xhigh":
			wantStrength = "strong"
		case "medium":
			wantStrength = "standard"
		default:
			wantStrength = "light"
		}
	}
	for _, profile := range profiles {
		if profile.Strength == wantStrength {
			decision.AgentModelProfile = profile.ID
			decision.AgentModelStrength = profile.Strength
			decision.AgentReasoningEffort = profile.ReasoningEffort
			decision.ReasonCodes = append(decision.ReasonCodes, "policy.canonical_agent_profile")
			return decision
		}
	}
	return decision
}

func withBackgroundProfileFloor(decision types.ClassificationDecision, target Target, profiles []advertisedAgentProfile) types.ClassificationDecision {
	if decision.Outcome != types.OutcomeStartBackgroundJob {
		return decision
	}
	want := "standard"
	lower := strings.ToLower(target.Envelope.Text)
	if containsAny(lower, "security", "token exposure", "credential exposure", "breach", "production systems") {
		want = "strong"
	}
	if decision.AgentModelStrength == want || (want == "standard" && decision.AgentModelStrength == "strong") {
		return decision
	}
	for _, profile := range profiles {
		if profile.Strength == want {
			decision.AgentModelProfile = profile.ID
			decision.AgentModelStrength = profile.Strength
			decision.AgentReasoningEffort = profile.ReasoningEffort
			decision.ReasonCodes = append(decision.ReasonCodes, "policy.background_profile_floor")
			return decision
		}
	}
	return decision
}

func appendUnique(values []string, value string) []string {
	if value == "" || slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func recommendationErrorCode(err error) string {
	switch err.Error() {
	case "silent classification included an action recommendation":
		return "silent_with_action"
	case "classification reaction is not allowlisted":
		return "reaction_not_allowlisted"
	case "classification outcome requires full agent work":
		return "agent_required"
	case "direct reply selected an invalid outcome":
		return "direct_reply_outcome"
	case "direct reply included an agent recommendation":
		return "direct_reply_with_agent"
	case "non-agent classification included an agent model recommendation":
		return "non_agent_with_model"
	case "classification selected an unavailable agent profile":
		return "agent_profile_unavailable"
	default:
		return "invalid_recommendation"
	}
}

func decodeErrorStage(raw string, err error) string {
	if raw == "" {
		return "decode_empty"
	}
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return "decode_syntax"
	}
	var mismatch *json.UnmarshalTypeError
	if errors.As(err, &mismatch) {
		return "decode_type"
	}
	if strings.Contains(err.Error(), "unknown field") {
		return "decode_unknown_field"
	}
	return "decode"
}

func isLoopbackURL(value *url.URL) bool {
	host := value.Hostname()
	if host == "localhost" {
		return value.Scheme == "http"
	}
	ip := net.ParseIP(host)
	return value.Scheme == "http" && ip != nil && ip.IsLoopback()
}

func validEmojiName(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' && character != '+' {
			return false
		}
	}
	return true
}

var _ Classifier = (*OpenAIClassifier)(nil)
