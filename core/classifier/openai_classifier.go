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
	"strings"
	"time"

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
	profiles := advertisedProfiles(c.agentProfiles.Snapshot().Profiles)
	if len(profiles) == 0 {
		return types.ClassificationDecision{}, classifierError("profiles", "no_agent_profiles", errors.New("no enabled agent model profiles"))
	}
	payload := classifierInput{
		Message:                         target.Envelope.Text,
		MessageAuthorID:                 target.Envelope.UserID,
		DestinationChannelID:            target.Envelope.ChannelID,
		Mode:                            target.Mode,
		DirectMention:                   target.Envelope.IsMention,
		ActiveThread:                    target.ActiveThread,
		Sources:                         pack.Sources,
		DestinationRecentParticipantIDs: destinationRecentParticipantIDs(target.Envelope.ChannelID, pack.Sources),
		AgentProfiles:                   profiles,
		ReactionEmojis:                  c.reactionEmojis,
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
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("structured output contained trailing JSON")
		}
		return types.ClassificationDecision{}, classifierError("decode_trailing", "invalid_structured_output", err)
	}
	decision = withDirectMentionPolicyCorrections(decision, target, profiles)
	decision = withAddressedSocialPolicyCorrections(decision, target)
	decision = withAmbientPolicyCorrections(decision, target, pack, profiles)
	decision = withCanonicalAgentProfile(decision, profiles)
	decision = withBackgroundProfileFloor(decision, target, profiles)
	decision = withDefaultReaction(decision, c.reactionEmojis)
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

type advertisedAgentProfile struct {
	ID              string `json:"id"`
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	Strength        string `json:"strength"`
	ReasoningEffort string `json:"reasoning_effort"`
}

func advertisedProfiles(profiles []types.ModelProfile) []advertisedAgentProfile {
	result := make([]advertisedAgentProfile, 0, len(profiles))
	for _, profile := range profiles {
		if !profile.Enabled {
			continue
		}
		strength := modelStrength(profile)
		result = append(result, advertisedAgentProfile{ID: profile.ID, Provider: profile.ProviderID, Model: profile.ModelID, Strength: strength, ReasoningEffort: profile.Variant})
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
	Message                         string                   `json:"message"`
	MessageAuthorID                 string                   `json:"message_author_id,omitempty"`
	DestinationChannelID            string                   `json:"destination_channel_id,omitempty"`
	Mode                            types.ParticipationMode  `json:"mode"`
	DirectMention                   bool                     `json:"direct_mention"`
	ActiveThread                    bool                     `json:"active_thread"`
	Sources                         []types.ContextSource    `json:"sources"`
	DestinationRecentParticipantIDs []string                 `json:"destination_recent_participant_ids,omitempty"`
	AgentProfiles                   []advertisedAgentProfile `json:"available_agent_profiles"`
	ReactionEmojis                  []string                 `json:"allowed_reaction_emojis"`
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

const classifierInstructions = `You are tos-tag's stateless, tool-free Slack classifier. Using only the immutable input, decide whether to remain silent, react only, answer in the current thread, answer in the channel, start background agent work, or request approval. Choose the least disruptive placement. A direct mention is a hard participation trigger but not automatically a thread: choose reply_in_channel when the expected answer is brief, self-contained, useful to the channel, and unlikely to invite follow-up; choose reply_in_thread when the answer is likely to become a deeper dive, needs multiple steps, code, a table, an artifact, research or tools, concerns a narrow side topic, or is likely to continue as a conversation. A requested table, structured report, code sample, artifact, research result, or multi-part comparison is not a brief answer and must use reply_in_thread even when it can be self-contained. Honor an explicit request to reply in the channel or in a thread. When active_thread is true, keep any substantive response in that thread. For ambient messages, default to silent on ambiguity, repetition, or an already-answered question.

Use destination-safe sources when they materially resolve the current message, and list every source used in releasable_evidence_ids. A clear operational-status question with current destination-safe incident evidence requires an answer rather than react-only; prefer a thread when the status is likely to need supporting detail or follow-up. Never answer from restricted-awareness-only material. Participation mode controls initiative: assist may answer a clear unresolved ambient question when useful; proactive may react to or answer a clear actionable failure, risk, or incident signal; mention and observe restrictions are enforced independently by the admission service. Prefer react-only for a top-level ambient, non-urgent metric or risk observation that asks for no work and needs no explanation. In particular, an elevated metric explicitly described as stable or steady with no errors or failures should normally receive only warning, not a reply or worker job, unless it is marked urgent, critical, paging, failing, or needing attention.

An ambient alignment intervention is appropriate when a current human statement materially conflicts with a recent, destination-safe factual report from a different human in another public channel, or with a clear destination-safe fact, and surfacing that conflict would prevent confusion, duplicated work, a bad operational decision, or a missed incident. Default to silent for opinions, preferences, predictions, minor wording differences, stale reports, ambiguous entities, weak inferences, or facts that do not change what this channel should know or do. A source author absent from destination_recent_participant_ids is a stronger reason to help, but that list means recent visible participation only: never claim the person is not a channel member. Attribute human reports without blame and without converting them into verified truth—for example, response_intent may say to note that <@AUTHOR_ID> reported the server down in <#CHANNEL_ID> and ask the worker to reconcile or verify the current state. Use only author_id, channel_id, channel_name, observed_at, and text supplied by a cited source; never invent a person, channel, time, or fact. A single clear conflict needing only a brief attributed status note must use reply_in_channel even though someone might follow up; use a thread or background work only when the response itself must reconcile evidence, investigate, or explain multiple facts. Use speech_balloon for ordinary alignment, warning or rotating_light for material operational risk. Never perform an alignment intervention from agent_output_unverified or any restricted-awareness source, and never reveal even the existence of another private channel, DM, or group DM. If a direct mention asks to quote, paste, show, share, or summarize private-channel, DM, or group-DM content outside its destination, use a light/low full agent for one brief reply_in_channel refusal. Cite no evidence and reveal no awareness, names, channels, or content.

For an unmistakably social message that needs no factual answer, tools, action, private context, or follow-up—such as thanks, a greeting, a farewell, light praise, or brief friendly banter—you may answer directly without a full agent. Set reply_in_channel for a top-level message or reply_in_thread for an active thread, set requires_full_agent false, leave all agent recommendation and evidence fields empty, and place one natural reply of at most 240 characters in direct_reply. Keep it to one plain-text line. It must not contain facts, advice, commitments, status claims, links, mentions, identifiers, markdown, or claims derived from context. Use white_check_mark for thanks or praise and speech_balloon for a greeting or banter. Examples include "You're welcome!", "Happy to help!", and "Morning!". Otherwise leave direct_reply empty. Never use direct_reply to answer a question, summarize content, perform a transformation, report work, or avoid full-agent admission.

For every non-silent action select exactly one allowed Slack reaction. Apply these meanings consistently: eyes acknowledges newly requested work; thinking_face marks a question or decision that needs consideration; white_check_mark marks a completed, confirmed, or immediately resolved acknowledgement; warning marks a non-urgent risk or degraded condition; rotating_light marks an active urgent incident; hammer_and_wrench marks implementation, repair, or build work; speech_balloon marks a conversational explanation or discussion. Select only from the allowed list and choose the closest semantic match.

Every reply, background action, or approval request other than a validated direct_reply is executed by the full agent, so those outcomes must set requires_full_agent true and select exactly one available agent profile plus its advertised strength and reasoning effort. Route brief factual answers and simple transformations to an available light/low profile. Route comparisons, bounded plans, requested tables, structured formatting, and moderate analysis to an available standard/medium profile unless the underlying work itself is unusually difficult or consequential. Route deep multi-step analysis, incident or security work, extensive multi-tool research, high-consequence decisions, and genuinely document-sized synthesis or long-form expository work that will probably need an Agent Wiki artifact to an available strong/max profile. A start_background_job action must use at least standard/medium; use strong/max when it investigates an active production incident or security concern. In proactive mode, an explicit current failure, outage, or needs-attention statement is a high-confidence actionable signal; do not stay silent merely because it is not phrased as a question. Mechanical reformatting and formatting alone never justify max. Choose the least costly profile that safely fits; do not use max merely because it is the deployment default. Reason codes are compact audit labels and must agree with the immutable booleans: never claim a direct mention when direct_mention is false, never claim an active thread when active_thread is false, and never claim either is absent when its value is true. React-only and direct-reply decisions must set requires_full_agent false and leave the agent recommendation fields empty or none as required by the schema. Never invent profile IDs, evidence IDs, emoji names, or channel facts. Restricted-awareness sources may appear only in restricted_signal_ids and may never ground final prose. Sources with provenance agent_output_unverified are prior generated prose for continuity, not factual evidence, and must not justify confidence or releasable evidence. Do not use tools and do not reveal chain-of-thought.`

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
		"disclosure_class":        map[string]any{"type": "string", "enum": []string{"destination_safe", "restricted_awareness_only"}},
		"requires_full_agent":     map[string]any{"type": "boolean"},
		"reaction":                map[string]any{"type": "string", "enum": reactionValues},
		"agent_model_profile":     map[string]any{"type": "string", "enum": profileIDs},
		"agent_model_strength":    map[string]any{"type": "string", "enum": strengths},
		"agent_reasoning_effort":  map[string]any{"type": "string", "enum": efforts},
	}
	required := []string{"outcome", "confidence", "reason_codes", "topic_ids", "releasable_evidence_ids", "restricted_signal_ids", "response_intent", "direct_reply", "disclosure_class", "requires_full_agent", "reaction", "agent_model_profile", "agent_model_strength", "agent_reasoning_effort"}
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
	if decision.DirectReply != "" {
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
	if target.ActiveThread || target.Envelope.IsMention || !explicitlyAddressesTag(target.Envelope.Text) || !isDirectSocialCandidate(target.Envelope.Text) {
		return decision
	}
	if decision.Outcome != types.OutcomeSilent && decision.Outcome != types.OutcomeReact {
		return decision
	}
	reply := "Hey!"
	lower := strings.ToLower(target.Envelope.Text)
	switch {
	case strings.Contains(lower, "morning"):
		reply = "Morning!"
	case strings.Contains(lower, "afternoon"):
		reply = "Good afternoon!"
	case strings.Contains(lower, "evening"):
		reply = "Good evening!"
	}
	return types.ClassificationDecision{
		Outcome:            types.OutcomeReplyInChannel,
		Confidence:         max(decision.Confidence, 0.99),
		ReasonCodes:        append(decision.ReasonCodes, "policy.addressed_social_reply"),
		ResponseIntent:     "brief social acknowledgement",
		DirectReply:        reply,
		DisclosureClass:    types.DisclosureDestinationSafe,
		Reaction:           "speech_balloon",
		AgentModelStrength: "none",
	}
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
	if decision.Outcome == types.OutcomeSilent || decision.DirectReply != "" || target.Envelope.IsMention || target.ActiveThread {
		return decision
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
	if source, ok := alignmentConflict(target, pack); ok {
		switch decision.Outcome {
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
	if decision.Outcome == types.OutcomeReact && asksOperationalStatus(target.Envelope.Text) {
		for _, source := range pack.Sources {
			if source.DisclosureClass != types.DisclosureDestinationSafe || (source.Partition != types.PartitionEvidence && source.Partition != types.PartitionSituation) || !containsIncident(source.Text) {
				continue
			}
			decision.Outcome = types.OutcomeReplyInThread
			decision.RequiresFullAgent = true
			decision.ReleasableEvidenceIDs = appendUnique(decision.ReleasableEvidenceIDs, source.ID)
			decision = withLightestProfile(decision, profiles)
			decision.ReasonCodes = append(decision.ReasonCodes, "policy.operational_question_requires_answer")
			break
		}
	}
	return decision
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
