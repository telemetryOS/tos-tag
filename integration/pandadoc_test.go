package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPandaDocReviewedHelperMatchesCanonicalSource(t *testing.T) {
	canonicalPath := filepath.Clean(filepath.Join("..", "..", "tag-agent-skills", "src", "skills", "pandadoc", "scripts", "pandadoc.sh"))
	reviewedPath := filepath.Join("..", "tool-marketplace", "tools", "pandadoc", "run.sh")
	canonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := os.ReadFile(reviewedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, reviewed) {
		t.Fatal("reviewed PandaDoc helper differs from canonical skill source")
	}
}

func TestPandaDocReadRoutesAndKeepsAPIKeyOutOfArgv(t *testing.T) {
	temporary := t.TempDir()
	capture := filepath.Join(temporary, "curl-argv.txt")
	authCapture := filepath.Join(temporary, "curl-auth.txt")
	fakeCurl := filepath.Join(temporary, "curl")
	const fakeCurlScript = `#!/bin/bash
set -eu
output=""
config=""
printf '%s\n' "$@" > "$CAPTURE_PATH"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) output="$2"; shift 2 ;;
    --config) config="$2"; shift 2 ;;
    *) shift ;;
  esac
done
cp "$config" "$AUTH_CAPTURE_PATH"
printf '%s' '{"results":[{"id":"document-1"}],"count":1}' > "$output"
printf '200'
`
	if err := os.WriteFile(fakeCurl, []byte(fakeCurlScript), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join("..", "tool-marketplace", "tools", "pandadoc", "run.sh")
	key := "fixture-pandadoc-api-key-0123456789"
	command := exec.Command("bash", helper, "documents", "--query", `{"count":10,"page":1,"status":2}`)
	command.Env = []string{
		"PATH=" + temporary + ":/usr/bin:/bin",
		"HOME=" + temporary,
		"PANDA_DOC_API_KEY=" + key,
		"TOS_TAG_OPERATION_ID=read",
		"CAPTURE_PATH=" + capture,
		"AUTH_CAPTURE_PATH=" + authCapture,
	}
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "document-1") {
		t.Fatalf("PandaDoc read failed: %v: %s", err, output)
	}
	arguments, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"--request\nGET",
		"--data-urlencode\ncount=10",
		"--data-urlencode\npage=1",
		"--data-urlencode\nstatus=2",
		"https://api.pandadoc.com/public/v1/documents",
	} {
		if !strings.Contains(string(arguments), expected) {
			t.Fatalf("curl arguments missing %q: %s", expected, arguments)
		}
	}
	if bytes.Contains(arguments, []byte(key)) || bytes.Contains(output, []byte(key)) {
		t.Fatal("PandaDoc key escaped into argv or output")
	}
	auth, err := os.ReadFile(authCapture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(auth), "Authorization: API-Key "+key) {
		t.Fatalf("API key was not supplied through the private curl config: %s", auth)
	}
}

func TestPandaDocWriteUsesPrivateBodyFile(t *testing.T) {
	temporary := t.TempDir()
	capture := filepath.Join(temporary, "curl-argv.txt")
	bodyCapture := filepath.Join(temporary, "curl-body.json")
	fakeCurl := filepath.Join(temporary, "curl")
	const fakeCurlScript = `#!/bin/bash
set -eu
output=""
body=""
printf '%s\n' "$@" > "$CAPTURE_PATH"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) output="$2"; shift 2 ;;
    --data-binary) body="${2#@}"; shift 2 ;;
    *) shift ;;
  esac
done
cp "$body" "$BODY_CAPTURE_PATH"
printf '%s' '{"id":"document-1","status":"document.sent"}' > "$output"
printf '200'
`
	if err := os.WriteFile(fakeCurl, []byte(fakeCurlScript), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join("..", "tool-marketplace", "tools", "pandadoc", "run.sh")
	body := `{"silent":false,"subject":"Please review"}`
	command := exec.Command("bash", helper, "send-document", "document-1", "--data", body)
	command.Env = []string{
		"PATH=" + temporary + ":/usr/bin:/bin",
		"HOME=" + temporary,
		"PANDA_DOC_API_KEY=fixture-pandadoc-api-key-0123456789",
		"TOS_TAG_OPERATION_ID=write",
		"CAPTURE_PATH=" + capture,
		"BODY_CAPTURE_PATH=" + bodyCapture,
	}
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "document.sent") {
		t.Fatalf("PandaDoc send failed: %v: %s", err, output)
	}
	arguments, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(arguments), "--request\nPOST") || !strings.Contains(string(arguments), "https://api.pandadoc.com/public/v1/documents/document-1/send") {
		t.Fatalf("unexpected send argv: %s", arguments)
	}
	if bytes.Contains(arguments, []byte(body)) {
		t.Fatalf("write body escaped into process argv: %s", arguments)
	}
	capturedBody, err := os.ReadFile(bodyCapture)
	if err != nil {
		t.Fatal(err)
	}
	if string(capturedBody) != body {
		t.Fatalf("private body mismatch: %s", capturedBody)
	}
}

func TestPandaDocHelperRejectsUnreviewedCommandsAndRisk(t *testing.T) {
	helper := filepath.Join("..", "tool-marketplace", "tools", "pandadoc", "run.sh")
	tests := []struct {
		name      string
		operation string
		arguments []string
		want      string
	}{
		{name: "raw request", operation: "read", arguments: []string{"get", "https://example.test"}, want: "unsupported command"},
		{name: "read cannot send", operation: "read", arguments: []string{"send-document", "document-1", "--data", `{}`}, want: "not permitted"},
		{name: "write cannot delete", operation: "write", arguments: []string{"delete-document", "document-1"}, want: "not permitted"},
		{name: "reject path identifier", operation: "read", arguments: []string{"document", "../secret"}, want: "malformed"},
		{name: "reject arbitrary flag", operation: "read", arguments: []string{"documents", "--header", "X-Test: value"}, want: "unsupported argument"},
		{name: "reject nested query", operation: "read", arguments: []string{"documents", "--query", `{"filter":{"name":"private"}}`}, want: "unsupported fields or values"},
		{name: "reject unbounded count", operation: "read", arguments: []string{"documents", "--query", `{"count":101}`}, want: "unsupported fields or values"},
		{name: "require write body", operation: "write", arguments: []string{"create-document"}, want: "requires --data"},
		{name: "require executor operation", operation: "", arguments: []string{"documents"}, want: "TOS_TAG_OPERATION_ID is required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("bash", append([]string{helper}, test.arguments...)...)
			command.Env = []string{
				"PATH=/usr/bin:/bin",
				"HOME=" + t.TempDir(),
				"PANDA_DOC_API_KEY=fixture-pandadoc-api-key-0123456789",
				"TOS_TAG_OPERATION_ID=" + test.operation,
			}
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), test.want) {
				t.Fatalf("expected rejection containing %q, err=%v output=%s", test.want, err, output)
			}
		})
	}
}

func TestPandaDocProviderFailureIsRedacted(t *testing.T) {
	temporary := t.TempDir()
	fakeCurl := filepath.Join(temporary, "curl")
	const fakeCurlScript = `#!/bin/bash
set -eu
output=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) output="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf '%s' '{"detail":"PRIVATE-PROVIDER-DETAIL"}' > "$output"
printf '403'
`
	if err := os.WriteFile(fakeCurl, []byte(fakeCurlScript), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join("..", "tool-marketplace", "tools", "pandadoc", "run.sh")
	command := exec.Command("bash", helper, "documents")
	command.Env = []string{
		"PATH=" + temporary + ":/usr/bin:/bin",
		"HOME=" + temporary,
		"PANDA_DOC_API_KEY=fixture-pandadoc-api-key-0123456789",
		"TOS_TAG_OPERATION_ID=read",
	}
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "API request failed with HTTP 403") {
		t.Fatalf("expected bounded provider failure, err=%v output=%s", err, output)
	}
	if strings.Contains(string(output), "PRIVATE-PROVIDER-DETAIL") {
		t.Fatalf("provider failure exposed response details: %s", output)
	}
}
