package api

import (
	"context"
	"sync"
	"time"

	"github.com/cnjack/jcloud/internal/jtype"
	"golang.org/x/sync/singleflight"
)

type agentBoardMetadataCacheEntry struct {
	board     *jtype.Board
	expiresAt time.Time
	storedAt  time.Time
}

// agentBoardMetadataCache keeps live board-schema checks cheap without hiding
// dependency failures. It is deliberately short-lived and bounded; failures
// are never cached, and singleflight collapses concurrent reads for one board.
type agentBoardMetadataCache struct {
	mu      sync.Mutex
	entries map[string]agentBoardMetadataCacheEntry
	group   singleflight.Group
	ttl     time.Duration
	max     int
	now     func() time.Time
}

func newAgentBoardMetadataCache(ttl time.Duration, maxEntries int) *agentBoardMetadataCache {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if maxEntries <= 0 {
		maxEntries = 128
	}
	return &agentBoardMetadataCache{
		entries: make(map[string]agentBoardMetadataCacheEntry),
		ttl:     ttl, max: maxEntries, now: time.Now,
	}
}

func cloneAgentBoard(board *jtype.Board) *jtype.Board {
	if board == nil {
		return nil
	}
	copyValue := *board
	copyValue.Columns = append([]jtype.BoardColumn(nil), board.Columns...)
	return &copyValue
}

func (c *agentBoardMetadataCache) cached(key string) (*jtype.Board, bool) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if !now.Before(entry.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	return cloneAgentBoard(entry.board), true
}

func (c *agentBoardMetadataCache) get(
	ctx context.Context,
	key string,
	loader func(context.Context) (*jtype.Board, error),
) (*jtype.Board, error) {
	if board, ok := c.cached(key); ok {
		return board, nil
	}
	result := c.group.DoChan(key, func() (any, error) {
		if board, ok := c.cached(key); ok {
			return board, nil
		}
		board, err := loader(ctx)
		if err != nil {
			return nil, err
		}
		stored := cloneAgentBoard(board)
		now := c.now()
		c.mu.Lock()
		if len(c.entries) >= c.max {
			var oldestKey string
			var oldest time.Time
			for candidate, entry := range c.entries {
				if !now.Before(entry.expiresAt) {
					delete(c.entries, candidate)
					continue
				}
				if oldestKey == "" || entry.storedAt.Before(oldest) {
					oldestKey, oldest = candidate, entry.storedAt
				}
			}
			if len(c.entries) >= c.max && oldestKey != "" {
				delete(c.entries, oldestKey)
			}
		}
		c.entries[key] = agentBoardMetadataCacheEntry{
			board: stored, storedAt: now, expiresAt: now.Add(c.ttl),
		}
		c.mu.Unlock()
		return cloneAgentBoard(stored), nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case value := <-result:
		if value.Err != nil {
			return nil, value.Err
		}
		return cloneAgentBoard(value.Val.(*jtype.Board)), nil
	}
}
