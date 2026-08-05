package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/marketplace"
	"github.com/telemetryos/tos-tag/core/tools"
	"github.com/telemetryos/tos-tag/core/workers"
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

func TestLinearReviewedHelperAcceptsInlineWorkerText(t *testing.T) {
	temporary := t.TempDir()
	capture := filepath.Join(temporary, "requests.jsonl")
	inputPath := filepath.Join(temporary, "last-input.json")
	fakeCurl := filepath.Join(temporary, "curl")
	const fakeCurlScript = `#!/bin/bash
set -eu
payload=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -d) payload="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf '%s\n' "$payload" >> "$CAPTURE_PATH"
case "$payload" in
  *'commentCreate'*)
    printf '%s' '{"data":{"commentCreate":{"success":true,"comment":{"id":"comment-1"}}}}' ;;
  *'VerifyUpdatedIssue'*)
    input="$(cat "$INPUT_PATH")"
    jq -cn --argjson input "$input" '{data:{issue:{identifier:"ENG-1234",title:$input.title,description:($input.description | gsub("\r\n"; "\n") | gsub("\r"; "\n") | sub("\n+$"; "")),state:{id:"state-1"},labelIds:[]}}}' ;;
  *'VerifyCreatedIssue'*)
    input="$(cat "$INPUT_PATH")"
    jq -cn --argjson input "$input" '{data:{issue:{identifier:"ENG-4321",title:$input.title,description:($input.description | gsub("\r\n"; "\n") | gsub("\r"; "\n") | sub("\n+$"; "")),parent:null,state:{id:$input.stateId,name:"Triage"},labelIds:($input.labelIds // []),labels:{nodes:(if (($input.labelIds // []) | length) > 0 then [{name:"Feature"}] else [] end)}}}}' ;;
  *'issueUpdate'*)
    input="$(printf '%s' "$payload" | jq -c '.variables.input')"
    printf '%s' "$input" > "$INPUT_PATH"
    jq -cn --argjson input "$input" '{data:{issueUpdate:{success:true,issue:{identifier:"ENG-1234",title:$input.title,description:"stale mutation payload",assignee:null,priority:3,state:{id:"state-1",name:"Triage"},labelIds:[],labels:{nodes:[]},parent:null}}}}' ;;
  *'issueCreate'*)
    input="$(printf '%s' "$payload" | jq -c '.variables.input')"
    printf '%s' "$input" > "$INPUT_PATH"
    jq -cn --argjson input "$input" '{data:{issueCreate:{success:true,issue:{identifier:"ENG-4321",url:"https://linear.example/ENG-4321",title:$input.title,description:"stale mutation payload",parent:null,state:{id:$input.stateId,name:"Triage"},labelIds:($input.labelIds // []),labels:{nodes:(if (($input.labelIds // []) | length) > 0 then [{name:"Feature"}] else [] end)}}}}}' ;;
  *'issueLabels'*)
    printf '%s' '{"data":{"issueLabels":{"nodes":[{"id":"label-feature","name":"Feature","team":{"id":"65861c09-dd00-4ace-ab70-bda91f06f929"}}]}}}' ;;
  *'issue(id:'*)
    printf '%s' '{"data":{"issue":{"id":"issue-1","identifier":"ENG-1234","labelIds":[],"state":{"id":"state-1"},"team":{"id":"team-1"}}}}' ;;
  *)
    printf '%s' '{"errors":[{"message":"unexpected test query"}]}' ;;
esac
`
	if err := os.WriteFile(fakeCurl, []byte(fakeCurlScript), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join("..", "tool-marketplace", "tools", "linear", "run.sh")
	environment := []string{"PATH=" + temporary + ":/usr/bin:/bin", "HOME=" + temporary, "LINEAR_API_KEY=test-token", "TOS_TAG_OPERATION_ID=write", "CAPTURE_PATH=" + capture, "INPUT_PATH=" + inputPath}

	create := exec.Command("/bin/bash", helper, "create", "--title", "Inline title", "--description", "## TL;DR\r\n\r\nInline description\r\n")
	create.Env = environment
	if output, err := create.CombinedOutput(); err != nil || !strings.Contains(string(output), "ISSUE=ENG-4321") || !strings.Contains(string(output), "DESCRIPTION_APPLIED=1") {
		t.Fatalf("inline create failed: %v: %s", err, output)
	}

	update := exec.Command("/bin/bash", helper, "update", "--issue", "ENG-1234", "--title", "Updated title", "--description", "Updated description", "--comment", "Concise update")
	update.Env = environment
	if output, err := update.CombinedOutput(); err != nil || !strings.Contains(string(output), "TITLE_APPLIED=1") || !strings.Contains(string(output), "DESCRIPTION_APPLIED=1") || !strings.Contains(string(output), "COMMENT_ID=comment-1") {
		t.Fatalf("inline update failed: %v: %s", err, output)
	}

	intakeEnvironment := []string{"PATH=" + temporary + ":/usr/bin:/bin", "HOME=" + temporary, "LINEAR_API_KEY=test-token", "TOS_TAG_OPERATION_ID=intake", "CAPTURE_PATH=" + capture, "INPUT_PATH=" + inputPath}
	intakeCreate := exec.Command("/bin/bash", helper, "create", "--title", "Intake feature", "--description", "Complete feature body", "--priority", "3", "--label", "Feature")
	intakeCreate.Env = intakeEnvironment
	if output, err := intakeCreate.CombinedOutput(); err != nil || !strings.Contains(string(output), "ISSUE=ENG-4321") || !strings.Contains(string(output), "LABELS_APPLIED=1") {
		t.Fatalf("approval-free intake create failed: %v: %s", err, output)
	}
	intakeComment := exec.Command("/bin/bash", helper, "comment", "--issue", "ENG-1234", "--body", "New defect evidence")
	intakeComment.Env = intakeEnvironment
	if output, err := intakeComment.CombinedOutput(); err != nil || !strings.Contains(string(output), "COMMENT_ID=comment-1") {
		t.Fatalf("approval-free intake comment failed: %v: %s", err, output)
	}

	requests, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(requests, []byte("Inline description")) || !bytes.Contains(requests, []byte("Concise update")) {
		t.Fatalf("inline text was not encoded into reviewed requests: %s", requests)
	}
	if !bytes.Contains(requests, []byte("VerifyCreatedIssue")) || !bytes.Contains(requests, []byte("VerifyUpdatedIssue")) {
		t.Fatalf("content writes did not use fresh Linear read-back queries")
	}
}

func TestLinearReviewedHelperRejectsSubstantiveFreshDescriptionMismatchWithoutLeakingContent(t *testing.T) {
	temporary := t.TempDir()
	fakeCurl := filepath.Join(temporary, "curl")
	const fakeCurlScript = `#!/bin/bash
set -eu
payload=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -d) payload="$2"; shift 2 ;;
    *) shift ;;
  esac
done
case "$payload" in
  *'VerifyCreatedIssue'*)
    printf '%s' '{"data":{"issue":{"identifier":"ENG-9876","title":"Mismatch title","description":"substantively different","parent":null,"state":{"id":"c6ddabe1-23a7-4ade-a65a-d52fccc31af6","name":"Triage"},"labelIds":[],"labels":{"nodes":[]}}}}' ;;
  *'issueCreate'*)
    printf '%s' '{"data":{"issueCreate":{"success":true,"issue":{"identifier":"ENG-9876","url":"https://linear.example/ENG-9876","title":"Mismatch title","description":"stale mutation payload","parent":null,"state":{"id":"c6ddabe1-23a7-4ade-a65a-d52fccc31af6","name":"Triage"},"labelIds":[],"labels":{"nodes":[]}}}}}' ;;
  *)
    printf '%s' '{"errors":[{"message":"unexpected test query"}]}' ;;
esac
`
	if err := os.WriteFile(fakeCurl, []byte(fakeCurlScript), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join("..", "tool-marketplace", "tools", "linear", "run.sh")
	const privateDescription = "PRIVATE-MARKDOWN-CONTENT"
	command := exec.Command("/bin/bash", helper, "create", "--title", "Mismatch title", "--description", privateDescription)
	command.Env = []string{"PATH=" + temporary + ":/usr/bin:/bin", "HOME=" + temporary, "LINEAR_API_KEY=test-token", "TOS_TAG_OPERATION_ID=write"}
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "ISSUE=ENG-9876") || !strings.Contains(string(output), "DESCRIPTION_APPLIED=0") || !strings.Contains(string(output), "read-back mismatched description") {
		t.Fatalf("substantive mismatch was not rejected safely: %v: %s", err, output)
	}
	if bytes.Contains(output, []byte(privateDescription)) || bytes.Contains(output, []byte("substantively different")) {
		t.Fatalf("description content leaked in mismatch output: %s", output)
	}
}

func TestWikiReviewedHelperEveryPageReadIncludesCanonicalHumanURL(t *testing.T) {
	temporary := t.TempDir()
	fakeCurl := filepath.Join(temporary, "curl")
	const fakeCurlScript = `#!/bin/bash
set -eu
output=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf '%s' '{"id":"page-opaque-123","namespace":"primer","slug":"node-mini","title":"Node Mini","body_html":"<p>Full page body</p>"}' > "$output"
printf '200'
`
	if err := os.WriteFile(fakeCurl, []byte(fakeCurlScript), 0o700); err != nil {
		t.Fatal(err)
	}

	// Deliberately omit --json: reviewed gateway reads must still return the
	// full envelope so a worker cannot lose canonical-link provenance.
	command := exec.Command("/bin/bash", filepath.Join("..", "tool-marketplace", "tools", "wiki", "run.sh"), "get", "primer/node-mini")
	command.Env = []string{"PATH=" + temporary + ":/usr/bin:/bin", "HOME=" + temporary, "WIKI_URL=https://wiki.example", "WIKI_TOKEN=test-token", "TOS_TAG_OPERATION_ID=read"}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("wiki full read failed: %v: %s", err, output)
	}
	var page map[string]any
	if err := json.Unmarshal(output, &page); err != nil {
		t.Fatalf("wiki full read returned invalid JSON: %v: %s", err, output)
	}
	if page["body_html"] != "<p>Full page body</p>" || page["url"] != "https://wiki.example/pages/page-opaque-123" {
		t.Fatalf("wiki full read = %#v", page)
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

func TestProductDocsReviewedHelperUsesOnlyFixedTelemetryOSSources(t *testing.T) {
	canonicalPath := filepath.Clean(filepath.Join("..", "..", "tag-agent-skills", "src", "skills", "product-knowledge", "scripts", "read-product-source.sh"))
	reviewedPath := filepath.Join("..", "tool-marketplace", "tools", "product-docs", "run.sh")
	if canonical, err := os.ReadFile(canonicalPath); err == nil {
		reviewed, readErr := os.ReadFile(reviewedPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(canonical, reviewed) {
			t.Fatal("reviewed product-docs helper drifted from tag-agent-skills canonical source")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}

	temporary := t.TempDir()
	capture := filepath.Join(temporary, "curl-argv.txt")
	fakeCurl := filepath.Join(temporary, "curl")
	const fakeCurlScript = `#!/bin/bash
set -eu
printf '%s\n' "$@" > "$CAPTURE_PATH"
printf '# fetched product source\n'
`
	if err := os.WriteFile(fakeCurl, []byte(fakeCurlScript), 0o700); err != nil {
		t.Fatal(err)
	}

	for name, testCase := range map[string]struct {
		arguments []string
		wantURL   string
	}{
		"docs index":     {arguments: []string{"docs-index"}, wantURL: "https://docs.telemetryos.com/llms.txt"},
		"docs page":      {arguments: []string{"docs-page", "https://docs.telemetryos.com/docs/node-pro.md"}, wantURL: "https://docs.telemetryos.com/docs/node-pro.md"},
		"corporate full": {arguments: []string{"corporate-full"}, wantURL: "https://www.telemetryos.com/llms-full.txt"},
	} {
		t.Run(name, func(t *testing.T) {
			command := exec.Command("/bin/bash", append([]string{reviewedPath}, testCase.arguments...)...)
			command.Env = []string{"PATH=" + temporary + ":/usr/bin:/bin", "HOME=" + temporary, "TOS_TAG_OPERATION_ID=read", "CAPTURE_PATH=" + capture}
			output, err := command.CombinedOutput()
			if err != nil || !strings.Contains(string(output), "fetched product source") {
				t.Fatalf("product source helper failed: %v: %s", err, output)
			}
			argv, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(argv), testCase.wantURL) || strings.Contains(string(argv), "--location") || strings.Contains(string(argv), "-L") {
				t.Fatalf("unexpected curl arguments: %s", argv)
			}
		})
	}

	for name, arguments := range map[string][]string{
		"foreign host": {"docs-page", "https://example.com/docs/node-pro.md"},
		"traversal":    {"docs-page", "docs/../runtime.env"},
		"query":        {"docs-page", "docs/node-pro.md?redirect=https://example.com"},
	} {
		t.Run("reject "+name, func(t *testing.T) {
			command := exec.Command("/bin/bash", append([]string{reviewedPath}, arguments...)...)
			command.Env = []string{"PATH=" + temporary + ":/usr/bin:/bin", "HOME=" + temporary, "TOS_TAG_OPERATION_ID=read", "CAPTURE_PATH=" + capture}
			if output, err := command.CombinedOutput(); err == nil {
				t.Fatalf("unsafe input succeeded: %s", output)
			}
		})
	}
}

func TestAnalyticsReviewedHelperUsesBoundedGETAndRedactsDirectIdentifiers(t *testing.T) {
	temporary := t.TempDir()
	capture := filepath.Join(temporary, "curl-argv.txt")
	fakeCurl := filepath.Join(temporary, "curl")
	const fakeCurlScript = `#!/bin/bash
set -eu
output=""
headers=""
url=""
printf '%s\n' "$@" > "$CAPTURE_PATH"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) output="$2"; shift 2 ;;
    --dump-header) headers="$2"; shift 2 ;;
    --url) url="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [[ "$url" == *"/analytics/site"* ]]; then
  printf 'HTTP/1.1 200 OK\r\nTotal-Records-Count: 23\r\n\r\n' > "$headers"
  printf '%s' '[{"type":"pageview","ip":"192.0.2.1","visitor_token":"visitor-secret","session_id":"session-secret","event_id":"event-secret","url":"https://www.telemetryos.com/pricing?email=person@example.com","referrer":"https://example.com/?secret=yes","props":{"customer_text":"private"},"ua":{"raw":"private-user-agent"},"path":"/pricing"}]' > "$output"
else
  printf '%s' '{"account":{"account_id":"0123456789abcdef01234567","email":"person@example.com","internal":false,"stage":"activated","demo_code":"secret-code","touches":[{"source":"google","token":"visitor-secret"}]},"events":[{"type":"pageview","ip":"192.0.2.1","visitor_token":"visitor-secret","session_id":"session-secret","event_id":"event-secret","url":"https://www.telemetryos.com/pricing?email=person@example.com","referrer":"https://example.com/?secret=yes","props":{"customer_text":"private"},"ua":{"raw":"private-user-agent"},"path":"/pricing"}],"self_reported_attribution":[{"label":"free-form customer answer","accounts":1}]}' > "$output"
fi
printf '200'
`
	if err := os.WriteFile(fakeCurl, []byte(fakeCurlScript), 0o700); err != nil {
		t.Fatal(err)
	}

	helper := filepath.Join("..", "tool-marketplace", "tools", "analytics", "run.sh")
	command := exec.Command("/bin/bash", helper, "account", "0123456789abcdef01234567")
	command.Env = []string{
		"PATH=" + temporary + ":/usr/bin:/bin",
		"HOME=" + temporary,
		"TMPDIR=" + temporary,
		"TOS_TAG_OPERATION_ID=read",
		"TELEMETRYOS_ANALYTICS_URL=https://qa-api.telemetryos.com",
		"SITE_ANALYTICS_TOKEN=s0123456789abcdef0123456789abcdef",
		"CAPTURE_PATH=" + capture,
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("analytics helper failed: %v: %s", err, output)
	}
	for _, forbidden := range []string{"person@example.com", "192.0.2.1", "visitor-secret", "session-secret", "event-secret", "secret-code", "customer_text", "private-user-agent", "free-form customer answer", "?email=", "?secret="} {
		if bytes.Contains(output, []byte(forbidden)) {
			t.Fatalf("analytics output leaked %q: %s", forbidden, output)
		}
	}
	for _, expected := range []string{"0123456789abcdef01234567", `"stage": "activated"`, `"path": "/pricing"`, "https://www.telemetryos.com/pricing"} {
		if !bytes.Contains(output, []byte(expected)) {
			t.Fatalf("analytics output is missing %q: %s", expected, output)
		}
	}
	argv, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(argv, []byte("s0123456789abcdef0123456789abcdef")) || !bytes.Contains(argv, []byte("https://qa-api.telemetryos.com/reporting/funnel/accounts/0123456789abcdef01234567")) || !bytes.Contains(argv, []byte("--request\nGET")) {
		t.Fatalf("unexpected analytics curl arguments: %s", argv)
	}

	command = exec.Command("/bin/bash", helper, "site-events", "--from", "2026-07-01T00:00:00Z", "--to", "2026-07-08T00:00:00Z", "--type", "pageview,signup_started", "--path", "/pricing", "--exclude-bots", "true", "--page", "2", "--per-page", "25")
	command.Env = []string{
		"PATH=" + temporary + ":/usr/bin:/bin",
		"HOME=" + temporary,
		"TMPDIR=" + temporary,
		"TOS_TAG_OPERATION_ID=read",
		"TELEMETRYOS_ANALYTICS_URL=https://qa-api.telemetryos.com",
		"SITE_ANALYTICS_TOKEN=s0123456789abcdef0123456789abcdef",
		"CAPTURE_PATH=" + capture,
	}
	rawOutput, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("raw site-events helper failed: %v: %s", err, rawOutput)
	}
	for _, forbidden := range []string{"person@example.com", "192.0.2.1", "visitor-secret", "session-secret", "event-secret", "customer_text", "private-user-agent", "?email=", "?secret="} {
		if bytes.Contains(rawOutput, []byte(forbidden)) {
			t.Fatalf("raw site-events output leaked %q: %s", forbidden, rawOutput)
		}
	}
	for _, expected := range []string{`"total": 23`, `"page": 2`, `"per_page": 25`, `"type": "pageview"`, `"path": "/pricing"`} {
		if !bytes.Contains(rawOutput, []byte(expected)) {
			t.Fatalf("raw site-events output is missing %q: %s", expected, rawOutput)
		}
	}
	argv, err = os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"https://qa-api.telemetryos.com/analytics/site?", "from=2026-07-01T00%3A00%3A00Z", "type=pageview%2Csignup_started", "path=%2Fpricing", "exclude_bots=true", "page=2", "per_page=25", "--dump-header"} {
		if !bytes.Contains(argv, []byte(expected)) {
			t.Fatalf("raw site-events curl arguments are missing %q: %s", expected, argv)
		}
	}
	if bytes.Contains(argv, []byte("perPage=")) || bytes.Contains(argv, []byte("s0123456789abcdef0123456789abcdef")) {
		t.Fatalf("raw site-events curl arguments crossed the reviewed boundary: %s", argv)
	}

	for name, args := range map[string][]string{
		"visitor lookup":          {"events", "--visitor", "visitor-secret"},
		"raw visitor lookup":      {"site-events", "--token", "visitor-secret"},
		"raw session lookup":      {"site-events", "--session-id", "session-secret"},
		"raw unsafe path":         {"site-events", "--path", "/pricing?email=person@example.com"},
		"raw missing bot scope":   {"site-events", "--from", "2026-07-01T00:00:00Z"},
		"conflicting bot filters": {"site-events", "--exclude-bots", "true", "--bots-only", "true"},
		"internal events":         {"events", "--include-internal", "true"},
		"invalid account":         {"account", "../secret"},
	} {
		t.Run("reject "+name, func(t *testing.T) {
			command := exec.Command("/bin/bash", append([]string{helper}, args...)...)
			command.Env = []string{
				"PATH=" + temporary + ":/usr/bin:/bin",
				"HOME=" + temporary,
				"TMPDIR=" + temporary,
				"TOS_TAG_OPERATION_ID=read",
				"TELEMETRYOS_ANALYTICS_URL=https://qa-api.telemetryos.com",
				"SITE_ANALYTICS_TOKEN=s0123456789abcdef0123456789abcdef",
				"CAPTURE_PATH=" + capture,
			}
			if rejectedOutput, rejectedErr := command.CombinedOutput(); rejectedErr == nil {
				t.Fatalf("unsafe Analytics input succeeded: %s", rejectedOutput)
			}
		})
	}
}

func TestCodeReviewedHelperRefreshesDefaultSnapshotAndBoundsSemanticSearch(t *testing.T) {
	temporary := t.TempDir()
	remote := filepath.Join(temporary, "remote.git")
	seed := filepath.Join(temporary, "seed")
	sourceRoot := filepath.Join(temporary, "source")
	snapshotRoot := filepath.Join(temporary, "snapshots")
	indexRoot := filepath.Join(temporary, "indexes")
	modelRoot := filepath.Join(temporary, "model")
	githubConfig := filepath.Join(temporary, "gh")
	binRoot := filepath.Join(temporary, "bin")
	for _, directory := range []string{sourceRoot, snapshotRoot, indexRoot, modelRoot, githubConfig, binRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(modelRoot, ".tos-tag-model-revision"), []byte("e9d2a44ca6a05ac6685f3b23709ea57eb7352d5b\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	runGit := func(directory string, arguments ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	if output, err := exec.Command("git", "init", "--bare", "--initial-branch=main", remote).CombinedOutput(); err != nil {
		t.Fatalf("init remote: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "init", "--initial-branch=main", seed).CombinedOutput(); err != nil {
		t.Fatalf("init seed: %v: %s", err, output)
	}
	if err := os.MkdirAll(filepath.Join(seed, "core"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "core", "auth.go"), []byte("package core\n\nfunc Authorize() bool { return true }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, ".env"), []byte("TOKEN=must-not-be-readable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(seed, "add", "core/auth.go", ".env")
	runGit(seed, "commit", "-m", "initial")
	runGit(seed, "remote", "add", "origin", "file://"+remote)
	runGit(seed, "push", "-u", "origin", "main")
	if output, err := exec.Command("git", "clone", "--quiet", "file://"+remote, filepath.Join(sourceRoot, "Demo")).CombinedOutput(); err != nil {
		t.Fatalf("clone source: %v: %s", err, output)
	}
	runGit(filepath.Join(sourceRoot, "Demo"), "remote", "set-url", "origin", "https://github.com/telemetryOS/Demo.git")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	fakeGit := filepath.Join(binRoot, "git")
	const fakeGitScript = `#!/usr/bin/env bash
set -euo pipefail
arguments=("$@")
for index in "${!arguments[@]}"; do
  if [[ "${arguments[index]}" == "https://github.com/telemetryOS/Demo.git" ]]; then
    arguments[index]="file://${TEST_REMOTE_PATH}"
  fi
done
exec "${REAL_GIT_BIN}" "${arguments[@]}"
`
	if err := os.WriteFile(fakeGit, []byte(fakeGitScript), 0o700); err != nil {
		t.Fatal(err)
	}

	capture := filepath.Join(temporary, "semble-arguments")
	fakeSemble := filepath.Join(binRoot, "semble")
	const fakeSembleScript = `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "--version" ]]; then
  printf '0.5.3\n'
  exit 0
fi
printf '%s\n' "$@" >"${CAPTURE_PATH}"
printf '%s\n' '{"query":"authorization path","results":[{"file_path":"core/auth.go","start_line":3,"end_line":3,"score":0.91,"content":"func Authorize() bool { return true }"}]}'
`
	if err := os.WriteFile(fakeSemble, []byte(fakeSembleScript), 0o700); err != nil {
		t.Fatal(err)
	}

	helper := filepath.Join("..", "tool-marketplace", "tools", "code", "run.sh")
	environment := []string{
		"PATH=" + binRoot + ":" + os.Getenv("PATH"),
		"HOME=" + temporary,
		"TOS_TAG_OPERATION_ID=read",
		"TAG_AION_DEVELOPER_PATH=" + sourceRoot,
		"TAG_CODE_SNAPSHOT_ROOT=" + snapshotRoot,
		"TAG_CODE_INDEX_ROOT=" + indexRoot,
		"TAG_CODE_MODEL_PATH=" + modelRoot,
		"TAG_CODE_GH_CONFIG_DIR=" + githubConfig,
		"REAL_GIT_BIN=" + realGit,
		"TEST_REMOTE_PATH=" + remote,
		"CAPTURE_PATH=" + capture,
	}
	runHelper := func(arguments ...string) ([]byte, error) {
		t.Helper()
		command := exec.Command("/bin/bash", append([]string{helper}, arguments...)...)
		command.Env = environment
		return command.CombinedOutput()
	}

	freshnessOutput, err := runHelper("freshness", "Demo")
	if err != nil {
		t.Fatalf("freshness failed: %v: %s", err, freshnessOutput)
	}
	var freshness map[string]any
	if err := json.Unmarshal(freshnessOutput, &freshness); err != nil {
		t.Fatalf("freshness JSON: %v: %s", err, freshnessOutput)
	}
	firstCommit, _ := freshness["commit"].(string)
	if freshness["repository"] != "Demo" || freshness["default_branch"] != "main" || freshness["status"] != "current" || len(firstCommit) != 40 {
		t.Fatalf("unexpected freshness: %#v", freshness)
	}
	if _, err := os.Stat(filepath.Join(snapshotRoot, "repos", "Demo", firstCommit, "core", "auth.go")); err != nil {
		t.Fatalf("default snapshot missing: %v", err)
	}
	receiptPath := filepath.Join(snapshotRoot, "receipts", "Demo.json")
	receiptBytes, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(receiptBytes, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt["fetched_at"] = ""
	invalidReceipt, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptPath, invalidReceipt, 0o600); err != nil {
		t.Fatal(err)
	}
	repairedOutput, err := runHelper("freshness", "Demo")
	if err != nil {
		t.Fatalf("invalid receipt was not refreshed: %v: %s", err, repairedOutput)
	}
	var repaired map[string]any
	if err := json.Unmarshal(repairedOutput, &repaired); err != nil {
		t.Fatal(err)
	}
	if repaired["fetched_at"] == "" {
		t.Fatalf("invalid fetched_at was accepted: %s", repairedOutput)
	}

	semanticOutput, err := runHelper("semantic-search", "Demo", "where is authorization enforced?", "3", "8")
	if err != nil {
		t.Fatalf("semantic search failed: %v: %s", err, semanticOutput)
	}
	if !bytes.Contains(semanticOutput, []byte(`"file_path": "Demo/core/auth.go"`)) || !bytes.Contains(semanticOutput, []byte(`"commit": "`+firstCommit+`"`)) {
		t.Fatalf("semantic output lacks bounded provenance: %s", semanticOutput)
	}
	semanticArguments, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"search", "where is authorization enforced?", "--top-k", "3", "--max-snippet-lines", "8", "--content", "all"} {
		if !bytes.Contains(semanticArguments, []byte(expected)) {
			t.Fatalf("Semble invocation is missing %q: %s", expected, semanticArguments)
		}
	}

	if err := os.WriteFile(filepath.Join(seed, "core", "auth.go"), []byte("package core\n\nfunc Authorize() bool { return false }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(seed, "add", "core/auth.go")
	runGit(seed, "commit", "-m", "change default")
	runGit(seed, "push", "origin", "main")
	if err := os.Remove(filepath.Join(snapshotRoot, "receipts", "Demo.json")); err != nil {
		t.Fatal(err)
	}
	refreshedOutput, err := runHelper("freshness", "Demo")
	if err != nil {
		t.Fatalf("refreshed freshness failed: %v: %s", err, refreshedOutput)
	}
	var refreshed map[string]any
	if err := json.Unmarshal(refreshedOutput, &refreshed); err != nil {
		t.Fatal(err)
	}
	if refreshed["commit"] == firstCommit {
		t.Fatalf("default branch did not refresh: %s", refreshedOutput)
	}
	readOutput, err := runHelper("read", "Demo/core/auth.go", "1", "5")
	if err != nil || !bytes.Contains(readOutput, []byte("return false")) {
		t.Fatalf("exact read did not use refreshed snapshot: %v: %s", err, readOutput)
	}
	if restrictedOutput, restrictedErr := runHelper("read", "Demo/.env", "1", "5"); restrictedErr == nil || bytes.Contains(restrictedOutput, []byte("must-not-be-readable")) {
		t.Fatalf("restricted default-branch file escaped: %v: %s", restrictedErr, restrictedOutput)
	}
	if unsafeOutput, unsafeErr := runHelper("semantic-search", "../Demo", "authorization", "3", "8"); unsafeErr == nil {
		t.Fatalf("unsafe semantic repository succeeded: %s", unsafeOutput)
	}
}

func TestCheckedInReviewedToolMarketplace(t *testing.T) {
	registry, err := tools.LoadMarketplace(filepath.Join("..", "tool-marketplace"), "catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"telemetryos.linear": true, "telemetryos.wiki": true, "telemetryos.otel": true, "telemetryos.analytics": true, "telemetryos.device-logs": true, "telemetryos.mongo": true, "telemetryos.code": true, "telemetryos.product-docs": true}
	for _, snapshot := range registry.List() {
		delete(want, snapshot.ToolID)
		if snapshot.ContentHash == "" || len(snapshot.Operations) == 0 {
			t.Fatalf("incomplete tool snapshot: %#v", snapshot)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing reviewed tools: %#v", want)
	}
	code, ok := registry.Resolve("telemetryos.code")
	if !ok || len(code.Manifest.Operations) != 1 || code.Manifest.Operations[0].ID != "read" || code.Manifest.Operations[0].Risk != "read" {
		t.Fatalf("source capability is not permanently read-only: %#v", code.Manifest)
	}
	codeRead := code.Manifest.Operations[0]
	if codeRead.TimeoutSeconds != 120 || codeRead.MaxOutputBytes != 1048576 || !reflect.DeepEqual(codeRead.Env, []string{"TAG_AION_DEVELOPER_PATH", "TAG_CODE_SNAPSHOT_ROOT", "TAG_CODE_INDEX_ROOT", "TAG_CODE_MODEL_PATH", "TAG_CODE_GH_CONFIG_DIR"}) {
		t.Fatalf("source refresh/semantic boundary is invalid: %#v", codeRead)
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
	linear, ok := registry.Resolve("telemetryos.linear")
	if !ok {
		t.Fatal("Linear tool was not resolved")
	}
	linearOperations := make(map[string]tools.Operation, len(linear.Manifest.Operations))
	for _, operation := range linear.Manifest.Operations {
		linearOperations[operation.ID] = operation
	}
	if len(linearOperations) != 3 || linearOperations["read"].Risk != "read" || linearOperations["intake"].Risk != "write" || linearOperations["write"].Risk != "write" {
		t.Fatalf("Linear operations are invalid: %#v", linearOperations)
	}
	if linearOperations["read"].RequiresApproval() || linearOperations["intake"].RequiresApproval() || !linearOperations["write"].RequiresApproval() {
		t.Fatalf("Linear approval boundary is invalid: %#v", linearOperations)
	}
	productDocs, ok := registry.Resolve("telemetryos.product-docs")
	if !ok || len(productDocs.Manifest.Operations) != 1 {
		t.Fatalf("product docs tool was not resolved safely: %#v", productDocs.Manifest)
	}
	productRead := productDocs.Manifest.Operations[0]
	if productRead.ID != "read" || productRead.Risk != "read" || productRead.RequiresApproval() || len(productRead.Env) != 0 || productRead.TimeoutSeconds != 30 || productRead.MaxOutputBytes != 524288 {
		t.Fatalf("product docs read boundary is invalid: %#v", productRead)
	}
	analytics, ok := registry.Resolve("telemetryos.analytics")
	if !ok || len(analytics.Manifest.Operations) != 1 {
		t.Fatalf("analytics tool was not resolved safely: %#v", analytics.Manifest)
	}
	analyticsRead := analytics.Manifest.Operations[0]
	if analyticsRead.ID != "read" || analyticsRead.Risk != "read" || analyticsRead.RequiresApproval() || analyticsRead.TimeoutSeconds != 90 || analyticsRead.MaxOutputBytes != 1048576 || !reflect.DeepEqual(analyticsRead.Env, []string{"TELEMETRYOS_ANALYTICS_URL", "SITE_ANALYTICS_TOKEN"}) || !reflect.DeepEqual(analyticsRead.PublicEnv, []string{"TELEMETRYOS_ANALYTICS_URL"}) {
		t.Fatalf("analytics read boundary is invalid: %#v", analyticsRead)
	}
}

func TestConfiguredBasePluginWhenAvailable(t *testing.T) {
	baseRoot := filepath.Clean(filepath.Join("..", "..", "tag-agent-skills"))
	if _, err := os.Stat(baseRoot); errors.Is(err, os.ErrNotExist) {
		t.Skipf("checkout not present: %s", baseRoot)
	}
	expectedNames := []string{"bug", "code-change-intake", "codebase-read", "digitalocean", "feature", "linear-issue-manager", "marketing-account-journey", "marketing-funnel-chain", "marketing-funnel-review", "marketing-messaging", "marketing-unstall-draft", "pandadoc", "product-knowledge", "slack-message-design", "suitability", "tag-triggers", "team-alignment", "telemetry-otel-fetch", "telemetryos-documentation", "wiki"}
	base, err := marketplace.LoadPlugin(baseRoot, filepath.Join(".claude-plugin", "marketplace.json"), "base")
	if err != nil || len(base) < len(expectedNames) {
		t.Fatalf("base skills=%d err=%v", len(base), err)
	}
	baseNames := map[string]bool{}
	for _, snapshot := range base {
		baseNames[snapshot.Name] = true
	}
	for _, expected := range expectedNames {
		if !baseNames[expected] {
			t.Fatalf("base plugin missing %s: %#v", expected, baseNames)
		}
	}
	for _, snapshot := range base {
		if strings.Contains(snapshot.Name, "/") {
			t.Fatalf("Codex skill name is not flat: %s", snapshot.Name)
		}
		for _, file := range snapshot.Files {
			if filepath.Ext(file) == ".sh" {
				t.Fatalf("executable leaked into behavioral snapshot: %s", file)
			}
		}
	}
	manager, err := workers.NewLocal(t.TempDir(), "/usr/bin:/bin")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := manager.Provision(context.Background(), workers.Spec{JobID: "base-discovery", AttemptID: "attempt-1", Command: []string{"/bin/sh", "-c", "sleep 30"}, Skills: base, WallTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Terminate(context.Background(), workspace)
	entries, err := os.ReadDir(workspace.SkillsDir)
	if err != nil {
		t.Fatal(err)
	}
	discovered := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			discovered[entry.Name()] = true
		}
	}
	if len(discovered) != len(baseNames) {
		t.Fatalf("fresh worker discovered %d skills, want %d: %#v", len(discovered), len(baseNames), discovered)
	}
	for name := range baseNames {
		if !discovered[name] {
			t.Fatalf("fresh worker did not discover %s: %#v", name, discovered)
		}
	}
}
