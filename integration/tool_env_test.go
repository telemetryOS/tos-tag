package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestToolEnvironmentSyncIsNarrowAndDoesNotPrintValues(t *testing.T) {
	runtimeFile := filepath.Join(t.TempDir(), "runtime.env")
	codeRoot := t.TempDir()
	semanticBin := filepath.Join(t.TempDir(), "semble")
	modelRoot := t.TempDir()
	githubConfig := t.TempDir()
	snapshotRoot := t.TempDir()
	indexRoot := t.TempDir()
	if err := os.WriteFile(semanticBin, []byte("#!/bin/sh\nprintf '0.5.3\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelRoot, ".tos-tag-model-revision"), []byte("e9d2a44ca6a05ac6685f3b23709ea57eb7352d5b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFile, []byte("TAG__KEYSTORE__MASTER_KEY='test-master-key-placeholder'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]string{
		"LINEAR_API_KEY":          "linear-fixture-secret",
		"WIKI_URL":                "https://wiki.example.test",
		"WIKI_TOKEN":              "wiki-fixture-secret",
		"SIGNOZ_URL":              "https://signoz.example.test",
		"SIGNOZ_API_KEY":          "signoz-fixture-secret",
		"DLA_API_BASE_URL":        "https://logs.example.test",
		"DLA_API_KEY":             "dla-fixture-secret",
		"DLA_ENV":                 "qa",
		"TAG_AION_DEVELOPER_PATH": codeRoot,
		"TAG_CODE_SEMBLE_BIN":     semanticBin,
		"TAG_CODE_SNAPSHOT_ROOT":  snapshotRoot,
		"TAG_CODE_INDEX_ROOT":     indexRoot,
		"TAG_CODE_MODEL_PATH":     modelRoot,
		"TAG_CODE_GH_CONFIG_DIR":  githubConfig,
	}
	command := exec.Command("bash", filepath.Join("..", "scripts", "sync-tool-env.sh"), runtimeFile)
	command.Env = filteredEnvironment(fixtures)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("sync tool environment: %v: %s", err, output)
	}
	for _, value := range fixtures {
		if strings.Contains(string(output), value) {
			t.Fatalf("sync output exposed a fixture value: %s", output)
		}
	}
	contents, err := os.ReadFile(runtimeFile)
	if err != nil {
		t.Fatal(err)
	}
	for name := range fixtures {
		if name == "TAG_CODE_SEMBLE_BIN" {
			continue
		}
		if !strings.Contains(string(contents), name+"=") {
			t.Fatalf("runtime file missing %s", name)
		}
	}
	for _, expected := range []string{
		"TAG__MARKETPLACES__INJECTED_TOOLS=",
		"TAG__MARKETPLACES__TOOLS_ENABLED='true'",
		"TAG__KEYSTORE__ENABLED='true'",
		"TAG_AION_DEVELOPER_PATH=",
		"TAG_CODE_SNAPSHOT_ROOT=",
		"TAG_CODE_INDEX_ROOT=",
		"TAG_CODE_MODEL_PATH=",
		"TAG_CODE_GH_CONFIG_DIR=",
	} {
		if !strings.Contains(string(contents), expected) {
			t.Fatalf("runtime file missing %s", expected)
		}
	}
	info, err := os.Stat(runtimeFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("runtime file mode=%o", info.Mode().Perm())
	}
}

func TestReviewedCodeToolReadsSourceAndRejectsSensitivePaths(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "Repo")
	if err := os.MkdirAll(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "main.go"), []byte("package main\n\nconst needle = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".env"), []byte("SECRET=never-read\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "private.go"), []byte("package private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repository, "escape")); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "init", "--quiet", repository).CombinedOutput(); err != nil {
		t.Fatalf("init source repository: %v: %s", err, output)
	}
	snapshotRoot := t.TempDir()
	indexRoot := t.TempDir()
	modelRoot := t.TempDir()
	githubConfig := t.TempDir()
	fakeBin := t.TempDir()
	commit := strings.Repeat("a", 40)
	snapshotRepository := filepath.Join(snapshotRoot, "repos", "Repo", commit)
	if err := os.MkdirAll(snapshotRepository, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{"main.go": "package main\n\nconst needle = true\n", ".env": "SECRET=never-read\n"} {
		if err := os.WriteFile(filepath.Join(snapshotRepository, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(snapshotRepository, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(snapshotRoot, "receipts"), 0o700); err != nil {
		t.Fatal(err)
	}
	receipt := fmt.Sprintf(`{"repository":"Repo","default_branch":"main","commit":"%s","fetched_at":"%s","fetched_at_epoch":%d,"status":"current"}`+"\n", commit, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Unix())
	if err := os.WriteFile(filepath.Join(snapshotRoot, "receipts", "Repo.json"), []byte(receipt), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelRoot, ".tos-tag-model-revision"), []byte("e9d2a44ca6a05ac6685f3b23709ea57eb7352d5b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "semble"), []byte("#!/bin/sh\nprintf '0.5.3\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "tool-marketplace", "tools", "code", "run.sh")
	run := func(arguments ...string) ([]byte, error) {
		command := exec.Command(path, arguments...)
		command.Env = filteredEnvironment(map[string]string{
			"TOS_TAG_OPERATION_ID":    "read",
			"TAG_AION_DEVELOPER_PATH": root,
			"TAG_CODE_SNAPSHOT_ROOT":  snapshotRoot,
			"TAG_CODE_INDEX_ROOT":     indexRoot,
			"TAG_CODE_MODEL_PATH":     modelRoot,
			"TAG_CODE_GH_CONFIG_DIR":  githubConfig,
			"PATH":                    fakeBin + ":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin",
		})
		return command.CombinedOutput()
	}
	for _, arguments := range [][]string{
		{"search", "needle", "Repo", "20"},
		{"read", "Repo/main.go", "1", "10"},
		{"files", "Repo", "20"},
	} {
		output, err := run(arguments...)
		if err != nil || !strings.Contains(string(output), "main.go") && arguments[0] != "read" {
			t.Fatalf("arguments=%v error=%v output=%s", arguments, err, output)
		}
	}
	for _, arguments := range [][]string{
		{"read", "../runtime.env"},
		{"read", "Repo/.env"},
		{"read", "Repo/escape/private.go"},
	} {
		output, err := run(arguments...)
		if err == nil || !strings.Contains(string(output), "path") && !strings.Contains(string(output), "restricted") {
			t.Fatalf("arguments=%v error=%v output=%s", arguments, err, output)
		}
	}
	output, err := run("files", "Repo", "20")
	if err != nil || strings.Contains(string(output), ".env") || strings.Contains(string(output), "escape") {
		t.Fatalf("file listing crossed a restricted boundary: error=%v output=%s", err, output)
	}
}

func filteredEnvironment(values map[string]string) []string {
	blocked := make(map[string]bool, len(values))
	for name := range values {
		blocked[name] = true
	}
	result := make([]string, 0, len(os.Environ())+len(values))
	for _, item := range os.Environ() {
		name := strings.SplitN(item, "=", 2)[0]
		if !blocked[name] {
			result = append(result, item)
		}
	}
	for name, value := range values {
		result = append(result, name+"="+value)
	}
	return result
}

func TestReviewedToolWrappersRejectBoundaryOverrides(t *testing.T) {
	fakeBin := t.TempDir()
	for _, name := range []string{"dla", "otel-fetch", "mongo-fetch", "rg"} {
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "semble"), []byte("#!/bin/sh\nif [ \"${1:-}\" = --version ]; then printf '0.5.3\\n'; fi\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		tool      string
		operation string
		args      []string
		want      string
	}{
		{name: "linear read cannot write", tool: "linear", operation: "read", args: []string{"create"}, want: "not permitted"},
		{name: "wiki read cannot write", tool: "wiki", operation: "read", args: []string{"put"}, want: "not permitted"},
		{name: "wiki cannot inspect namespaces", tool: "wiki", operation: "read", args: []string{"ns", "ls"}, want: "not permitted"},
		{name: "wiki cannot inspect activity", tool: "wiki", operation: "read", args: []string{"activity"}, want: "not permitted"},
		{name: "wiki cannot upload assets", tool: "wiki", operation: "write", args: []string{"asset", "put", "image.png"}, want: "not permitted"},
		{name: "wiki cannot publish files", tool: "wiki", operation: "write", args: []string{"publish", "artifacts/page", "page.md"}, want: "not permitted"},
		{name: "wiki cannot cascade moves", tool: "wiki", operation: "write", args: []string{"mv", "artifacts/page", "renamed"}, want: "not permitted"},
		{name: "wiki put cannot read files", tool: "wiki", operation: "write", args: []string{"put", "artifacts/page", "--title", "Page", "/etc/passwd"}, want: "file input is unavailable"},
		{name: "wiki append cannot read files", tool: "wiki", operation: "write", args: []string{"append", "artifacts/page", "/etc/passwd"}, want: "file input is unavailable"},
		{name: "wiki cannot undo arbitrary activity", tool: "wiki", operation: "delete", args: []string{"undo", "activity-id"}, want: "only recoverable page soft-delete"},
		{name: "wiki rejects legacy destructive capability", tool: "wiki", operation: "destructive", args: []string{"rm", "artifacts/page"}, want: "only page CRUD"},
		{name: "wiki cannot request admin", tool: "wiki", operation: "admin", args: []string{"ns", "set", "artifacts"}, want: "only page CRUD"},
		{name: "otel cannot override credential", tool: "otel", operation: "read", args: []string{"--api-key=chosen-by-model"}, want: "overrides are not permitted"},
		{name: "dla cannot override environment", tool: "device-logs", operation: "read", args: []string{"--env", "prod", "config"}, want: "overrides are not permitted"},
		{name: "mongo cannot open session", tool: "mongo", operation: "read", args: []string{"connect"}, want: "not available"},
		{name: "code cannot traverse", tool: "code", operation: "read", args: []string{"read", "../runtime.env"}, want: "traversal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshotRoot := t.TempDir()
			indexRoot := t.TempDir()
			modelRoot := t.TempDir()
			githubConfig := t.TempDir()
			if err := os.WriteFile(filepath.Join(modelRoot, ".tos-tag-model-revision"), []byte("e9d2a44ca6a05ac6685f3b23709ea57eb7352d5b\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join("..", "tool-marketplace", "tools", tc.tool, "run.sh")
			command := exec.Command(path, tc.args...)
			command.Env = filteredEnvironment(map[string]string{
				"TOS_TAG_OPERATION_ID":    tc.operation,
				"WIKI_URL":                "https://wiki.example.test",
				"WIKI_TOKEN":              "wiki-fixture-secret",
				"TAG_AION_DEVELOPER_PATH": t.TempDir(),
				"TAG_CODE_SNAPSHOT_ROOT":  snapshotRoot,
				"TAG_CODE_INDEX_ROOT":     indexRoot,
				"TAG_CODE_MODEL_PATH":     modelRoot,
				"TAG_CODE_GH_CONFIG_DIR":  githubConfig,
				"PATH":                    fakeBin + ":/usr/bin:/bin",
			})
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), tc.want) {
				t.Fatalf("error=%v output=%s", err, output)
			}
		})
	}
}
