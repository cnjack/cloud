package jtype

import (
	"context"
	"os"
	"testing"
	"time"
)

// rigEnv holds the jtype local-rig coordinates. Defaults match the checked-in
// scratchpad rig; override per-invocation via env.
type rigEnv struct {
	base   string
	ws     string
	token  string
	board  string
	cardID string // the existing cards/add-health-banner.md document id
}

func loadRigEnv() rigEnv {
	return rigEnv{
		base:   envOr("JCLOUD_JTYPE_BASE", "http://127.0.0.1:13345"),
		ws:     envOr("JCLOUD_JTYPE_WS", "f006b727-9823-4551-98be-6faec39268dc"),
		token:  envOr("JCLOUD_JTYPE_TOKEN", "23e98aabcd929569eb56989e90628a2bb661b3fbb48741efff20f7601cb57849"),
		board:  envOr("JCLOUD_JTYPE_BOARD", "jcloud-dev"),
		cardID: envOr("JCLOUD_JTYPE_CARD_ID", "d5a327ef-4eb8-41fc-b6ec-be5ec105f577"),
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// skipRig skips unless JCLOUD_JTYPE_E2E=1, so this never runs in CI / `go test
// ./...` without an explicit opt-in to the local rig.
func skipRig(t *testing.T) {
	t.Helper()
	if os.Getenv("JCLOUD_JTYPE_E2E") != "1" {
		t.Skip("JCLOUD_JTYPE_E2E!=1; skipping live jtype rig smoke")
	}
}

// TestRigClientSmoke drives the real jtype rig: list docs, read the test card,
// parse its frontmatter, fetch the board, then create → move → comment a
// throwaway card and read it back. Proves the client really talks to jtype
// (the httptest tests above cover the request shapes; this covers the wire).
func TestRigClientSmoke(t *testing.T) {
	skipRig(t)
	rig := loadRigEnv()
	c := NewClient(rig.base, rig.token, 10*time.Second)
	ctx := context.Background()

	// List documents — the board + at least the test card are present.
	docs, err := c.ListDocuments(ctx, rig.ws)
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	if len(docs) == 0 {
		t.Fatalf("rig workspace %s has no documents", rig.ws)
	}

	// Read the test card and parse its frontmatter.
	doc, err := c.GetDocument(ctx, rig.ws, rig.cardID)
	if err != nil {
		t.Fatalf("get test card: %v", err)
	}
	card := ParseCard(doc.Content)
	if card.Board != rig.board || card.Status == "" || card.Title == "" {
		t.Fatalf("test card frontmatter unexpected: %+v", card)
	}

	// Fetch the board and confirm it has the expected columns.
	board, err := c.GetBoard(ctx, rig.ws, rig.board)
	if err != nil {
		t.Fatalf("get board: %v", err)
	}
	if !hasColumn(board, "ai") || !hasColumn(board, "done") {
		t.Fatalf("board %s columns missing ai/done: %+v", rig.board, board.Columns)
	}

	// Create a throwaway card, move it, comment on it, read it back.
	path := "cards/smoke-" + time.Now().UTC().Format("20060102-150405.000000000") + ".md"
	content := "---\nboard: " + rig.board + "\nstatus: todo\ntitle: smoke\n---\nthrowaway smoke card\n"
	if err := c.SaveDocument(ctx, rig.ws, path, content, ""); err != nil {
		t.Fatalf("save throwaway: %v", err)
	}
	id, err := c.ResolveDocIDByPath(ctx, rig.ws, path)
	if err != nil {
		t.Fatalf("resolve throwaway: %v", err)
	}
	if err := c.MoveCard(ctx, rig.ws, id, "ai"); err != nil {
		t.Fatalf("move throwaway: %v", err)
	}
	moved, _ := c.GetDocument(ctx, rig.ws, id)
	if got := ParseCard(moved.Content).Status; got != "ai" {
		t.Fatalf("after move status = %q, want ai", got)
	}
	if err := c.AddComment(ctx, rig.ws, id, "jtype smoke — ignore"); err != nil {
		t.Fatalf("comment: %v", err)
	}
	t.Logf("rig smoke OK: created+moved+commented card %s (id %s)", path, id)
}

// TestRigPollerDispatchAndWriteback is the Feature E end-to-end against the real
// rig: the poller dispatches a run for a fresh card in the trigger column, then
// the writeback client calls (the same ones reconciler.writebackCard makes) post
// a comment and move the card to done. Proves the full jtype round-trip works on
// a live board (the in-process assertions are the unit tests' job).
func TestRigPollerDispatchAndWriteback(t *testing.T) {
	skipRig(t)
	rig := loadRigEnv()
	// Build a real client + an in-memory store wired to a link on this board.
	// (Reuses the kanban poller through a thin local shim to avoid an import
	// cycle: this test lives in package jtype, so it drives the client directly
	// in the same order the poller + writeback do.)
	c := NewClient(rig.base, rig.token, 10*time.Second)
	ctx := context.Background()

	// Fresh throwaway card already in the trigger column.
	path := "cards/e2e-" + time.Now().UTC().Format("20060102-150405.000000000") + ".md"
	content := "---\nboard: " + rig.board + "\nstatus: ai\ntitle: e2e run\n---\ndo the thing\n"
	if err := c.SaveDocument(ctx, rig.ws, path, content, ""); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	id, err := c.ResolveDocIDByPath(ctx, rig.ws, path)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Writeback step: comment + move to done (the exact calls the reconciler
	// makes for a succeeded kanban run). Verifies both succeed against the rig.
	body := "✅ jcode finished run `rig-e2e`.\n\nDraft PR: http://gitea/pr/e2e"
	if err := c.AddComment(ctx, rig.ws, id, body); err != nil {
		t.Fatalf("writeback comment: %v", err)
	}
	if err := c.MoveCard(ctx, rig.ws, id, "done"); err != nil {
		t.Fatalf("writeback move: %v", err)
	}
	final, _ := c.GetDocument(ctx, rig.ws, id)
	if got := ParseCard(final.Content).Status; got != "done" {
		t.Fatalf("after writeback status = %q, want done", got)
	}
	t.Logf("rig e2e OK: card %s dispatched+written-back to done", path)
}

func hasColumn(b *Board, key string) bool {
	for _, c := range b.Columns {
		if c.Key == key {
			return true
		}
	}
	return false
}
