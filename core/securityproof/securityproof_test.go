package securityproof

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/config"
	"github.com/telemetryos/tos-tag/core/keystore"
	"github.com/telemetryos/tos-tag/core/tools"
	"github.com/telemetryos/tos-tag/core/workers"
)

func TestSecretSentinelNeverCrossesManagedSurfaces(t *testing.T) {
	const sentinel = "tos-tag-secret-sentinel-7dd70c"
	ctx := context.Background()

	cfg := config.DefaultConfiguration
	cfg.Auth.AdminToken = sentinel
	cfg.Slack.AppLevelToken = "xapp-" + sentinel
	cfg.Slack.BotUserOAuthToken = "xoxb-" + sentinel
	cfg.Classifier.OpenAIAPIKey = sentinel
	status, _ := json.Marshal(cfg.RedactedStatus())
	assertAbsent(t, "redacted status/log fields", status, sentinel)

	secrets, err := keystore.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	reference, err := secrets.Put(ctx, "org", "LINEAR_API_KEY", "linear helper", sentinel)
	if err != nil {
		t.Fatal(err)
	}
	listed, _ := secrets.List(ctx, "org")
	encodedReferences, _ := json.Marshal(listed)
	assertAbsent(t, "keystore listing", encodedReferences, sentinel)

	toolRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(toolRoot, "tool.json"), []byte(`{"id":"proof","version":"v1","script":"proof.sh","operations":[{"id":"read","env":["LINEAR_API_KEY"],"timeout_seconds":5,"max_output_bytes":4096,"risk":"read"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolRoot, "proof.sh"), []byte("#!/bin/sh\nprintf '%s' \"$LINEAR_API_KEY\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	bundle, err := tools.LoadBundle(toolRoot, "tool.json")
	if err != nil {
		t.Fatal(err)
	}
	result, err := (tools.Executor{Enabled: true}).Execute(ctx, bundle, tools.Request{OrganizationID: "org", OperationID: "read", SecretValues: map[string]string{"LINEAR_API_KEY": sentinel}, Capability: tools.Capability{ToolID: "proof", ToolVersion: "v1", OperationID: "read", AttemptToken: "attempt", SteeringEpoch: 1, ExpiresAt: time.Now().Add(time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	assertAbsent(t, "tool output", []byte(result.Output), sentinel)
	if _, err := (tools.Executor{Enabled: true}).Execute(ctx, bundle, tools.Request{OperationID: "read", Args: []string{sentinel}, SecretValues: map[string]string{"LINEAR_API_KEY": sentinel}, Capability: tools.Capability{ToolID: "proof", ToolVersion: "v1", OperationID: "read", AttemptToken: "attempt", SteeringEpoch: 1, ExpiresAt: time.Now().Add(time.Minute)}}); err == nil {
		t.Fatal("secret was accepted in tool argv")
	}

	t.Setenv("TOS_TAG_HOST_SENTINEL", sentinel)
	manager, err := workers.NewLocal(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := manager.Provision(ctx, workers.Spec{JobID: "proof", AttemptID: "attempt", Command: []string{"/bin/sh", "-c", `env > "$TOS_TAG_ARTIFACTS/env.txt"`}, WallTime: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Terminate(context.Background(), workspace) })
	var artifact []byte
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		items, exportErr := manager.ExportArtifacts(ctx, workspace, []workers.ArtifactSpec{{Path: "env.txt", MaxBytes: 16 << 10}})
		if exportErr == nil && len(items) == 1 && len(items[0].Data) > 0 {
			artifact = items[0].Data
			break
		}
	}
	if len(artifact) == 0 {
		t.Fatal("worker environment artifact was not produced")
	}
	assertAbsent(t, "worker environment/artifact", artifact, sentinel)

	chain, err := audit.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chain.Append(audit.AppendRequest{OrganizationID: "org", Type: "proof", RetentionEpoch: "2026-07", Metadata: map[string]any{"secret_value": sentinel}}); err == nil {
		t.Fatal("content-bearing receipt metadata was accepted")
	}
	resolved, err := secrets.Resolve(ctx, "org", reference.ID)
	if err != nil || resolved != sentinel {
		t.Fatal("keystore failed to resolve the secret only at the authorized execution boundary")
	}
}

func assertAbsent(t *testing.T, surface string, value []byte, sentinel string) {
	t.Helper()
	if strings.Contains(string(value), sentinel) {
		t.Fatalf("%s leaked the secret sentinel", surface)
	}
}
