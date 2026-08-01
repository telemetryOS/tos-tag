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
// classifier may recommend for admitted OpenCode work.
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
// never creates an OpenCode session and never exposes the API key to a worker.
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
		Message:        target.Envelope.Text,
		Mode:           target.Mode,
		ActiveThread:   target.ActiveThread,
		Sources:        pack.Sources,
		AgentProfiles:  profiles,
		ReactionEmojis: c.reactionEmojis,
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
	Message        string                   `json:"message"`
	Mode           types.ParticipationMode  `json:"mode"`
	ActiveThread   bool                     `json:"active_thread"`
	Sources        []types.ContextSource    `json:"sources"`
	AgentProfiles  []advertisedAgentProfile `json:"available_agent_profiles"`
	ReactionEmojis []string                 `json:"allowed_reaction_emojis"`
}

const classifierInstructions = `You are tos-tag's stateless, tool-free Slack classifier. Using only the immutable input, decide whether to remain silent, react only, answer in the current thread, answer in the channel, start background agent work, or request approval. Choose the least disruptive placement. Default to silent on ambiguity, social chatter, repetition, or an already-answered question. For every non-silent action select exactly one allowed Slack reaction; use eyes when acknowledging work, thinking_face when considering a question, warning or rotating_light for risk/incident signals, white_check_mark for a completed acknowledgement, and another allowed emoji only when it clearly fits. A reply or background action requiring research, tools, or substantial reasoning must set requires_full_agent true and select exactly one available agent profile plus its advertised strength and reasoning effort. Never invent profile IDs, evidence IDs, emoji names, or channel facts. Restricted-awareness sources may appear only in restricted_signal_ids and may never ground final prose. Sources with provenance agent_output_unverified are prior generated prose for continuity, not factual evidence, and must not justify confidence or releasable evidence. Do not use tools and do not reveal chain-of-thought.`

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
		"disclosure_class":        map[string]any{"type": "string", "enum": []string{"destination_safe", "restricted_awareness_only"}},
		"requires_full_agent":     map[string]any{"type": "boolean"},
		"reaction":                map[string]any{"type": "string", "enum": reactionValues},
		"agent_model_profile":     map[string]any{"type": "string", "enum": profileIDs},
		"agent_model_strength":    map[string]any{"type": "string", "enum": strengths},
		"agent_reasoning_effort":  map[string]any{"type": "string", "enum": efforts},
	}
	required := []string{"outcome", "confidence", "reason_codes", "topic_ids", "releasable_evidence_ids", "restricted_signal_ids", "response_intent", "disclosure_class", "requires_full_agent", "reaction", "agent_model_profile", "agent_model_strength", "agent_reasoning_effort"}
	return map[string]any{
		"type":        "json_schema",
		"name":        "tos_tag_classification",
		"description": "A safe Slack participation and OpenCode admission decision.",
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
		if decision.Reaction != "" || decision.RequiresFullAgent || decision.AgentModelProfile != "" || decision.AgentModelStrength != "none" || decision.AgentReasoningEffort != "" {
			return errors.New("silent classification included an action recommendation")
		}
		return nil
	}
	if !slices.Contains(reactions, decision.Reaction) {
		return errors.New("classification reaction is not allowlisted")
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

func recommendationErrorCode(err error) string {
	switch err.Error() {
	case "silent classification included an action recommendation":
		return "silent_with_action"
	case "classification reaction is not allowlisted":
		return "reaction_not_allowlisted"
	case "classification outcome requires full agent work":
		return "agent_required"
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
