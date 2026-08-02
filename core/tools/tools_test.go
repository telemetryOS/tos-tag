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
	content := "#!/bin/sh\nprintf 'arg=%s\\n' \"$1\"\nprintf 'operation=%s\\n' \"$TOS_TAG_OPERATION_ID\"\nprintf 'linear=%s\\n' \"$LINEAR_API_KEY\"\nprintf 'slack=%s\\n' \"${SLACK_BOT_TOKEN-unset}\"\n"
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
	if !strings.Contains(result.Output, "arg=value; echo injection") || !strings.Contains(result.Output, "operation=read") || !strings.Contains(result.Output, "linear=[REDACTED]") || !strings.Contains(result.Output, "slack=unset") {
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

func TestReviewedPublicURLCanEnterArgvAndOutputWithoutUnredactingToken(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "wiki.sh")
	content := "#!/bin/sh\nprintf 'arg=%s\\nurl=%s\\ntoken=%s\\n' \"$1\" \"$WIKI_URL\" \"$WIKI_TOKEN\"\n"
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"wiki","version":"1.0.0","script":"wiki.sh","operations":[{"id":"read","env":["WIKI_URL","WIKI_TOKEN"],"public_env":["WIKI_URL"],"timeout_seconds":2,"max_output_bytes":4096,"risk":"read"}]}`
	if err := os.WriteFile(filepath.Join(root, "tool.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, err := LoadBundle(root, "tool.json")
	if err != nil {
		t.Fatal(err)
	}
	const publicURL = "https://wiki.example/pages/0123456789abcdef01234567"
	request := Request{OperationID: "read", Args: []string{publicURL}, SecretValues: map[string]string{"WIKI_URL": "https://wiki.example", "WIKI_TOKEN": "secret-token"}, Capability: Capability{ToolID: "wiki", ToolVersion: "1.0.0", OperationID: "read", AttemptToken: "lease", SteeringEpoch: 1, ExpiresAt: time.Now().Add(time.Minute)}}
	result, err := (Executor{Enabled: true}).Execute(context.Background(), bundle, request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, publicURL) || !strings.Contains(result.Output, "url=https://wiki.example") || !strings.Contains(result.Output, "token=[REDACTED]") || strings.Contains(result.Output, "secret-token") {
		t.Fatalf("unexpected public URL output: %q", result.Output)
	}

	request.SecretValues["WIKI_URL"] = "https://user:password@wiki.example"
	request.Args = []string{"https://user:password@wiki.example"}
	if _, err := (Executor{Enabled: true}).Execute(context.Background(), bundle, request); err == nil {
		t.Fatal("credential-bearing public URL was accepted in argv")
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

func TestLoadBundleRejectsAdminRisk(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"tool","version":"v1","script":"run.sh","operations":[{"id":"admin","timeout_seconds":1,"max_output_bytes":10,"risk":"admin"}]}`
	if err := os.WriteFile(filepath.Join(root, "tool.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBundle(root, "tool.json"); err == nil {
		t.Fatal("admin-risk operation was accepted")
	}

	bundle := Bundle{Manifest: Manifest{ID: "tool", Version: "v1", Operations: []Operation{{ID: "admin", Risk: "admin"}}}}
	if _, err := (Executor{Enabled: true}).Execute(context.Background(), bundle, Request{OperationID: "admin"}); err == nil || !strings.Contains(err.Error(), "admin tool operations are disabled") {
		t.Fatalf("executor admin-risk error=%v", err)
	}
}

func TestTelemetryOSCodeToolIsPermanentlyReadOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\nprintf ok\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"telemetryos.code","version":"v1","script":"run.sh","operations":[{"id":"write","timeout_seconds":1,"max_output_bytes":10,"risk":"write"}]}`
	if err := os.WriteFile(filepath.Join(root, "tool.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBundle(root, "tool.json"); err == nil || !strings.Contains(err.Error(), "permanently read-only") {
		t.Fatalf("source write manifest error=%v", err)
	}

	bundle := Bundle{Root: root, ScriptHash: digest([]byte("#!/bin/sh\nprintf ok\n")), Manifest: Manifest{ID: "telemetryos.code", Version: "v1", Script: "run.sh", Operations: []Operation{{ID: "write", TimeoutSeconds: 1, MaxOutputBytes: 10, Risk: "write"}}}}
	request := Request{OperationID: "write", Capability: Capability{ToolID: "telemetryos.code", ToolVersion: "v1", OperationID: "write", AttemptToken: "attempt", SteeringEpoch: 1, ExpiresAt: time.Now().Add(time.Minute)}}
	if _, err := (Executor{Enabled: true}).Execute(context.Background(), bundle, request); err == nil || !strings.Contains(err.Error(), "permanently read-only") {
		t.Fatalf("source write execution error=%v", err)
	}
}

func TestLoadBundleValidatesExplicitApprovalPolicy(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"tool","version":"v1","script":"run.sh","operations":[{"id":"op","timeout_seconds":1,"max_output_bytes":10,"risk":"write","approval":"never"}]}`
	if err := os.WriteFile(filepath.Join(root, "tool.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, err := LoadBundle(root, "tool.json")
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.Operations[0].RequiresApproval() {
		t.Fatal("reviewed approval=never operation still requires approval")
	}

	invalid := strings.Replace(manifest, `"approval":"never"`, `"approval":"sometimes"`, 1)
	if err := os.WriteFile(filepath.Join(root, "tool.json"), []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBundle(root, "tool.json"); err == nil {
		t.Fatal("unknown approval policy was accepted")
	}
}

func TestLoadBundleRejectsReservedControlEnvironment(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"tool","version":"v1","script":"run.sh","operations":[{"id":"read","env":["TOS_TAG_OPERATION_ID"],"timeout_seconds":1,"max_output_bytes":10,"risk":"read"}]}`
	if err := os.WriteFile(filepath.Join(root, "tool.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBundle(root, "tool.json"); err == nil {
		t.Fatal("reserved control environment was accepted")
	}
}

func TestLoadBundleRejectsUndeclaredOrNonURLPublicEnvironment(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, manifest := range map[string]string{
		"undeclared": `{"id":"tool","version":"v1","script":"run.sh","operations":[{"id":"read","env":["API_TOKEN"],"public_env":["WIKI_URL"],"timeout_seconds":1,"max_output_bytes":10,"risk":"read"}]}`,
		"non URL":    `{"id":"tool","version":"v1","script":"run.sh","operations":[{"id":"read","env":["PUBLIC_VALUE"],"public_env":["PUBLIC_VALUE"],"timeout_seconds":1,"max_output_bytes":10,"risk":"read"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(root, "tool.json"), []byte(manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadBundle(root, "tool.json"); err == nil {
				t.Fatal("invalid public environment was accepted")
			}
		})
	}
}

func TestExecutorRunsReviewedBashHelpers(t *testing.T) {
	root := t.TempDir()
	content := "#!/usr/bin/env bash\nvalues=(one two)\n[[ $TOS_TAG_OPERATION_ID == read ]]\nprintf '%s' \"${values[1]}\"\n"
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"bash","version":"v1","script":"run.sh","operations":[{"id":"read","timeout_seconds":2,"max_output_bytes":4096,"risk":"read"}]}`
	if err := os.WriteFile(filepath.Join(root, "tool.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, err := LoadBundle(root, "tool.json")
	if err != nil {
		t.Fatal(err)
	}
	result, err := (Executor{Enabled: true}).Execute(context.Background(), bundle, Request{OperationID: "read", Capability: Capability{ToolID: "bash", ToolVersion: "v1", OperationID: "read", AttemptToken: "attempt", SteeringEpoch: 1, ExpiresAt: time.Now().Add(time.Minute)}})
	if err != nil || result.Output != "two" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestToolExecutionReceivesPrivateTemporaryHome(t *testing.T) {
	root := writeBundle(t, `#!/bin/sh
[ -n "$HOME" ] || exit 10
[ "$HOME" = "$TMPDIR" ] || exit 11
case "$HOME" in
  "$HOST_HOME") exit 12 ;;
esac
[ -d "$HOME" ] || exit 13
printf 'private-home-ready'
`)
	bundle, err := LoadBundle(root, "tool.json")
	if err != nil {
		t.Fatal(err)
	}
	request := Request{OperationID: "read", SecretValues: map[string]string{"API_TOKEN": "value"}, Capability: Capability{ToolID: "linear", ToolVersion: "v1", OperationID: "read", AttemptToken: "lease", SteeringEpoch: 1, ExpiresAt: time.Now().Add(time.Minute)}}
	result, err := (Executor{Enabled: true}).Execute(context.Background(), bundle, request)
	if err != nil || result.Output != "private-home-ready" {
		t.Fatalf("result=%#v err=%v", result, err)
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
