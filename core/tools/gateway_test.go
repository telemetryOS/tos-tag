package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/keystore"
)

func TestEnvironmentBindingsAreDerivedServerSide(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\nprintf '%s' \"$API_TOKEN\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tool.json"), []byte(`{"id":"demo","version":"v1","script":"run.sh","operations":[{"id":"read","env":["API_TOKEN"],"timeout_seconds":2,"max_output_bytes":4096,"risk":"read"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, err := LoadBundle(root, "tool.json")
	if err != nil {
		t.Fatal(err)
	}
	registry := &Registry{bundles: map[string]Bundle{"demo": bundle}}
	secrets, err := keystore.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := ImportEnvironmentBindings(context.Background(), registry, []string{"demo"}, secrets, "org", func(name string) (string, bool) {
		return map[string]string{"API_TOKEN": "server-side-secret"}[name], name == "API_TOKEN"
	})
	if err != nil {
		t.Fatal(err)
	}
	request := GatewayRequest{Request: Request{OrganizationID: "org", OperationID: "read", Capability: Capability{ToolID: "demo", ToolVersion: "v1", OperationID: "read", AttemptToken: "attempt", SteeringEpoch: 1, ExpiresAt: time.Now().Add(time.Minute)}}}
	result, err := (Gateway{Registry: registry, Secrets: secrets, Bindings: bindings, Executor: Executor{Enabled: true}}).Execute(context.Background(), "demo", request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "[REDACTED]" {
		t.Fatalf("secret output=%q", result.Output)
	}
}

func TestEnvironmentBindingImportFailsClosedWithoutDeclaredValue(t *testing.T) {
	bundleRoot := writeBundle(t, "#!/bin/sh\n")
	bundle, err := LoadBundle(bundleRoot, "tool.json")
	if err != nil {
		t.Fatal(err)
	}
	registry := &Registry{bundles: map[string]Bundle{"linear": bundle}}
	secrets, _ := keystore.New([]byte("01234567890123456789012345678901"))
	_, err = ImportEnvironmentBindings(context.Background(), registry, []string{"linear"}, secrets, "org", func(string) (string, bool) { return "", false })
	if err == nil || !strings.Contains(err.Error(), "API_TOKEN") {
		t.Fatalf("missing binding error=%v", err)
	}
}
