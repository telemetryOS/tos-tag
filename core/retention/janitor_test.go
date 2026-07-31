package retention

import (
	"testing"
	"time"
)

func TestClampExpiryNeverOutlivesEarliestSource(t *testing.T) {
	now := time.Now().UTC()
	got, err := ClampExpiry(now.Add(24*time.Hour), now.Add(3*time.Hour), now.Add(7*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(now.Add(3 * time.Hour)) {
		t.Fatalf("got %s", got)
	}
}
func TestClampExpiryRequiresSources(t *testing.T) {
	if _, err := ClampExpiry(time.Now()); err == nil {
		t.Fatal("missing source accepted")
	}
}
