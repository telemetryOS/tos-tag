package jobs

import (
	"context"
	"github.com/telemetryos/tos-tag/core/sessions"
	"testing"
)

func TestRestartAndBranchCreateNewSessionGenerations(t *testing.T) {
	queue := NewMemoryQueue(nil)
	sessionStore := sessions.NewMemoryStore(nil)
	session, _, _ := sessionStore.Resolve(context.Background(), "o", "w", "c", "root")
	original, _, _ := queue.Enqueue(context.Background(), Spec{OrganizationID: "o", WorkspaceID: "w", ChannelID: "c", RootThreadTS: "root", SessionID: session.ID, Generation: 1, IdempotencyKey: "original", Kind: "agent", MaxAttempts: 2})
	operations := Operations{Queue: queue, Sessions: sessionStore}
	restarted, err := operations.Restart(context.Background(), string(original.ID))
	if err != nil || restarted.Generation != 2 {
		t.Fatalf("restart=%#v err=%v", restarted, err)
	}
	branched, err := operations.Branch(context.Background(), string(original.ID), "branch")
	if err != nil || branched.RootThreadTS != "branch" || branched.SessionID == original.SessionID {
		t.Fatalf("branch=%#v err=%v", branched, err)
	}
}
