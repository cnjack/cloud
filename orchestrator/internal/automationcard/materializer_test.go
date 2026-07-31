package automationcard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
	"github.com/cnjack/jcloud/internal/store"
)

type fakeDocuments struct {
	docs      []jtype.Doc
	content   map[string]string
	saveCalls int
	listCalls int
	listError map[int]error
	saveError error
}

func (f *fakeDocuments) ListDocuments(context.Context, string) ([]jtype.Doc, error) {
	f.listCalls++
	if err := f.listError[f.listCalls]; err != nil {
		return nil, err
	}
	return append([]jtype.Doc(nil), f.docs...), nil
}
func (f *fakeDocuments) GetDocument(_ context.Context, _ string, id string) (*jtype.Document, error) {
	body, ok := f.content[id]
	if !ok {
		return nil, errors.New("missing")
	}
	for _, doc := range f.docs {
		if doc.ID == id {
			return &jtype.Document{Path: doc.Path, Content: body}, nil
		}
	}
	return nil, errors.New("missing")
}
func (f *fakeDocuments) SaveDocument(_ context.Context, _ string, path, content, _ string) error {
	f.saveCalls++
	id := "doc-created"
	f.docs = append(f.docs, jtype.Doc{ID: id, Path: path, Title: "Card"})
	f.content[id] = content
	return f.saveError
}

func materializerFixture(t *testing.T) (*Materializer, domain.AutomationExecution, *fakeDocuments) {
	t.Helper()
	ctx := context.Background()
	st := store.NewMemStore()
	now := time.Now().UTC()
	project := &domain.Project{ID: "project", Name: "P", CreatedAt: now}
	service := &domain.Service{
		ID: "service", ProjectID: project.ID, Name: "S",
		RepoKind: domain.RepoKindRaw, RawRepoURL: "u", DefaultBranch: "main", CreatedAt: now,
	}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	cfg := &domain.ProviderConfig{
		Provider: domain.PluginJType, BaseURL: "https://jtype.example",
		PluginEnabled: true, ConfigRevision: 1, UpdatedAt: now,
	}
	if err := st.UpsertProviderConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	installation := &domain.PluginInstallation{
		ID: "installation", ProjectID: project.ID, Provider: domain.PluginJType,
		Status: domain.PluginStatusEnabled, WorkspaceID: "workspace",
		AccessTokenEnc: []byte("token"), ConfigRevision: cfg.ConfigRevision,
		ConsentVersion: "v1", ConsentedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreatePluginInstallation(ctx, installation); err != nil {
		t.Fatal(err)
	}
	automation := &domain.PluginAutomation{
		ID: "kanban", ServiceID: service.ID, InstallationID: installation.ID,
		Name: "Service Kanban", TriggerKind: "kanban", PromptTemplate: "{{card}}",
		Enabled: true, CreatedAt: now,
	}
	trigger := &domain.KanbanTrigger{
		AutomationID: automation.ID, InstallationID: installation.ID,
		BoardRef: "board-id", TriggerColumn: "agent",
	}
	if err := st.CreatePluginAutomation(ctx, automation, nil, nil, trigger, nil); err != nil {
		t.Fatal(err)
	}
	api := &fakeDocuments{content: map[string]string{}, listError: map[int]error{}}
	materializer := New(st, func(value []byte) (string, error) { return string(value), nil })
	materializer.clientFor = func(_, _ string) documentAPI { return api }
	execution := domain.AutomationExecution{
		ID: "execution", AutomationID: "cron", AutomationName: "Issue sweep",
		PromptSnapshot: "Triage open issues.", ProjectID: project.ID, ServiceID: service.ID,
		OutputMode: domain.AutomationOutputCreateCard,
		CardPath:   DeterministicPath("cron", "execution"),
	}
	return materializer, execution, api
}

func TestMaterializeCreatesDeterministicCardInTriggerColumn(t *testing.T) {
	materializer, execution, api := materializerFixture(t)
	result := materializer.Materialize(context.Background(), execution, true)
	if result.CardState != "bound" || result.DocumentID != "doc-created" || api.saveCalls != 1 {
		t.Fatalf("result=%+v saves=%d", result, api.saveCalls)
	}
	body := api.content[result.DocumentID]
	for _, wanted := range []string{
		`board: "board-id"`, `status: "agent"`,
		executionMarker(execution.ID), execution.PromptSnapshot,
	} {
		if !strings.Contains(body, wanted) {
			t.Fatalf("card body missing %q:\n%s", wanted, body)
		}
	}
}

func TestMaterializeRecoversExistingMarkerWithoutSaving(t *testing.T) {
	materializer, execution, api := materializerFixture(t)
	api.docs = []jtype.Doc{{ID: "existing", Path: execution.CardPath}}
	api.content["existing"] = "---\nstatus: agent\n---\n" + executionMarker(execution.ID)
	result := materializer.Materialize(context.Background(), execution, false)
	if result.CardState != "bound" || result.DocumentID != "existing" || api.saveCalls != 0 {
		t.Fatalf("result=%+v saves=%d", result, api.saveCalls)
	}
}

func TestMaterializeRestartWithMissingPathBlocksInsteadOfRecreating(t *testing.T) {
	materializer, execution, api := materializerFixture(t)
	result := materializer.Materialize(context.Background(), execution, false)
	if result.ReasonCode != "card_creation_uncertain" || result.CardState != "unavailable" || api.saveCalls != 0 {
		t.Fatalf("result=%+v saves=%d", result, api.saveCalls)
	}
}

func TestMaterializePathCollisionIsVisible(t *testing.T) {
	materializer, execution, api := materializerFixture(t)
	api.docs = []jtype.Doc{{ID: "other", Path: execution.CardPath}}
	api.content["other"] = "---\nstatus: todo\n---\nnot ours"
	result := materializer.Materialize(context.Background(), execution, true)
	if result.ReasonCode != "card_path_conflict" || api.saveCalls != 0 {
		t.Fatalf("result=%+v saves=%d", result, api.saveCalls)
	}
}

func TestMaterializePostSaveAmbiguityRecoversWithoutAnotherSave(t *testing.T) {
	materializer, execution, api := materializerFixture(t)
	api.listError[2] = errors.New("eventual consistency")

	first := materializer.Materialize(context.Background(), execution, true)
	if first.CardState != "creating" || first.ReasonCode != "card_creation_unconfirmed" ||
		api.saveCalls != 1 {
		t.Fatalf("first=%+v saves=%d", first, api.saveCalls)
	}

	recovered := materializer.Materialize(context.Background(), execution, false)
	if recovered.CardState != "bound" || recovered.DocumentID != "doc-created" ||
		api.saveCalls != 1 {
		t.Fatalf("recovered=%+v saves=%d", recovered, api.saveCalls)
	}
}

func TestMaterializeAmbiguousSaveRecoversWithoutAnotherSave(t *testing.T) {
	materializer, execution, api := materializerFixture(t)
	api.saveError = errors.New("response lost after save")

	first := materializer.Materialize(context.Background(), execution, true)
	if first.CardState != "creating" || first.ReasonCode != "card_write_unavailable" ||
		api.saveCalls != 1 {
		t.Fatalf("first=%+v saves=%d", first, api.saveCalls)
	}

	api.saveError = nil
	recovered := materializer.Materialize(context.Background(), execution, false)
	if recovered.CardState != "bound" || recovered.DocumentID != "doc-created" ||
		api.saveCalls != 1 {
		t.Fatalf("recovered=%+v saves=%d", recovered, api.saveCalls)
	}
}
