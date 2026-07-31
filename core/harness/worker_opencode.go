package harness

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/core/marketplace"
	"github.com/telemetryos/tos-tag/core/tools"
	"github.com/telemetryos/tos-tag/core/workers"
	"github.com/telemetryos/tos-tag/types"
)

type WorkerOpenCodeOptions struct {
	Manager    workers.Manager
	Command    string
	Skills     []marketplace.SkillSnapshot
	Timeout    time.Duration
	ToolBridge *tools.Bridge
	ToolIDs    []string
}

type workerSession struct {
	client    *OpenCode
	workspace workers.Workspace
}

// WorkerOpenCode provisions one loopback-only, disposable OpenCode server for
// each harness session. It does not inherit host credentials or publish a port.
type WorkerOpenCode struct {
	manager    workers.Manager
	command    string
	skills     []marketplace.SkillSnapshot
	timeout    time.Duration
	toolBridge *tools.Bridge
	toolIDs    []string
	mu         sync.Mutex
	sessions   map[string]workerSession
}

func NewWorkerOpenCode(options WorkerOpenCodeOptions) (*WorkerOpenCode, error) {
	if options.Manager == nil || options.Command == "" || options.Timeout <= 0 {
		return nil, errors.New("worker OpenCode manager, command, and timeout are required")
	}
	return &WorkerOpenCode{manager: options.Manager, command: options.Command, skills: append([]marketplace.SkillSnapshot(nil), options.Skills...), timeout: options.Timeout, toolBridge: options.ToolBridge, toolIDs: append([]string(nil), options.ToolIDs...), sessions: make(map[string]workerSession)}, nil
}

func (w *WorkerOpenCode) Health(context.Context) error { return nil }

func (w *WorkerOpenCode) CreateSession(ctx context.Context, title string) (Session, error) {
	return w.createSession(ctx, JobSessionSpec{Title: title})
}

func (w *WorkerOpenCode) CreateJobSession(ctx context.Context, spec JobSessionSpec) (Session, error) {
	if spec.JobID == "" || spec.OrganizationID == "" || spec.LeaseToken == "" || spec.SteeringEpoch <= 0 || spec.ExpiresAt.IsZero() {
		return Session{}, errors.New("job-scoped OpenCode session is incomplete")
	}
	return w.createSession(ctx, spec)
}

func (w *WorkerOpenCode) createSession(ctx context.Context, spec JobSessionSpec) (Session, error) {
	port, err := reserveLoopbackPort()
	if err != nil {
		return Session{}, err
	}
	attemptID := types.NewID("attempt")
	environment := map[string]string{}
	customTools := map[string][]byte{}
	if w.toolBridge != nil && spec.JobID != "" {
		access, accessErr := w.toolBridge.Register(tools.JobScope{OrganizationID: spec.OrganizationID, WorkspaceID: spec.WorkspaceID, ChannelID: spec.ChannelID, JobID: spec.JobID, AttemptID: attemptID, LeaseToken: spec.LeaseToken, SteeringEpoch: spec.SteeringEpoch, ExpiresAt: spec.ExpiresAt, AllowedTools: w.toolIDs})
		if accessErr != nil {
			return Session{}, accessErr
		}
		environment["TOS_TAG_TOOL_ENDPOINT"] = access.Endpoint
		environment["TOS_TAG_CAPABILITY"] = access.Capability
		customTools["tos_tag_tool.ts"] = []byte(openCodeToolSource)
	}
	jobID := spec.JobID
	if jobID == "" {
		jobID = spec.Title
	}
	// The worker has a clean HOME/XDG root, so host plugins cannot load. Do not
	// pass --pure: that also suppresses the one project-local custom tool that
	// implements the capability bridge.
	workspace, err := w.manager.Provision(ctx, workers.Spec{OrganizationID: spec.OrganizationID, JobID: jobID, AttemptID: attemptID, Command: []string{w.command, "serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port)}, Environment: environment, Skills: w.skills, CustomTools: customTools, WallTime: w.timeout})
	if err != nil {
		if w.toolBridge != nil {
			_ = w.toolBridge.RevokeAttempt(context.Background(), attemptID)
		}
		return Session{}, err
	}
	client, err := NewOpenCode(OpenCodeOptions{Enabled: true, BaseURL: fmt.Sprintf("http://127.0.0.1:%d", port), Timeout: w.timeout})
	if err != nil {
		_ = w.manager.Terminate(context.Background(), workspace)
		return Session{}, err
	}
	readyCtx, cancel := context.WithTimeout(ctx, minDuration(w.timeout, 30*time.Second))
	defer cancel()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	for {
		connection, dialErr := (&net.Dialer{Timeout: time.Second}).DialContext(readyCtx, "tcp", address)
		if dialErr == nil {
			_ = connection.Close()
			healthCtx, healthCancel := context.WithTimeout(readyCtx, 2*time.Second)
			healthErr := client.Health(healthCtx)
			healthCancel()
			if healthErr == nil {
				break
			}
		}
		select {
		case <-readyCtx.Done():
			_ = w.manager.Terminate(context.Background(), workspace)
			return Session{}, fmt.Errorf("OpenCode worker readiness: %w", readyCtx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	session, err := client.CreateSession(ctx, spec.Title)
	if err != nil {
		_ = w.manager.Terminate(context.Background(), workspace)
		return Session{}, err
	}
	w.mu.Lock()
	w.sessions[session.ID] = workerSession{client: client, workspace: workspace}
	w.mu.Unlock()
	return session, nil
}

const openCodeToolSource = `import { tool } from "@opencode-ai/plugin"

export default tool({
  description: "Run one reviewed tos-tag marketplace operation through the job-scoped server gateway. Write, destructive, and admin operations require an independently approved approval_id.",
  args: {
    tool_id: tool.schema.string(),
    operation_id: tool.schema.string(),
    arguments: tool.schema.array(tool.schema.string()).default([]),
    secret_references: tool.schema.record(tool.schema.string(), tool.schema.string()).default({}),
    approval_id: tool.schema.string().optional()
  },
  async execute(args) {
    const response = await fetch(process.env.TOS_TAG_TOOL_ENDPOINT!, {
      method: "POST",
      headers: {"authorization": "Bearer " + process.env.TOS_TAG_CAPABILITY!, "content-type": "application/json"},
      body: JSON.stringify(args)
    })
    return JSON.stringify(await response.json())
  }
})
`

func (w *WorkerOpenCode) Prompt(ctx context.Context, sessionID string, prompt Prompt) error {
	session, err := w.lookup(sessionID)
	if err != nil {
		return err
	}
	return session.client.Prompt(ctx, sessionID, prompt)
}

func (w *WorkerOpenCode) Events(ctx context.Context, sessionID string) (<-chan Event, <-chan error) {
	out := make(chan Event)
	errs := make(chan error, 1)
	session, err := w.lookup(sessionID)
	if err != nil {
		close(out)
		errs <- err
		close(errs)
		return out, errs
	}
	upstream, upstreamErrs := session.client.Events(ctx, sessionID)
	go func() {
		defer close(out)
		defer close(errs)
		defer w.terminate(sessionID)
		for upstream != nil || upstreamErrs != nil {
			select {
			case event, ok := <-upstream:
				if !ok {
					upstream = nil
					continue
				}
				select {
				case out <- event:
				case <-ctx.Done():
					errs <- ctx.Err()
					return
				}
				if event.Type == "session.idle" {
					return
				}
			case eventErr, ok := <-upstreamErrs:
				if !ok {
					upstreamErrs = nil
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
	return out, errs
}

func (w *WorkerOpenCode) Permission(ctx context.Context, sessionID string, decision PermissionDecision) error {
	session, err := w.lookup(sessionID)
	if err != nil {
		return err
	}
	return session.client.Permission(ctx, sessionID, decision)
}

func (w *WorkerOpenCode) Abort(ctx context.Context, sessionID string) error {
	session, err := w.lookup(sessionID)
	if err != nil {
		return err
	}
	err = session.client.Abort(ctx, sessionID)
	w.terminate(sessionID)
	return err
}

func (w *WorkerOpenCode) Close(context.Context) error {
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

func (w *WorkerOpenCode) lookup(id string) (workerSession, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	session, ok := w.sessions[id]
	if !ok {
		return workerSession{}, ErrSessionNotFound
	}
	return session, nil
}

func (w *WorkerOpenCode) terminate(id string) {
	w.mu.Lock()
	session, ok := w.sessions[id]
	delete(w.sessions, id)
	w.mu.Unlock()
	if ok {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = w.manager.Terminate(ctx, session.workspace)
	}
}

func reserveLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

var _ Harness = (*WorkerOpenCode)(nil)
var _ JobScopedHarness = (*WorkerOpenCode)(nil)
