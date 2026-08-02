package main

import (
	"errors"
	"strings"
	"sync"
)

const (
	exitModelRateLimited      = 75
	terminalSignalMaxBytes    = 16 * 1024
	rateLimitSummaryPrefix    = "Rate limited"
	rateLimitExhaustedRetries = ", and retries didn't clear it."
)

var errModelRateLimited = errors.New("model remained rate limited after agent retries")

// terminalSignal keeps a bounded copy of live agent text so acpdrive can
// preserve jcode's canonical terminal rate-limit classification even when the
// ACP session/prompt call itself returns only a generic JSON-RPC error. ACP
// notifications may be dispatched concurrently, hence the mutex.
type terminalSignal struct {
	mu   sync.Mutex
	text string
}

func (s *terminalSignal) Reset() {
	s.mu.Lock()
	s.text = ""
	s.mu.Unlock()
}

func (s *terminalSignal) Observe(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	s.text += text
	if len(s.text) > terminalSignalMaxBytes {
		s.text = s.text[len(s.text)-terminalSignalMaxBytes:]
	}
	s.mu.Unlock()
}

// ModelRateLimited recognizes only jcode's canonical terminal summary at the
// start of the response (or a new line), never arbitrary prose that happens to
// mention rate limiting. Callers additionally require session/prompt to have
// failed, so a successful turn containing the same words is not reclassified.
func (s *terminalSignal) ModelRateLimited() bool {
	s.mu.Lock()
	text := strings.TrimSpace(s.text)
	s.mu.Unlock()
	for {
		if strings.HasPrefix(text, rateLimitSummaryPrefix) && strings.Contains(text, rateLimitExhaustedRetries) {
			return true
		}
		next := strings.Index(text, "\n"+rateLimitSummaryPrefix)
		if next < 0 {
			return false
		}
		text = strings.TrimSpace(text[next+1:])
	}
}
