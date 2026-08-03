package schedule

import (
	"testing"
	"time"
)

func TestCronUsesExplicitTimezoneAndAdvancesPastMissedWindows(t *testing.T) {
	spec, err := Parse("0 9 * * 1-5", "America/Vancouver", 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 3, 18, 30, 0, 0, time.UTC)
	next := spec.Advance(time.Date(2026, time.August, 3, 16, 0, 0, 0, time.UTC), now)
	want := time.Date(2026, time.August, 4, 16, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next=%s want=%s", next, want)
	}
}

func TestCronDefaultsToUTCAndRejectsEmbeddedTimezone(t *testing.T) {
	spec, err := Parse("*/15 * * * *", "", 0)
	if err != nil || spec.Timezone != DefaultTimezone {
		t.Fatalf("spec=%#v err=%v", spec, err)
	}
	if _, err := Parse("CRON_TZ=UTC */15 * * * *", "UTC", 0); err == nil {
		t.Fatal("embedded timezone accepted")
	}
	if _, err := Parse("@every 15m", "UTC", 0); err == nil {
		t.Fatal("non-standard cron descriptor accepted")
	}
}

func TestLegacyIntervalRemainsSupported(t *testing.T) {
	spec, err := Parse("", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	current := time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC)
	if got := spec.Advance(current, current.Add(3*time.Hour+time.Minute)); !got.Equal(current.Add(4 * time.Hour)) {
		t.Fatalf("next=%s", got)
	}
}
