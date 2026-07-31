package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/approvals"
	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/chatgating"
	"github.com/telemetryos/tos-tag/core/harness"
	"github.com/telemetryos/tos-tag/core/jobs"
	"github.com/telemetryos/tos-tag/core/keystore"
	"github.com/telemetryos/tos-tag/core/modelrouter"
	toolruntime "github.com/telemetryos/tos-tag/core/tools"
	"github.com/telemetryos/tos-tag/core/workers"
	"github.com/telemetryos/tos-tag/types"
)

// This opt-in test exercises the same disposable headless OpenCode server used
// by production jobs. It intentionally relies on an anonymous/free provider by
// default and never copies host credentials into the worker.
func TestHeadlessOpenCodeProviderRoute(t *testing.T) {
	if os.Getenv("TOS_TAG_INTEGRATION_OPENCODE") != "1" {
		t.Skip("set TOS_TAG_INTEGRATION_OPENCODE=1 for the external provider smoke test")
	}
	executable, err := exec.LookPath("opencode")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Dir(executable) + ":/usr/local/bin:/usr/bin:/bin"
	manager, err := workers.NewLocal(t.TempDir(), path)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := harness.NewWorkerOpenCode(harness.WorkerOpenCodeOptions{Manager: manager, Command: executable, Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	session, err := adapter.CreateSession(ctx, "tos-tag provider smoke")
	if err != nil {
		t.Fatal(err)
	}
	model := os.Getenv("TOS_TAG_INTEGRATION_OPENCODE_MODEL")
	if model == "" {
		model = "opencode/north-mini-code-free"
	}
	expectedProvider, expectedModel, _ := strings.Cut(model, "/")
	if err := adapter.Prompt(ctx, session.ID, harness.Prompt{Text: "Return exactly: *provider route verified*", System: "Use Slack mrkdwn.", Model: model, RequestID: "provider-smoke", SlackFormat: "slack-mrkdwn/v1"}); err != nil {
		t.Fatal(err)
	}
	events, errs := adapter.Events(ctx, session.ID)
	var output strings.Builder
	routeSeen := false
	for events != nil || errs != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if text, ok := event.Data["text"].(string); ok {
				output.WriteString(text)
			}
			if event.Type == "message.updated" {
				if info, ok := event.Data["info"].(map[string]any); ok && info["role"] == "assistant" && info["providerID"] == expectedProvider && info["modelID"] == expectedModel {
					routeSeen = true
				}
			}
		case eventErr, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if eventErr != nil {
				t.Fatal(eventErr)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if !strings.Contains(strings.ToLower(output.String()), "provider route verified") {
		t.Fatalf("unexpected provider output %q", output.String())
	}
	if !routeSeen {
		t.Fatal("OpenCode did not report the requested provider/model route")
	}
}

func TestHeadlessOpenCodeGatingClassifier(t *testing.T) {
	if os.Getenv("TOS_TAG_INTEGRATION_OPENCODE") != "1" {
		t.Skip("set TOS_TAG_INTEGRATION_OPENCODE=1")
	}
	executable, err := exec.LookPath("opencode")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := workers.NewLocal(t.TempDir(), filepath.Dir(executable)+":/usr/local/bin:/usr/bin:/bin")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := harness.NewWorkerOpenCode(harness.WorkerOpenCodeOptions{Manager: manager, Command: executable, Timeout: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close(context.Background())
	model := os.Getenv("TOS_TAG_INTEGRATION_OPENCODE_MODEL")
	if model == "" {
		model = "opencode/north-mini-code-free"
	}
	provider, modelID, _ := strings.Cut(model, "/")
	router, err := modelrouter.NewRegistry([]types.ModelProfile{{ID: "gate", ProviderID: provider, ModelID: modelID, RequiredCapabilities: []string{"structured"}, AllowedDataClasses: []string{"internal"}, MaxInputTokens: 200000, MaxOutputTokens: 4096, Enabled: true}}, nil, nil, "gate", "test/v1")
	if err != nil {
		t.Fatal(err)
	}
	classifier, err := chatgating.NewHarnessClassifier(adapter, router)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pack := types.ContextPackRevision{OrganizationID: "org", TotalTokens: 1000, Sources: []types.ContextSource{{ID: "alerts/100.1", ChannelID: "alerts", Partition: types.PartitionEvidence, Text: "Active production outage", DisclosureClass: types.DisclosureDestinationSafe}}}
	decision, err := classifier.Decide(ctx, chatgating.Target{ObservationID: "observation", Envelope: types.SlackEnvelope{OrganizationID: "org", TeamID: "team", ChannelID: "support", Text: "Is the system down?"}, Mode: types.ModeAssist}, pack)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome == types.OutcomeSilent || len(decision.ReleasableEvidenceIDs) == 0 || decision.ReleasableEvidenceIDs[0] != "alerts/100.1" {
		t.Fatalf("unexpected gating decision %#v", decision)
	}
}

func TestHeadlessOpenCodeToolBridge(t *testing.T) {
	if os.Getenv("TOS_TAG_INTEGRATION_OPENCODE") != "1" {
		t.Skip("set TOS_TAG_INTEGRATION_OPENCODE=1 for the external provider smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	queue := jobs.NewMemoryQueue(time.Now)
	_, _, err := queue.Enqueue(ctx, jobs.Spec{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", RootThreadTS: "100.0", SessionID: "session", Generation: 1, IdempotencyKey: "tool-job", Kind: "response", MaxAttempts: 1, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := queue.Claim(ctx, "worker", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	running, err := queue.Transition(ctx, leased.ID, leased.Lease.Token, jobs.StateRunning, nil)
	if err != nil {
		t.Fatal(err)
	}

	marketplaceRoot := t.TempDir()
	toolRoot := filepath.Join(marketplaceRoot, "demo")
	if err := os.Mkdir(toolRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marketplaceRoot, "catalog.json"), []byte(`{"id":"integration-tools","version":"v1","tools":[{"name":"demo","path":"demo"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolRoot, "SKILL.md"), []byte("# Demo\nUse the demo read operation for the bridge smoke test.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolRoot, "tool.json"), []byte(`{"id":"demo","version":"v1","script":"tool.sh","operations":[{"id":"read","timeout_seconds":5,"max_output_bytes":1024,"risk":"read"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolRoot, "tool.sh"), []byte("printf 'bridge:%s' \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	registry, err := toolruntime.LoadMarketplace(marketplaceRoot, "catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	toolSkills, toolIDs, err := registry.Select([]string{"demo"})
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := keystore.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	auditLog, err := audit.NewMemoryAppender([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := toolruntime.NewBridge(toolruntime.Gateway{Registry: registry, Secrets: secrets, Executor: toolruntime.Executor{Enabled: true}}, queue, approvals.NewStore(), auditLog)
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.Start(); err != nil {
		t.Fatal(err)
	}
	defer bridge.Stop(context.Background())

	executable, err := exec.LookPath("opencode")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := workers.NewLocalWithDependencies(t.TempDir(), filepath.Dir(executable)+":/usr/local/bin:/usr/bin:/bin", bridge, nil)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := harness.NewWorkerOpenCode(harness.WorkerOpenCodeOptions{Manager: manager, Command: executable, Timeout: 2 * time.Minute, ToolBridge: bridge, Skills: toolSkills, ToolIDs: toolIDs})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close(context.Background())
	session, err := adapter.CreateJobSession(ctx, harness.JobSessionSpec{Title: "tos-tag tool smoke", OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", JobID: string(running.ID), LeaseToken: running.Lease.Token, SteeringEpoch: running.SteeringEpoch, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	model := os.Getenv("TOS_TAG_INTEGRATION_OPENCODE_MODEL")
	if model == "" {
		model = "opencode/north-mini-code-free"
	}
	prompt := "You must call `tos_tag_tool` once with `tool_id`=`demo`, `operation_id`=`read`, and `arguments`=[`probe`]. If its JSON output contains `bridge:probe`, reply exactly `*tool bridge verified*`."
	if err := adapter.Prompt(ctx, session.ID, harness.Prompt{Text: prompt, System: "Use the available reviewed tool exactly as requested. Use Slack mrkdwn.", Model: model, RequestID: "tool-bridge-smoke", SlackFormat: "slack-mrkdwn/v1"}); err != nil {
		t.Fatal(err)
	}
	output := collectOpenCodeOutput(t, ctx, adapter, session.ID)
	if !strings.Contains(strings.ToLower(output), "tool bridge verified") {
		t.Fatalf("unexpected tool bridge output %q", output)
	}
}

func collectOpenCodeOutput(t *testing.T, ctx context.Context, adapter *harness.WorkerOpenCode, sessionID string) string {
	t.Helper()
	events, errs := adapter.Events(ctx, sessionID)
	var output strings.Builder
	for events != nil || errs != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event.Type == "message.delta" {
				if text, ok := event.Data["text"].(string); ok {
					output.WriteString(text)
				}
			}
		case eventErr, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if eventErr != nil {
				t.Fatal(eventErr)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	return output.String()
}
