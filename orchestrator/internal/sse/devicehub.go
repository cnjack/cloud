package sse

import "sync"

// DeviceEvent is one frame on a device's client stream (docs/17 §4.3): a
// device.status transition, a durable session.event notification, or an
// ephemeral session.delta forward. Type is the SSE `event:` line; Data is the
// JSON `data:` payload. Unlike RunEvent there is no global seq — durable
// events carry their per-session seq inside Data, so reconnects fill gaps via
// the events API (after_seq), exactly like the run stream's replay path.
type DeviceEvent struct {
	Type string
	Data map[string]any
}

// Device stream event types (docs/17 §4.3).
const (
	// DeviceEventStatus announces an online/offline transition; Data {"online"}.
	DeviceEventStatus = "device.status"
	// DeviceEventSessionEvent notifies a durable event landed; Data
	// {"session_id","seq","kind","payload"}.
	DeviceEventSessionEvent = "session.event"
	// DeviceEventSessionDelta forwards an ephemeral event (never persisted);
	// Data {"session_id","kind","payload"}.
	DeviceEventSessionDelta = "session.delta"
)

// DeviceHub fans out device stream events to live subscribers, keyed by
// device ID. It mirrors Hub (the per-run fan-out) — same single-process,
// in-memory, drop-on-slow-consumer semantics.
type DeviceHub struct {
	mu   sync.RWMutex
	subs map[string]map[*deviceSubscriber]struct{}
}

type deviceSubscriber struct {
	ch chan DeviceEvent
}

// NewDeviceHub returns an empty DeviceHub.
func NewDeviceHub() *DeviceHub {
	return &DeviceHub{subs: make(map[string]map[*deviceSubscriber]struct{})}
}

// Subscribe registers interest in a device's events. It returns a receive-only
// channel and an unsubscribe func the caller MUST invoke when done. The channel
// is buffered; if a slow consumer's buffer fills, further events for that
// subscriber are dropped (the client reconnects and replays durable events via
// after_seq; ephemeral deltas are lossy by design, docs/17 §4.4).
func (h *DeviceHub) Subscribe(deviceID string) (<-chan DeviceEvent, func()) {
	s := &deviceSubscriber{ch: make(chan DeviceEvent, 128)}
	h.mu.Lock()
	if h.subs[deviceID] == nil {
		h.subs[deviceID] = make(map[*deviceSubscriber]struct{})
	}
	h.subs[deviceID][s] = struct{}{}
	h.mu.Unlock()

	unsub := func() {
		h.mu.Lock()
		if m := h.subs[deviceID]; m != nil {
			delete(m, s)
			if len(m) == 0 {
				delete(h.subs, deviceID)
			}
		}
		h.mu.Unlock()
		close(s.ch)
	}
	return s.ch, unsub
}

// SubscriberCount returns the number of live subscribers for deviceID. Used by
// tests to synchronise on a stream having entered its live phase.
func (h *DeviceHub) SubscriberCount(deviceID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[deviceID])
}

// Publish delivers ev to every current subscriber of deviceID. Non-blocking: a
// subscriber whose buffer is full is skipped (see Subscribe).
func (h *DeviceHub) Publish(deviceID string, ev DeviceEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.subs[deviceID] {
		select {
		case s.ch <- ev:
		default:
			// drop for this slow subscriber
		}
	}
}
