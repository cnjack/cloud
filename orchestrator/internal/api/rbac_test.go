package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

// rbacFixture sets up one project with an owner/member/viewer plus an admin, a
// stranger, and the service principal, and returns their bearer tokens.
type rbacFixture struct {
	ts        *httptest.Server
	st        *store.MemStore
	projectID string
	target    *domain.User
	tokens    map[string]string // role name -> bearer token ("" for service uses consoleToken)
}

func setupRBAC(t *testing.T) rbacFixture {
	t.Helper()
	ts, st, _ := newTestServer(t)

	// The FIRST user becomes cluster admin (store semantics).
	admin := mkUser(t, st, "admin")
	owner := mkUser(t, st, "owner")
	member := mkUser(t, st, "member")
	viewer := mkUser(t, st, "viewer")
	stranger := mkUser(t, st, "stranger")
	target := mkUser(t, st, "target") // used as add-member subject

	tokens := map[string]string{
		"admin":    mkSession(t, st, admin.ID),
		"owner":    mkSession(t, st, owner.ID),
		"member":   mkSession(t, st, member.ID),
		"viewer":   mkSession(t, st, viewer.ID),
		"stranger": mkSession(t, st, stranger.ID),
		"service":  consoleToken,
	}

	// Owner creates the project (becomes owner member) with a default service.
	resp := do(t, "POST", ts.URL+"/api/v1/projects", tokens["owner"], map[string]any{
		"name": "rbac", "repo_url": "https://git/x.git",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("owner create project: status=%d want 201", resp.StatusCode)
	}
	var pv projectView
	decode(t, resp, &pv)
	if pv.Role != "owner" {
		t.Fatalf("creator role=%q want owner", pv.Role)
	}

	// Owner adds member + viewer.
	for uid, role := range map[string]string{member.ID: "member", viewer.ID: "viewer"} {
		r := do(t, "POST", ts.URL+"/api/v1/projects/"+pv.ID+"/members", tokens["owner"],
			map[string]any{"user_id": uid, "role": role})
		if r.StatusCode != http.StatusOK {
			t.Fatalf("add %s: status=%d want 200", role, r.StatusCode)
		}
		r.Body.Close()
	}

	return rbacFixture{ts: ts, st: st, projectID: pv.ID, target: target, tokens: tokens}
}

func TestRBACMatrix(t *testing.T) {
	f := setupRBAC(t)
	pid := f.projectID

	// Each action returns the HTTP status for a given bearer token.
	type action struct {
		name string
		run  func(tok string) int
	}
	actions := []action{
		{"viewProject", func(tok string) int {
			r := do(t, "GET", f.ts.URL+"/api/v1/projects/"+pid, tok, nil)
			defer r.Body.Close()
			return r.StatusCode
		}},
		{"listServices", func(tok string) int {
			r := do(t, "GET", f.ts.URL+"/api/v1/projects/"+pid+"/services", tok, nil)
			defer r.Body.Close()
			return r.StatusCode
		}},
		{"createRun", func(tok string) int {
			r := do(t, "POST", f.ts.URL+"/api/v1/projects/"+pid+"/runs", tok, map[string]any{"prompt": "hi"})
			defer r.Body.Close()
			return r.StatusCode
		}},
		{"patchProject", func(tok string) int {
			r := do(t, "PATCH", f.ts.URL+"/api/v1/projects/"+pid, tok, map[string]any{"name": "renamed"})
			defer r.Body.Close()
			return r.StatusCode
		}},
		{"manageMembers", func(tok string) int {
			r := do(t, "POST", f.ts.URL+"/api/v1/projects/"+pid+"/members", tok,
				map[string]any{"user_id": f.target.ID, "role": "viewer"})
			defer r.Body.Close()
			return r.StatusCode
		}},
	}

	// Expected status per action per role.
	ok := func(view, createRun, ownerOnly int) map[string]int {
		return map[string]int{
			"viewProject":   view,
			"listServices":  view,
			"createRun":     createRun,
			"patchProject":  ownerOnly,
			"manageMembers": ownerOnly,
		}
	}
	// view=200; createRun success=201; ownerOnly success=200.
	forbidden := http.StatusForbidden
	want := map[string]map[string]int{
		"admin":    ok(200, 201, 200),
		"owner":    ok(200, 201, 200),
		"member":   ok(200, 201, forbidden),
		"viewer":   ok(200, forbidden, forbidden),
		"stranger": ok(forbidden, forbidden, forbidden),
		"service":  ok(200, 201, 200),
	}

	for role, tok := range f.tokens {
		for _, a := range actions {
			got := a.run(tok)
			exp := want[role][a.name]
			if got != exp {
				t.Errorf("role=%s action=%s: status=%d want %d", role, a.name, got, exp)
			}
		}
	}
}

// TestListProjectsScopedToMembership: a cluster-admin sees every project; a
// stranger sees none of the ones they are not a member of.
func TestListProjectsScopedToMembership(t *testing.T) {
	f := setupRBAC(t)

	// Admin sees the project.
	adminResp := do(t, "GET", f.ts.URL+"/api/v1/projects", f.tokens["admin"], nil)
	var al struct {
		Projects []projectView `json:"projects"`
	}
	decode(t, adminResp, &al)
	if len(al.Projects) != 1 {
		t.Fatalf("admin project list len=%d want 1", len(al.Projects))
	}

	// Stranger sees none.
	strResp := do(t, "GET", f.ts.URL+"/api/v1/projects", f.tokens["stranger"], nil)
	var sl struct {
		Projects []projectView `json:"projects"`
	}
	decode(t, strResp, &sl)
	if len(sl.Projects) != 0 {
		t.Fatalf("stranger project list len=%d want 0", len(sl.Projects))
	}

	// Member sees it, with role=member.
	memResp := do(t, "GET", f.ts.URL+"/api/v1/projects", f.tokens["member"], nil)
	var ml struct {
		Projects []projectView `json:"projects"`
	}
	decode(t, memResp, &ml)
	if len(ml.Projects) != 1 || ml.Projects[0].Role != "member" {
		t.Fatalf("member project list = %+v want 1 with role member", ml.Projects)
	}
}

// TestTriggeredByRecordedForUser: a run created by a user records
// triggered_by_user_id; one created by the service principal leaves it null.
func TestTriggeredByRecordedForUser(t *testing.T) {
	f := setupRBAC(t)
	ctx := context.Background()

	// Member triggers a run.
	r := do(t, "POST", f.ts.URL+"/api/v1/projects/"+f.projectID+"/runs", f.tokens["member"],
		map[string]any{"prompt": "task"})
	var run domain.Run
	decode(t, r, &run)
	got, _ := f.st.GetRun(ctx, run.ID)
	if got.TriggeredByUserID == nil {
		t.Fatal("member-triggered run should record triggered_by_user_id")
	}

	// Service principal triggers a run => null triggered_by.
	r2 := do(t, "POST", f.ts.URL+"/api/v1/projects/"+f.projectID+"/runs", consoleToken,
		map[string]any{"prompt": "task2"})
	var run2 domain.Run
	decode(t, r2, &run2)
	got2, _ := f.st.GetRun(ctx, run2.ID)
	if got2.TriggeredByUserID != nil {
		t.Fatalf("service-triggered run triggered_by=%v want nil", *got2.TriggeredByUserID)
	}
}

// TestCannotRemoveLastOwner: removing the sole owner is a 409; after promoting a
// second owner the original can be removed.
func TestCannotRemoveLastOwner(t *testing.T) {
	f := setupRBAC(t)
	pid := f.projectID

	// Find the owner user id from the members list (as admin).
	lr := do(t, "GET", f.ts.URL+"/api/v1/projects/"+pid+"/members", f.tokens["admin"], nil)
	var ml struct {
		Members []memberView `json:"members"`
	}
	decode(t, lr, &ml)
	var ownerID string
	for _, m := range ml.Members {
		if m.Role == "owner" {
			ownerID = m.UserID
		}
	}
	if ownerID == "" {
		t.Fatal("no owner found in members")
	}

	// Removing the last owner => 409.
	del := do(t, "DELETE", f.ts.URL+"/api/v1/projects/"+pid+"/members/"+ownerID, f.tokens["admin"], nil)
	if del.StatusCode != http.StatusConflict {
		t.Fatalf("remove last owner: status=%d want 409", del.StatusCode)
	}
	del.Body.Close()

	// Promote the target user to owner, then removing the original owner works.
	pr := do(t, "POST", f.ts.URL+"/api/v1/projects/"+pid+"/members", f.tokens["admin"],
		map[string]any{"user_id": f.target.ID, "role": "owner"})
	pr.Body.Close()
	del2 := do(t, "DELETE", f.ts.URL+"/api/v1/projects/"+pid+"/members/"+ownerID, f.tokens["admin"], nil)
	if del2.StatusCode != http.StatusNoContent {
		t.Fatalf("remove non-last owner: status=%d want 204", del2.StatusCode)
	}
	del2.Body.Close()
}

// TestAddMemberByProviderUsername exercises the {provider, username} add form.
func TestAddMemberByProviderUsername(t *testing.T) {
	f := setupRBAC(t)
	// "target" was created with a gitea identity username "target".
	r := do(t, "POST", f.ts.URL+"/api/v1/projects/"+f.projectID+"/members", f.tokens["owner"],
		map[string]any{"provider": "gitea", "username": "target", "role": "member"})
	if r.StatusCode != http.StatusOK {
		t.Fatalf("add by username: status=%d want 200", r.StatusCode)
	}
	var mv memberView
	decode(t, r, &mv)
	if mv.UserID != f.target.ID || mv.Role != "member" {
		t.Fatalf("added member = %+v want target as member", mv)
	}
}
