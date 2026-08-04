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
	"strings"
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
	if err := os.WriteFile(script, []byte("if [ \"$1\" = fail ]; then printf 'safe diagnostic' >&2; exit 7; fi\nif [ \"$1\" = failwiki ]; then printf 'wiki: put requires inline --body' >&2; exit 7; fi\nprintf 'ok:%s' \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	bundle := Bundle{Root: root, Manifest: Manifest{ID: "demo", Version: "v1", Script: "tool.sh", Operations: []Operation{{ID: "read", Risk: "read", TimeoutSeconds: 2, MaxOutputBytes: 1024}, {ID: "write", Risk: "write", TimeoutSeconds: 2, MaxOutputBytes: 1024}, {ID: "trusted-write", Risk: "write", Approval: ApprovalNever, TimeoutSeconds: 2, MaxOutputBytes: 1024}}}}
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	bundle.ScriptHash = digest(data)
	wikiBundle := bundle
	wikiBundle.Manifest.ID = "telemetryos.wiki"
	registry := &Registry{bundles: map[string]Bundle{"demo": bundle, "telemetryos.wiki": wikiBundle}}
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
	bridge.scopes[capability] = JobScope{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", JobID: string(running.ID), AttemptID: "attempt", LeaseToken: running.Lease.Token, SteeringEpoch: running.SteeringEpoch, ExpiresAt: time.Now().Add(time.Minute), AllowedTools: []string{"demo", "telemetryos.wiki"}}

	status, body := bridgeCall(t, bridge, capability, bridgeRequest{ToolID: "demo", OperationID: "read", Arguments: []string{"one"}})
	if status != http.StatusOK || !bytes.Contains(body, []byte("ok:one")) {
		t.Fatalf("status=%d body=%s", status, body)
	}

	status, body = bridgeCall(t, bridge, capability, bridgeRequest{ToolID: "demo", OperationID: "read", Arguments: []string{"fail"}})
	if status != http.StatusUnprocessableEntity || !bytes.Contains(body, []byte(`"error_code":"nonzero_exit"`)) || !bytes.Contains(body, []byte("safe diagnostic")) {
		t.Fatalf("failed tool status=%d body=%s", status, body)
	}
	var foundFailure bool
	for _, receipt := range auditLog.List("org") {
		if receipt.Type == "tool.execution.failed" && receipt.Metadata["error_code"] == "nonzero_exit" && receipt.Metadata["exit_code"] == float64(7) {
			foundFailure = true
		}
	}
	if !foundFailure {
		t.Fatal("failed tool execution was not recorded with a content-free error code")
	}

	status, body = bridgeCall(t, bridge, capability, bridgeRequest{ToolID: "telemetryos.wiki", OperationID: "read", Arguments: []string{"get", "artifacts/guide"}})
	if status != http.StatusBadRequest || !bytes.Contains(body, []byte(`"typed_wiki_request_required"`)) {
		t.Fatalf("untyped Wiki request status=%d body=%s", status, body)
	}

	status, body = bridgeCall(t, bridge, capability, bridgeRequest{ToolID: "telemetryos.wiki", OperationID: "read", Arguments: []string{"failwiki"}, Wiki: &wikiAction{Operation: "get", PageReference: "artifacts/guide"}})
	if status != http.StatusUnprocessableEntity || !bytes.Contains(body, []byte(`"validation_code":"wiki.cli.missing_body"`)) {
		t.Fatalf("Wiki validation failure status=%d body=%s", status, body)
	}
	foundValidation := false
	for _, receipt := range auditLog.List("org") {
		if receipt.Type == "tool.execution.failed" && receipt.Metadata["validation_code"] == "wiki.cli.missing_body" {
			foundValidation = true
		}
	}
	if !foundValidation {
		t.Fatal("Wiki validation failure did not persist its sanitized code")
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

	status, body = bridgeCall(t, bridge, capability, bridgeRequest{ToolID: "demo", OperationID: "trusted-write", Arguments: []string{"three"}})
	if status != http.StatusOK || !bytes.Contains(body, []byte("ok:three")) {
		t.Fatalf("trusted write status=%d body=%s", status, body)
	}
	wikiSemantic := &wikiAction{Operation: "put", PageReference: "artifacts/guide", Title: "Guide", Body: "body", Format: "markdown"}
	status, body = bridgeCall(t, bridge, capability, bridgeRequest{ToolID: "telemetryos.wiki", OperationID: "write", Arguments: []string{"two"}, Wiki: wikiSemantic})
	if status != http.StatusConflict {
		t.Fatalf("typed Wiki approval status=%d body=%s", status, body)
	}
	var wikiRequested struct {
		ApprovalID string `json:"approval_id"`
	}
	if err := json.Unmarshal(body, &wikiRequested); err != nil || wikiRequested.ApprovalID == "" {
		t.Fatalf("typed Wiki approval response=%s err=%v", body, err)
	}
	pendingWiki, err := approvalStore.GetContext(ctx, "org", wikiRequested.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if typed, ok := pendingWiki.Action.Arguments["wiki"].(wikiAction); !ok || typed.PageReference != "artifacts/guide" || typed.Operation != "put" {
		t.Fatalf("typed Wiki action = %#v", pendingWiki.Action.Arguments)
	}
	if _, err := approvalStore.ApproveContext(ctx, "org", wikiRequested.ApprovalID, "human"); err != nil {
		t.Fatal(err)
	}
	status, body = bridgeCall(t, bridge, capability, bridgeRequest{ToolID: "telemetryos.wiki", OperationID: "write", Arguments: []string{"two"}, ApprovalID: wikiRequested.ApprovalID, Wiki: wikiSemantic})
	if status != http.StatusOK || !bytes.Contains(body, []byte("ok:two")) {
		t.Fatalf("typed Wiki approval resume status=%d body=%s", status, body)
	}
	storedApprovals, err := approvalStore.List(ctx, "org")
	if err != nil || len(storedApprovals) != 2 {
		t.Fatalf("trusted write created an approval: approvals=%d err=%v", len(storedApprovals), err)
	}
	var foundNeverPolicy bool
	for _, receipt := range auditLog.List("org") {
		if receipt.Type == "tool.execution.requested" && receipt.Metadata["operation_id"] == "trusted-write" && receipt.Metadata["approval_policy"] == "never" {
			foundNeverPolicy = true
		}
	}
	if !foundNeverPolicy {
		t.Fatal("trusted write did not record its approval policy in the audit receipt")
	}

	if err := bridge.RevokeAttempt(ctx, "attempt"); err != nil {
		t.Fatal(err)
	}
	status, _ = bridgeCall(t, bridge, capability, bridgeRequest{ToolID: "demo", OperationID: "read"})
	if status != http.StatusUnauthorized {
		t.Fatalf("revoked capability status=%d", status)
	}
}

func TestBridgeRejectsModelSuppliedSecretReferences(t *testing.T) {
	secrets, err := keystore.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	bridge := &Bridge{gateway: Gateway{Registry: &Registry{bundles: map[string]Bundle{}}, Secrets: secrets, Executor: Executor{Enabled: true}}, scopes: map[string]JobScope{"capability": {OrganizationID: "org", JobID: "job", AttemptID: "attempt", LeaseToken: "lease", SteeringEpoch: 1, ExpiresAt: time.Now().Add(time.Minute)}}}
	request := httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader(`{"tool_id":"demo","operation_id":"read","secret_references":{"API_TOKEN":"chosen-by-model"}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer capability")
	recorder := httptest.NewRecorder()
	bridge.serve(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_request") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
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
	job, _, err := queue.Enqueue(ctx, jobs.Spec{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", RootThreadTS: "100.1", SessionID: "session", Generation: 1, RequesterID: "human", IdempotencyKey: "job-trigger", Kind: "agent", MaxAttempts: 3})
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
	input := triggerSubscriptionRequest{Operation: "put", ID: "incident-watch", Instruction: "Check for an unresolved incident.", Cron: "*/5 * * * *", Timezone: "America/Vancouver", MinConfidence: .8, Enabled: &enabled}
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
	requestedApproval, err := approvalStore.GetContext(ctx, "org", requested.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if requestedApproval.RequesterID != "agent:"+string(job.ID) {
		t.Fatalf("tool action requester = %q, want executing agent", requestedApproval.RequesterID)
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
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"id":"incident-watch"`)) || !bytes.Contains(body, []byte(`"cron":"*/5 * * * *"`)) {
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
	status, body := triggerBridgeCall(t, bridge, "capability", triggerSubscriptionRequest{Operation: "put", ID: "invalid", Instruction: "invalid schedule", Cron: "not a cron", Timezone: "UTC"})
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
