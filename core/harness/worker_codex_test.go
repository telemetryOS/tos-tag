package harness

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/activity"
	"github.com/telemetryos/tos-tag/core/workers"
)

type failingConnectedManager struct{ err error }

func (m failingConnectedManager) Provision(context.Context, workers.Spec) (workers.Workspace, error) {
	return workers.Workspace{}, m.err
}
func (m failingConnectedManager) ProvisionConnected(context.Context, workers.Spec) (workers.Connection, error) {
	return workers.Connection{}, m.err
}
func (m failingConnectedManager) ExportArtifacts(context.Context, workers.Workspace, []workers.ArtifactSpec) ([]workers.Artifact, error) {
	return nil, nil
}
func (m failingConnectedManager) Terminate(context.Context, workers.Workspace) error { return nil }

func TestWorkerCodexReportsProvisionStageWithoutExposingCause(t *testing.T) {
	cause := errors.New("sensitive process detail")
	worker, err := NewWorkerCodex(WorkerCodexOptions{Manager: failingConnectedManager{err: cause}, Command: "codex", CodexHome: "/safe/codex", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	_, err = worker.CreateSession(context.Background(), "test")
	if err == nil {
		t.Fatal("expected provision failure")
	}
	var diagnostic interface{ DiagnosticCode() string }
	if !errors.As(err, &diagnostic) || diagnostic.DiagnosticCode() != "worker.provision" {
		t.Fatalf("unexpected diagnostic: %v", err)
	}
	if err.Error() != "Codex worker failed at worker.provision" {
		t.Fatalf("error leaked its wrapped cause: %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("wrapped cause is not available to programmatic callers")
	}
}

type scriptedCodexManager struct {
	mu         sync.Mutex
	spec       workers.Spec
	thread     map[string]any
	turn       map[string]any
	terminated bool
}

func (m *scriptedCodexManager) Provision(context.Context, workers.Spec) (workers.Workspace, error) {
	return workers.Workspace{}, errors.New("unexpected disconnected provision")
}

func (m *scriptedCodexManager) ProvisionConnected(_ context.Context, spec workers.Spec) (workers.Connection, error) {
	m.mu.Lock()
	m.spec = spec
	m.mu.Unlock()
	serverInput, clientInput := io.Pipe()
	clientOutput, serverOutput := io.Pipe()
	workspace := workers.Workspace{ID: "worker-1", JobID: spec.JobID, AttemptID: spec.AttemptID, WorkDir: "/isolated/work", Root: "/isolated"}
	go m.serve(serverInput, serverOutput)
	return workers.Connection{Workspace: workspace, Stdin: clientInput, Stdout: clientOutput}, nil
}

func (m *scriptedCodexManager) serve(input io.Reader, output io.Writer) {
	scanner := bufio.NewScanner(input)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var message map[string]any
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			return
		}
		method, _ := message["method"].(string)
		id, hasID := message["id"]
		params, _ := message["params"].(map[string]any)
		switch method {
		case "initialize":
			_ = encoder.Encode(map[string]any{"id": id, "result": map[string]any{"userAgent": "test"}})
		case "thread/start":
			m.mu.Lock()
			m.thread = params
			m.mu.Unlock()
			_ = encoder.Encode(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "thread-1"}}})
		case "turn/start":
			m.mu.Lock()
			m.turn = params
			m.mu.Unlock()
			_ = encoder.Encode(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}}})
			_ = encoder.Encode(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 1, "item": map[string]any{"id": "web-1", "type": "webSearch", "query": "current documentation", "action": map[string]any{"type": "search", "query": "current documentation"}}}})
			_ = encoder.Encode(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 1, "item": map[string]any{"id": "item-1", "type": "agentMessage", "phase": "final_answer", "text": `{"segments":[{"kind":"mrkdwn_text","text":"hello"}]}`}}})
			_ = encoder.Encode(map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "tokenUsage": map[string]any{"last": map[string]any{"inputTokens": 21_000, "outputTokens": 1_200, "cachedInputTokens": 8_000, "reasoningOutputTokens": 600, "totalTokens": 22_200}, "total": map[string]any{"inputTokens": 21_000, "outputTokens": 1_200, "cachedInputTokens": 8_000, "reasoningOutputTokens": 600, "totalTokens": 22_200}}}})
			_ = encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": "turn-1", "status": "completed", "items": []any{}}}})
		case "turn/interrupt":
			_ = encoder.Encode(map[string]any{"id": id, "result": map[string]any{}})
		default:
			if hasID {
				_ = encoder.Encode(map[string]any{"id": id, "error": map[string]any{"code": -32601, "message": "unknown"}})
			}
		}
	}
}

func (m *scriptedCodexManager) ExportArtifacts(context.Context, workers.Workspace, []workers.ArtifactSpec) ([]workers.Artifact, error) {
	return nil, nil
}
func (m *scriptedCodexManager) Terminate(context.Context, workers.Workspace) error {
	m.mu.Lock()
	m.terminated = true
	m.mu.Unlock()
	return nil
}

func TestWorkerCodexUsesEphemeralReadOnlyAppServerTurnWithLiveWebSearch(t *testing.T) {
	manager := &scriptedCodexManager{}
	activityFeed := activity.New(100)
	worker, err := NewWorkerCodex(WorkerCodexOptions{Manager: manager, Command: "codex", CodexHome: "/safe/codex", Timeout: time.Minute, WebSearchMode: "live", Activity: activityFeed})
	if err != nil {
		t.Fatal(err)
	}
	session, err := worker.CreateJobSession(context.Background(), JobSessionSpec{Title: "test", OrganizationID: "org", JobID: "job", LeaseToken: "lease", SteeringEpoch: 1, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Prompt(context.Background(), session.ID, Prompt{Text: "hello", System: "system boundary", Model: "openai/gpt-5.6-luna", Variant: "max", RequestID: "request-1"}); err != nil {
		t.Fatal(err)
	}
	events, errs := worker.Events(context.Background(), session.ID)
	var text string
	var webSearchObserved bool
	var usageObserved bool
	for event := range events {
		if event.Type == "message.delta" {
			text += event.Data["text"].(string)
		} else if event.Type == "web.search.completed" {
			webSearchObserved = event.Data["query"] == "current documentation"
		} else if event.Type == "usage.updated" {
			usageObserved = event.Data["input_tokens"] == int64(21_000) && event.Data["output_tokens"] == int64(1_200) && event.Data["total_tokens"] == int64(22_200)
		}
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if text != `{"segments":[{"kind":"mrkdwn_text","text":"hello"}]}` {
		t.Fatalf("final text = %q", text)
	}
	if !webSearchObserved {
		t.Fatal("native web search activity was not surfaced by the harness")
	}
	if !usageObserved {
		t.Fatal("provider-reported turn usage was not surfaced by the harness")
	}
	activityRecords := activityFeed.Snapshot("org", 100)
	methods := make(map[string]bool)
	for _, record := range activityRecords {
		if method, ok := record.Details["method"].(string); ok {
			methods[method] = true
		}
		encoded, _ := json.Marshal(record)
		if strings.Contains(string(encoded), "system boundary") || strings.Contains(string(encoded), `\"segments\"`) {
			t.Fatalf("Codex activity leaked prompt or output: %s", encoded)
		}
	}
	for _, method := range []string{"initialize", "thread/start", "turn/start", "item/completed", "thread/tokenUsage/updated", "turn/completed"} {
		if !methods[method] {
			t.Fatalf("Codex activity did not expose %s lifecycle: %#v", method, activityRecords)
		}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if got := manager.spec.Environment["CODEX_HOME"]; got != "/safe/codex" {
		t.Fatalf("CODEX_HOME = %q", got)
	}
	if len(manager.spec.Command) != 3 || manager.spec.Command[1] != "app-server" || manager.spec.Command[2] != "--stdio" {
		t.Fatalf("command = %#v", manager.spec.Command)
	}
	if manager.thread["ephemeral"] != true || manager.thread["approvalPolicy"] != "never" || manager.thread["sandbox"] != "read-only" || manager.thread["developerInstructions"] != "system boundary" {
		t.Fatalf("thread params = %#v", manager.thread)
	}
	threadConfig, ok := manager.thread["config"].(map[string]any)
	if !ok || threadConfig["web_search"] != "live" {
		t.Fatalf("thread config = %#v", manager.thread["config"])
	}
	threadTools, ok := threadConfig["tools"].(map[string]any)
	if !ok || threadTools["web_search"] != true {
		t.Fatalf("thread tools = %#v", threadConfig["tools"])
	}
	if manager.turn["effort"] != "max" || manager.turn["approvalPolicy"] != "never" {
		t.Fatalf("turn params = %#v", manager.turn)
	}
	sandboxPolicy, ok := manager.turn["sandboxPolicy"].(map[string]any)
	if !ok || sandboxPolicy["type"] != "readOnly" || sandboxPolicy["networkAccess"] != false {
		t.Fatalf("turn sandbox policy = %#v", manager.turn["sandboxPolicy"])
	}
	if manager.turn["outputSchema"] == nil {
		t.Fatal("turn did not constrain the final Slack output schema")
	}
}

func TestWorkerCodexRejectsUnsupportedWebSearchMode(t *testing.T) {
	_, err := NewWorkerCodex(WorkerCodexOptions{Manager: &scriptedCodexManager{}, Command: "codex", CodexHome: "/safe/codex", Timeout: time.Minute, WebSearchMode: "browser-everything"})
	if err == nil || !strings.Contains(err.Error(), "web search mode") {
		t.Fatalf("web search mode validation error = %v", err)
	}
}

func TestCodexSlackOutputSchemaIncludesSafeAgentPresentationBlocks(t *testing.T) {
	encoded, err := json.Marshal(codexSlackOutputSchema())
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, required := range []string{`"const":"card"`, `"const":"carousel"`, `"caption"`, `"page_size"`, `"row_header_column_index"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("Slack output schema missing %s: %s", required, text)
		}
	}
	for _, forbidden := range []string{`"actions"`, `"button"`, `"action_id"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("model schema unexpectedly grants interactivity %s", forbidden)
		}
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		t.Fatal(err)
	}
	assertStrictObjectSchemas(t, normalized, "$")
}

func assertStrictObjectSchemas(t *testing.T, value any, path string) {
	t.Helper()
	switch schema := value.(type) {
	case map[string]any:
		if schema["type"] == "object" && schema["additionalProperties"] == false {
			properties, _ := schema["properties"].(map[string]any)
			requiredValues, _ := schema["required"].([]any)
			required := make(map[string]struct{}, len(requiredValues))
			for _, item := range requiredValues {
				required[fmt.Sprint(item)] = struct{}{}
			}
			for name := range properties {
				if _, ok := required[name]; !ok {
					t.Errorf("strict schema %s declares optional property %q without nullable required representation", path, name)
				}
			}
		}
		for name, child := range schema {
			assertStrictObjectSchemas(t, child, path+"."+name)
		}
	case []any:
		for index, child := range schema {
			assertStrictObjectSchemas(t, child, fmt.Sprintf("%s[%d]", path, index))
		}
	}
}

func TestWorkerCodexCallsCapabilityGatewayFromControlPlane(t *testing.T) {
	var authorization string
	var body map[string]any
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		authorization = request.Header.Get("Authorization")
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"output":"ok","exit_code":0}`)), Request: request}, nil
	})}
	worker := &WorkerCodex{httpClient: client}
	output, success := worker.callBridge(context.Background(), "attempt-capability", "http://127.0.0.1/execute", json.RawMessage(`{"tool_id":"telemetryos.wiki","operation_id":"search","arguments":["term"]}`))
	if !success || !strings.Contains(output, `"output":"ok"`) {
		t.Fatalf("output=%q success=%v", output, success)
	}
	if authorization != "Bearer attempt-capability" || body["tool_id"] != "telemetryos.wiki" {
		t.Fatalf("authorization=%q body=%#v", authorization, body)
	}
}

func TestCompletedToolOperationExposesOnlyToolIdentity(t *testing.T) {
	toolID, operationID, resourceAction := completedToolOperation("tos_tag_tool", json.RawMessage(`{"tool_id":"telemetryos.wiki","operation_id":"read","arguments":["get","primer/secret-page"]}`))
	if toolID != "telemetryos.wiki" || operationID != "read" || resourceAction != "get" {
		t.Fatalf("completed operation=%q/%q/%q", toolID, operationID, resourceAction)
	}
	if toolID, operationID, resourceAction := completedToolOperation("tos_tag_tool", json.RawMessage(`{"tool_id":"telemetryos.linear","operation_id":"read","arguments":["get","ENG-1234"]}`)); toolID != "telemetryos.linear" || operationID != "read" || resourceAction != "" {
		t.Fatalf("non-product argument leaked as resource action=%q/%q/%q", toolID, operationID, resourceAction)
	}
	if toolID, operationID, resourceAction := completedToolOperation("tos_tag_trigger", json.RawMessage(`{"operation":"list"}`)); toolID != "" || operationID != "" || resourceAction != "" {
		t.Fatalf("non-marketplace operation leaked as tool completion=%q/%q/%q", toolID, operationID, resourceAction)
	}
}

func TestWorkerCodexPublishesReviewedToolIdentityWithoutArguments(t *testing.T) {
	activityFeed := activity.New(10)
	session := &codexWorkerSession{activity: activityFeed, organizationID: "org", jobID: "job", attemptID: "attempt", threadID: "thread"}
	arguments := json.RawMessage(`{"tool_id":"telemetryos.code","operation_id":"read","arguments":["versions","tos-tag","go"]}`)
	session.publishToolResult("item/tool/call", "completed", "tos_tag_tool", arguments)
	records := activityFeed.Snapshot("org", 10)
	if len(records) != 1 || records[0].Kind != "codex.tool" || records[0].Details["tool_id"] != "telemetryos.code" || records[0].Details["operation_id"] != "read" || records[0].Details["resource_action"] != "versions" {
		t.Fatalf("tool activity = %#v", records)
	}
	encoded, _ := json.Marshal(records[0])
	if bytes.Contains(encoded, []byte("tos-tag")) {
		t.Fatalf("tool activity leaked arguments: %s", encoded)
	}
}

func TestWorkerCodexTreatsNonSuccessGatewayStatusAsToolFailure(t *testing.T) {
	worker := &WorkerCodex{httpClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnprocessableEntity, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"tool_failed"}`)), Request: request}, nil
	})}}
	output, success := worker.callBridge(context.Background(), "attempt-capability", "http://127.0.0.1/execute", json.RawMessage(`{"tool_id":"telemetryos.wiki","operation_id":"write","arguments":[]}`))
	if success || !strings.Contains(output, `"tool_failed"`) {
		t.Fatalf("output=%q success=%v", output, success)
	}
}

func TestProducedWikiArtifactURLRequiresReviewedSuccessfulWrite(t *testing.T) {
	arguments := json.RawMessage(`{"tool_id":"telemetryos.wiki","operation_id":"write","arguments":["put","artifacts/guide"]}`)
	bridgeOutput := `{"output":"{\"namespace\":\"artifacts\",\"slug\":\"guide\",\"revision\":1,\"url\":\"https://wiki.example/artifacts/guide\"}","exit_code":0}`
	if got := producedWikiArtifactURL("tos_tag_tool", arguments, bridgeOutput); got != "https://wiki.example/artifacts/guide" {
		t.Fatalf("produced URL = %q", got)
	}
	for name, testCase := range map[string]struct {
		tool      string
		arguments json.RawMessage
		output    string
	}{
		"wrong dynamic tool": {tool: "tos_tag_trigger", arguments: arguments, output: bridgeOutput},
		"read operation":     {tool: "tos_tag_tool", arguments: json.RawMessage(`{"tool_id":"telemetryos.wiki","operation_id":"read"}`), output: bridgeOutput},
		"failed execution":   {tool: "tos_tag_tool", arguments: arguments, output: `{"output":"{\"url\":\"https://wiki.example/fake\"}","exit_code":1}`},
		"non HTTPS URL":      {tool: "tos_tag_tool", arguments: arguments, output: `{"output":"{\"url\":\"http://wiki.example/fake\"}","exit_code":0}`},
	} {
		t.Run(name, func(t *testing.T) {
			if got := producedWikiArtifactURL(testCase.tool, testCase.arguments, testCase.output); got != "" {
				t.Fatalf("untrusted URL = %q", got)
			}
		})
	}
}

func TestResolvedWikiReferenceRequiresReviewedSuccessfulRead(t *testing.T) {
	getArguments := json.RawMessage(`{"tool_id":"telemetryos.wiki","operation_id":"read","arguments":["get","primer/node-mini","--json"]}`)
	fingerprint, resolvedURL := resolvedWikiReference("tos_tag_tool", getArguments, `{"output":"{\"id\":\"page-1\",\"url\":\"https://wiki.example/pages/page-1\"}","exit_code":0}`)
	if fingerprint != fmt.Sprintf("%x", sha256.Sum256([]byte("primer/node-mini"))) || resolvedURL != "https://wiki.example/pages/page-1" {
		t.Fatalf("resolved get reference = %q, %q", fingerprint, resolvedURL)
	}
	urlArguments := json.RawMessage(`{"tool_id":"telemetryos.wiki","operation_id":"read","arguments":["url","primer/node-mini"]}`)
	if _, got := resolvedWikiReference("tos_tag_tool", urlArguments, `{"output":"https://wiki.example/pages/page-1\n","exit_code":0}`); got != resolvedURL {
		t.Fatalf("resolved url reference = %q", got)
	}
	plainGetArguments := json.RawMessage(`{"tool_id":"telemetryos.wiki","operation_id":"read","arguments":["get","primer/node-mini"]}`)
	if _, got := resolvedWikiReference("tos_tag_tool", plainGetArguments, `{"output":"{\"id\":\"page-1\",\"url\":\"https://wiki.example/pages/page-1\"}","exit_code":0}`); got != resolvedURL {
		t.Fatalf("resolved plain get reference = %q", got)
	}
	opaqueGetArguments := json.RawMessage(`{"tool_id":"telemetryos.wiki","operation_id":"read","arguments":["get","https://wiki.example/pages/0123456789abcdef01234567"]}`)
	fingerprint, got := resolvedWikiReference("tos_tag_tool", opaqueGetArguments, `{"output":"{\"id\":\"0123456789abcdef01234567\",\"namespace\":\"primer\",\"slug\":\"node-mini\",\"url\":\"https://wiki.example/pages/0123456789abcdef01234567\"}","exit_code":0}`)
	if fingerprint != fmt.Sprintf("%x", sha256.Sum256([]byte("primer/node-mini"))) || got != "https://wiki.example/pages/0123456789abcdef01234567" {
		t.Fatalf("resolved opaque get reference = %q, %q", fingerprint, got)
	}
	for name, testCase := range map[string]struct {
		tool      string
		arguments json.RawMessage
		output    string
	}{
		"wrong tool":       {tool: "tos_tag_trigger", arguments: getArguments, output: `{"output":"https://wiki.example/pages/page-1","exit_code":0}`},
		"write operation":  {tool: "tos_tag_tool", arguments: json.RawMessage(`{"tool_id":"telemetryos.wiki","operation_id":"write","arguments":["put","primer/node-mini"]}`), output: `{"output":"{\"url\":\"https://wiki.example/pages/page-1\"}","exit_code":0}`},
		"failed execution": {tool: "tos_tag_tool", arguments: getArguments, output: `{"output":"{\"url\":\"https://wiki.example/pages/page-1\"}","exit_code":1}`},
		"non JSON get":     {tool: "tos_tag_tool", arguments: getArguments, output: `{"output":"<p>body</p>","exit_code":0}`},
		"non HTTPS URL":    {tool: "tos_tag_tool", arguments: getArguments, output: `{"output":"{\"url\":\"http://wiki.example/pages/page-1\"}","exit_code":0}`},
	} {
		t.Run(name, func(t *testing.T) {
			if fingerprint, pageURL := resolvedWikiReference(testCase.tool, testCase.arguments, testCase.output); fingerprint != "" || pageURL != "" {
				t.Fatalf("untrusted reference = %q, %q", fingerprint, pageURL)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
