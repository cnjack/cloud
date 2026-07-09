package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/modelcfg"
	"github.com/cnjack/jcloud/internal/schedule"
)

// schedulesURL is the service-scoped collection endpoint.
func (f kanbanFixture) schedulesURL() string {
	return f.ts.URL + "/api/v1/services/" + f.serviceID + "/schedules"
}

func (f kanbanFixture) scheduleURL(id string) string {
	return f.ts.URL + "/api/v1/schedules/" + id
}

// createSchedule POSTs a schedule as the given role and returns the decoded row.
func createSchedule(t *testing.T, f kanbanFixture, role string, body map[string]any) (*http.Response, domain.Schedule) {
	t.Helper()
	resp := do(t, http.MethodPost, f.schedulesURL(), f.tokens[role], body)
	var sc domain.Schedule
	if resp.StatusCode == http.StatusCreated {
		decode(t, resp, &sc)
	}
	return resp, sc
}

// TestScheduleCRUD_OwnerFlow: owner creates, lists, patches, deletes a schedule;
// the returned row echoes cron/prompt/enabled and an (empty) last_error.
func TestScheduleAPI_OwnerCRUD(t *testing.T) {
	f := setupKanban(t, fakeBoardValidator{})

	resp, sc := createSchedule(t, f, "owner", map[string]any{
		"cron_expr": "0 9 * * 1-5", "prompt": "morning standup notes",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create want 201, got %d", resp.StatusCode)
	}
	if sc.CronExpr != "0 9 * * 1-5" || sc.Prompt != "morning standup notes" || !sc.Enabled {
		t.Fatalf("created schedule wrong: %+v", sc)
	}
	if sc.ServiceID != f.serviceID {
		t.Fatalf("schedule service = %q, want %q", sc.ServiceID, f.serviceID)
	}

	// List (owner) → 1 row.
	resp = do(t, http.MethodGet, f.schedulesURL(), f.tokens["owner"], nil)
	var list struct {
		Schedules []domain.Schedule `json:"schedules"`
	}
	decode(t, resp, &list)
	if len(list.Schedules) != 1 {
		t.Fatalf("list = %d schedules, want 1", len(list.Schedules))
	}

	// PATCH cron + prompt + enabled.
	resp = do(t, http.MethodPatch, f.scheduleURL(sc.ID), f.tokens["owner"], map[string]any{
		"cron_expr": "0 0 * * *", "prompt": "nightly", "enabled": false,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch want 200, got %d", resp.StatusCode)
	}
	var patched domain.Schedule
	decode(t, resp, &patched)
	if patched.CronExpr != "0 0 * * *" || patched.Prompt != "nightly" || patched.Enabled {
		t.Fatalf("patch not applied: %+v", patched)
	}

	// DELETE (owner) → 200, then list empty.
	resp = do(t, http.MethodDelete, f.scheduleURL(sc.ID), f.tokens["owner"], nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete want 200, got %d", resp.StatusCode)
	}
	resp = do(t, http.MethodGet, f.schedulesURL(), f.tokens["owner"], nil)
	decode(t, resp, &list)
	if len(list.Schedules) != 0 {
		t.Fatalf("after delete list = %d, want 0", len(list.Schedules))
	}
}

// TestScheduleAPI_CronValidation: invalid and too-frequent crons are fail-visible
// 400s with typed codes, at create AND patch.
func TestScheduleAPI_CronValidation(t *testing.T) {
	f := setupKanban(t, fakeBoardValidator{})

	cases := []struct {
		cron string
		code string
	}{
		{"not a cron", "invalid_cron"},
		{"* * * * *", "cron_too_frequent"},
		{"*/2 * * * *", "cron_too_frequent"},
	}
	for _, c := range cases {
		resp := do(t, http.MethodPost, f.schedulesURL(), f.tokens["owner"],
			map[string]any{"cron_expr": c.cron, "prompt": "p"})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("cron %q: status %d, want 400", c.cron, resp.StatusCode)
		}
		var body errorBody
		decode(t, resp, &body)
		if body.Error.Code != c.code {
			t.Fatalf("cron %q: code %q, want %q", c.cron, body.Error.Code, c.code)
		}
	}

	// Missing prompt is a bad_request.
	resp := do(t, http.MethodPost, f.schedulesURL(), f.tokens["owner"],
		map[string]any{"cron_expr": "0 * * * *"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing prompt: status %d, want 400", resp.StatusCode)
	}

	// PATCH to an invalid cron is likewise rejected.
	_, sc := createSchedule(t, f, "owner", map[string]any{"cron_expr": "0 * * * *", "prompt": "p"})
	resp = do(t, http.MethodPatch, f.scheduleURL(sc.ID), f.tokens["owner"],
		map[string]any{"cron_expr": "*/1 * * * *"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("patch invalid cron: status %d, want 400", resp.StatusCode)
	}
	var body errorBody
	decode(t, resp, &body)
	if body.Error.Code != "cron_too_frequent" {
		t.Fatalf("patch cron code = %q, want cron_too_frequent", body.Error.Code)
	}
}

// TestScheduleAPI_RBAC: create/patch/delete are owner-only; listing is member+
// (a viewer/stranger cannot read).
func TestScheduleAPI_RBAC(t *testing.T) {
	f := setupKanban(t, fakeBoardValidator{})

	// Owner seeds one schedule.
	_, sc := createSchedule(t, f, "owner", map[string]any{"cron_expr": "0 * * * *", "prompt": "p"})

	// A member may NOT create (owner-only management).
	if resp, _ := createSchedule(t, f, "member", map[string]any{"cron_expr": "0 0 * * *", "prompt": "x"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member create: status %d, want 403", resp.StatusCode)
	}

	// A member CAN read the list.
	if resp := do(t, http.MethodGet, f.schedulesURL(), f.tokens["member"], nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("member list: status %d, want 200", resp.StatusCode)
	}
	// A viewer (below member) cannot read (design §5.7 is member+).
	if resp := do(t, http.MethodGet, f.schedulesURL(), f.tokens["viewer"], nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer list: status %d, want 403", resp.StatusCode)
	}
	// A stranger (no membership) cannot read.
	if resp := do(t, http.MethodGet, f.schedulesURL(), f.tokens["stranger"], nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("stranger list: status %d, want 403", resp.StatusCode)
	}

	// A member may NOT patch or delete.
	if resp := do(t, http.MethodPatch, f.scheduleURL(sc.ID), f.tokens["member"], map[string]any{"enabled": false}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member patch: status %d, want 403", resp.StatusCode)
	}
	if resp := do(t, http.MethodDelete, f.scheduleURL(sc.ID), f.tokens["member"], nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member delete: status %d, want 403", resp.StatusCode)
	}

	// A nonexistent schedule is a 404 for the owner (not a 403/500).
	if resp := do(t, http.MethodDelete, f.scheduleURL("does-not-exist"), f.tokens["owner"], nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete missing: status %d, want 404", resp.StatusCode)
	}
}

// scheduleStubModels satisfies schedule.ModelResolver for the C1 regression
// tests below (always selects a model, so only the window logic gates dispatch).
type scheduleStubModels struct{}

func (scheduleStubModels) SelectModel(context.Context, string, string, string) (modelcfg.Selection, modelcfg.SelectOutcome, error) {
	return modelcfg.Selection{ModelID: "m", ModelName: "p/m"}, modelcfg.SelectOK, nil
}

// tickSchedules drives one real poller pass against the fixture's store.
func tickSchedules(t *testing.T, f kanbanFixture) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	schedule.NewPoller(f.st, scheduleStubModels{}, nil, log, time.Minute).Tick(context.Background())
}

// TestScheduleAPI_CronChangeDoesNotBackfill (C1): editing the cron expression
// must not make an already-past boundary (computed off the OLD last_fired_at
// baseline) look due — the PATCH resets the window to the edit instant, so the
// first fire of the NEW expression is computed from now and nothing dispatches
// immediately.
func TestScheduleAPI_CronChangeDoesNotBackfill(t *testing.T) {
	f := setupKanban(t, fakeBoardValidator{})
	ctx := context.Background()

	_, sc := createSchedule(t, f, "owner", map[string]any{"cron_expr": "0 * * * *", "prompt": "p"})
	// Simulate an old fire baseline: last window claimed two hours ago.
	twoHoursAgo := time.Now().UTC().Add(-2 * time.Hour)
	if won, err := f.st.AdvanceSchedule(ctx, sc.ID, nil, twoHoursAgo, ""); err != nil || !won {
		t.Fatalf("backdate last_fired_at: won=%v err=%v", won, err)
	}

	// A prompt-only PATCH does NOT reset the window (nothing about the cadence
	// changed) — the old baseline is retained.
	resp := do(t, http.MethodPatch, f.scheduleURL(sc.ID), f.tokens["owner"], map[string]any{"prompt": "p2"})
	var afterPrompt domain.Schedule
	decode(t, resp, &afterPrompt)
	if afterPrompt.LastFiredAt == nil || !afterPrompt.LastFiredAt.Equal(twoHoursAgo) {
		t.Fatalf("prompt-only patch moved last_fired_at: %v (want %v)", afterPrompt.LastFiredAt, twoHoursAgo)
	}

	// Changing the cron RESETS the window to the edit instant…
	resp = do(t, http.MethodPatch, f.scheduleURL(sc.ID), f.tokens["owner"], map[string]any{"cron_expr": "*/10 * * * *"})
	var patched domain.Schedule
	decode(t, resp, &patched)
	if patched.LastFiredAt == nil || time.Since(*patched.LastFiredAt) > time.Minute {
		t.Fatalf("cron change did not reset last_fired_at to now: %v", patched.LastFiredAt)
	}
	// …so a poller tick right after the edit dispatches NOTHING (the old baseline
	// + */10 would have made 2h-ago+10m look due and fired a catch-up run).
	tickSchedules(t, f)
	runs, err := f.st.ListRunsByService(ctx, f.serviceID, 100)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("cron edit backfilled %d runs, want 0 (first fire computes from the edit instant)", len(runs))
	}
}

// TestScheduleAPI_ReenableDoesNotBackfill (C1): flipping enabled false→true
// resets the window to the re-enable instant — a schedule disabled for hours
// must not fire the moment it is switched back on.
func TestScheduleAPI_ReenableDoesNotBackfill(t *testing.T) {
	f := setupKanban(t, fakeBoardValidator{})
	ctx := context.Background()

	_, sc := createSchedule(t, f, "owner", map[string]any{"cron_expr": "0 * * * *", "prompt": "p"})
	// Disable (true→false: no reset), then backdate the baseline two hours.
	resp := do(t, http.MethodPatch, f.scheduleURL(sc.ID), f.tokens["owner"], map[string]any{"enabled": false})
	var disabled domain.Schedule
	decode(t, resp, &disabled)
	if disabled.LastFiredAt != nil {
		t.Fatalf("disable moved last_fired_at: %v", disabled.LastFiredAt)
	}
	twoHoursAgo := time.Now().UTC().Add(-2 * time.Hour)
	if won, err := f.st.AdvanceSchedule(ctx, sc.ID, nil, twoHoursAgo, ""); err != nil || !won {
		t.Fatalf("backdate last_fired_at: won=%v err=%v", won, err)
	}

	// Re-enable: the window resets to the re-enable instant…
	resp = do(t, http.MethodPatch, f.scheduleURL(sc.ID), f.tokens["owner"], map[string]any{"enabled": true})
	var reenabled domain.Schedule
	decode(t, resp, &reenabled)
	if !reenabled.Enabled {
		t.Fatalf("re-enable not applied: %+v", reenabled)
	}
	if reenabled.LastFiredAt == nil || time.Since(*reenabled.LastFiredAt) > time.Minute {
		t.Fatalf("re-enable did not reset last_fired_at to now: %v", reenabled.LastFiredAt)
	}
	// …so the next tick dispatches nothing (hourly boundaries missed while
	// disabled are dropped, exactly like the restart no-backfill rule).
	tickSchedules(t, f)
	runs, err := f.st.ListRunsByService(ctx, f.serviceID, 100)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("re-enable backfilled %d runs, want 0 (first fire computes from the re-enable instant)", len(runs))
	}
}
