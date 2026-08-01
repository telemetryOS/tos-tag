package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/core/marketplace"
	"github.com/telemetryos/tos-tag/core/tools"
	"github.com/telemetryos/tos-tag/core/workers"
	"github.com/telemetryos/tos-tag/types"
)

type WorkerCodexOptions struct {
	Manager    workers.ConnectedManager
	Command    string
	CodexHome  string
	Skills     []marketplace.SkillSnapshot
	Timeout    time.Duration
	ToolBridge *tools.Bridge
	ToolIDs    []string
}

type WorkerStageError struct {
	Code string
	Err  error
}

func (e *WorkerStageError) Error() string          { return "Codex worker failed at " + e.Code }
func (e *WorkerStageError) Unwrap() error          { return e.Err }
func (e *WorkerStageError) DiagnosticCode() string { return e.Code }

func workerStageError(code string, err error) error {
	if err == nil {
		return nil
	}
	return &WorkerStageError{Code: code, Err: err}
}

type codexWorkerSession struct {
	client    *codexAppServer
	workspace workers.Workspace
	access    tools.Access

	mu       sync.Mutex
	threadID string
	turnID   string
	events   chan Event
	errs     chan error
	closed   bool
}

func (s *codexWorkerSession) emit(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.events <- event
}

func (s *codexWorkerSession) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if err != nil {
		s.errs <- err
	}
	s.closed = true
	close(s.events)
	close(s.errs)
}

func (s *codexWorkerSession) complete() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.events <- Event{ID: types.NewID("event"), SessionID: s.threadID, Type: "session.idle", CreatedAt: time.Now().UTC()}
	s.closed = true
	close(s.events)
	close(s.errs)
}

type WorkerCodex struct {
	manager    workers.ConnectedManager
	command    string
	codexHome  string
	skills     []marketplace.SkillSnapshot
	timeout    time.Duration
	toolBridge *tools.Bridge
	toolIDs    []string
	httpClient *http.Client

	mu       sync.Mutex
	sessions map[string]*codexWorkerSession
}

func NewWorkerCodex(options WorkerCodexOptions) (*WorkerCodex, error) {
	if options.Manager == nil || strings.TrimSpace(options.Command) == "" || strings.TrimSpace(options.CodexHome) == "" || options.Timeout <= 0 {
		return nil, errors.New("Codex worker manager, command, home, and timeout are required")
	}
	return &WorkerCodex{manager: options.Manager, command: options.Command, codexHome: options.CodexHome, skills: append([]marketplace.SkillSnapshot(nil), options.Skills...), timeout: options.Timeout, toolBridge: options.ToolBridge, toolIDs: append([]string(nil), options.ToolIDs...), httpClient: &http.Client{Timeout: minDuration(options.Timeout, 5*time.Minute)}, sessions: make(map[string]*codexWorkerSession)}, nil
}

func (*WorkerCodex) Health(context.Context) error { return nil }

func (w *WorkerCodex) CreateSession(ctx context.Context, title string) (Session, error) {
	return w.createSession(ctx, JobSessionSpec{Title: title})
}

func (w *WorkerCodex) CreateJobSession(ctx context.Context, spec JobSessionSpec) (Session, error) {
	if spec.JobID == "" || spec.OrganizationID == "" || spec.LeaseToken == "" || spec.SteeringEpoch <= 0 || spec.ExpiresAt.IsZero() {
		return Session{}, errors.New("job-scoped Codex session is incomplete")
	}
	return w.createSession(ctx, spec)
}

func (w *WorkerCodex) createSession(ctx context.Context, spec JobSessionSpec) (Session, error) {
	attemptID := types.NewID("attempt")
	jobID := spec.JobID
	if jobID == "" {
		jobID = spec.Title
	}
	connection, err := w.manager.ProvisionConnected(ctx, workers.Spec{
		OrganizationID: spec.OrganizationID,
		JobID:          jobID,
		AttemptID:      attemptID,
		Command:        []string{w.command, "app-server", "--stdio"},
		Environment:    map[string]string{"CODEX_HOME": w.codexHome},
		Skills:         w.skills,
		WallTime:       w.timeout,
	})
	if err != nil {
		return Session{}, workerStageError("worker.provision", err)
	}
	sessionID := types.NewID("codex")
	session := &codexWorkerSession{workspace: connection.Workspace, events: make(chan Event, 128), errs: make(chan error, 4)}
	client, err := newCodexAppServer(connection.Stdin, connection.Stdout, session.notification, func(callCtx context.Context, method string, params json.RawMessage) (any, error) {
		return w.serverRequest(callCtx, session, method, params)
	}, session.fail)
	if err != nil {
		_ = w.manager.Terminate(context.Background(), connection.Workspace)
		return Session{}, workerStageError("worker.client", err)
	}
	session.client = client
	readyCtx, cancel := context.WithTimeout(ctx, minDuration(w.timeout, 30*time.Second))
	defer cancel()
	if err := client.initialize(readyCtx); err != nil {
		client.close()
		_ = w.manager.Terminate(context.Background(), connection.Workspace)
		return Session{}, workerStageError("worker.initialize", err)
	}
	if w.toolBridge != nil && spec.JobID != "" {
		access, accessErr := w.toolBridge.Register(tools.JobScope{OrganizationID: spec.OrganizationID, WorkspaceID: spec.WorkspaceID, ChannelID: spec.ChannelID, ThreadTS: spec.ThreadTS, JobID: spec.JobID, AttemptID: attemptID, LeaseToken: spec.LeaseToken, SteeringEpoch: spec.SteeringEpoch, ExpiresAt: spec.ExpiresAt, AllowedTools: w.toolIDs})
		if accessErr != nil {
			client.close()
			_ = w.manager.Terminate(context.Background(), connection.Workspace)
			return Session{}, workerStageError("worker.tool_register", accessErr)
		}
		session.access = access
	}
	w.mu.Lock()
	w.sessions[sessionID] = session
	w.mu.Unlock()
	return Session{ID: sessionID, Title: spec.Title, CreatedAt: time.Now().UTC()}, nil
}

func (w *WorkerCodex) Prompt(ctx context.Context, sessionID string, prompt Prompt) error {
	session, err := w.lookup(sessionID)
	if err != nil {
		return err
	}
	provider, model, found := strings.Cut(prompt.Model, "/")
	if !found {
		model = prompt.Model
	} else if provider != "openai" {
		return errors.New("Codex App Server requires an OpenAI model profile")
	}
	if strings.TrimSpace(model) == "" || strings.TrimSpace(prompt.RequestID) == "" {
		return errors.New("Codex prompt model and request ID are required")
	}
	dynamicTools := []map[string]any{}
	if session.access.Capability != "" {
		dynamicTools = codexDynamicTools()
	}
	var threadResponse struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	threadParams := map[string]any{
		"model":                 model,
		"cwd":                   session.workspace.WorkDir,
		"approvalPolicy":        "never",
		"sandbox":               "read-only",
		"developerInstructions": prompt.System,
		"dynamicTools":          dynamicTools,
		"ephemeral":             true,
		"serviceName":           "tos-tag",
		"config": map[string]any{
			"features":                 map[string]any{"shell_tool": false, "plugins": false},
			"agents":                   map[string]any{"enabled": false},
			"tools":                    map[string]any{"web_search": false},
			"mcp_servers":              map[string]any{},
			"shell_environment_policy": map[string]any{"inherit": "none"},
		},
	}
	if err := session.client.call(ctx, "thread/start", threadParams, &threadResponse); err != nil {
		w.terminate(sessionID)
		return workerStageError("worker.thread_start", err)
	}
	if threadResponse.Thread.ID == "" {
		w.terminate(sessionID)
		return workerStageError("worker.thread_start", &CodexProtocolError{Code: "empty_thread"})
	}
	session.mu.Lock()
	session.threadID = threadResponse.Thread.ID
	session.mu.Unlock()
	turnParams := map[string]any{
		"threadId":            threadResponse.Thread.ID,
		"input":               []map[string]any{{"type": "text", "text": prompt.Text}},
		"clientUserMessageId": prompt.RequestID,
		"model":               model,
		"effort":              prompt.Variant,
		"approvalPolicy":      "never",
		"sandboxPolicy":       map[string]any{"type": "readOnly", "networkAccess": false},
		"outputSchema":        codexSlackOutputSchema(),
	}
	if prompt.Variant == "" {
		delete(turnParams, "effort")
	}
	var turnResponse struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := session.client.call(ctx, "turn/start", turnParams, &turnResponse); err != nil {
		w.terminate(sessionID)
		return workerStageError("worker.turn_start", err)
	}
	if turnResponse.Turn.ID == "" {
		w.terminate(sessionID)
		return workerStageError("worker.turn_start", &CodexProtocolError{Code: "empty_turn"})
	}
	session.mu.Lock()
	session.turnID = turnResponse.Turn.ID
	session.mu.Unlock()
	return nil
}

func (w *WorkerCodex) Events(ctx context.Context, sessionID string) (<-chan Event, <-chan error) {
	session, err := w.lookup(sessionID)
	if err != nil {
		events := make(chan Event)
		errs := make(chan error, 1)
		close(events)
		errs <- err
		close(errs)
		return events, errs
	}
	events := make(chan Event)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		defer w.terminate(sessionID)
		sourceEvents, sourceErrs := session.events, session.errs
		for sourceEvents != nil || sourceErrs != nil {
			select {
			case event, ok := <-sourceEvents:
				if !ok {
					sourceEvents = nil
					continue
				}
				select {
				case events <- event:
				case <-ctx.Done():
					errs <- ctx.Err()
					return
				}
			case eventErr, ok := <-sourceErrs:
				if !ok {
					sourceErrs = nil
					continue
				}
				if eventErr != nil {
					errs <- eventErr
				}
				return
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}
		}
	}()
	return events, errs
}

func (w *WorkerCodex) Permission(_ context.Context, sessionID string, _ PermissionDecision) error {
	if _, err := w.lookup(sessionID); err != nil {
		return err
	}
	return errors.New("Codex built-in approvals are disabled; tos-tag approvals use the job capability gateway")
}

func (w *WorkerCodex) Abort(ctx context.Context, sessionID string) error {
	session, err := w.lookup(sessionID)
	if err != nil {
		return err
	}
	session.mu.Lock()
	threadID, turnID := session.threadID, session.turnID
	session.mu.Unlock()
	if threadID != "" && turnID != "" {
		var ignored map[string]any
		_ = session.client.call(ctx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, &ignored)
	}
	w.terminate(sessionID)
	return nil
}

func (w *WorkerCodex) Close(context.Context) error {
	w.mu.Lock()
	ids := make([]string, 0, len(w.sessions))
	for id := range w.sessions {
		ids = append(ids, id)
	}
	w.mu.Unlock()
	for _, id := range ids {
		w.terminate(id)
	}
	return nil
}

func (w *WorkerCodex) lookup(id string) (*codexWorkerSession, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	session, ok := w.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return session, nil
}

func (w *WorkerCodex) terminate(id string) {
	w.mu.Lock()
	session, ok := w.sessions[id]
	delete(w.sessions, id)
	w.mu.Unlock()
	if !ok {
		return
	}
	session.client.close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = w.manager.Terminate(ctx, session.workspace)
}

func (s *codexWorkerSession) notification(method string, raw json.RawMessage) {
	s.mu.Lock()
	threadID := s.threadID
	s.mu.Unlock()
	switch method {
	case "item/completed":
		var params struct {
			ThreadID string         `json:"threadId"`
			Item     map[string]any `json:"item"`
		}
		if json.Unmarshal(raw, &params) != nil || params.ThreadID != threadID {
			return
		}
		kind, _ := params.Item["type"].(string)
		phase, _ := params.Item["phase"].(string)
		text, _ := params.Item["text"].(string)
		if kind == "agentMessage" && text != "" && (phase == "" || phase == "final_answer") {
			s.emit(Event{ID: types.NewID("event"), SessionID: threadID, Type: "message.delta", Data: map[string]any{"text": text, "upstream_type": method}, CreatedAt: time.Now().UTC()})
		}
	case "turn/completed":
		var params struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  *struct {
					CodexErrorInfo any `json:"codexErrorInfo"`
				} `json:"error"`
			} `json:"turn"`
		}
		if json.Unmarshal(raw, &params) != nil || params.ThreadID != threadID {
			return
		}
		switch params.Turn.Status {
		case "completed":
			s.complete()
		case "interrupted":
			s.fail(context.Canceled)
		default:
			code := "turn_failed"
			if params.Turn.Error != nil {
				code += "_" + codexErrorCode(params.Turn.Error.CodexErrorInfo)
			}
			s.fail(&CodexProtocolError{Code: code})
		}
	case "error":
		var params struct {
			ThreadID  string `json:"threadId"`
			WillRetry bool   `json:"willRetry"`
			Error     struct {
				CodexErrorInfo any    `json:"codexErrorInfo"`
				Message        string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &params) == nil && params.ThreadID == threadID && !params.WillRetry {
			code := "turn_error_" + codexErrorCode(params.Error.CodexErrorInfo)
			if params.Error.Message != "" {
				code += "_" + codexErrorMessageCode(params.Error.Message)
			}
			s.fail(&CodexProtocolError{Code: code})
		}
	}
}

func (w *WorkerCodex) serverRequest(ctx context.Context, session *codexWorkerSession, method string, raw json.RawMessage) (any, error) {
	session.mu.Lock()
	threadID := session.threadID
	session.mu.Unlock()
	switch method {
	case "item/tool/call":
		var request struct {
			ThreadID  string          `json:"threadId"`
			Tool      string          `json:"tool"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(raw, &request); err != nil || request.ThreadID != threadID {
			return nil, errors.New("invalid dynamic tool request")
		}
		endpoint := ""
		switch request.Tool {
		case "tos_tag_tool":
			endpoint = session.access.Endpoint
		case "tos_tag_trigger":
			endpoint = session.access.TriggerEndpoint
		default:
			return nil, errors.New("dynamic tool is not admitted")
		}
		output, success := w.callBridge(ctx, session.access.Capability, endpoint, request.Arguments)
		if success {
			if artifactURL := producedWikiArtifactURL(request.Tool, request.Arguments, output); artifactURL != "" {
				session.emit(Event{ID: types.NewID("event"), SessionID: threadID, Type: "artifact.produced", Data: map[string]any{"url": artifactURL}, CreatedAt: time.Now().UTC()})
			}
		}
		return map[string]any{"success": success, "contentItems": []map[string]any{{"type": "inputText", "text": output}}}, nil
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return map[string]any{"decision": "decline"}, nil
	default:
		return nil, errors.New("unsupported Codex server request")
	}
}

func (w *WorkerCodex) callBridge(ctx context.Context, capability, endpoint string, arguments json.RawMessage) (string, bool) {
	if capability == "" || endpoint == "" || len(arguments) == 0 {
		return `{"error":"capability_unavailable"}`, false
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(arguments))
	if err != nil {
		return `{"error":"gateway_request_failed"}`, false
	}
	request.Header.Set("Authorization", "Bearer "+capability)
	request.Header.Set("Content-Type", "application/json")
	response, err := w.httpClient.Do(request)
	if err != nil {
		return `{"error":"gateway_unavailable"}`, false
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || !json.Valid(data) {
		return `{"error":"gateway_response_invalid"}`, false
	}
	return string(data), response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices
}

func producedWikiArtifactURL(tool string, arguments json.RawMessage, bridgeOutput string) string {
	if tool != "tos_tag_tool" {
		return ""
	}
	var request struct {
		ToolID      string `json:"tool_id"`
		OperationID string `json:"operation_id"`
	}
	if json.Unmarshal(arguments, &request) != nil || request.ToolID != "telemetryos.wiki" || request.OperationID != "write" {
		return ""
	}
	var result struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
	}
	if json.Unmarshal([]byte(bridgeOutput), &result) != nil || result.ExitCode != 0 {
		return ""
	}
	var page struct {
		URL string `json:"url"`
	}
	if json.Unmarshal([]byte(result.Output), &page) != nil {
		return ""
	}
	parsed, err := url.Parse(page.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return ""
	}
	return page.URL
}

func codexDynamicTools() []map[string]any {
	return []map[string]any{
		{
			"type": "function", "name": "tos_tag_tool",
			"description": "Run one reviewed tos-tag marketplace operation through the current job capability. Write, destructive, and admin operations require an independently approved approval_id.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"tool_id", "operation_id", "arguments"}, "properties": map[string]any{
				"tool_id": map[string]any{"type": "string"}, "operation_id": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "approval_id": map[string]any{"type": "string"},
			}},
		},
		{
			"type": "function", "name": "tos_tag_trigger",
			"description": "List, inspect, create, update, pause, or resume classifier-gated tos-tag heartbeat subscriptions in the current Slack channel. Mutations require an independently approved approval_id.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"operation"}, "properties": map[string]any{
				"operation": map[string]any{"type": "string", "enum": []string{"list", "get", "put", "disable"}}, "id": map[string]any{"type": "string"}, "instruction": map[string]any{"type": "string"}, "interval_seconds": map[string]any{"type": "integer"}, "next_run": map[string]any{"type": "string"}, "min_confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1}, "enabled": map[string]any{"type": "boolean"}, "root_thread_ts": map[string]any{"type": "string"}, "approval_id": map[string]any{"type": "string"},
			}},
		},
	}
}

func codexSlackOutputSchema() map[string]any {
	textSegment := func(kind string) map[string]any {
		return map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"kind", "text"},
			"properties": map[string]any{
				"kind": map[string]any{"type": "string", "const": kind},
				"text": map[string]any{"type": "string"},
			},
		}
	}
	cellSchema := map[string]any{
		"anyOf": []any{
			map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"type", "text"},
				"properties": map[string]any{"type": map[string]any{"type": "string", "const": "raw_text"}, "text": map[string]any{"type": "string"}},
			},
			map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"type", "text"},
				"properties": map[string]any{"type": map[string]any{"type": "string", "const": "rich_text"}, "text": map[string]any{"type": "string"}},
			},
			map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"type", "number"},
				"properties": map[string]any{"type": map[string]any{"type": "string", "const": "raw_number"}, "number": map[string]any{"type": "number"}},
			},
		},
	}
	tableSchema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"columns", "rows"},
		"properties": map[string]any{
			"columns": map[string]any{
				"type": "array", "minItems": 1, "maxItems": 20,
				"items": map[string]any{
					"type": "object", "additionalProperties": false, "required": []string{"header"},
					"properties": map[string]any{"header": map[string]any{"type": "string"}},
				},
			},
			"rows": map[string]any{
				"type": "array", "maxItems": 100,
				"items": map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "items": cellSchema},
			},
		},
	}
	segments := []any{
		textSegment("header"),
		textSegment("mrkdwn_text"),
		textSegment("context"),
		map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"kind"},
			"properties": map[string]any{"kind": map[string]any{"type": "string", "const": "divider"}},
		},
		map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"kind", "table"},
			"properties": map[string]any{"kind": map[string]any{"type": "string", "const": "table"}, "table": tableSchema},
		},
		map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"kind", "image"},
			"properties": map[string]any{
				"kind": map[string]any{"type": "string", "const": "image"},
				"image": map[string]any{
					"type": "object", "additionalProperties": false, "required": []string{"url", "alt_text", "title"},
					"properties": map[string]any{"url": map[string]any{"type": "string"}, "alt_text": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}},
				},
			},
		},
		map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"kind", "artifact"},
			"properties": map[string]any{
				"kind": map[string]any{"type": "string", "const": "artifact"},
				"artifact": map[string]any{
					"type": "object", "additionalProperties": false, "required": []string{"name", "media_type", "url"},
					"properties": map[string]any{"name": map[string]any{"type": "string"}, "media_type": map[string]any{"type": "string"}, "url": map[string]any{"type": "string"}},
				},
			},
		},
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"segments"},
		"properties": map[string]any{
			"segments": map[string]any{
				"type": "array", "minItems": 1, "maxItems": 50,
				"items": map[string]any{"anyOf": segments},
			},
		},
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

var _ Harness = (*WorkerCodex)(nil)
var _ JobScopedHarness = (*WorkerCodex)(nil)
