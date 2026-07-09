// Package schedule implements the F11 / D24 cron trigger: a `schedules` row binds
// a standard 5-field cron expression + a prompt to a service, and a poller tick
// (mirroring the D17 kanban poller's poll/idempotency philosophy) dispatches a
// headless agent run each time a schedule comes due.
//
// This file is the cron layer shared by the API validation gate and the poller's
// next-fire computation: one parser configured for the crontab(5) 5-field form
// (no seconds, no @descriptors), plus a min-interval guard that rejects
// self-harming high-frequency expressions before they are ever stored.
package schedule

import (
	"errors"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// cronParser is the standard 5-field parser: minute hour day-of-month month
// day-of-week. Seconds and @descriptors (@hourly, @every 1m, …) are deliberately
// NOT enabled — a schedule is a crontab line, and @every would trivially defeat
// the min-interval guard. Shared (stateless) by ParseCron below.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// MinInterval is the smallest gap allowed between two consecutive fires. An
// expression that would fire more often than this is rejected at create/update
// (cron_too_frequent) so a runaway "* * * * *" cannot self-DoS the cluster with a
// run every minute. 5 minutes is well below any legitimate scheduled-automation
// cadence while still blocking the pathological cases.
const MinInterval = 5 * time.Minute

// minGuardSamples is how many consecutive fires ValidateCron inspects when
// enforcing MinInterval. A single "next two fires" check is fooled by IRREGULAR
// expressions (e.g. "0,1 * * * *" has a 1-minute gap that only shows up between
// the :00 and :01 of the SAME hour); sampling a run of fires surfaces the
// smallest real gap. 64 covers every intra-hour / intra-day pattern cheaply.
const minGuardSamples = 64

var (
	// ErrInvalidCron: the expression is not a valid 5-field cron (API → 400
	// invalid_cron).
	ErrInvalidCron = errors.New("invalid cron expression")
	// ErrCronTooFrequent: the expression fires more often than MinInterval (API →
	// 400 cron_too_frequent).
	ErrCronTooFrequent = errors.New("cron fires too frequently")
)

// ParseCron parses a standard 5-field cron expression, returning the compiled
// schedule or ErrInvalidCron. It does NOT enforce the min-interval guard — the
// poller uses this to compute the next fire off a schedule already validated at
// write time. Whitespace is trimmed so a trailing newline from a form field is
// tolerated.
func ParseCron(expr string) (cron.Schedule, error) {
	sched, err := cronParser.Parse(strings.TrimSpace(expr))
	if err != nil {
		return nil, ErrInvalidCron
	}
	return sched, nil
}

// ValidateCron parses expr AND enforces the min-interval guard, returning
// ErrInvalidCron or ErrCronTooFrequent (both map to a fail-visible 400 at the
// API). It walks minGuardSamples consecutive fires from a fixed neutral reference
// and rejects the expression if any two adjacent fires are closer than
// MinInterval — catching irregular high-frequency patterns a single "next gap"
// check would miss.
func ValidateCron(expr string) error {
	sched, err := ParseCron(expr)
	if err != nil {
		return err
	}
	// A neutral, DST-free reference (UTC). cron.Next returns the first fire
	// STRICTLY after its argument. We take that FIRST fire as the anchor and never
	// measure the (partial) gap from the arbitrary reference to it — only gaps
	// between CONSECUTIVE fires are real inter-fire intervals. (Measuring the
	// reference→first gap would wrongly reject e.g. "*/5 * * * *": from :00:30 the
	// first fire is :05:00, a 4m30s partial gap.)
	prev := sched.Next(time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC))
	if prev.IsZero() {
		// A 5-field expression that parses but never fires (an impossible date such
		// as Feb 30) is a useless schedule — reject it as invalid rather than storing
		// a trigger that can never run.
		return ErrInvalidCron
	}
	for i := 0; i < minGuardSamples; i++ {
		next := sched.Next(prev)
		if next.IsZero() {
			break // no further fires — remaining samples cannot add a sub-interval gap
		}
		if next.Sub(prev) < MinInterval {
			return ErrCronTooFrequent
		}
		prev = next
	}
	return nil
}
