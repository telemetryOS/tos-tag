// Package schedule validates and advances recurring automation schedules.
package schedule

import (
	"errors"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

const DefaultTimezone = "UTC"

// Spec is either a standard five-field cron expression (preferred) or a
// legacy fixed interval. Cron expressions always take precedence when both
// fields exist on a record migrated from an older release.
type Spec struct {
	Cron     string
	Timezone string
	Interval time.Duration

	schedule cron.Schedule
	location *time.Location
}

// Parse validates an automation schedule. Cron expressions use a separate
// IANA timezone so schedule meaning remains visible and unambiguous.
func Parse(expression, timezone string, interval time.Duration) (Spec, error) {
	expression = strings.TrimSpace(expression)
	timezone = strings.TrimSpace(timezone)
	if expression == "" {
		if interval < time.Minute {
			return Spec{}, errors.New("cron expression or interval of at least one minute is required")
		}
		return Spec{Interval: interval, Timezone: DefaultTimezone, location: time.UTC}, nil
	}
	if strings.Contains(expression, "TZ=") {
		return Spec{}, errors.New("timezone must be supplied separately from the cron expression")
	}
	if len(strings.Fields(expression)) != 5 {
		return Spec{}, errors.New("cron expression must contain exactly five fields")
	}
	if timezone == "" {
		timezone = DefaultTimezone
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return Spec{}, errors.New("invalid schedule timezone")
	}
	parsed, err := cron.ParseStandard(expression)
	if err != nil {
		return Spec{}, errors.New("invalid cron expression")
	}
	return Spec{Cron: expression, Timezone: timezone, Interval: interval, schedule: parsed, location: location}, nil
}

// Next returns the first scheduled instant strictly after the supplied time.
func (s Spec) Next(after time.Time) time.Time {
	if s.schedule != nil {
		return s.schedule.Next(after.In(s.location)).UTC()
	}
	return after.UTC().Add(s.Interval)
}

// Advance returns the next future occurrence, skipping missed windows.
func (s Spec) Advance(current, now time.Time) time.Time {
	if s.schedule != nil {
		return s.Next(now)
	}
	next := current.UTC()
	for !next.After(now) {
		next = next.Add(s.Interval)
	}
	return next
}

// Window returns a bounded lifetime for work launched at one occurrence.
func (s Spec) Window(occurrence time.Time) time.Duration {
	window := s.Next(occurrence).Sub(occurrence)
	if window < time.Minute {
		return time.Minute
	}
	return window
}
