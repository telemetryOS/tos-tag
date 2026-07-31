package sessions

import (
	"context"
	"testing"
)

func TestResolveAndRestart(t *testing.T) {
	store := NewMemoryStore(nil)
	first, created, err := store.Resolve(context.Background(), "org", "team", "channel", "100.1")
	if err != nil || !created || first.CurrentGeneration != 1 {
		t.Fatalf("resolve: %#v %v %v", first, created, err)
	}
	second, created, _ := store.Resolve(context.Background(), "org", "team", "channel", "100.1")
	if created || first.ID != second.ID {
		t.Fatalf("duplicate session: %#v %#v", first, second)
	}
	restarted, err := store.Restart(context.Background(), "org", "team", "channel", "100.1")
	if err != nil || restarted.CurrentGeneration != 2 || restarted.ID != first.ID {
		t.Fatalf("restart: %#v %v", restarted, err)
	}
}
