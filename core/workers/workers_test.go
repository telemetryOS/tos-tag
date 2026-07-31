package workers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/marketplace"
	"github.com/telemetryos/tos-tag/core/usage"
)

func TestLocalWorkerHasCleanEnvironmentAndTerminatesProcessGroup(t *testing.T) {
	manager, err := NewLocal(t.TempDir(), "/usr/bin:/bin")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := manager.Provision(context.Background(), Spec{JobID: "job-1", AttemptID: "attempt-1", Command: []string{"/bin/sh", "-c", "sleep 30 & wait"}, Environment: map[string]string{"SAFE_SETTING": "yes"}, WallTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if workspace.PID <= 0 {
		t.Fatal("worker PID missing")
	}
	policy, err := os.ReadFile(filepath.Join(workspace.WorkDir, "opencode.json"))
	if err != nil || !strings.Contains(string(policy), `"*":"deny"`) || !strings.Contains(string(policy), `"skill":"allow"`) {
		t.Fatalf("worker policy=%q err=%v", policy, err)
	}
	if err := manager.Terminate(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worker root remains: %v", err)
	}
	if err := syscall.Kill(workspace.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("worker process remains: %v pid=%s", err, strconv.Itoa(workspace.PID))
	}
}

func TestAuthorizedSkillsAreHashVerifiedAndReadOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "skill"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "catalog.json"), []byte(`{"id":"skills","version":"v1","skills":[{"name":"wiki","path":"skill"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skill", "SKILL.md"), []byte("# Wiki\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, snapshots, err := marketplace.Load(root, "catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	manager, _ := NewLocal(t.TempDir(), "/usr/bin:/bin")
	workspace, err := manager.Provision(context.Background(), Spec{JobID: "j", AttemptID: "a", Command: []string{"/bin/sh", "-c", "sleep 30"}, Skills: snapshots, WallTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Terminate(context.Background(), workspace)
	info, err := os.Stat(filepath.Join(workspace.SkillsDir, "wiki", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("skill mode=%o", info.Mode().Perm())
	}
	tampered := snapshots
	tampered[0].Hash = "sha256:bad"
	if _, err := manager.Provision(context.Background(), Spec{JobID: "j2", AttemptID: "a2", Command: []string{"/bin/true"}, Skills: tampered, WallTime: time.Second}); !errors.Is(err, ErrUnsafeSpec) {
		t.Fatalf("tampered snapshot accepted: %v", err)
	}
}

func TestCustomToolIsReadOnlyAndNarrowlyAllowed(t *testing.T) {
	manager, _ := NewLocal(t.TempDir(), "/usr/bin:/bin")
	workspace, err := manager.Provision(context.Background(), Spec{JobID: "j", AttemptID: "a", Command: []string{"/bin/sh", "-c", "sleep 30"}, CustomTools: map[string][]byte{"tos_tag_tool.ts": []byte("export default {}\n")}, WallTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Terminate(context.Background(), workspace)
	toolPath := filepath.Join(workspace.WorkDir, ".opencode", "tools", "tos_tag_tool.ts")
	info, err := os.Stat(toolPath)
	if err != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("tool info=%v err=%v", info, err)
	}
	policy, err := os.ReadFile(filepath.Join(workspace.WorkDir, "opencode.json"))
	if err != nil || !strings.Contains(string(policy), `"tos_tag_tool":"allow"`) || !strings.Contains(string(policy), `"*":"deny"`) {
		t.Fatalf("policy=%s err=%v", policy, err)
	}
}

func TestWorkerDoesNotInheritHostSecrets(t *testing.T) {
	t.Setenv("SLACK_BOT_TOKEN", "must-not-leak")
	manager, _ := NewLocal(t.TempDir(), "/usr/bin:/bin")
	workspace, err := manager.Provision(context.Background(), Spec{JobID: "j", AttemptID: "a", Command: []string{"/bin/sh", "-c", "env > \"$TOS_TAG_ARTIFACTS/env.txt\"; sleep 30"}, WallTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Terminate(context.Background(), workspace)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(filepath.Join(workspace.ArtifactsDir, "env.txt")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("environment artifact not created")
		}
		time.Sleep(time.Millisecond)
	}
	artifacts, err := manager.ExportArtifacts(context.Background(), workspace, []ArtifactSpec{{Path: "env.txt", MaxBytes: 8192}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(artifacts[0].Data), "must-not-leak") || strings.Contains(string(artifacts[0].Data), "SLACK_BOT_TOKEN") {
		t.Fatalf("host secret inherited: %s", artifacts[0].Data)
	}
}

type testRevoker struct{ attempt string }

func (r *testRevoker) RevokeAttempt(_ context.Context, attempt string) error {
	r.attempt = attempt
	return nil
}
func TestTerminationRevokesAttemptCapability(t *testing.T) {
	revoker := &testRevoker{}
	recorder := usage.NewMemory()
	manager, err := NewLocalWithDependencies(t.TempDir(), "/usr/bin:/bin", revoker, recorder)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := manager.Provision(context.Background(), Spec{OrganizationID: "org", JobID: "job", AttemptID: "attempt", Command: []string{"/bin/sh", "-c", "sleep 30"}, WallTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Terminate(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if revoker.attempt != "attempt" {
		t.Fatalf("revoked %q", revoker.attempt)
	}
	events, _ := recorder.List(context.Background(), "org", 10)
	if len(events) != 2 {
		t.Fatalf("worker usage=%#v", events)
	}
}

func TestLocalRejectsHostSecretEnvironmentAndArtifactTraversal(t *testing.T) {
	manager, err := NewLocal(t.TempDir(), "/usr/bin:/bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Provision(context.Background(), Spec{JobID: "j", AttemptID: "a", Command: []string{"/bin/true"}, Environment: map[string]string{"SLACK_TOKEN": "secret"}, WallTime: time.Second}); !errors.Is(err, ErrUnsafeSpec) {
		t.Fatalf("expected unsafe spec, got %v", err)
	}
	workspace, err := manager.Provision(context.Background(), Spec{JobID: "j", AttemptID: "a", Command: []string{"/bin/sh", "-c", "sleep 30"}, WallTime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Terminate(context.Background(), workspace)
	if err := os.WriteFile(filepath.Join(workspace.ArtifactsDir, "ok.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifacts, err := manager.ExportArtifacts(context.Background(), workspace, []ArtifactSpec{{Path: "ok.txt", MaxBytes: 8}})
	if err != nil || string(artifacts[0].Data) != "ok" {
		t.Fatalf("artifacts=%#v err=%v", artifacts, err)
	}
	if _, err := manager.ExportArtifacts(context.Background(), workspace, []ArtifactSpec{{Path: "../outside", MaxBytes: 8}}); !errors.Is(err, ErrUnsafeSpec) {
		t.Fatalf("traversal result: %v", err)
	}
}

func TestFakeWorkerLifecycle(t *testing.T) {
	fake := NewFake()
	workspace, err := fake.Provision(context.Background(), Spec{JobID: "job", AttemptID: "attempt", WallTime: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := fake.Terminate(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
}
