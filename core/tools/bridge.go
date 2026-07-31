package tools

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/core/approvals"
	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/jobs"
	"github.com/telemetryos/tos-tag/types"
)

type JobScope struct {
	OrganizationID string
	WorkspaceID    string
	ChannelID      string
	JobID          string
	AttemptID      string
	LeaseToken     string
	SteeringEpoch  int64
	ExpiresAt      time.Time
	AllowedTools   []string
}

type Access struct {
	Endpoint   string
	Capability string
}

type Bridge struct {
	gateway   Gateway
	jobs      jobs.Queue
	approvals approvals.Repository
	audit     audit.Appender

	mu       sync.RWMutex
	scopes   map[string]JobScope
	listener net.Listener
	server   *http.Server
}

func NewBridge(gateway Gateway, queue jobs.Queue, approvalStore approvals.Repository, auditAppender audit.Appender) (*Bridge, error) {
	if gateway.Registry == nil || gateway.Secrets == nil || !gateway.Executor.Enabled || queue == nil || approvalStore == nil || auditAppender == nil {
		return nil, errors.New("tool bridge requires an enabled gateway, jobs, approvals, and audit")
	}
	return &Bridge{gateway: gateway, jobs: queue, approvals: approvalStore, audit: auditAppender, scopes: make(map[string]JobScope)}, nil
}

func (b *Bridge) Start() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.listener != nil {
		return nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	b.listener = listener
	b.server = &http.Server{Handler: http.HandlerFunc(b.serve), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = b.server.Serve(listener) }()
	return nil
}

func (b *Bridge) Stop(ctx context.Context) error {
	b.mu.Lock()
	server := b.server
	b.server, b.listener = nil, nil
	b.scopes = make(map[string]JobScope)
	b.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}

func (b *Bridge) Register(scope JobScope) (Access, error) {
	if scope.OrganizationID == "" || scope.JobID == "" || scope.AttemptID == "" || scope.LeaseToken == "" || scope.SteeringEpoch <= 0 || !scope.ExpiresAt.After(time.Now().UTC()) {
		return Access{}, errors.New("invalid tool scope")
	}
	capability, err := randomCapability()
	if err != nil {
		return Access{}, err
	}
	b.mu.Lock()
	if b.listener == nil {
		b.mu.Unlock()
		return Access{}, errors.New("tool bridge is not started")
	}
	endpoint := "http://" + b.listener.Addr().String() + "/execute"
	b.scopes[capability] = scope
	b.mu.Unlock()
	return Access{Endpoint: endpoint, Capability: capability}, nil
}

func (b *Bridge) RevokeAttempt(_ context.Context, attemptID string) error {
	b.mu.Lock()
	for capability, scope := range b.scopes {
		if scope.AttemptID == attemptID {
			delete(b.scopes, capability)
		}
	}
	b.mu.Unlock()
	return nil
}

type bridgeRequest struct {
	ToolID           string            `json:"tool_id"`
	OperationID      string            `json:"operation_id"`
	Arguments        []string          `json:"arguments"`
	SecretReferences map[string]string `json:"secret_references"`
	ApprovalID       string            `json:"approval_id"`
}

func (b *Bridge) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		writeBridge(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	capability := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	b.mu.RLock()
	scope, ok := b.scopes[capability]
	b.mu.RUnlock()
	if !ok || capability == "" {
		writeBridge(w, http.StatusUnauthorized, map[string]any{"error": "capability_invalid"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input bridgeRequest
	if err := decoder.Decode(&input); err != nil || input.ToolID == "" || input.OperationID == "" {
		writeBridge(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	if err := b.authorize(r.Context(), scope); err != nil {
		_ = b.RevokeAttempt(r.Context(), scope.AttemptID)
		writeBridge(w, http.StatusForbidden, map[string]any{"error": "execution_revoked"})
		return
	}
	bundle, found := b.gateway.Registry.Resolve(input.ToolID)
	operation, operationFound := findOperation(bundle.Manifest, input.OperationID)
	if !found || !operationFound {
		writeBridge(w, http.StatusNotFound, map[string]any{"error": "operation_not_found"})
		return
	}
	if !containsTool(scope.AllowedTools, input.ToolID) {
		writeBridge(w, http.StatusForbidden, map[string]any{"error": "tool_not_admitted"})
		return
	}
	action := approvals.Action{OrganizationID: scope.OrganizationID, ToolID: bundle.Manifest.ID, ToolVersion: bundle.Manifest.Version, OperationID: operation.ID, Arguments: map[string]any{"argv": input.Arguments, "secret_references": input.SecretReferences}, Destination: scope.WorkspaceID + "/" + scope.ChannelID, Risk: operation.Risk}
	if operation.Risk != "read" {
		if input.ApprovalID == "" {
			approval, err := b.approvals.RequestContext(r.Context(), action, "agent:"+scope.JobID, 30*time.Minute)
			if err != nil {
				writeBridge(w, http.StatusInternalServerError, map[string]any{"error": "approval_unavailable"})
				return
			}
			actionBytes, _ := json.Marshal(action)
			if _, err := b.audit.Append(r.Context(), audit.AppendRequest{OrganizationID: scope.OrganizationID, Type: "tool.approval.requested", ActorID: "agent:" + scope.JobID, ResourceID: approval.ID, RetentionEpoch: time.Now().UTC().Format("2006-01"), IdempotencyKey: "tool-approval/" + approval.ID, Metadata: map[string]any{"tool_id": input.ToolID, "operation_id": input.OperationID, "risk": operation.Risk}, Content: actionBytes}); err != nil {
				writeBridge(w, http.StatusServiceUnavailable, map[string]any{"error": "audit_unavailable"})
				return
			}
			writeBridge(w, http.StatusConflict, map[string]any{"error": "approval_required", "approval_id": approval.ID, "expires_at": approval.ExpiresAt})
			return
		}
		if _, err := b.approvals.ConsumeContext(r.Context(), scope.OrganizationID, input.ApprovalID, action); err != nil {
			writeBridge(w, http.StatusForbidden, map[string]any{"error": "approval_invalid"})
			return
		}
	}
	actionBytes, _ := json.Marshal(action)
	executionID := types.NewID("toolrun")
	if _, err := b.audit.Append(r.Context(), audit.AppendRequest{OrganizationID: scope.OrganizationID, Type: "tool.execution.requested", ActorID: "agent:" + scope.JobID, ResourceID: executionID, RetentionEpoch: time.Now().UTC().Format("2006-01"), IdempotencyKey: "tool-execution/" + executionID + "/requested", Metadata: map[string]any{"tool_id": input.ToolID, "operation_id": input.OperationID, "risk": operation.Risk}, Content: actionBytes}); err != nil {
		writeBridge(w, http.StatusServiceUnavailable, map[string]any{"error": "audit_unavailable"})
		return
	}
	result, err := b.gateway.Execute(r.Context(), input.ToolID, GatewayRequest{Request: Request{OrganizationID: scope.OrganizationID, JobID: scope.JobID, OperationID: input.OperationID, Args: input.Arguments, Capability: Capability{ToolID: bundle.Manifest.ID, ToolVersion: bundle.Manifest.Version, OperationID: input.OperationID, AttemptToken: scope.AttemptID, SteeringEpoch: scope.SteeringEpoch, ExpiresAt: minTime(scope.ExpiresAt, time.Now().UTC().Add(time.Duration(operation.TimeoutSeconds)*time.Second))}}, SecretReferences: input.SecretReferences})
	if err != nil {
		writeBridge(w, http.StatusUnprocessableEntity, map[string]any{"error": "tool_failed", "detail": err.Error()})
		return
	}
	if _, err := b.audit.Append(r.Context(), audit.AppendRequest{OrganizationID: scope.OrganizationID, Type: "tool.execution.completed", ActorID: "agent:" + scope.JobID, ResourceID: executionID, RetentionEpoch: time.Now().UTC().Format("2006-01"), IdempotencyKey: "tool-execution/" + executionID + "/completed", Metadata: map[string]any{"tool_id": input.ToolID, "operation_id": input.OperationID, "exit_code": result.ExitCode}}); err != nil {
		writeBridge(w, http.StatusServiceUnavailable, map[string]any{"error": "audit_unavailable"})
		return
	}
	writeBridge(w, http.StatusOK, result)
}

func containsTool(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (b *Bridge) authorize(ctx context.Context, scope JobScope) error {
	if !scope.ExpiresAt.After(time.Now().UTC()) {
		return errors.New("scope expired")
	}
	job, err := b.jobs.Get(ctx, jobsID(scope.JobID))
	if err != nil || job.OrganizationID != scope.OrganizationID || job.State != jobs.StateRunning || job.SteeringEpoch != scope.SteeringEpoch || job.Lease.Token != scope.LeaseToken || !job.Lease.ExpiresAt.After(time.Now().UTC()) {
		return errors.New("job lease no longer authorizes tools")
	}
	return nil
}

func jobsID(value string) types.JobID { return types.JobID(value) }

func randomCapability() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func writeBridge(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func minTime(a, c time.Time) time.Time {
	if a.Before(c) {
		return a
	}
	return c
}

var _ interface {
	RevokeAttempt(context.Context, string) error
} = (*Bridge)(nil)
