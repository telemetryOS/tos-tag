package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttioReviewedHelperMatchesCanonicalSource(t *testing.T) {
	canonicalPath := filepath.Clean(filepath.Join("..", "..", "tag-agent-skills", "src", "skills", "attio", "scripts", "attio.sh"))
	reviewedPath := filepath.Join("..", "tool-marketplace", "tools", "attio", "run.sh")
	canonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := os.ReadFile(reviewedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, reviewed) {
		t.Fatal("reviewed Attio helper drifted from the canonical skill source")
	}
}

func TestAttioReviewedHelperRoutesAndProtectsToken(t *testing.T) {
	temporary := t.TempDir()
	capture := filepath.Join(temporary, "curl-argv.txt")
	fakeCurl := filepath.Join(temporary, "curl")
	const fakeCurlScript = `#!/bin/bash
set -eu
output=""
printf '%s\n' "$@" > "$CAPTURE_PATH"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) output="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf '%s' '{"data":[{"id":{"object_id":"object-1"}}]}' > "$output"
printf '200'
`
	if err := os.WriteFile(fakeCurl, []byte(fakeCurlScript), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join("..", "tool-marketplace", "tools", "attio", "run.sh")
	token := "fixture-attio-token-with-safe-shape"
	command := exec.Command("/bin/bash", helper, "get", "/v2/objects", "--query", `{"limit":10,"participants":["one@example.test","two@example.test"]}`)
	command.Env = []string{
		"PATH=" + temporary + ":/usr/bin:/bin",
		"HOME=" + temporary,
		"ATTIO_ACCESS_TOKEN=" + token,
		"TOS_TAG_OPERATION_ID=read",
		"CAPTURE_PATH=" + capture,
	}
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "object-1") {
		t.Fatalf("Attio read failed: %v: %s", err, output)
	}
	arguments, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"--request\nGET",
		"https://api.attio.com/v2/objects?limit=10&participants=one%40example.test&participants=two%40example.test",
	} {
		if !strings.Contains(string(arguments), expected) {
			t.Fatalf("curl arguments missing %q: %s", expected, arguments)
		}
	}
	if bytes.Contains(arguments, []byte(token)) || bytes.Contains(output, []byte(token)) {
		t.Fatal("Attio token escaped into argv or output")
	}
}

func TestAttioReviewedHelperEnforcesRiskAndEndpointCatalog(t *testing.T) {
	helper := filepath.Join("..", "tool-marketplace", "tools", "attio", "run.sh")
	tests := []struct {
		name      string
		operation string
		arguments []string
		want      string
	}{
		{name: "read cannot write", operation: "read", arguments: []string{"post", "/v2/tasks", "--data", `{}`}, want: "not permitted"},
		{name: "write cannot query", operation: "write", arguments: []string{"query", "/v2/sql", "--data", `{"query":"select 1"}`}, want: "not permitted"},
		{name: "delete cannot get", operation: "delete", arguments: []string{"get", "/v2/tasks/task-1"}, want: "not permitted"},
		{name: "reject arbitrary origin", operation: "read", arguments: []string{"get", "https://example.test/v2/objects"}, want: "documented /v2 path"},
		{name: "reject unknown route", operation: "read", arguments: []string{"get", "/v2/admin/secrets"}, want: "not in the reviewed"},
		{name: "reject binary download", operation: "read", arguments: []string{"get", "/v2/files/file-1/download"}, want: "not in the reviewed"},
		{name: "require JSON body", operation: "write", arguments: []string{"patch", "/v2/tasks/task-1"}, want: "requires --data"},
		{name: "reject nested query value", operation: "read", arguments: []string{"get", "/v2/objects", "--query", `{"filter":{"name":"private"}}`}, want: "unsupported key or value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("/bin/bash", append([]string{helper}, test.arguments...)...)
			command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "ATTIO_ACCESS_TOKEN=fixture-attio-token", "TOS_TAG_OPERATION_ID=" + test.operation}
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), test.want) {
				t.Fatalf("error=%v output=%s", err, output)
			}
		})
	}
}

func TestAttioReviewedHelperRedactsUnselectedErrorDetails(t *testing.T) {
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
printf '%s' '{"status_code":400,"type":"invalid_request_error","code":"validation_type","message":"Invalid request","details":"PRIVATE-PROVIDER-DETAIL"}' > "$output"
printf '400'
`
	if err := os.WriteFile(fakeCurl, []byte(fakeCurlScript), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join("..", "tool-marketplace", "tools", "attio", "run.sh")
	command := exec.Command("/bin/bash", helper, "get", "/v2/self")
	command.Env = []string{"PATH=" + temporary + ":/usr/bin:/bin", "HOME=" + temporary, "ATTIO_ACCESS_TOKEN=fixture-attio-token", "TOS_TAG_OPERATION_ID=read"}
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "validation_type") || bytes.Contains(output, []byte("PRIVATE-PROVIDER-DETAIL")) {
		t.Fatalf("error=%v output=%s", err, output)
	}
}
