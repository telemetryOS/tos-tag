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

	"github.com/telemetryos/tos-tag/core/approvals"
	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/jobs"
	"github.com/telemetryos/tos-tag/core/keystore"
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
