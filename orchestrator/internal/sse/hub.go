// Package sse implements a per-run fan-out hub used to push live run events to
// SSE subscribers. It is the "realtime" path; durability lives in the store.
package sse

import (
	"sync"

	"github.com/cnjack/jcloud/internal/domain"
)

// Hub fans out run events to live subscribers, keyed by run ID.
type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[*subscriber]struct{}
}

type subscriber struct {
	ch chan domain.RunEvent
}

// NewHub returns an empty Hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[*subscriber]struct{})}
}

// Subscribe registers interest in a run's events. It returns a receive-only
// channel and an unsubscribe func the caller MUST invoke when done. The channel
// is buffered; if a slow consumer's buffer fills, further events for that
// subscriber are dropped from the live stream (the client can reconnect and
// replay from the durable log via after_seq).
func (h *Hub) Subscribe(runID string) (<-chan domain.RunEvent, func()) {
	s := &subscriber{ch: make(chan domain.RunEvent, 128)}
	h.mu.Lock()
	if h.subs[runID] == nil {
		h.subs[runID] = make(map[*subscriber]struct{})
	}
	h.subs[runID][s] = struct{}{}
	h.mu.Unlock()

	unsub := func() {
		h.mu.Lock()
		if m := h.subs[runID]; m != nil {
			delete(m, s)
			if len(m) == 0 {
				delete(h.subs, runID)
			}
		}
		h.mu.Unlock()
		close(s.ch)
	}
	return s.ch, unsub
}

// Publish delivers ev to every current subscriber of runID. Non-blocking: a
// subscriber whose buffer is full is skipped (see Subscribe).
func (h *Hub) Publish(runID string, ev domain.RunEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.subs[runID] {
		select {
		case s.ch <- ev:
		default:
			// drop for this slow subscriber
		}
	}
}
