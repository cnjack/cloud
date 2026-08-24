package api

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/jtype"
)

func TestAgentBoardMetadataCacheCoalescesAndDoesNotCacheErrors(t *testing.T) {
	cache := newAgentBoardMetadataCache(30*time.Second, 8)
	var calls atomic.Int32
	start := make(chan struct{})
	loader := func(context.Context) (*jtype.Board, error) {
		calls.Add(1)
		<-start
		return &jtype.Board{ID: "board", Columns: []jtype.BoardColumn{{Key: "todo"}}}, nil
	}

	const readers = 8
	var wg sync.WaitGroup
	wg.Add(readers)
	results := make([]*jtype.Board, readers)
	for i := range readers {
		go func(index int) {
			defer wg.Done()
			results[index], _ = cache.get(context.Background(), "installation|1|workspace|delivery.board", loader)
		}(i)
	}
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(start)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("coalesced loader calls=%d want 1", calls.Load())
	}
	for i := range results {
		if results[i] == nil || results[i].ID != "board" {
			t.Fatalf("result[%d]=%+v", i, results[i])
		}
	}
	if _, err := cache.get(context.Background(), "installation|1|workspace|delivery.board", loader); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("cache hit loader calls=%d want 1", calls.Load())
	}

	boom := errors.New("upstream unavailable")
	errorCalls := 0
	for range 2 {
		_, err := cache.get(context.Background(), "installation|1|workspace|broken.board", func(context.Context) (*jtype.Board, error) {
			errorCalls++
			return nil, boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("error=%v want %v", err, boom)
		}
	}
	if errorCalls != 2 {
		t.Fatalf("errors were cached: calls=%d want 2", errorCalls)
	}
}

func TestAgentBoardMetadataCacheIsBounded(t *testing.T) {
	cache := newAgentBoardMetadataCache(time.Minute, 2)
	for _, key := range []string{"a", "b", "c"} {
		if _, err := cache.get(context.Background(), key, func(context.Context) (*jtype.Board, error) {
			return &jtype.Board{ID: key}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) > 2 {
		t.Fatalf("cache entries=%d want <=2", len(cache.entries))
	}
}
