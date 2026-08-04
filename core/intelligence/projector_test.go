package intelligence

import (
	"testing"

	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

func TestProjectionCleanupOnlyRunsForMutations(t *testing.T) {
	if requiresProjectionCleanup(models.Observation{EventType: string(types.SlackEventMessage)}) {
		t.Fatal("ordinary new message requested projection cleanup")
	}
	for _, observation := range []models.Observation{
		{EventType: string(types.SlackEventEdit)},
		{EventType: string(types.SlackEventDelete)},
		{EventType: string(types.SlackEventMessage), MutationTargetTS: "123.456"},
	} {
		if !requiresProjectionCleanup(observation) {
			t.Fatalf("mutation did not request cleanup: %#v", observation)
		}
	}
}
