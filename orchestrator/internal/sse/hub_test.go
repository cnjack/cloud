package sse

import (
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func TestHubFanOut(t *testing.T) {
	h := NewHub()
	ch1, unsub1 := h.Subscribe("run1")
	ch2, unsub2 := h.Subscribe("run1")
	defer unsub1()
	defer unsub2()

	// A subscriber on a different run must not receive run1's events.
	other, unsubOther := h.Subscribe("run2")
	defer unsubOther()

	ev := domain.RunEvent{RunID: "run1", Seq: 1, Type: "agent.text"}
	h.Publish("run1", ev)

	for i, ch := range []<-chan domain.RunEvent{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Seq != 1 {
				t.Errorf("sub%d got seq %d", i, got.Seq)
			}
		case <-time.After(time.Second):
			t.Fatalf("sub%d did not receive event", i)
		}
	}

	select {
	case got := <-other:
		t.Fatalf("run2 subscriber received run1 event: %+v", got)
	case <-time.After(50 * time.Millisecond):
		// expected: nothing
	}
}

func TestHubUnsubscribe(t *testing.T) {
	h := NewHub()
	ch, unsub := h.Subscribe("run1")
	unsub()
	// Channel should be closed after unsub.
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after unsubscribe")
	}
	// Publishing after unsub must not panic.
	h.Publish("run1", domain.RunEvent{RunID: "run1", Seq: 1})
}

func TestHubDropsOnFullBuffer(t *testing.T) {
	h := NewHub()
	ch, unsub := h.Subscribe("run1")
	defer unsub()
	// Overfill the 128-buffer without draining; Publish must not block.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			h.Publish("run1", domain.RunEvent{RunID: "run1", Seq: int64(i)})
		}
		close(done)
	}()
	select {
	case <-done:
		// good: never blocked
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber buffer")
	}
	_ = ch
}
