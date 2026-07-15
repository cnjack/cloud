package kanban

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
	"github.com/cnjack/jcloud/internal/store"
)

// TestRigPollerDispatches is the Feature E happy path against the real jtype
// rig: seed a card in the trigger column, run one poller tick, and assert it
// dispatched a kanban-origin run + stamped the claim. (The reconciler writeback
// half is unit-tested in internal/reconciler, and its jtype wire calls are
// proven live by TestRigPollerDispatchAndWriteback in internal/jtype.)
//
// Skips unless JCLOUD_JTYPE_E2E=1 so `go test ./...` never needs the rig.
func TestRigPollerDispatches(t *testing.T) {
	if os.Getenv("JCLOUD_JTYPE_E2E") != "1" {
		t.Skip("JCLOUD_JTYPE_E2E!=1; skipping live jtype rig poller smoke")
	}
	base := envOrRig("JCLOUD_JTYPE_BASE", "http://127.0.0.1:13345")
	ws := envOrRig("JCLOUD_JTYPE_WS", "f006b727-9823-4551-98be-6faec39268dc")
	tok := envOrRig("JCLOUD_JTYPE_TOKEN", "23e98aabcd929569eb56989e90628a2bb661b3fbb48741efff20f7601cb57849")
	board := envOrRig("JCLOUD_JTYPE_BOARD", "jcloud-dev")

	ctx := context.Background()
	client := jtype.NewClient(base, tok, 10*time.Second)

	// Seed a fresh card already in the trigger (ai) column.
	path := os.Getenv("JCLOUD_JTYPE_CARD_PATH")
	if path == "" {
		path = "cards/poller-e2e-" + time.Now().UTC().Format("20060102-150405.000000000") + ".md"
	}
	content := "---\nboard: " + board + "\nstatus: ai\ntitle: poller e2e\n---\nbody of the card\n"
	if err := client.SaveDocument(ctx, ws, path, content, ""); err != nil {
		t.Fatalf("seed card: %v", err)
	}

	// MemStore + project/service/link pointed at the rig board.
	st := store.NewMemStore()
	p := &domain.Project{ID: domain.NewID(), Name: "rig", CreatedAt: time.Now().UTC()}
	_ = st.CreateProject(ctx, p)
	svc := &domain.Service{ID: domain.NewID(), ProjectID: p.ID, Name: "default",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: time.Now().UTC()}
	_ = st.CreateService(ctx, svc)
	link := &domain.KanbanLink{ID: domain.NewID(), WorkspaceID: ws, BoardRef: board,
		ProjectID: p.ID, ServiceID: svc.ID, TriggerColumn: "ai", DoneColumn: "done",
		// Start at sequence 0 so this live rig proves the durable event endpoint,
		// rather than satisfying the assertion through the compatibility scan.
		EventSequence: int64Ptr(0), Enabled: true,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := st.CreateKanbanLink(ctx, link); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// The rig link has no per-link token; the poller resolves the rig PAT via the
	// effective cluster fallback (env source) and returns the pre-built client
	// (ignoring the resolved factory).
	clientFor := func(*jtype.Factory, string) DocumentAPI { return client }
	poller := New(st, envResolver(st, base, tok), clientFor, nil, stubFor(true), log, "http://console", time.Second)
	poller.Tick(ctx)

	// Assert by THIS card's claim (robust to leftover test cards on the shared
	// rig): its claim must be stamped with a kanban-origin run that exists.
	id, err := client.ResolveDocIDByPath(ctx, ws, path)
	if err != nil {
		t.Fatalf("resolve seeded card: %v", err)
	}
	claim, err := st.EnsureKanbanClaim(ctx, link.ID, id, path)
	if err != nil {
		t.Fatal(err)
	}
	if claim.RunID == "" {
		t.Fatalf("poller did not dispatch a run for card %s (claim run_id empty)", path)
	}
	run, err := st.GetRun(ctx, claim.RunID)
	if err != nil {
		t.Fatalf("dispatched run %s not found: %v", claim.RunID, err)
	}
	if run.Origin != domain.RunOriginKanban {
		t.Fatalf("run origin = %q, want kanban", run.Origin)
	}
	if run.Prompt != "poller e2e\n\nbody of the card" {
		t.Fatalf("run prompt = %q", run.Prompt)
	}
	gotLink, err := st.GetKanbanLink(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotLink.EventSequence == nil || *gotLink.EventSequence <= 0 {
		t.Fatalf("poller did not commit the live jtype event sequence: %v", gotLink.EventSequence)
	}
	t.Logf("rig poller OK: card %s → run %s", path, run.ID)
}

func envOrRig(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
