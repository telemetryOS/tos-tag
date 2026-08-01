//go:build live

package integration

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/approvals"
	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/deliveries"
	"github.com/telemetryos/tos-tag/core/harness"
	"github.com/telemetryos/tos-tag/core/jobs"
	"github.com/telemetryos/tos-tag/core/tools"
	"github.com/telemetryos/tos-tag/core/workers"
)

func TestLiveCodexAppServerTurn(t *testing.T) {
	if os.Getenv("TOS_TAG_LIVE_CODEX") != "1" {
		t.Skip("set TOS_TAG_LIVE_CODEX=1 to run the real Codex App Server smoke")
	}
	command, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			t.Fatal(homeErr)
		}
		codexHome = filepath.Join(home, ".codex")
	}
	manager, err := workers.NewLocal(t.TempDir(), filepath.Dir(command)+":/usr/local/bin:/usr/bin:/bin")
	if err != nil {
		t.Fatal(err)
	}
	auditLog, err := audit.NewMemoryAppender([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := tools.NewBridge(tools.Gateway{}, jobs.NewMemoryQueue(nil), approvals.NewStore(), auditLog)
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Stop(context.Background()) })
	worker, err := harness.NewWorkerCodex(harness.WorkerCodexOptions{Manager: manager, Command: command, CodexHome: codexHome, Timeout: 2 * time.Minute, ToolBridge: bridge})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Close(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	session, err := worker.CreateJobSession(ctx, harness.JobSessionSpec{Title: "tos-tag Codex App Server live smoke", OrganizationID: "org", WorkspaceID: "workspace", ChannelID: "channel", JobID: "live-codex-smoke", LeaseToken: "lease", SteeringEpoch: 1, ExpiresAt: time.Now().Add(2 * time.Minute)})
	if err != nil {
		t.Fatalf("%v: %v", err, errors.Unwrap(err))
	}
	if err := worker.Prompt(ctx, session.ID, harness.Prompt{Text: "Return exactly one short Slack result saying Codex App Server is ready.", System: deliveries.WithSlackOutputContract("Answer the request directly."), Model: "openai/gpt-5.6-luna", Variant: "low", RequestID: "live-codex-smoke", SlackFormat: deliveries.SlackOutputContractVersion}); err != nil {
		t.Fatalf("%v: %v", err, errors.Unwrap(err))
	}
	events, errs := worker.Events(ctx, session.ID)
	var output strings.Builder
	for event := range events {
		if event.Type == "message.delta" {
			text, _ := event.Data["text"].(string)
			output.WriteString(text)
		}
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"segments"`) || !strings.Contains(output.String(), "Codex App Server") {
		t.Fatalf("unexpected App Server output: %q", output.String())
	}
}
