package integration

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/harness"
	"github.com/telemetryos/tos-tag/core/workers"
)

func TestDisposableOpenCodeWorkerLifecycle(t *testing.T) {
	if os.Getenv("TOS_TAG_INTEGRATION_OPENCODE") != "1" {
		t.Skip("set TOS_TAG_INTEGRATION_OPENCODE=1")
	}
	command, err := exec.LookPath("opencode")
	if err != nil {
		t.Skip("opencode is not installed")
	}
	manager, err := workers.NewLocal(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := harness.NewWorkerOpenCode(harness.WorkerOpenCodeOptions{Manager: manager, Command: command, Timeout: 45 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	session, err := adapter.CreateSession(ctx, "integration-worker")
	if err != nil {
		t.Fatal(err)
	}
	if session.ID == "" {
		t.Fatal("OpenCode returned an empty session ID")
	}
	if err := adapter.Abort(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
}
