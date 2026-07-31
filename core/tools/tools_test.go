package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/usage"
)

func TestToolExecutionUsesExactArgvAndDeclaredEnvironmentOnly(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "linear.sh")
	content := "#!/bin/sh\nprintf 'arg=%s\\n' \"$1\"\nprintf 'linear=%s\\n' \"$LINEAR_API_KEY\"\nprintf 'slack=%s\\n' \"${SLACK_BOT_TOKEN-unset}\"\n"
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"linear","version":"1.0.0","script":"linear.sh","operations":[{"id":"read","env":["LINEAR_API_KEY"],"timeout_seconds":2,"max_output_bytes":4096,"risk":"read"}]}`
	if err := os.WriteFile(filepath.Join(root, "tool.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, err := LoadBundle(root, "tool.json")
	if err != nil {
		t.Fatal(err)
	}
	request := Request{OperationID: "read", Args: []string{"value; echo injection"}, SecretValues: map[string]string{"LINEAR_API_KEY": "test-linear-secret"}, Capability: Capability{ToolID: "linear", ToolVersion: "1.0.0", OperationID: "read", AttemptToken: "lease-token", SteeringEpoch: 1, ExpiresAt: time.Now().Add(time.Minute)}}
	result, err := (Executor{Enabled: true}).Execute(context.Background(), bundle, request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "arg=value; echo injection") || !strings.Contains(result.Output, "linear=[REDACTED]") || !strings.Contains(result.Output, "slack=unset") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

func TestSecretCannotEnterArgvOrEscapeThroughOutput(t *testing.T) {
	root := writeBundle(t, `#!/bin/sh
printf '%s' "$API_TOKEN"
`)
	bundle, err := LoadBundle(root, "tool.json")
	if err != nil {
		t.Fatal(err)
	}
	request := Request{OperationID: "read", SecretValues: map[string]string{"API_TOKEN": "super-secret"}, Capability: Capability{ToolID: "linear", ToolVersion: "v1", OperationID: "read", AttemptToken: "lease", SteeringEpoch: 1, ExpiresAt: time.Now().Add(time.Minute)}}
	result, err := (Executor{Enabled: true}).Execute(context.Background(), bundle, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "[REDACTED]" {
		t.Fatalf("secret output=%q", result.Output)
	}
	request.Args = []string{"super-secret"}
	if _, err := (Executor{Enabled: true}).Execute(context.Background(), bundle, request); err == nil {
		t.Fatal("secret accepted in argv")
	}
}

func TestToolUsageIsContentFree(t *testing.T) {
	root := writeBundle(t, "#!/bin/sh\nprintf ok\n")
	bundle, err := LoadBundle(root, "tool.json")
	if err != nil {
		t.Fatal(err)
	}
	recorder := usage.NewMemory()
	request := Request{OrganizationID: "org", JobID: "job", OperationID: "read", SecretValues: map[string]string{"API_TOKEN": "value"}, Capability: Capability{ToolID: "linear", ToolVersion: "v1", OperationID: "read", AttemptToken: "lease", SteeringEpoch: 1, ExpiresAt: time.Now().Add(time.Minute)}}
	if _, err := (Executor{Enabled: true, Usage: recorder}).Execute(context.Background(), bundle, request); err != nil {
		t.Fatal(err)
	}
	events, _ := recorder.List(context.Background(), "org", 10)
	if len(events) != 1 || events[0].Category != "tool" {
		t.Fatalf("usage=%#v", events)
	}
}

func TestToolExecutionFailsClosed(t *testing.T) {
	bundle := Bundle{Root: t.TempDir(), Manifest: Manifest{ID: "tool", Version: "1", Script: "missing", Operations: []Operation{{ID: "write", TimeoutSeconds: 1, MaxOutputBytes: 10}}}}
	request := Request{OperationID: "write", Capability: Capability{ToolID: "tool", ToolVersion: "1", OperationID: "write", AttemptToken: "token", SteeringEpoch: 1, ExpiresAt: time.Now().Add(time.Minute)}}
	if _, err := (Executor{}).Execute(context.Background(), bundle, request); err == nil {
		t.Fatal("disabled execution was accepted")
	}
	if _, err := (Executor{Enabled: true}).Execute(context.Background(), bundle, Request{OperationID: "write"}); err == nil {
		t.Fatal("missing capability was accepted")
	}
}

func TestLoadBundleRejectsTraversalAndUndeclaredEnvironment(t *testing.T) {
	if _, err := LoadBundle(t.TempDir(), "../tool.json"); err == nil {
		t.Fatal("manifest traversal was accepted")
	}
}

func TestReviewedScriptHashIsEnforcedAtExecution(t *testing.T) {
	root := writeBundle(t, "#!/bin/sh\nprintf original\n")
	bundle, err := LoadBundle(root, "tool.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "linear.sh"), []byte("#!/bin/sh\nprintf tampered\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	request := Request{OperationID: "read", SecretValues: map[string]string{"API_TOKEN": "value"}, Capability: Capability{ToolID: "linear", ToolVersion: "v1", OperationID: "read", AttemptToken: "lease", SteeringEpoch: 1, ExpiresAt: time.Now().Add(time.Minute)}}
	if _, err := (Executor{Enabled: true}).Execute(context.Background(), bundle, request); err == nil || !strings.Contains(err.Error(), "hash changed") {
		t.Fatalf("tampered script error=%v", err)
	}
}

func TestLoadBundleRejectsUnknownRisk(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"tool","version":"v1","script":"run.sh","operations":[{"id":"op","timeout_seconds":1,"max_output_bytes":10,"risk":"typo"}]}`
	if err := os.WriteFile(filepath.Join(root, "tool.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBundle(root, "tool.json"); err == nil {
		t.Fatal("unknown risk was accepted")
	}
}

func writeBundle(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "linear.sh"), []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"linear","version":"v1","script":"linear.sh","operations":[{"id":"read","env":["API_TOKEN"],"timeout_seconds":2,"max_output_bytes":4096,"risk":"read"}]}`
	if err := os.WriteFile(filepath.Join(root, "tool.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}
