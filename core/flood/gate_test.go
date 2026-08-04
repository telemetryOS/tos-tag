package flood

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryDeniesBeyondFixedWindowLimitAndResets(t *testing.T) {
	now := time.Date(2026, 8, 3, 10, 17, 0, 0, time.UTC)
	gate, err := NewMemory(2, time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{OrganizationID: "org-test", TeamID: "team-test"}
	for wantCount := int64(1); wantCount <= 3; wantCount++ {
		result, admitErr := gate.Admit(context.Background(), scope)
		if admitErr != nil {
			t.Fatal(admitErr)
		}
		if result.Count != wantCount || result.Allowed != (wantCount <= 2) {
			t.Fatalf("count %d result = %#v", wantCount, result)
		}
	}
	now = now.Add(time.Hour)
	result, err := gate.Admit(context.Background(), scope)
	if err != nil || !result.Allowed || result.Count != 1 {
		t.Fatalf("new window result = %#v err=%v", result, err)
	}
}

func TestMemoryConcurrentAdmissionNeverAllowsMoreThanLimit(t *testing.T) {
	gate, err := NewMemory(10, time.Hour, func() time.Time { return time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, admitErr := gate.Admit(context.Background(), Scope{OrganizationID: "org-test", TeamID: "team-test"})
			if admitErr != nil {
				t.Errorf("admit: %v", admitErr)
				return
			}
			if result.Allowed {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 10 {
		t.Fatalf("allowed = %d, want 10", got)
	}
}

func TestMemorySeparatesWorkspaceBuckets(t *testing.T) {
	gate, err := NewMemory(1, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, teamID := range []string{"team-a", "team-b"} {
		result, admitErr := gate.Admit(context.Background(), Scope{OrganizationID: "org-test", TeamID: teamID})
		if admitErr != nil || !result.Allowed {
			t.Fatalf("team %s result=%#v err=%v", teamID, result, admitErr)
		}
	}
}
