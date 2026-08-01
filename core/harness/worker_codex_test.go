package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

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
			_ = encoder.Encode(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 1, "item": map[string]any{"id": "item-1", "type": "agentMessage", "phase": "final_answer", "text": `{"segments":[{"kind":"mrkdwn_text","text":"hello"}]}`}}})
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

func TestWorkerCodexUsesEphemeralRestrictedAppServerTurn(t *testing.T) {
	manager := &scriptedCodexManager{}
	worker, err := NewWorkerCodex(WorkerCodexOptions{Manager: manager, Command: "codex", CodexHome: "/safe/codex", Timeout: time.Minute})
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
	for event := range events {
		if event.Type == "message.delta" {
			text += event.Data["text"].(string)
		}
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if text != `{"segments":[{"kind":"mrkdwn_text","text":"hello"}]}` {
		t.Fatalf("final text = %q", text)
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
	if manager.turn["effort"] != "max" || manager.turn["approvalPolicy"] != "never" {
		t.Fatalf("turn params = %#v", manager.turn)
	}
	if manager.turn["outputSchema"] == nil {
		t.Fatal("turn did not constrain the final Slack output schema")
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
