package sessions

import (
	"context"
	"reflect"
	"testing"
	"time"
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

func TestListRootsUsesScopeAndActivityWindow(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(func() time.Time { return now })
	_, _, _ = store.Resolve(context.Background(), "org", "team", "channel", "200.1")
	_, _, _ = store.Resolve(context.Background(), "org", "team", "channel", "100.1")
	_, _, _ = store.Resolve(context.Background(), "other", "team", "channel", "050.1")

	roots, err := store.ListRoots(context.Background(), "org", "team", "channel", now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"100.1", "200.1"}; !reflect.DeepEqual(roots, want) {
		t.Fatalf("roots = %v, want %v", roots, want)
	}
	if roots, err := store.ListRoots(context.Background(), "org", "team", "channel", now.Add(time.Minute)); err != nil || len(roots) != 0 {
		t.Fatalf("future activity window roots = %v err=%v", roots, err)
	}
}
