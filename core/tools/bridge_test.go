package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/admission"
	"github.com/telemetryos/tos-tag/core/approvals"
	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/deliveries"
	"github.com/telemetryos/tos-tag/core/jobs"
	"github.com/telemetryos/tos-tag/core/keystore"
	"github.com/telemetryos/tos-tag/core/triggers"
)

func TestBridgeFencesReadToolAndRequiresIndependentApprovalForWrite(t *testing.T) {
	ctx := context.Background()
	queue := jobs.NewMemoryQueue(time.Now)
	_, _, err := queue.Enqueue(ctx, jobs.Spec{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", RootThreadTS: "100.0", SessionID: "session", Generation: 1, IdempotencyKey: "job", Kind: "response", MaxAttempts: 1, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := queue.Claim(ctx, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	running, err := queue.Transition(ctx, leased.ID, leased.Lease.Token, jobs.StateRunning, nil)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	script := filepath.Join(root, "tool.sh")
	if err := os.WriteFile(script, []byte("printf 'ok:%s' \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	bundle := Bundle{Root: root, Manifest: Manifest{ID: "demo", Version: "v1", Script: "tool.sh", Operations: []Operation{{ID: "read", Risk: "read", TimeoutSeconds: 2, MaxOutputBytes: 1024}, {ID: "write", Risk: "write", TimeoutSeconds: 2, MaxOutputBytes: 1024}}}}
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	bundle.ScriptHash = digest(data)
	registry := &Registry{bundles: map[string]Bundle{"demo": bundle}}
	secrets, err := keystore.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	approvalStore := approvals.NewStore()
	auditLog, err := audit.NewMemoryAppender([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewBridge(Gateway{Registry: registry, Secrets: secrets, Executor: Executor{Enabled: true}}, queue, approvalStore, auditLog)
	if err != nil {
		t.Fatal(err)
	}
	capability := "test-capability"
	bridge.scopes[capability] = JobScope{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", JobID: string(running.ID), AttemptID: "attempt", LeaseToken: running.Lease.Token, SteeringEpoch: running.SteeringEpoch, ExpiresAt: time.Now().Add(time.Minute), AllowedTools: []string{"demo"}}

	status, body := bridgeCall(t, bridge, capability, bridgeRequest{ToolID: "demo", OperationID: "read", Arguments: []string{"one"}})
	if status != http.StatusOK || !bytes.Contains(body, []byte("ok:one")) {
		t.Fatalf("status=%d body=%s", status, body)
	}

	status, body = bridgeCall(t, bridge, capability, bridgeRequest{ToolID: "demo", OperationID: "write", Arguments: []string{"two"}})
	if status != http.StatusConflict {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var requested struct {
		ApprovalID string `json:"approval_id"`
	}
	if err := json.Unmarshal(body, &requested); err != nil || requested.ApprovalID == "" {
		t.Fatalf("approval response=%s err=%v", body, err)
	}
	if _, err := approvalStore.ApproveContext(ctx, "org", requested.ApprovalID, "human"); err != nil {
		t.Fatal(err)
	}
	status, body = bridgeCall(t, bridge, capability, bridgeRequest{ToolID: "demo", OperationID: "write", Arguments: []string{"two"}, ApprovalID: requested.ApprovalID})
	if status != http.StatusOK || !bytes.Contains(body, []byte("ok:two")) {
		t.Fatalf("status=%d body=%s", status, body)
	}

	if err := bridge.RevokeAttempt(ctx, "attempt"); err != nil {
		t.Fatal(err)
	}
	status, _ = bridgeCall(t, bridge, capability, bridgeRequest{ToolID: "demo", OperationID: "read"})
	if status != http.StatusUnauthorized {
		t.Fatalf("revoked capability status=%d", status)
	}
}

func TestTriggerBridgeIsChannelScopedAndResumesExactApprovedMutation(t *testing.T) {
	ctx := context.Background()
	queue := jobs.NewMemoryQueue(nil)
	deliveryQueue := deliveries.NewMemoryQueue(nil)
	approvalStore := approvals.NewStore()
	auditLog, err := audit.NewMemoryAppender([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := approvals.NewCoordinator(approvalStore, queue, deliveryQueue, auditLog, approvals.ApproverAuthorizerFunc(func(context.Context, string, string, string, string) error { return nil }), admission.NewMemory(nil))
	if err != nil {
		t.Fatal(err)
	}
	triggerStore := triggers.NewStore(nil)
	job, _, err := queue.Enqueue(ctx, jobs.Spec{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", RootThreadTS: "100.1", SessionID: "session", Generation: 1, IdempotencyKey: "job-trigger", Kind: "agent", MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	job, err = queue.Claim(ctx, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	job, err = queue.Transition(ctx, job.ID, job.Lease.Token, jobs.StateRunning, nil)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{jobs: queue, approvals: approvalStore, audit: auditLog, approvalCoordinator: coordinator, triggers: triggerStore, scopes: make(map[string]JobScope)}
	bridge.scopes["first"] = JobScope{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", ThreadTS: "100.1", JobID: string(job.ID), AttemptID: "attempt-1", LeaseToken: job.Lease.Token, SteeringEpoch: job.SteeringEpoch, ExpiresAt: time.Now().Add(time.Minute)}
	enabled := true
	input := triggerSubscriptionRequest{Operation: "put", ID: "incident-watch", Instruction: "Check for an unresolved incident.", IntervalSeconds: 300, MinConfidence: .8, Enabled: &enabled}
	status, body := triggerBridgeCall(t, bridge, "first", input)
	if status != http.StatusConflict {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var requested struct {
		ApprovalID string `json:"approval_id"`
	}
	if err := json.Unmarshal(body, &requested); err != nil || requested.ApprovalID == "" {
		t.Fatalf("approval response=%s err=%v", body, err)
	}
	if err := coordinator.HandleSlackDecision(ctx, approvals.SlackDecision{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", UserID: "human", ApprovalID: requested.ApprovalID, Approve: true}); err != nil {
		t.Fatal(err)
	}
	job, err = queue.Claim(ctx, "worker-2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	job, err = queue.Transition(ctx, job.ID, job.Lease.Token, jobs.StateRunning, nil)
	if err != nil {
		t.Fatal(err)
	}
	bridge.scopes["second"] = JobScope{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", ThreadTS: "100.1", JobID: string(job.ID), AttemptID: "attempt-2", LeaseToken: job.Lease.Token, SteeringEpoch: job.SteeringEpoch, ExpiresAt: time.Now().Add(time.Minute)}
	input.ApprovalID = requested.ApprovalID
	status, body = triggerBridgeCall(t, bridge, "second", input)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"id":"incident-watch"`)) {
		t.Fatalf("status=%d body=%s", status, body)
	}
	status, body = triggerBridgeCall(t, bridge, "second", triggerSubscriptionRequest{Operation: "list"})
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"id":"incident-watch"`)) {
		t.Fatalf("list status=%d body=%s", status, body)
	}
}

func TestTriggerBridgeRejectsInvalidMutationBeforeApproval(t *testing.T) {
	ctx := context.Background()
	queue := jobs.NewMemoryQueue(nil)
	approvalStore := approvals.NewStore()
	auditLog, _ := audit.NewMemoryAppender([]byte("01234567890123456789012345678901"))
	job, _, _ := queue.Enqueue(ctx, jobs.Spec{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", RootThreadTS: "100.1", SessionID: "session", Generation: 1, RequesterID: "requester", IdempotencyKey: "invalid-trigger", Kind: "agent", MaxAttempts: 3})
	job, _ = queue.Claim(ctx, "worker", time.Minute)
	job, _ = queue.Transition(ctx, job.ID, job.Lease.Token, jobs.StateRunning, nil)
	bridge := &Bridge{jobs: queue, approvals: approvalStore, audit: auditLog, triggers: triggers.NewStore(nil), scopes: map[string]JobScope{
		"capability": {OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", ThreadTS: "100.1", JobID: string(job.ID), AttemptID: "attempt", LeaseToken: job.Lease.Token, SteeringEpoch: job.SteeringEpoch, ExpiresAt: time.Now().Add(time.Minute)},
	}}
	status, body := triggerBridgeCall(t, bridge, "capability", triggerSubscriptionRequest{Operation: "put", ID: "invalid", Instruction: "too frequent", IntervalSeconds: 30})
	if status != http.StatusUnprocessableEntity || !bytes.Contains(body, []byte("invalid_trigger_subscription")) {
		t.Fatalf("status=%d body=%s", status, body)
	}
	values, _ := approvalStore.List(ctx, "org")
	current, _ := queue.Get(ctx, job.ID)
	if len(values) != 0 || current.State != jobs.StateRunning {
		t.Fatalf("invalid mutation consumed human approval workflow: approvals=%v job=%#v", values, current)
	}
}

func bridgeCall(t *testing.T, bridge *Bridge, capability string, input bridgeRequest) (int, []byte) {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "/execute", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+capability)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	bridge.serve(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}

func triggerBridgeCall(t *testing.T, bridge *Bridge, capability string, input triggerSubscriptionRequest) (int, []byte) {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/trigger-subscriptions", bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+capability)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	bridge.serve(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}
