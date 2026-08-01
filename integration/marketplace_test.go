package integration

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/telemetryos/tos-tag/core/marketplace"
	"github.com/telemetryos/tos-tag/core/tools"
)

func TestWikiReviewedHelperAcceptsInlinePutBody(t *testing.T) {
	const body = "# tos-tag architecture\n\nInline content from a disposable worker."
	temporary := t.TempDir()
	capture := filepath.Join(temporary, "request.json")
	fakeCurl := filepath.Join(temporary, "curl")
	const fakeCurlScript = `#!/bin/bash
set -eu
output=""
input=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output="$2"; shift 2 ;;
    --data-binary) input="${2#@}"; shift 2 ;;
    *) shift ;;
  esac
done
cp "$input" "$CAPTURE_PATH"
printf '%s' '{"namespace":"artifacts","slug":"inline-test","revision":1,"url":"https://wiki.example/pages/inline-test"}' > "$output"
printf '200'
`
	if err := os.WriteFile(fakeCurl, []byte(fakeCurlScript), 0o700); err != nil {
		t.Fatal(err)
	}

	command := exec.Command("/bin/bash", filepath.Join("..", "tool-marketplace", "tools", "wiki", "run.sh"),
		"put", "artifacts/inline-test", "--title", "Inline test", "--body", body, "--md", "--json")
	command.Env = []string{"PATH=" + temporary + ":/usr/bin:/bin", "HOME=" + temporary, "WIKI_URL=https://wiki.example", "WIKI_TOKEN=test-token", "TOS_TAG_OPERATION_ID=write", "CAPTURE_PATH=" + capture}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("wiki helper failed: %v: %s", err, output)
	}
	if !strings.Contains(string(output), `"namespace": "artifacts"`) || !strings.Contains(string(output), `"slug": "inline-test"`) {
		t.Fatalf("unexpected helper output: %s", output)
	}
	payloadBytes, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["body_html"] != body || payload["format"] != "markdown" || payload["title"] != "Inline test" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestWikiReviewedHelperAllowsOnlyRecoverablePageDelete(t *testing.T) {
	temporary := t.TempDir()
	capture := filepath.Join(temporary, "curl-argv.txt")
	fakeCurl := filepath.Join(temporary, "curl")
	const fakeCurlScript = `#!/bin/bash
set -eu
output=""
printf '%s\n' "$@" > "$CAPTURE_PATH"
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf '{}' > "$output"
printf '200'
`
	if err := os.WriteFile(fakeCurl, []byte(fakeCurlScript), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/bash", filepath.Join("..", "tool-marketplace", "tools", "wiki", "run.sh"), "rm", "artifacts/obsolete-page")
	command.Env = []string{"PATH=" + temporary + ":/usr/bin:/bin", "HOME=" + temporary, "WIKI_URL=https://wiki.example", "WIKI_TOKEN=test-token", "TOS_TAG_OPERATION_ID=delete", "CAPTURE_PATH=" + capture}
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "deleted (soft) artifacts/obsolete-page") {
		t.Fatalf("wiki soft-delete failed: %v: %s", err, output)
	}
	argv, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "DELETE") || !strings.Contains(string(argv), "/api/v1/pages/artifacts/obsolete-page") || strings.Contains(string(argv), "/namespaces") {
		t.Fatalf("unexpected delete request: %s", argv)
	}
}

func TestTelemetryOSAgentSkillsMarketplaceWhenAvailable(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "telemetryos-agent-skills"))
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		t.Skip("telemetryos-agent-skills checkout not present")
	}
	snapshots, err := marketplace.LoadPluginMarketplace(root, filepath.Join(".claude-plugin", "marketplace.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) < 10 {
		t.Fatalf("loaded only %d behavioral skills", len(snapshots))
	}
	for _, snapshot := range snapshots {
		for _, file := range snapshot.Files {
			if filepath.Ext(file) == ".sh" {
				t.Fatalf("executable leaked into behavioral snapshot: %s", file)
			}
		}
	}
}

func TestCheckedInReviewedToolMarketplace(t *testing.T) {
	registry, err := tools.LoadMarketplace(filepath.Join("..", "tool-marketplace"), "catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"telemetryos.linear": true, "telemetryos.wiki": true, "telemetryos.otel": true, "telemetryos.device-logs": true, "telemetryos.mongo": true, "telemetryos.code": true}
	for _, snapshot := range registry.List() {
		delete(want, snapshot.ToolID)
		if snapshot.ContentHash == "" || len(snapshot.Operations) == 0 {
			t.Fatalf("incomplete tool snapshot: %#v", snapshot)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing reviewed tools: %#v", want)
	}
	wiki, ok := registry.Resolve("telemetryos.wiki")
	if !ok {
		t.Fatal("wiki tool was not resolved")
	}
	operations := make(map[string]tools.Operation, len(wiki.Manifest.Operations))
	for _, operation := range wiki.Manifest.Operations {
		operations[operation.ID] = operation
		if operation.Risk == "admin" {
			t.Fatal("wiki exposed an admin-risk operation")
		}
	}
	if len(operations) != 3 || operations["read"].Risk != "read" || operations["write"].Risk != "write" || operations["delete"].Risk != "destructive" {
		t.Fatalf("wiki operations are not page CRUD only: %#v", operations)
	}
	if operations["read"].RequiresApproval() || operations["write"].RequiresApproval() || !operations["delete"].RequiresApproval() {
		t.Fatalf("wiki approval boundary is invalid: %#v", operations)
	}
}

func TestConfiguredPluginPairWhenAvailable(t *testing.T) {
	headlessRoot := filepath.Clean(filepath.Join("..", "..", "telemetryos-agent-skills"))
	baseRoot := filepath.Clean(filepath.Join("..", "..", "tag-agent-skills"))
	for _, root := range []string{headlessRoot, baseRoot} {
		if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
			t.Skipf("checkout not present: %s", root)
		}
	}
	headless, err := marketplace.LoadPlugin(headlessRoot, filepath.Join(".claude-plugin", "marketplace.json"), "telemetryos-automation")
	if err != nil || len(headless) < 10 {
		t.Fatalf("headless skills=%d err=%v", len(headless), err)
	}
	base, err := marketplace.LoadPlugin(baseRoot, filepath.Join(".claude-plugin", "marketplace.json"), "base")
	if err != nil || len(base) != 3 {
		t.Fatalf("base skills=%d err=%v", len(base), err)
	}
	baseNames := map[string]bool{}
	for _, snapshot := range base {
		baseNames[snapshot.Name] = true
	}
	for _, expected := range []string{"slack-message-design", "tag-triggers", "team-alignment"} {
		if !baseNames[expected] {
			t.Fatalf("base plugin missing %s: %#v", expected, baseNames)
		}
	}
	for _, snapshot := range append(headless, base...) {
		if strings.Contains(snapshot.Name, "/") {
			t.Fatalf("Codex skill name is not flat: %s", snapshot.Name)
		}
		for _, file := range snapshot.Files {
			if filepath.Ext(file) == ".sh" {
				t.Fatalf("executable leaked into behavioral snapshot: %s", file)
			}
		}
	}
}
