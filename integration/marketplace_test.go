package integration

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/telemetryos/tos-tag/core/marketplace"
)

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
