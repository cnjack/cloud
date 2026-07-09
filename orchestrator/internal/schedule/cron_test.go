package schedule

import (
	"errors"
	"testing"
	"time"
)

func TestValidateCronAccepts(t *testing.T) {
	// Legitimate crons at or above the 5-minute guard, including boundary
	// (exactly 5 minutes) and irregular-but-sparse patterns.
	valid := []string{
		"*/5 * * * *",  // every 5 minutes — the boundary, allowed
		"*/15 * * * *", // every 15 minutes
		"0 * * * *",    // hourly
		"0 0 * * *",    // daily midnight
		"30 9 * * 1-5", // 09:30 on weekdays
		"0 0 1 * *",    // first of the month
		"0,30 * * * *", // twice an hour, 30m apart
		"0 0 29 2 *",   // Feb 29 (fires on leap years) — sparse, allowed
		"  0 9 * * *",  // leading whitespace tolerated
	}
	for _, expr := range valid {
		if err := ValidateCron(expr); err != nil {
			t.Errorf("ValidateCron(%q) = %v, want nil", expr, err)
		}
	}
}

func TestValidateCronRejectsInvalid(t *testing.T) {
	invalid := []string{
		"",            // empty
		"not a cron",  // garbage
		"* * *",       // too few fields
		"* * * * * *", // too many fields (seconds not enabled)
		"60 * * * *",  // minute out of range
		"* 24 * * *",  // hour out of range
		"@hourly",     // descriptors not enabled
		"@every 1m",   // @every not enabled (would defeat the guard)
		"0 0 30 2 *",  // Feb 30 never fires → invalid, not "too frequent"
	}
	for _, expr := range invalid {
		err := ValidateCron(expr)
		if !errors.Is(err, ErrInvalidCron) {
			t.Errorf("ValidateCron(%q) = %v, want ErrInvalidCron", expr, err)
		}
	}
}

func TestValidateCronRejectsTooFrequent(t *testing.T) {
	tooFrequent := []string{
		"* * * * *",     // every minute
		"*/1 * * * *",   // every minute
		"*/2 * * * *",   // every 2 minutes
		"*/4 * * * *",   // every 4 minutes (< 5)
		"0,1 * * * *",   // :00 and :01 — a 1-minute gap the naive "next two" check misses
		"0,3,6 * * * *", // 3-minute gaps
	}
	for _, expr := range tooFrequent {
		err := ValidateCron(expr)
		if !errors.Is(err, ErrCronTooFrequent) {
			t.Errorf("ValidateCron(%q) = %v, want ErrCronTooFrequent", expr, err)
		}
	}
}

func TestParseCronNextAdvances(t *testing.T) {
	sched, err := ParseCron("0 * * * *") // hourly at minute 0
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	base := time.Date(2026, 7, 9, 14, 30, 0, 0, time.UTC)
	next := sched.Next(base)
	want := time.Date(2026, 7, 9, 15, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("Next(%v) = %v, want %v", base, next, want)
	}
	// Next is strictly after its argument (drives the poller's exactly-once check).
	if !sched.Next(next).After(next) {
		t.Fatalf("Next(%v) is not strictly after it", next)
	}
}

func TestParseCronRejectsInvalid(t *testing.T) {
	if _, err := ParseCron("nonsense"); !errors.Is(err, ErrInvalidCron) {
		t.Fatalf("ParseCron(nonsense) = %v, want ErrInvalidCron", err)
	}
}
