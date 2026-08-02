package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestOpenAISummarizerUsesLunaMediumStatelessStructuredCall(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("unexpected request %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "gpt-5.6-luna" || body["store"] != false || body["reasoning"].(map[string]any)["effort"] != "medium" {
			t.Fatalf("unexpected model request: %#v", body)
		}
		response := []byte(`{"id":"resp-memory","status":"completed","usage":{"input_tokens":120,"output_tokens":40},"output":[{"content":[{"type":"output_text","text":"{\"summary\":\"The team selected the staged rollout.\",\"confidence\":0.91,\"facts\":[{\"text\":\"The rollout is staged.\",\"confidence\":0.93,\"source_ids\":[\"C/1\"],\"valid_for_hours\":72}]}"}]}]}`)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(response)), Request: r}, nil
	})}
	summarizer, err := NewOpenAISummarizer(OpenAIOptions{BaseURL: "https://api.example.test", APIKey: "test-key", Model: "gpt-5.6-luna", ReasoningEffort: "medium", Timeout: time.Second, MaxOutputTokens: 1000, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	result, err := summarizer.Summarize(context.Background(), Batch{ChannelID: "C", Scope: ScopeChannel, Messages: []SourceMessage{{ID: "C/1", Text: "We selected a staged rollout."}}})
	if err != nil || result.Summary == "" || len(result.Facts) != 1 || result.InputTokens != 120 || result.OutputTokens != 40 {
		t.Fatalf("summary result = %#v, %v", result, err)
	}
}
