package classifier

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/modelrouter"
	"github.com/telemetryos/tos-tag/types"
)

func TestOpenAIClassifierUsesDirectStructuredResponsesAPI(t *testing.T) {
	var received map[string]any
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/responses" || request.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected request path or authorization")
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		decision := `{"outcome":"reply_in_thread","confidence":0.99,"reason_codes":["direct_question"],"topic_ids":["outage"],"releasable_evidence_ids":["alerts/1"],"restricted_signal_ids":[],"response_intent":"answer status","disclosure_class":"destination_safe","requires_full_agent":true,"reaction":"rotating_light","agent_model_profile":"chatgpt-luna-max","agent_model_strength":"strong","agent_reasoning_effort":"max"}`
		body := fmt.Sprintf(`{"id":"resp_test","status":"completed","usage":{"input_tokens":1234,"output_tokens":321},"output":[{"type":"message","content":[{"type":"output_text","text":%q}]}]}`, decision)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}

	router := testProfileRegistry(t)
	client, err := NewOpenAIClassifier(OpenAIOptions{
		BaseURL: "http://127.0.0.1/v1", APIKey: "test-key", Model: "gpt-5.6-luna",
		ReasoningEffort: "max", Timeout: time.Second, MaxOutputTokens: 2048,
		ReactionEmojis: []string{"eyes", "rotating_light"}, AgentProfiles: router, HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	pack := types.ContextPackRevision{TotalTokens: 1000, Sources: []types.ContextSource{{ID: "alerts/1", ChannelID: "alerts", Partition: types.PartitionEvidence, Text: "Active production outage", DisclosureClass: types.DisclosureDestinationSafe}}}
	decision, err := client.Decide(context.Background(), Target{ObservationID: "obs-1", Envelope: types.SlackEnvelope{Text: "Is it down?"}, Mode: types.ModeAssist}, pack)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != types.OutcomeReplyInThread || decision.Reaction != "rotating_light" || decision.AgentModelProfile != "chatgpt-luna-max" || decision.AgentModelStrength != "strong" || decision.AgentReasoningEffort != "max" || decision.ClassifierModel != "gpt-5.6-luna" || decision.ClassifierResponseID != "resp_test" || decision.ClassifierInputTokens != 1234 || decision.ClassifierOutputTokens != 321 {
		t.Fatalf("unexpected decision: %#v", decision)
	}
	if received["model"] != "gpt-5.6-luna" || received["store"] != false {
		t.Fatalf("unexpected Responses API request: %#v", received)
	}
	reasoning := received["reasoning"].(map[string]any)
	if reasoning["effort"] != "max" {
		t.Fatalf("reasoning effort = %#v", reasoning["effort"])
	}
	if _, hasTools := received["tools"]; hasTools {
		t.Fatal("classifier request unexpectedly enabled tools")
	}
}

func TestOpenAIClassifierRejectsUnallowlistedRecommendationWithoutLeakingContent(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		decision := `{"outcome":"reply_in_channel","confidence":0.99,"reason_codes":["test"],"topic_ids":[],"releasable_evidence_ids":[],"restricted_signal_ids":[],"response_intent":"test","disclosure_class":"destination_safe","requires_full_agent":true,"reaction":"party_parrot","agent_model_profile":"invented","agent_model_strength":"strong","agent_reasoning_effort":"max"}`
		body := fmt.Sprintf(`{"id":"resp_test","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":%q}]}]}`, decision)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	client, err := NewOpenAIClassifier(OpenAIOptions{BaseURL: "http://127.0.0.1", APIKey: "secret-key", Model: "test", ReasoningEffort: "max", Timeout: time.Second, MaxOutputTokens: 100, ReactionEmojis: []string{"eyes"}, AgentProfiles: testProfileRegistry(t), HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Decide(context.Background(), Target{ObservationID: "obs", Envelope: types.SlackEnvelope{Text: "private-content"}}, types.ContextPackRevision{})
	if err == nil {
		t.Fatal("invalid recommendation was accepted")
	}
	if message := err.Error(); strings.Contains(message, "secret-key") || strings.Contains(message, "private-content") || strings.Contains(message, "party_parrot") {
		t.Fatalf("classifier error leaked sensitive data: %s", message)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func testProfileRegistry(t *testing.T) *modelrouter.Registry {
	t.Helper()
	router, err := modelrouter.NewRegistry([]types.ModelProfile{{ID: "chatgpt-luna-max", ProviderID: "openai", ModelID: "gpt-5.6-luna", Variant: "max", ProviderOptions: map[string]any{"strength": "strong"}, RequiredCapabilities: []string{"structured"}, AllowedDataClasses: []string{"internal"}, MaxInputTokens: 200000, MaxOutputTokens: 16000, Enabled: true}}, nil, nil, "chatgpt-luna-max", "test/v1")
	if err != nil {
		t.Fatal(err)
	}
	return router
}
