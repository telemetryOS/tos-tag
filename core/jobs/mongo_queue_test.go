package jobs

import (
	"reflect"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

func TestMongoTransitionSetPersistsRoutingMutations(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	resolved := types.ResolvedModel{ProfileID: "strong", ProviderID: "openai", ModelID: "gpt-5.6-sol", Variant: "high", PolicyRev: "test/v1"}
	trace := types.DecisionTrace{MatchedRule: "routine-default", Tried: []string{"routine-default"}}

	set := transitionSet(Job{ResolvedModel: resolved, RouteTrace: trace}, StateRunning, now)
	if got := set["resolved_model"]; !reflect.DeepEqual(got, resolved) {
		t.Fatalf("resolved_model = %#v, want %#v", got, resolved)
	}
	if got := set["route_trace"]; !reflect.DeepEqual(got, trace) {
		t.Fatalf("route_trace = %#v, want %#v", got, trace)
	}
	if got := set["state"]; got != string(StateRunning) {
		t.Fatalf("state = %#v, want %q", got, StateRunning)
	}
	if got := set["updated_at"]; got != now {
		t.Fatalf("updated_at = %#v, want %s", got, now)
	}
}
