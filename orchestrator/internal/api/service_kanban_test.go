package api

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/config"
	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
	"github.com/cnjack/jcloud/internal/sse"
	"github.com/cnjack/jcloud/internal/store"
)

type serviceKanbanBoardValidator struct{ board *jtype.Board }

func (v serviceKanbanBoardValidator) GetBoard(context.Context, string, string) (*jtype.Board, error) {
	return v.board, nil
}

type serviceKanbanProxy struct {
	status int
	body   string
	bodies []string
	calls  int
}

func (p *serviceKanbanProxy) ProxyDocumentAPI(context.Context, string, string, io.Reader) (*http.Response, error) {
	p.calls++
	body := p.body
	if len(p.bodies) > 0 {
		body = p.bodies[0]
		p.bodies = p.bodies[1:]
	}
	return &http.Response{
		StatusCode: p.status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func newServiceKanbanServer(t *testing.T) (*httptest.Server, *store.MemStore, *Server) {
	t.Helper()
	st := store.NewMemStore()
	cfg := withTestModel(&config.Config{ConsoleToken: consoleToken, MasterKey: validTokenKey(t)})
	srv := New(st, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), sse.NewHub(), nil)
	srv.boardValidatorFor = func(*jtype.Factory, string) boardValidator {
		return serviceKanbanBoardValidator{board: &jtype.Board{
			ID:      "b_board",
			Columns: []jtype.BoardColumn{{Key: "ai"}, {Key: "done"}},
		}}
	}
	ts := httptest.NewServer(srv.Handler())
	registerTestServerStore(t, ts, st)
	t.Cleanup(ts.Close)
	return ts, st, srv
}

func TestServiceKanbanUsesDefaultTriggerAndStaysOutOfAutomations(t *testing.T) {
	ts, st, srv := newServiceKanbanServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	project := &domain.Project{ID: domain.NewID(), Name: "service-kanban", CreatedAt: now}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	service := &domain.Service{ID: domain.NewID(), ProjectID: project.ID, Name: "repo", RepoKind: domain.RepoKindProvider, Provider: domain.ProviderGitHub, RepoOwnerName: "acme/repo", DefaultBranch: "main", CreatedAt: now}
	if err := st.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProviderConfig(ctx, &domain.ProviderConfig{Provider: domain.PluginJType, BaseURL: "https://jtype.test", PluginEnabled: true, ConfigRevision: 1}); err != nil {
		t.Fatal(err)
	}
	sealed, err := srv.Cipher().EncryptString("jtype-secret-that-must-not-leak")
	if err != nil {
		t.Fatal(err)
	}
	installation := &domain.PluginInstallation{ID: domain.NewID(), ProjectID: project.ID, Provider: domain.PluginJType, Status: domain.PluginStatusEnabled, WorkspaceID: "workspace-1", AccessTokenEnc: sealed, ConfigRevision: 1, ConsentedAt: now, CreatedAt: now}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}

	// Workspace browsing is granted by the healthy Project Plugin before any
	// Service trigger exists.
	proxy := &serviceKanbanProxy{status: http.StatusOK, body: `[]`}
	srv.boardProxyFor = func(*jtype.Factory, string) jtypeBoardProxy { return proxy }
	resp := do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+project.ID+"/kanban/board/documents?workspace=workspace-1", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("browse before enable status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodPut, ts.URL+"/api/v1/services/"+service.ID+"/kanban", consoleToken, map[string]any{
		"installation_id": installation.ID,
		"board_ref":       "b_board",
		"enabled":         true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enable status=%d", resp.StatusCode)
	}
	var created domain.PluginAutomationSpec
	decode(t, resp, &created)
	if created.Automation.TriggerKind != "kanban" || created.Kanban == nil || created.Kanban.BoardRef != "b_board" || created.Kanban.TriggerColumn != "ai" || created.Kanban.DoneColumn != "done" {
		t.Fatalf("binding=%+v", created)
	}

	resp = do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+project.ID+"/automations", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("automations status=%d", resp.StatusCode)
	}
	var list struct {
		Automations []domain.PluginAutomationSpec `json:"automations"`
	}
	decode(t, resp, &list)
	if len(list.Automations) != 0 {
		t.Fatalf("implicit Kanban leaked into Automations: %+v", list.Automations)
	}
	resp = do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+project.ID+"/automations", consoleToken, map[string]any{
		"service_id": service.ID, "name": "manual", "prompt_template": "task",
		"kanban": map[string]any{"installation_id": installation.ID, "board_ref": "b_board", "trigger_column": "ai"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("public Kanban Automation status=%d, want 400", resp.StatusCode)
	}

	resp = do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+project.ID+"/kanban/board/links", consoleToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("board links status=%d", resp.StatusCode)
	}
	var links struct {
		Links []boardEmbedLinkView `json:"links"`
	}
	decode(t, resp, &links)
	if len(links.Links) != 1 || links.Links[0].ServiceID != service.ID || links.Links[0].WorkspaceID != "workspace-1" || links.Links[0].BoardRef != "b_board" {
		t.Fatalf("board links=%+v", links.Links)
	}

	// A compromised upstream cannot reflect the server-held token to a member.
	proxy.body = `{"echo":"jtype-secret-that-must-not-leak"}`
	resp = do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+project.ID+"/kanban/board/documents?workspace=workspace-1", consoleToken, nil)
	reflectedBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || strings.Contains(string(reflectedBody), "jtype-secret-that-must-not-leak") {
		t.Fatalf("reflected credential response status=%d body=%s", resp.StatusCode, reflectedBody)
	}
	proxy.body = `{"echo":"` + base64.StdEncoding.EncodeToString([]byte("jtype-secret-that-must-not-leak")) + `"}`
	resp = do(t, http.MethodGet, ts.URL+"/api/v1/projects/"+project.ID+"/kanban/board/documents?workspace=workspace-1", consoleToken, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("encoded credential response status=%d", resp.StatusCode)
	}

	// Even with a valid card body, the board proxy cannot overwrite an arbitrary
	// Markdown note outside the card namespace.
	proxy.body = `{}`
	before := proxy.calls
	resp = do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+project.ID+"/kanban/board/documents/save?workspace=workspace-1", consoleToken, map[string]any{
		"relativePath": "notes/private.md",
		"content":      "---\nboard: b_board\nstatus: ai\n---\nsecret",
	})
	if resp.StatusCode != http.StatusForbidden || proxy.calls != before {
		t.Fatalf("arbitrary note write status=%d upstream_calls=%d want=%d", resp.StatusCode, proxy.calls, before)
	}
	resp.Body.Close()

	// A member cannot overwrite a card from another board by changing its
	// frontmatter to the board enabled for this Service.
	proxy.bodies = []string{
		`[{"id":"other-card","relativePath":"cards/Other.md"}]`,
		`{"content":"---\nboard: b_other\nstatus: ai\n---\nother","contentHash":"other-hash"}`,
	}
	before = proxy.calls
	resp = do(t, http.MethodPost, ts.URL+"/api/v1/projects/"+project.ID+"/kanban/board/documents/save?workspace=workspace-1", consoleToken, map[string]any{
		"relativePath":    "cards/other.md",
		"content":         "---\nboard: b_board\nstatus: ai\n---\nhijack",
		"baseContentHash": "other-hash",
	})
	if resp.StatusCode != http.StatusForbidden || proxy.calls != before+2 {
		t.Fatalf("cross-board overwrite status=%d upstream_calls=%d want=%d", resp.StatusCode, proxy.calls, before+2)
	}
	resp.Body.Close()

	resp = do(t, http.MethodDelete, ts.URL+"/api/v1/services/"+service.ID+"/kanban", consoleToken, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("disable status=%d", resp.StatusCode)
	}
}
