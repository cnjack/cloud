package domain

import "time"

// Backoff computes the retry delay for a failed run using Symphony's formula
// (cloud/docs/05-symphony-and-references.md):
//
//	delay = min(baseMs * 2^(attempt-1), maxMs)
//
// with Symphony's defaults baseMs=10000, maxMs=300000 (5m). attempt is 1-based:
// the first retry (attempt 1) waits baseMs. attempt <= 0 is treated as 1.
//
// MVP note: retries are manual (operator-triggered) so this delay is not yet
// enforced by an auto-retry loop, but the value is surfaced so the state can be
// carried forward for a future auto-retry reconciler. See the reconciler
// package divergence notes.
func Backoff(attempt int, baseMs, maxMs int64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if baseMs <= 0 {
		baseMs = 10000
	}
	if maxMs <= 0 {
		maxMs = 300000
	}
	delayMs := baseMs
	// Multiply by 2 for each attempt beyond the first, capping early to avoid
	// int64 overflow on absurd attempt counts.
	for i := 1; i < attempt; i++ {
		if delayMs >= maxMs {
			delayMs = maxMs
			break
		}
		delayMs *= 2
	}
	if delayMs > maxMs {
		delayMs = maxMs
	}
	return time.Duration(delayMs) * time.Millisecond
}
