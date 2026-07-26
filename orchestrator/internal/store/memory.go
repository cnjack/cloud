package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// MemStore is an in-memory Store for tests. It enforces the same state-machine
// and idempotency semantics as PGStore so tests exercise real behaviour without
// a database. It is safe for concurrent use.
type MemStore struct {
	mu                       sync.Mutex
	projects                 map[string]domain.Project
	services                 map[string]domain.Service
	runs                     map[string]domain.Run
	events                   map[string][]domain.RunEvent    // keyed by runID, kept sorted by seq
	dedupe                   map[string]bool                 // keyed by runID+"|"+source+"|"+client_seq
	artifacts                map[string]domain.RunArtifact   // keyed by runID+"/"+kind
	users                    map[string]domain.User          // keyed by user id
	identities               map[string]domain.UserIdentity  // keyed by identity id
	sessions                 map[string]domain.Session       // keyed by session id
	members                  map[string]domain.ProjectMember // keyed by projectID+"|"+userID
	modelProviders           map[string]domain.ModelProvider // keyed by provider id
	models                   map[string]domain.Model         // catalog, keyed by model id (D21)
	modelGrants              map[string]bool                 // keyed by modelID+"|"+projectID
	modelAccountGrants       map[string]string               // keyed by modelID+"|"+userID, value=granting user
	integrations             map[string]domain.Integration   // keyed by integration id (D19 / F5)
	providerConfigs          map[domain.ProviderKind]domain.ProviderConfig
	providerConfigVersions   map[string]domain.ProviderConfig
	pluginInstallations      map[string]domain.PluginInstallation
	pluginCredentialVersions map[string]domain.PluginCredentialVersion
	serviceRepoBindings      map[string]domain.ServiceRepositoryBinding
	pluginAutomations        map[string]domain.PluginAutomation
	pluginSCMActions         map[string]domain.SCMAction // service|family|action
	pluginSCMTriggers        map[string]domain.SCMTrigger
	pluginKanbanTriggers     map[string]domain.KanbanTrigger
	pluginKanbanClaims       map[string]domain.PluginKanbanClaim
	pluginCronTriggers       map[string]domain.CronTrigger
	webhookReceipts          map[string]domain.WebhookReceipt // provider|delivery id
	runPluginSnapshots       map[string]map[string]domain.RunPluginSnapshot
	pluginAuditEvents        map[string]domain.PluginAuditEvent
	clusterSettings          *domain.ClusterSettings
	kanbanLinks              map[string]domain.KanbanLink            // keyed by link id
	kanbanClaims             map[string]domain.KanbanClaim           // keyed by linkID+"|"+documentID
	schedules                map[string]domain.Schedule              // keyed by schedule id (F11 / D24)
	automations              map[string]domain.Automation            // keyed by automation id
	webhookBindings          map[string]domain.WebhookBinding        // keyed by service id
	runMessages              map[string][]domain.RunMessage          // session follow-up queue, keyed by runID (D22)
	permissions              map[string]domain.RunPermission         // permission requests, keyed by request_id (F8b)
	apiKeys                  map[string]domain.APIKey                // keyed by api key id (F12 / D24)
	accountSettings          map[string]domain.AccountSettings       // keyed by user id (docs/19)
	accountSyncKeys          map[string]domain.AccountSyncKey        // keyed by user id (docs/19)
	accountSyncWraps         map[string]domain.AccountSyncKeyWrap    // keyed by userID+"|"+deviceID
	accountProviders         map[string]domain.AccountProviderConfig // keyed by userID+"|"+providerID
	kanbanConfig             *domain.KanbanConfig                    // single-row cluster kanban config, nil = absent (D27)
	devices                  map[string]domain.Device                // keyed by device id (docs/17)
	deviceTokens             map[string]domain.DeviceToken           // keyed by device token id
	deviceSessions           map[string]domain.DeviceSession         // keyed by deviceID+"|"+sessionID
	deviceEvents             map[string][]domain.DeviceEvent         // keyed by deviceID+"|"+sessionID, kept sorted by seq
	deviceCommands           map[string]domain.DeviceCommand         // keyed by command id
	devicePairings           map[string]domain.DevicePairing         // keyed by pairing id
	deviceOffers             map[string]domain.DevicePairingOffer    // keyed by offer id
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{
		projects:                 map[string]domain.Project{},
		services:                 map[string]domain.Service{},
		runs:                     map[string]domain.Run{},
		events:                   map[string][]domain.RunEvent{},
		dedupe:                   map[string]bool{},
		artifacts:                map[string]domain.RunArtifact{},
		users:                    map[string]domain.User{},
		identities:               map[string]domain.UserIdentity{},
		sessions:                 map[string]domain.Session{},
		members:                  map[string]domain.ProjectMember{},
		modelProviders:           map[string]domain.ModelProvider{},
		models:                   map[string]domain.Model{},
		modelGrants:              map[string]bool{},
		modelAccountGrants:       map[string]string{},
		integrations:             map[string]domain.Integration{},
		providerConfigs:          map[domain.ProviderKind]domain.ProviderConfig{},
		providerConfigVersions:   map[string]domain.ProviderConfig{},
		pluginInstallations:      map[string]domain.PluginInstallation{},
		pluginCredentialVersions: map[string]domain.PluginCredentialVersion{},
		serviceRepoBindings:      map[string]domain.ServiceRepositoryBinding{},
		pluginAutomations:        map[string]domain.PluginAutomation{},
		pluginSCMActions:         map[string]domain.SCMAction{},
		pluginSCMTriggers:        map[string]domain.SCMTrigger{},
		pluginKanbanTriggers:     map[string]domain.KanbanTrigger{},
		pluginKanbanClaims:       map[string]domain.PluginKanbanClaim{},
		pluginCronTriggers:       map[string]domain.CronTrigger{},
		webhookReceipts:          map[string]domain.WebhookReceipt{},
		runPluginSnapshots:       map[string]map[string]domain.RunPluginSnapshot{},
		pluginAuditEvents:        map[string]domain.PluginAuditEvent{},
		kanbanLinks:              map[string]domain.KanbanLink{},
		kanbanClaims:             map[string]domain.KanbanClaim{},
		schedules:                map[string]domain.Schedule{},
		automations:              map[string]domain.Automation{},
		webhookBindings:          map[string]domain.WebhookBinding{},
		runMessages:              map[string][]domain.RunMessage{},
		permissions:              map[string]domain.RunPermission{},
		apiKeys:                  map[string]domain.APIKey{},
		accountSettings:          map[string]domain.AccountSettings{},
		accountSyncKeys:          map[string]domain.AccountSyncKey{},
		accountSyncWraps:         map[string]domain.AccountSyncKeyWrap{},
		accountProviders:         map[string]domain.AccountProviderConfig{},
		devices:                  map[string]domain.Device{},
		deviceTokens:             map[string]domain.DeviceToken{},
		deviceSessions:           map[string]domain.DeviceSession{},
		deviceEvents:             map[string][]domain.DeviceEvent{},
		deviceCommands:           map[string]domain.DeviceCommand{},
		devicePairings:           map[string]domain.DevicePairing{},
		deviceOffers:             map[string]domain.DevicePairingOffer{},
	}
}

// dedupeKey builds the per-source idempotency key mirroring the DB unique index
// on (run_id, source, client_seq).
func dedupeKey(runID, source string, clientSeq int64) string {
	return runID + "|" + source + "|" + strconv.FormatInt(clientSeq, 10)
}

// maxSeqLocked returns the current highest seq for a run (0 if none). Caller
// must hold m.mu.
func (m *MemStore) maxSeqLocked(runID string) int64 {
	var max int64
	for _, e := range m.events[runID] {
		if e.Seq > max {
			max = e.Seq
		}
	}
	return max
}

func (m *MemStore) Close() {}

// --- projects ---

func (m *MemStore) CreateProject(_ context.Context, p *domain.Project) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projects[p.ID] = *p
	return nil
}

func (m *MemStore) GetProject(_ context.Context, id string) (*domain.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := p
	return &cp, nil
}

func (m *MemStore) ListProjects(_ context.Context) ([]domain.Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Project, 0, len(m.projects))
	for _, p := range m.projects {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) UpdateProject(_ context.Context, p *domain.Project) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projects[p.ID]; !ok {
		return ErrNotFound
	}
	m.projects[p.ID] = *p
	return nil
}

func (m *MemStore) DeleteProject(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projects[id]; !ok {
		return ErrNotFound
	}
	// Delete services through the same aggregate cascade as DeleteService: this
	// is how PostgreSQL reaches repository bindings, v2 Automations and their
	// typed children when projects.project_id is deleted.
	for sid, svc := range m.services {
		if svc.ProjectID == id {
			m.deleteServiceLocked(sid)
		}
	}
	// Tests may seed a run without its Service; project deletion still owns it.
	for rid, r := range m.runs {
		if r.ProjectID == id {
			m.deleteRunLocked(rid)
		}
	}
	// Project plugin installations are children of projects. The database keeps
	// receipt/audit history but clears the removed installation reference.
	for installationID, installation := range m.pluginInstallations {
		if installation.ProjectID != id {
			continue
		}
		m.clearPluginInstallationReferencesLocked(installationID)
		delete(m.pluginInstallations, installationID)
	}
	for auditID, event := range m.pluginAuditEvents {
		if event.ProjectID == id {
			delete(m.pluginAuditEvents, auditID)
		}
	}
	for k, mem := range m.members {
		if mem.ProjectID == id {
			delete(m.members, k)
		}
	}
	delete(m.projects, id)
	return nil
}

// --- services ---

func (m *MemStore) CreateService(_ context.Context, s *domain.Service) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.GitMode == "" {
		s.GitMode = domain.GitModeReadonly
	}
	if s.DefaultBranch == "" {
		s.DefaultBranch = "main"
	}
	m.services[s.ID] = *s
	return nil
}

func (m *MemStore) CreatePluginBoundService(_ context.Context, s *domain.Service, binding *domain.ServiceRepositoryBinding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s == nil || binding == nil || s.ID == "" || binding.ServiceID != s.ID || binding.InstallationID == "" || binding.ProviderRepoID == "" {
		return errors.New("create plugin-bound service: invalid aggregate")
	}
	if _, exists := m.services[s.ID]; exists {
		return ErrAlreadyExists
	}
	installation, exists := m.pluginInstallations[binding.InstallationID]
	if !exists {
		return ErrNotFound
	}
	if err := validateServiceRepositoryBinding(s, &installation); err != nil {
		return err
	}
	for _, existing := range m.serviceRepoBindings {
		if existing.InstallationID == binding.InstallationID && existing.ProviderRepoID == binding.ProviderRepoID {
			return ErrAlreadyExists
		}
	}
	if s.GitMode == "" {
		s.GitMode = domain.GitModeReadonly
	}
	if s.DefaultBranch == "" {
		s.DefaultBranch = "main"
	}
	m.services[s.ID] = *s
	m.serviceRepoBindings[s.ID] = *binding
	return nil
}

func (m *MemStore) GetService(_ context.Context, id string) (*domain.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.services[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := s
	return &cp, nil
}

func (m *MemStore) ListServices(_ context.Context, projectID string) ([]domain.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Service
	for _, s := range m.services {
		if s.ProjectID == projectID {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) GetDefaultService(_ context.Context, projectID string) (*domain.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.services {
		if s.ProjectID == projectID && s.Name == "default" {
			cp := s
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemStore) ListServicesByRepo(_ context.Context, provider domain.GitProvider, repoOwnerName string) ([]domain.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Service
	for _, s := range m.services {
		if s.RepoKind == domain.RepoKindProvider && s.Provider == provider && s.RepoOwnerName == repoOwnerName {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) UpdateService(_ context.Context, s *domain.Service) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.services[s.ID]; !ok {
		return ErrNotFound
	}
	if binding, ok := m.serviceRepoBindings[s.ID]; ok {
		installation, exists := m.pluginInstallations[binding.InstallationID]
		if !exists {
			return ErrNotFound
		}
		if err := validateServiceRepositoryBinding(s, &installation); err != nil {
			return err
		}
	}
	m.services[s.ID] = *s
	return nil
}

func (m *MemStore) DeleteService(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.services[id]; !ok {
		return ErrNotFound
	}
	m.deleteServiceLocked(id)
	return nil
}

// deleteServiceLocked mirrors the FK cascade rooted at services. Caller must
// hold m.mu.
func (m *MemStore) deleteServiceLocked(id string) {
	for runID, run := range m.runs {
		if run.ServiceID == id {
			m.deleteRunLocked(runID)
		}
	}
	for linkID, link := range m.kanbanLinks {
		if link.ServiceID == id {
			delete(m.kanbanLinks, linkID)
			for claimID, claim := range m.kanbanClaims {
				if claim.LinkID == linkID {
					delete(m.kanbanClaims, claimID)
				}
			}
		}
	}
	for scheduleID, schedule := range m.schedules {
		if schedule.ServiceID == id {
			delete(m.schedules, scheduleID)
		}
	}
	for automationID, automation := range m.automations {
		if automation.ServiceID == id {
			delete(m.automations, automationID)
		}
	}
	for automationID, automation := range m.pluginAutomations {
		if automation.ServiceID == id {
			m.deletePluginAutomationLocked(automationID)
		}
	}
	delete(m.serviceRepoBindings, id)
	delete(m.webhookBindings, id)
	delete(m.services, id)
}

func (m *MemStore) MarkServiceDeleting(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	svc, ok := m.services[id]
	if !ok {
		return ErrNotFound
	}
	if svc.DeletingAt == nil {
		t := at
		svc.DeletingAt = &t
		m.services[id] = svc
	}
	return nil
}

func (m *MemStore) deleteRunLocked(runID string) {
	delete(m.runs, runID)
	delete(m.events, runID)
	delete(m.runMessages, runID)
	delete(m.runPluginSnapshots, runID)
	for key := range m.artifacts {
		if strings.HasPrefix(key, runID+"/") {
			delete(m.artifacts, key)
		}
	}
	for key := range m.dedupe {
		if strings.HasPrefix(key, runID+"|") {
			delete(m.dedupe, key)
		}
	}
	for requestID, permission := range m.permissions {
		if permission.RunID == runID {
			delete(m.permissions, requestID)
		}
	}
	// automation_kanban_claims.run_id is ON DELETE SET NULL, not CASCADE.
	for key, claim := range m.pluginKanbanClaims {
		if claim.RunID == runID {
			claim.RunID = ""
			m.pluginKanbanClaims[key] = claim
		}
	}
}

// ListArchiveCandidates mirrors the PG query (F10): a service not already
// archived, with at least one run, whose most-recent run predates idleBefore and
// which has no run in a non-terminal state.
func (m *MemStore) ListArchiveCandidates(_ context.Context, idleBefore time.Time) ([]ArchiveCandidate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Per-service: most-recent run time + whether any run is live (non-terminal).
	type agg struct {
		last    time.Time
		anyRun  bool
		hasLive bool
	}
	byService := map[string]*agg{}
	for _, r := range m.runs {
		a := byService[r.ServiceID]
		if a == nil {
			a = &agg{}
			byService[r.ServiceID] = a
		}
		a.anyRun = true
		if r.CreatedAt.After(a.last) {
			a.last = r.CreatedAt
		}
		switch r.Status {
		case domain.StatusQueued, domain.StatusScheduling, domain.StatusRunning, domain.StatusAwaitingInput:
			a.hasLive = true
		}
	}
	var out []ArchiveCandidate
	for sid, svc := range m.services {
		if svc.ArchivedAt != nil {
			continue
		}
		a := byService[sid]
		if a == nil || !a.anyRun || a.hasLive {
			continue
		}
		if !a.last.Before(idleBefore) {
			continue
		}
		out = append(out, ArchiveCandidate{ServiceID: sid, ProjectID: svc.ProjectID, LastActivity: a.last})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastActivity.Before(out[j].LastActivity) })
	return out, nil
}

// MarkServiceArchived stamps archived_at + archive_key (F10).
func (m *MemStore) MarkServiceArchived(_ context.Context, serviceID, archiveKey string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	svc, ok := m.services[serviceID]
	if !ok {
		return ErrNotFound
	}
	t := at
	svc.ArchivedAt = &t
	svc.ArchiveKey = archiveKey
	m.services[serviceID] = svc
	return nil
}

// ClearServiceArchive clears archived_at + archive_key (F10). Idempotent.
func (m *MemStore) ClearServiceArchive(_ context.Context, serviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	svc, ok := m.services[serviceID]
	if !ok {
		return ErrNotFound
	}
	svc.ArchivedAt = nil
	svc.ArchiveKey = ""
	m.services[serviceID] = svc
	return nil
}

// --- runs ---

func (m *MemStore) CreateRun(_ context.Context, r *domain.Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Older focused store tests seed standalone runs directly; preserve that
	// convenience while still enforcing the deletion fence whenever the service
	// aggregate is present (production PG always has the FK).
	if svc, ok := m.services[r.ServiceID]; ok && svc.DeletingAt != nil {
		return ErrServiceDeleting
	}
	if r.Kind == "" {
		r.Kind = domain.RunKindAgent
	}
	if r.Origin == "" {
		r.Origin = domain.RunOriginAPI
	}
	// Mirror the PG partial-unique index on origin_comment_id: a redelivered
	// webhook comment cannot create a second run.
	if r.OriginCommentID != "" {
		for _, ex := range m.runs {
			if ex.OriginCommentID == r.OriginCommentID {
				return fmt.Errorf("origin_comment_id already used: %s", r.OriginCommentID)
			}
		}
	}
	if r.OriginEventKey != "" {
		for _, ex := range m.runs {
			if ex.OriginEventKey == r.OriginEventKey {
				return fmt.Errorf("origin_event_key already used: %s", r.OriginEventKey)
			}
		}
	}
	m.runs[r.ID] = *r
	return nil
}

func (m *MemStore) GetRunByOriginCommentID(_ context.Context, commentID string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if commentID == "" {
		return nil, ErrNotFound
	}
	for _, r := range m.runs {
		if r.OriginCommentID == commentID {
			cp := r
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemStore) GetRunByOriginEventKey(_ context.Context, eventKey string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if eventKey == "" {
		return nil, ErrNotFound
	}
	for _, r := range m.runs {
		if r.OriginEventKey == eventKey {
			cp := r
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemStore) GetRun(_ context.Context, id string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := r
	return &cp, nil
}

func (m *MemStore) GetRunByTokenHash(_ context.Context, tokenHash string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tokenHash == "" {
		return nil, ErrNotFound
	}
	for _, r := range m.runs {
		if r.TokenHash == tokenHash {
			cp := r
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemStore) ListRuns(_ context.Context, projectID string, limit int) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Run
	for _, r := range m.runs {
		if projectID == "" || r.ProjectID == projectID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) ListRunsByService(_ context.Context, serviceID string, limit int) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Run
	for _, r := range m.runs {
		if r.ServiceID == serviceID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) ListRunsByStatus(_ context.Context, statuses ...domain.RunStatus) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[domain.RunStatus]bool{}
	for _, s := range statuses {
		want[s] = true
	}
	var out []domain.Run
	for _, r := range m.runs {
		if want[r.Status] {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) CountActiveRuns(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.runs {
		if r.Status == domain.StatusScheduling || r.Status == domain.StatusRunning {
			n++
		}
	}
	return n, nil
}

func (m *MemStore) CountRunsByStatus(_ context.Context, statuses ...domain.RunStatus) (map[domain.RunStatus]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[domain.RunStatus]int, len(statuses))
	for _, s := range statuses {
		out[s] = 0 // every requested status is present as a key, defaulting to 0
	}
	for _, r := range m.runs {
		if _, ok := out[r.Status]; ok {
			out[r.Status]++
		}
	}
	return out, nil
}

// transitionLocked applies a status change plus a field mutator to the CURRENTLY
// stored row (never a caller snapshot), enforcing the state machine. It mirrors
// PGStore's "re-read committed row, mutate named fields, return committed copy"
// semantics so the two stores stay behaviourally identical. Caller holds m.mu.
func (m *MemStore) transitionLocked(id string, to domain.RunStatus, mut func(*domain.Run)) (*domain.Run, error) {
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if !domain.CanTransition(cur.Status, to) {
		return nil, ErrInvalidTransition
	}
	cur.Status = to
	mut(&cur)
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

func (m *MemStore) ScheduleRun(_ context.Context, id, jobName, tokenHash, phase string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transitionLocked(id, domain.StatusScheduling, func(r *domain.Run) {
		r.Phase = phase
		r.K8sJobName = jobName
		r.TokenHash = tokenHash
	})
}

func (m *MemStore) ClaimRunDispatch(_ context.Context, id, jobName, tokenHash, phase, requiredInstallationID string, snapshots []domain.RunPluginSnapshot) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if run.Status != domain.StatusQueued {
		return nil, fmt.Errorf("%w: %s is not queued", ErrInvalidTransition, id)
	}
	seen := map[string]struct{}{}
	requiredFound := requiredInstallationID == ""
	for _, snap := range snapshots {
		if snap.RunID != id || snap.InstallationID == "" {
			return nil, fmt.Errorf("%w: invalid snapshot", ErrDispatchClaimUnavailable)
		}
		if _, duplicate := seen[snap.InstallationID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate snapshot", ErrDispatchClaimUnavailable)
		}
		seen[snap.InstallationID] = struct{}{}
		installation, exists := m.pluginInstallations[snap.InstallationID]
		cfg, configExists := m.providerConfigs[installation.Provider]
		if !exists || !configExists || installation.ProjectID != run.ProjectID ||
			installation.Status != domain.PluginStatusEnabled || installation.LastHealthError != "" ||
			!cfg.PluginEnabled || cfg.ConfigRevision != installation.ConfigRevision ||
			installation.CredentialVersionID == "" ||
			(installation.Provider == domain.PluginGitHub && installation.GitHubInstallID == "") ||
			(installation.Provider != domain.PluginGitHub && !installation.TokenSet()) {
			return nil, fmt.Errorf("%w: plugin %s", ErrDispatchClaimUnavailable, snap.InstallationID)
		}
		if snap.InstallationID == requiredInstallationID {
			requiredFound = true
		}
	}
	if !requiredFound {
		return nil, fmt.Errorf("%w: required plugin missing", ErrDispatchClaimUnavailable)
	}
	// Validation completed before mutation: a bad later input cannot leave a
	// partial batch of snapshots behind.
	staged := map[string]domain.RunPluginSnapshot{}
	for _, snap := range snapshots {
		if snap.CreatedAt.IsZero() {
			snap.CreatedAt = time.Now().UTC()
		}
		installation := m.pluginInstallations[snap.InstallationID]
		snap.Provider = installation.Provider
		snap.ProviderConfigRevision = installation.ConfigRevision
		snap.CredentialVersionID = installation.CredentialVersionID
		// The in-memory snapshot representation mirrors SQL: secret material lives
		// in immutable version maps and is only joined for the issuer.
		snap.ProviderBaseURL, snap.ProviderClientID, snap.ProviderClientSecretEnc = "", "", nil
		snap.ProviderAppID, snap.ProviderAppPrivateKeyEnc = "", nil
		snap.GitHubInstallID, snap.AccessTokenEnc, snap.RefreshTokenEnc, snap.TokenExpiresAt = "", nil, nil, nil
		staged[snap.InstallationID] = snap
	}
	m.runPluginSnapshots[id] = staged
	run.Status = domain.StatusScheduling
	run.Phase = phase
	run.K8sJobName = jobName
	run.TokenHash = tokenHash
	m.runs[id] = run
	copy := run
	return &copy, nil
}

func (m *MemStore) FailRunDispatch(_ context.Context, id, jobName, phase, message string, finishedAt time.Time) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if run.Status != domain.StatusScheduling || run.K8sJobName != jobName {
		return nil, fmt.Errorf("%w: dispatch claim no longer current", ErrInvalidTransition)
	}
	delete(m.runPluginSnapshots, id)
	run.Status = domain.StatusFailed
	run.Phase = phase
	run.Error = message
	run.FailureReason = domain.FailureSetupFailed
	run.FailureMessage = message
	run.TokenHash = ""
	if run.FinishedAt == nil {
		t := finishedAt
		run.FinishedAt = &t
	}
	m.runs[id] = run
	copy := run
	return &copy, nil
}

func (m *MemStore) MarkRunning(_ context.Context, id, phase string, startedAt time.Time) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transitionLocked(id, domain.StatusRunning, func(r *domain.Run) {
		r.Phase = phase
		if r.StartedAt == nil {
			t := startedAt
			r.StartedAt = &t
		}
	})
}

func (m *MemStore) MarkSucceeded(_ context.Context, id, phase string, finishedAt time.Time) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transitionLocked(id, domain.StatusSucceeded, func(r *domain.Run) {
		r.Phase = phase
		if r.FinishedAt == nil {
			t := finishedAt
			r.FinishedAt = &t
		}
	})
}

func (m *MemStore) MarkFailed(_ context.Context, id, phase string, reason domain.FailureReason, msg string, finishedAt time.Time) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transitionLocked(id, domain.StatusFailed, func(r *domain.Run) {
		r.Phase = phase
		if r.FailureReason == "" {
			r.FailureReason = reason
		}
		if r.FailureMessage == "" {
			r.FailureMessage = msg
		}
		r.Error = r.FailureMessage
		if r.FinishedAt == nil {
			t := finishedAt
			r.FinishedAt = &t
		}
	})
}

func (m *MemStore) SetRunnerFailure(_ context.Context, id string, reason domain.FailureReason, msg string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if cur.Status.Terminal() {
		cp := cur
		return &cp, nil // already terminal: leave it
	}
	if cur.FailureReason == "" {
		cur.FailureReason = reason
	}
	if cur.FailureMessage == "" {
		cur.FailureMessage = msg
	}
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

func (m *MemStore) CancelRun(_ context.Context, id, phase string, finishedAt time.Time) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transitionLocked(id, domain.StatusCanceled, func(r *domain.Run) {
		r.Phase = phase
		if r.FinishedAt == nil {
			t := finishedAt
			r.FinishedAt = &t
		}
	})
}

// MarkJobCleaned stamps JobCleanedAt once the run's Job is confirmed deleted.
// K8sJobName is KEPT (historical record). Idempotent: a prior stamp is
// preserved. No status change; a missing run is a no-op.
func (m *MemStore) MarkJobCleaned(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runs[id]; ok && r.JobCleanedAt == nil {
		t := time.Now().UTC()
		r.JobCleanedAt = &t
		m.runs[id] = r
	}
	return nil
}

func (m *MemStore) ListTerminalRunsWithJob(_ context.Context) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Run
	for _, r := range m.runs {
		if r.Status.Terminal() && r.K8sJobName != "" && r.JobCleanedAt == nil {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// SetRunGit records branch/commit first-writer-wins, no status change.
func (m *MemStore) SetRunGit(_ context.Context, id, branch, commitSHA string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if cur.GitBranch == "" {
		cur.GitBranch = branch
	}
	if cur.CommitSHA == "" {
		cur.CommitSHA = commitSHA
	}
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

// SetRunResult records a run outcome (run.result) first-writer-wins, no status
// change. Writes only where result is still nil, so a duplicate event is a no-op.
func (m *MemStore) SetRunResult(_ context.Context, id string, result domain.RunResult) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if cur.Result == nil {
		rr := result
		cur.Result = &rr
	}
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

// SetRunACPSession records the run's ACP session id (run.session) first-writer-
// wins, no status change. Writes only where acp_session_id is still empty (and
// the id is non-empty), so a duplicate event / a pre-filled resume run is a no-op.
func (m *MemStore) SetRunACPSession(_ context.Context, id, acpSessionID string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if cur.AcpSessionID == "" && acpSessionID != "" {
		cur.AcpSessionID = acpSessionID
	}
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

// MarkPRCreated stamps pr_url/pr_number idempotently, first-writer-wins.
func (m *MemStore) MarkPRCreated(_ context.Context, id, prURL string, prNumber int) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if cur.PRURL == "" {
		cur.PRURL = prURL
		cur.PRNumber = prNumber
	}
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

// ListRunsAwaitingPR returns succeeded NON-session agent runs with a recorded
// branch but no PR yet. Session runs are handled by ListSessionRunsAwaitingPush.
func (m *MemStore) ListRunsAwaitingPR(_ context.Context) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Run
	for _, r := range m.runs {
		if r.Status == domain.StatusSucceeded && r.Kind == domain.RunKindAgent &&
			r.GitBranch != "" && r.PRURL == "" && !r.Session {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// --- Session runs (D22) ---

// SetRunAwaitingInput: running -> awaiting_input, stamping awaiting_since only
// where it is still nil (first-writer-wins) so a duplicate turn-complete does
// not reset the idle timer.
func (m *MemStore) SetRunAwaitingInput(_ context.Context, id string, at time.Time) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transitionLocked(id, domain.StatusAwaitingInput, func(r *domain.Run) {
		if r.AwaitingSince == nil {
			t := at
			r.AwaitingSince = &t
		}
	})
}

// ResumeRun: awaiting_input -> running, clearing awaiting_since.
func (m *MemStore) ResumeRun(_ context.Context, id, phase string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transitionLocked(id, domain.StatusRunning, func(r *domain.Run) {
		r.Phase = phase
		r.AwaitingSince = nil
	})
}

// MarkSessionFinalizing sets session_finalizing while non-terminal (idempotent).
func (m *MemStore) MarkSessionFinalizing(_ context.Context, id string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if cur.Status.Terminal() {
		cp := cur
		return &cp, nil
	}
	cur.SessionFinalizing = true
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

// FinalizeIdleSession — conditional finalize (idle-timeout pass): flips the flag
// only while the run is STILL awaiting_input, not already finalizing, and idle
// since at-or-before cutoff. All checks under the same lock (no TOCTOU).
func (m *MemStore) FinalizeIdleSession(_ context.Context, id string, cutoff time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return false, nil
	}
	if cur.Status != domain.StatusAwaitingInput || cur.SessionFinalizing ||
		cur.AwaitingSince == nil || cur.AwaitingSince.After(cutoff) {
		return false, nil
	}
	cur.SessionFinalizing = true
	m.runs[id] = cur
	return true, nil
}

// AppendRunMessage enqueues a follow-up prompt, allocating the next per-run seq.
func (m *MemStore) AppendRunMessage(_ context.Context, runID, prompt, createdBy string) (*domain.RunMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[runID]; !ok {
		return nil, ErrNotFound
	}
	var maxSeq int64
	for _, msg := range m.runMessages[runID] {
		if msg.Seq > maxSeq {
			maxSeq = msg.Seq
		}
	}
	msg := domain.RunMessage{
		ID: domain.NewID(), RunID: runID, Seq: maxSeq + 1, Prompt: prompt,
		CreatedBy: createdBy, CreatedAt: time.Now().UTC(),
	}
	m.runMessages[runID] = append(m.runMessages[runID], msg)
	cp := msg
	return &cp, nil
}

// OfferNextMessage — phase 1 of the two-phase delivery, all under one lock so
// two concurrent offers converge on the SAME message (never two different ones):
// an offered-but-not-consumed message is re-delivered verbatim (fresh=false),
// otherwise the oldest unoffered one is stamped offered_at (fresh=true).
func (m *MemStore) OfferNextMessage(_ context.Context, runID string, at time.Time) (*domain.RunMessage, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[runID]; !ok {
		return nil, false, ErrNotFound
	}
	msgs := m.runMessages[runID]
	// msgs is kept append-ordered (ascending seq).
	for i := range msgs {
		if msgs[i].OfferedAt != nil && msgs[i].ConsumedAt == nil {
			cp := msgs[i]
			return &cp, false, nil // idempotent re-delivery
		}
	}
	for i := range msgs {
		if msgs[i].OfferedAt == nil {
			t := at
			msgs[i].OfferedAt = &t
			m.runMessages[runID] = msgs
			cp := msgs[i]
			return &cp, true, nil
		}
	}
	return nil, false, ErrNotFound
}

// ConsumeOfferedMessage — phase 2: stamps consumed_at on the offered message.
// (false, nil) when none is offered (e.g. the first TASK_PROMPT turn).
func (m *MemStore) ConsumeOfferedMessage(_ context.Context, runID string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs := m.runMessages[runID]
	consumed := false
	for i := range msgs {
		if msgs[i].OfferedAt != nil && msgs[i].ConsumedAt == nil {
			t := at
			msgs[i].ConsumedAt = &t
			consumed = true
		}
	}
	if consumed {
		m.runMessages[runID] = msgs
	}
	return consumed, nil
}

// ListRunMessages returns a run's queued messages, oldest first.
func (m *MemStore) ListRunMessages(_ context.Context, runID string) ([]domain.RunMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.RunMessage, len(m.runMessages[runID]))
	copy(out, m.runMessages[runID])
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// --- Session permission approval (F8b) ---------------------------------------

// copyPermission deep-copies a RunPermission (Options is a slice; pointer
// fields are re-pointed at copies) so callers can never mutate stored state.
func copyPermission(p domain.RunPermission) domain.RunPermission {
	cp := p
	cp.Options = append([]domain.PermissionOption(nil), p.Options...)
	if p.DecidedOptionID != nil {
		v := *p.DecidedOptionID
		cp.DecidedOptionID = &v
	}
	if p.DecidedBy != nil {
		v := *p.DecidedBy
		cp.DecidedBy = &v
	}
	if p.DecidedAt != nil {
		v := *p.DecidedAt
		cp.DecidedAt = &v
	}
	if p.ResolvedOptionID != nil {
		v := *p.ResolvedOptionID
		cp.ResolvedOptionID = &v
	}
	if p.Resolution != nil {
		v := *p.Resolution
		cp.Resolution = &v
	}
	if p.ResolvedAt != nil {
		v := *p.ResolvedAt
		cp.ResolvedAt = &v
	}
	return cp
}

// UpsertRunPermission — insert-only idempotency: an existing request_id is left
// completely untouched (a duplicate request event must never reset a
// decided/resolved row).
func (m *MemStore) UpsertRunPermission(_ context.Context, p *domain.RunPermission) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[p.RunID]; !ok {
		return ErrNotFound
	}
	if _, ok := m.permissions[p.RequestID]; ok {
		return nil // idempotent re-delivery: never reset decided/resolved state
	}
	m.permissions[p.RequestID] = copyPermission(*p)
	return nil
}

func (m *MemStore) GetRunPermission(_ context.Context, runID, requestID string) (*domain.RunPermission, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.permissions[requestID]
	if !ok || p.RunID != runID {
		return nil, ErrNotFound
	}
	cp := copyPermission(p)
	return &cp, nil
}

// DecideRunPermission — the conditional user-answer write: wins only while the
// row is neither decided nor resolved (mirrors the PG WHERE clause).
func (m *MemStore) DecideRunPermission(_ context.Context, runID, requestID, optionID, decidedBy string, at time.Time) (*domain.RunPermission, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.permissions[requestID]
	if !ok || p.RunID != runID {
		return nil, false, ErrNotFound
	}
	if p.DecidedOptionID != nil || p.ResolvedAt != nil {
		cp := copyPermission(p)
		return &cp, false, nil // already answered/resolved: the caller 409s
	}
	opt := optionID
	t := at
	p.DecidedOptionID = &opt
	p.DecidedAt = &t
	if decidedBy != "" {
		by := decidedBy
		p.DecidedBy = &by
	}
	m.permissions[requestID] = p
	cp := copyPermission(p)
	return &cp, true, nil
}

// ResolveRunPermission — first-writer-wins on the resolved_* fields; a missing
// row or an already-resolved row is a silent no-op (duplicate/orphan events).
func (m *MemStore) ResolveRunPermission(_ context.Context, runID, requestID, optionID, resolution string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.permissions[requestID]
	if !ok || p.RunID != runID || p.ResolvedAt != nil {
		return nil
	}
	opt := optionID
	res := resolution
	t := at
	p.ResolvedOptionID = &opt
	p.Resolution = &res
	p.ResolvedAt = &t
	m.permissions[requestID] = p
	return nil
}

// ListRunPermissions returns a run's permission requests, oldest first.
func (m *MemStore) ListRunPermissions(_ context.Context, runID string) ([]domain.RunPermission, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.RunPermission
	for _, p := range m.permissions {
		if p.RunID == runID {
			out = append(out, copyPermission(p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// BumpBundleRev increments bundle_rev.
func (m *MemStore) BumpBundleRev(_ context.Context, id string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	cur.BundleRev++
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

// SetPushedRev advances pushed_rev to at-least rev and records commit_sha. An
// empty sha preserves the stored value (PR-already-exists recovery pushes
// nothing and must not wipe the last recorded tip).
func (m *MemStore) SetPushedRev(_ context.Context, id string, rev int64, commitSHA string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if rev > cur.PushedRev {
		cur.PushedRev = rev
	}
	if commitSHA != "" {
		cur.CommitSHA = commitSHA
	}
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

// ListSessionRunsAwaitingPush returns session agent runs with a recorded branch
// and a bundle newer than the last push, still non-final. Oldest-first.
func (m *MemStore) ListSessionRunsAwaitingPush(_ context.Context) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Run
	for _, r := range m.runs {
		if r.Session && r.Kind == domain.RunKindAgent && r.GitBranch != "" &&
			r.BundleRev > r.PushedRev &&
			r.Status != domain.StatusFailed && r.Status != domain.StatusCanceled {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ListAwaitingInputRuns returns every run currently in awaiting_input.
func (m *MemStore) ListAwaitingInputRuns(ctx context.Context) ([]domain.Run, error) {
	return m.ListRunsByStatus(ctx, domain.StatusAwaitingInput)
}

// ListReviewRunsAwaitingPost returns succeeded review runs with output that has
// not been posted to the PR yet.
func (m *MemStore) ListReviewRunsAwaitingPost(_ context.Context) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Run
	for _, r := range m.runs {
		if r.Status == domain.StatusSucceeded && r.Kind == domain.RunKindReview &&
			r.ReviewOutput != "" && r.ReviewPostedAt == nil {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) ListRunsAwaitingUpdatePush(_ context.Context) ([]domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Run
	for _, r := range m.runs {
		if r.Status == domain.StatusSucceeded && r.Origin == domain.RunOriginWebhook &&
			r.Kind == domain.RunKindAgent && r.GitBranch != "" && r.PRURL != "" && r.CommitSHA == "" {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// SetReviewOutput records a review run's output first-writer-wins, no status change.
func (m *MemStore) SetReviewOutput(_ context.Context, id, md string) (*domain.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	if cur.ReviewOutput == "" {
		cur.ReviewOutput = md
	}
	m.runs[id] = cur
	cp := cur
	return &cp, nil
}

// MarkReviewPosted stamps review_posted_at idempotently, returning true only for
// the caller that actually stamped it.
func (m *MemStore) MarkReviewPosted(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.runs[id]
	if !ok {
		return false, ErrNotFound
	}
	if cur.ReviewPostedAt != nil {
		return false, nil
	}
	t := time.Now().UTC()
	cur.ReviewPostedAt = &t
	m.runs[id] = cur
	return true, nil
}

// --- events ---

func (m *MemStore) AppendEvents(_ context.Context, runID string, events []EventInput) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing := map[int64]bool{}
	for _, e := range m.events[runID] {
		existing[e.Seq] = true
	}
	inserted := 0
	for _, e := range events {
		if existing[e.Seq] {
			continue // idempotent by (run_id, seq)
		}
		payload := e.Payload
		if payload == nil {
			payload = map[string]any{}
		}
		m.events[runID] = append(m.events[runID], domain.RunEvent{
			RunID: runID, Seq: e.Seq, Type: e.Type, Payload: payload,
		})
		m.dedupe[dedupeKey(runID, SourceInternal, e.Seq)] = true
		existing[e.Seq] = true
		inserted++
	}
	sort.Slice(m.events[runID], func(i, j int) bool {
		return m.events[runID][i].Seq < m.events[runID][j].Seq
	})
	return inserted, nil
}

func (m *MemStore) AppendRunnerEvents(_ context.Context, runID string, events []EventInput) ([]domain.RunEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[runID]; !ok {
		return nil, ErrNotFound
	}
	next := m.maxSeqLocked(runID)
	out := make([]domain.RunEvent, 0, len(events))
	for _, e := range events {
		key := dedupeKey(runID, SourceRunner, e.Seq)
		if m.dedupe[key] {
			continue // idempotent by (run_id, runner, client_seq); no seq consumed
		}
		payload := e.Payload
		if payload == nil {
			payload = map[string]any{}
		}
		next++
		ev := domain.RunEvent{RunID: runID, Seq: next, TS: time.Now().UTC(), Type: e.Type, Payload: payload}
		m.events[runID] = append(m.events[runID], ev)
		m.dedupe[key] = true
		out = append(out, ev)
	}
	sort.Slice(m.events[runID], func(i, j int) bool {
		return m.events[runID][i].Seq < m.events[runID][j].Seq
	})
	return out, nil
}

func (m *MemStore) AppendInternalEvent(_ context.Context, runID, typ string, payload map[string]any) (domain.RunEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[runID]; !ok {
		return domain.RunEvent{}, ErrNotFound
	}
	if payload == nil {
		payload = map[string]any{}
	}
	seq := m.maxSeqLocked(runID) + 1
	ev := domain.RunEvent{RunID: runID, Seq: seq, TS: time.Now().UTC(), Type: typ, Payload: payload}
	m.events[runID] = append(m.events[runID], ev)
	m.dedupe[dedupeKey(runID, SourceInternal, seq)] = true
	return ev, nil
}

func (m *MemStore) NextEventSeq(_ context.Context, runID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxSeqLocked(runID) + 1, nil
}

func (m *MemStore) ListEvents(_ context.Context, runID string, afterSeq int64, limit int) ([]domain.RunEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.RunEvent
	for _, e := range m.events[runID] {
		if e.Seq > afterSeq {
			out = append(out, e)
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// --- artifacts ---

func (m *MemStore) PutArtifact(_ context.Context, a *domain.RunArtifact) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.artifacts[a.RunID+"/"+string(a.Kind)] = *a
	return nil
}

func (m *MemStore) GetArtifact(_ context.Context, runID string, kind domain.ArtifactKind) (*domain.RunArtifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.artifacts[runID+"/"+string(kind)]
	if !ok {
		return nil, ErrNotFound
	}
	cp := a
	return &cp, nil
}

// PutRunBundle stores a run's git bundle bytes (kind=bundle) in the artifact map.
func (m *MemStore) PutRunBundle(_ context.Context, runID string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	m.artifacts[runID+"/"+string(domain.ArtifactBundle)] = domain.RunArtifact{
		RunID: runID, Kind: domain.ArtifactBundle, Bytes: cp, CreatedAt: time.Now().UTC(),
	}
	return nil
}

// GetRunBundle returns a run's stored git bundle bytes (ErrNotFound if absent).
func (m *MemStore) GetRunBundle(_ context.Context, runID string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.artifacts[runID+"/"+string(domain.ArtifactBundle)]
	if !ok {
		return nil, ErrNotFound
	}
	cp := make([]byte, len(a.Bytes))
	copy(cp, a.Bytes)
	return cp, nil
}

// --- model catalog + project grants (D21) ---

func cloneModelProvider(p domain.ModelProvider) domain.ModelProvider {
	if p.APIKeyEnc != nil {
		p.APIKeyEnc = append([]byte(nil), p.APIKeyEnc...)
	}
	if p.HeadersEnc != nil {
		p.HeadersEnc = append([]byte(nil), p.HeadersEnc...)
	}
	if p.CatalogAvailable != nil {
		v := *p.CatalogAvailable
		p.CatalogAvailable = &v
	}
	if p.LastVerifiedAt != nil {
		v := *p.LastVerifiedAt
		p.LastVerifiedAt = &v
	}
	return p
}

func (m *MemStore) CreateModelProvider(_ context.Context, p *domain.ModelProvider) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Scope-aware uniqueness (M1): a name is unique WITHIN its scope
	// (COALESCE(project_id,'')), so the cluster and each project can name a
	// provider the same thing.
	for _, existing := range m.modelProviders {
		if existing.Name == p.Name && existing.ProjectID == p.ProjectID {
			return ErrAlreadyExists
		}
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	p.UpdatedAt = p.CreatedAt
	m.modelProviders[p.ID] = cloneModelProvider(*p)
	return nil
}

func (m *MemStore) GetModelProvider(_ context.Context, id string) (*domain.ModelProvider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.modelProviders[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := cloneModelProvider(p)
	return &cp, nil
}

func (m *MemStore) ListModelProviders(_ context.Context) ([]domain.ModelProvider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.ModelProvider, 0, len(m.modelProviders))
	for _, p := range m.modelProviders {
		out = append(out, cloneModelProvider(p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ListModelProvidersForProject returns the providers OWNED by a project (M1),
// oldest first. Cluster-global providers (project_id "") are never returned.
func (m *MemStore) ListModelProvidersForProject(_ context.Context, projectID string) ([]domain.ModelProvider, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.ModelProvider
	for _, p := range m.modelProviders {
		if p.ProjectID == projectID {
			out = append(out, cloneModelProvider(p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) UpdateModelProvider(_ context.Context, p *domain.ModelProvider) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.modelProviders[p.ID]; !ok {
		return ErrNotFound
	}
	for id, existing := range m.modelProviders {
		if id != p.ID && existing.Name == p.Name && existing.ProjectID == p.ProjectID {
			return ErrAlreadyExists
		}
	}
	p.UpdatedAt = time.Now().UTC()
	m.modelProviders[p.ID] = cloneModelProvider(*p)
	for id, mod := range m.models {
		if mod.ProviderID == p.ID {
			mod.BaseURL = p.BaseURL
			mod.APIKeyEnc = append([]byte(nil), p.APIKeyEnc...)
			mod.HeadersEnc = append([]byte(nil), p.HeadersEnc...)
			mod.ModelName = p.Kind + "/" + mod.ModelID
			m.models[id] = mod
		}
	}
	return nil
}

func (m *MemStore) deleteModelLocked(id string) error {
	if _, ok := m.models[id]; !ok {
		return ErrNotFound
	}
	delete(m.models, id)
	for k := range m.modelGrants {
		if strings.HasPrefix(k, id+"|") {
			delete(m.modelGrants, k)
		}
	}
	for k := range m.modelAccountGrants {
		if strings.HasPrefix(k, id+"|") {
			delete(m.modelAccountGrants, k)
		}
	}
	for sid, svc := range m.services {
		if svc.DefaultModelID != nil && *svc.DefaultModelID == id {
			svc.DefaultModelID = nil
			m.services[sid] = svc
		}
	}
	for rid, run := range m.runs {
		if run.ModelID != nil && *run.ModelID == id {
			run.ModelID = nil
			m.runs[rid] = run
		}
	}
	return nil
}

func (m *MemStore) DeleteModelProvider(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.modelProviders[id]; !ok {
		return ErrNotFound
	}
	for modelID, mod := range m.models {
		if mod.ProviderID == id {
			_ = m.deleteModelLocked(modelID)
		}
	}
	delete(m.modelProviders, id)
	return nil
}

func (m *MemStore) ListModelsForProvider(_ context.Context, providerID string) ([]domain.Model, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.modelProviders[providerID]; !ok {
		return nil, ErrNotFound
	}
	var out []domain.Model
	for _, mod := range m.models {
		if mod.ProviderID == providerID {
			out = append(out, cloneModel(mod))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// cloneModel deep-copies a model so callers can't mutate the stored api_key_enc
// or headers_enc.
func cloneModel(m domain.Model) domain.Model {
	if m.APIKeyEnc != nil {
		m.APIKeyEnc = append([]byte(nil), m.APIKeyEnc...)
	}
	if m.HeadersEnc != nil {
		m.HeadersEnc = append([]byte(nil), m.HeadersEnc...)
	}
	return m
}

func grantKey(modelID, projectID string) string { return modelID + "|" + projectID }

// CreateModel inserts a catalog model. Duplicate name => ErrAlreadyExists. A new
// model is always enabled (jcode Switch parity; the DB column default is true).
func (m *MemStore) CreateModel(_ context.Context, mod *domain.Model) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mod.Enabled = true
	// Scope-aware uniqueness (M1): unique WITHIN scope (COALESCE(project_id,'')).
	for _, e := range m.models {
		if e.Name == mod.Name && e.ProjectID == mod.ProjectID {
			return ErrAlreadyExists
		}
	}
	if mod.CreatedAt.IsZero() {
		mod.CreatedAt = time.Now().UTC()
	}
	if mod.ProviderID == "" {
		mod.ProviderID = mod.ID
		authType := domain.ModelProviderAuthNone
		if len(mod.APIKeyEnc) > 0 {
			authType = domain.ModelProviderAuthAPIKey
		}
		m.modelProviders[mod.ProviderID] = domain.ModelProvider{
			ID: mod.ProviderID, ProjectID: mod.ProjectID, Name: mod.Name, Kind: "custom", BaseURL: mod.BaseURL,
			AuthType: authType, APIKeyEnc: append([]byte(nil), mod.APIKeyEnc...),
			CatalogMode: domain.ModelProviderCatalogDisabled, CreatedAt: mod.CreatedAt,
			UpdatedAt: mod.CreatedAt, UpdatedBy: mod.UpdatedBy,
		}
	}
	if _, ok := m.modelProviders[mod.ProviderID]; !ok {
		return ErrNotFound
	}
	if mod.Source == "" {
		mod.Source = "custom"
	}
	if mod.ModelID == "" {
		_, bare, ok := strings.Cut(mod.ModelName, "/")
		if ok {
			mod.ModelID = bare
		} else {
			mod.ModelID = mod.ModelName
		}
	}
	m.models[mod.ID] = cloneModel(*mod)
	return nil
}

// GetModel returns a catalog model by id.
func (m *MemStore) GetModel(_ context.Context, id string) (*domain.Model, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.models[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := cloneModel(e)
	return &cp, nil
}

// ListModels returns the whole catalog, newest first.
func (m *MemStore) ListModels(_ context.Context) ([]domain.Model, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Model, 0, len(m.models))
	for _, e := range m.models {
		out = append(out, cloneModel(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// CountModels returns the number of catalog models.
func (m *MemStore) CountModels(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.models), nil
}

// UpdateModel updates a model's mutable fields. Duplicate name => ErrAlreadyExists.
func (m *MemStore) UpdateModel(_ context.Context, mod *domain.Model) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.models[mod.ID]; !ok {
		return ErrNotFound
	}
	for id, e := range m.models {
		if id != mod.ID && e.Name == mod.Name && e.ProjectID == mod.ProjectID {
			return ErrAlreadyExists
		}
	}
	if mod.Source == "" {
		mod.Source = "custom"
	}
	if mod.ModelID == "" {
		_, bare, ok := strings.Cut(mod.ModelName, "/")
		if ok {
			mod.ModelID = bare
		} else {
			mod.ModelID = mod.ModelName
		}
	}
	m.models[mod.ID] = cloneModel(*mod)
	if provider, ok := m.modelProviders[mod.ProviderID]; ok {
		kind, _, hasKind := strings.Cut(mod.ModelName, "/")
		if hasKind {
			provider.Kind = kind
		}
		provider.BaseURL = mod.BaseURL
		provider.APIKeyEnc = append([]byte(nil), mod.APIKeyEnc...)
		// Recompute auth_type from key presence, but preserve a valid keyless
		// service_identity provider — editing a model's metadata must not silently
		// downgrade it to none (mirrors the PG UpdateModel sync).
		if len(mod.APIKeyEnc) > 0 {
			provider.AuthType = domain.ModelProviderAuthAPIKey
		} else if provider.AuthType != domain.ModelProviderAuthServiceIdentity {
			provider.AuthType = domain.ModelProviderAuthNone
		}
		provider.UpdatedAt = time.Now().UTC()
		provider.UpdatedBy = mod.UpdatedBy
		m.modelProviders[provider.ID] = provider
		for id, sibling := range m.models {
			if sibling.ProviderID != provider.ID {
				continue
			}
			sibling.BaseURL = provider.BaseURL
			sibling.APIKeyEnc = append([]byte(nil), provider.APIKeyEnc...)
			sibling.ModelName = provider.Kind + "/" + sibling.ModelID
			m.models[id] = sibling
		}
	}
	return nil
}

// DeleteModel removes a model, cascading its grants and nulling any service
// default / run reference (mirrors the ON DELETE SET NULL / CASCADE FKs).
func (m *MemStore) DeleteModel(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deleteModelLocked(id)
}

// ListModelsForProject returns a project's USABLE model set, newest first (M1):
// its OWN enabled models UNION the cluster models granted to it.
func (m *MemStore) ListModelsForProject(_ context.Context, projectID string) ([]domain.Model, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Model
	for id, mod := range m.models {
		ownedEnabled := mod.ProjectID == projectID && mod.Enabled
		if ownedEnabled || m.modelGrants[grantKey(id, projectID)] {
			out = append(out, cloneModel(mod))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// ListProjectIDsForModel returns the project ids a model is granted to.
func (m *MemStore) ListProjectIDsForModel(_ context.Context, modelID string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.models[modelID]; !ok {
		return nil, ErrNotFound
	}
	var out []string
	prefix := modelID + "|"
	for k := range m.modelGrants {
		if strings.HasPrefix(k, prefix) {
			out = append(out, strings.TrimPrefix(k, prefix))
		}
	}
	sort.Strings(out)
	return out, nil
}

// GrantModel authorizes a project to use a model (idempotent).
func (m *MemStore) GrantModel(_ context.Context, modelID, projectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.models[modelID]; !ok {
		return ErrNotFound
	}
	if _, ok := m.projects[projectID]; !ok {
		return ErrNotFound
	}
	m.modelGrants[grantKey(modelID, projectID)] = true
	return nil
}

// RevokeModel removes a project's grant (idempotent no-op when absent).
func (m *MemStore) RevokeModel(_ context.Context, modelID, projectID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.modelGrants, grantKey(modelID, projectID))
	return nil
}

func accountGrantKey(modelID, userID string) string { return modelID + "|" + userID }

func (m *MemStore) ListModelsForAccount(_ context.Context, userID string) ([]domain.Model, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Model
	for modelID, model := range m.models {
		_, granted := m.modelAccountGrants[accountGrantKey(modelID, userID)]
		if model.ProjectID == "" && granted {
			out = append(out, cloneModel(model))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) ListAccountIDsForModel(_ context.Context, modelID string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.models[modelID]; !ok {
		return nil, ErrNotFound
	}
	prefix := modelID + "|"
	var out []string
	for key := range m.modelAccountGrants {
		if strings.HasPrefix(key, prefix) {
			out = append(out, strings.TrimPrefix(key, prefix))
		}
	}
	sort.Strings(out)
	return out, nil
}

func (m *MemStore) GrantModelToAccount(_ context.Context, modelID, userID, grantedBy string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	model, ok := m.models[modelID]
	if !ok || model.ProjectID != "" {
		return ErrNotFound
	}
	if _, ok := m.users[userID]; !ok {
		return ErrNotFound
	}
	m.modelAccountGrants[accountGrantKey(modelID, userID)] = grantedBy
	return nil
}

func (m *MemStore) RevokeModelFromAccount(_ context.Context, modelID, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.modelAccountGrants, accountGrantKey(modelID, userID))
	return nil
}

// --- cluster kanban config (D27, slimmed by D36) ---

func (m *MemStore) GetClusterKanbanConfig(_ context.Context) (*domain.KanbanConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.kanbanConfig == nil {
		return nil, ErrNotFound
	}
	// Clone so a caller can't mutate the stored row.
	cp := *m.kanbanConfig
	return &cp, nil
}

func (m *MemStore) UpsertClusterKanbanConfig(_ context.Context, cfg *domain.KanbanConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kanbanConfig = &domain.KanbanConfig{BaseURL: cfg.BaseURL, UpdatedBy: cfg.UpdatedBy, UpdatedAt: time.Now().UTC()}
	return nil
}

func (m *MemStore) DeleteClusterKanbanConfig(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kanbanConfig = nil // idempotent: absent => still nil
	return nil
}

// --- kanban integration (Feature E) ---

// claimKey is the kanban_claims natural key (linkID, documentID).
// --- integrations (D19 / F5) ---

func (m *MemStore) CreateIntegration(_ context.Context, in *domain.Integration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.integrations {
		if e.ProjectID == in.ProjectID && e.Name == in.Name {
			return fmt.Errorf("create integration: %w", ErrAlreadyExists)
		}
	}
	cp := *in
	if in.CredType == "" {
		cp.CredType = domain.CredTypePAT
	}
	cp.TokenEnc = append([]byte(nil), in.TokenEnc...)
	m.integrations[in.ID] = cp
	return nil
}

func (m *MemStore) GetIntegration(_ context.Context, id string) (*domain.Integration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.integrations[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := in
	cp.TokenEnc = append([]byte(nil), in.TokenEnc...)
	return &cp, nil
}

func (m *MemStore) ListIntegrationsByProject(_ context.Context, projectID string) ([]domain.Integration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Integration, 0)
	for _, in := range m.integrations {
		if in.ProjectID == projectID {
			cp := in
			cp.TokenEnc = append([]byte(nil), in.TokenEnc...)
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) UpdateIntegration(_ context.Context, in *domain.Integration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.integrations[in.ID]
	if !ok {
		return ErrNotFound
	}
	// Name uniqueness within the project (excluding this row).
	for id, e := range m.integrations {
		if id != in.ID && e.ProjectID == cur.ProjectID && e.Name == in.Name {
			return fmt.Errorf("update integration: %w", ErrAlreadyExists)
		}
	}
	cur.Name = in.Name
	cur.TokenEnc = append([]byte(nil), in.TokenEnc...)
	cur.BotUsername = in.BotUsername
	cur.UpdatedAt = time.Now().UTC()
	m.integrations[in.ID] = cur
	return nil
}

func (m *MemStore) DeleteIntegration(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.integrations[id]; !ok {
		return ErrNotFound
	}
	delete(m.integrations, id)
	// Null the FK on any service that referenced it (ON DELETE SET NULL parity).
	for sid, svc := range m.services {
		if svc.IntegrationID != nil && *svc.IntegrationID == id {
			svc.IntegrationID = nil
			m.services[sid] = svc
		}
	}
	return nil
}

func (m *MemStore) CountServicesUsingIntegration(_ context.Context, integrationID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, svc := range m.services {
		if svc.IntegrationID != nil && *svc.IntegrationID == integrationID {
			n++
		}
	}
	return n, nil
}

// --- Project plugins --------------------------------------------------------

func clonePluginInstallation(in domain.PluginInstallation) domain.PluginInstallation {
	cp := in
	cp.Scopes = append([]string(nil), in.Scopes...)
	cp.AccessTokenEnc = append([]byte(nil), in.AccessTokenEnc...)
	cp.RefreshTokenEnc = append([]byte(nil), in.RefreshTokenEnc...)
	return cp
}

func clonePluginProviderConfig(in domain.ProviderConfig) domain.ProviderConfig {
	cp := in
	cp.Capabilities = append([]string{}, in.Capabilities...)
	cp.ClientSecretEnc = append([]byte(nil), in.ClientSecretEnc...)
	cp.AppPrivateKeyEnc = append([]byte(nil), in.AppPrivateKeyEnc...)
	cp.WebhookSecretEnc = append([]byte(nil), in.WebhookSecretEnc...)
	return cp
}

func clonePluginCredentialVersion(in domain.PluginCredentialVersion) domain.PluginCredentialVersion {
	cp := in
	cp.AccessTokenEnc = append([]byte(nil), in.AccessTokenEnc...)
	cp.RefreshTokenEnc = append([]byte(nil), in.RefreshTokenEnc...)
	if in.TokenExpiresAt != nil {
		t := *in.TokenExpiresAt
		cp.TokenExpiresAt = &t
	}
	return cp
}

func providerConfigVersionKey(provider domain.ProviderKind, revision int64) string {
	return string(provider) + "|" + strconv.FormatInt(revision, 10)
}

func (m *MemStore) appendPluginCredentialVersionLocked(in *domain.PluginInstallation) {
	version := domain.PluginCredentialVersion{
		ID: domain.NewID(), InstallationID: in.ID, Provider: in.Provider,
		GitHubInstallID: in.GitHubInstallID,
		AccessTokenEnc:  append([]byte(nil), in.AccessTokenEnc...),
		RefreshTokenEnc: append([]byte(nil), in.RefreshTokenEnc...),
		CreatedAt:       time.Now().UTC(),
	}
	if in.TokenExpiresAt != nil {
		t := *in.TokenExpiresAt
		version.TokenExpiresAt = &t
	}
	m.pluginCredentialVersions[version.ID] = version
	in.CredentialVersionID = version.ID
}

// hydrateRunPluginSnapshotLocked exposes immutable version material only to
// the in-process credential issuer. The stored snapshot map itself retains
// just references, matching the SQL schema and avoiding per-run secret copies.
func (m *MemStore) hydrateRunPluginSnapshotLocked(snap domain.RunPluginSnapshot) domain.RunPluginSnapshot {
	if cfg, ok := m.providerConfigVersions[providerConfigVersionKey(snap.Provider, snap.ProviderConfigRevision)]; ok {
		snap.ProviderBaseURL = cfg.BaseURL
		snap.ProviderClientID = cfg.ClientID
		snap.ProviderClientSecretEnc = append([]byte(nil), cfg.ClientSecretEnc...)
		snap.ProviderAppID = cfg.AppID
		snap.ProviderAppPrivateKeyEnc = append([]byte(nil), cfg.AppPrivateKeyEnc...)
	}
	if version, ok := m.pluginCredentialVersions[snap.CredentialVersionID]; ok &&
		version.InstallationID == snap.InstallationID && version.Provider == snap.Provider {
		snap.GitHubInstallID = version.GitHubInstallID
		snap.AccessTokenEnc = append([]byte(nil), version.AccessTokenEnc...)
		snap.RefreshTokenEnc = append([]byte(nil), version.RefreshTokenEnc...)
		if version.TokenExpiresAt != nil {
			t := *version.TokenExpiresAt
			snap.TokenExpiresAt = &t
		}
	}
	return snap
}

func (m *MemStore) GetClusterSettings(_ context.Context) (*domain.ClusterSettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clusterSettings == nil {
		return nil, ErrNotFound
	}
	cp := *m.clusterSettings
	return &cp, nil
}

func (m *MemStore) UpsertClusterSettings(_ context.Context, settings *domain.ClusterSettings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *settings
	cp.UpdatedAt = time.Now().UTC()
	m.clusterSettings = &cp
	return nil
}

func (m *MemStore) GetProviderConfig(_ context.Context, provider domain.ProviderKind) (*domain.ProviderConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, ok := m.providerConfigs[provider]
	if !ok {
		return nil, ErrNotFound
	}
	cp := clonePluginProviderConfig(cfg)
	return &cp, nil
}

func (m *MemStore) ListProviderConfigs(_ context.Context) ([]domain.ProviderConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.ProviderConfig, 0, len(m.providerConfigs))
	for _, cfg := range m.providerConfigs {
		out = append(out, clonePluginProviderConfig(cfg))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out, nil
}

func (m *MemStore) UpsertProviderConfig(_ context.Context, cfg *domain.ProviderConfig) error {
	return m.upsertProviderConfigAndInvalidate(cfg, false, "")
}

func (m *MemStore) UpsertProviderConfigAndInvalidate(_ context.Context, cfg *domain.ProviderConfig, invalidate bool, reason string) error {
	return m.upsertProviderConfigAndInvalidate(cfg, invalidate, reason)
}

func (m *MemStore) upsertProviderConfigAndInvalidate(cfg *domain.ProviderConfig, invalidate bool, reason string) error {
	if cfg == nil || !domain.ValidProviderKind(cfg.Provider) {
		return fmt.Errorf("upsert provider config: invalid provider")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := clonePluginProviderConfig(*cfg)
	if old, ok := m.providerConfigs[cfg.Provider]; ok {
		cp.ConfigRevision = old.ConfigRevision + 1
	}
	if cp.ConfigRevision == 0 {
		cp.ConfigRevision = 1
	}
	cp.UpdatedAt = time.Now().UTC()
	m.providerConfigs[cfg.Provider] = cp
	m.providerConfigVersions[providerConfigVersionKey(cp.Provider, cp.ConfigRevision)] = clonePluginProviderConfig(cp)
	for id, installation := range m.pluginInstallations {
		if installation.Provider != cfg.Provider {
			continue
		}
		if invalidate {
			installation.Status = domain.PluginStatusActionRequired
			installation.LastHealthError = reason
		} else {
			installation.ConfigRevision = cp.ConfigRevision
		}
		installation.UpdatedAt = cp.UpdatedAt
		m.pluginInstallations[id] = installation
	}
	cfg.ConfigRevision = cp.ConfigRevision
	return nil
}

func (m *MemStore) CountProviderConfigImpact(_ context.Context, provider domain.ProviderKind) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, installation := range m.pluginInstallations {
		if installation.Provider == provider {
			n++
		}
	}
	return n, nil
}

func (m *MemStore) CreatePluginInstallation(_ context.Context, in *domain.PluginInstallation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.pluginInstallations {
		if existing.ProjectID == in.ProjectID && existing.Provider == in.Provider {
			return ErrAlreadyExists
		}
	}
	cp := clonePluginInstallation(*in)
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	cp.UpdatedAt = cp.CreatedAt
	m.appendPluginCredentialVersionLocked(&cp)
	m.pluginInstallations[cp.ID] = cp
	in.CredentialVersionID = cp.CredentialVersionID
	return nil
}

func (m *MemStore) GetPluginInstallation(_ context.Context, id string) (*domain.PluginInstallation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.pluginInstallations[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := clonePluginInstallation(in)
	return &cp, nil
}

func (m *MemStore) GetPluginInstallationForProject(_ context.Context, projectID string, provider domain.ProviderKind) (*domain.PluginInstallation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, in := range m.pluginInstallations {
		if in.ProjectID == projectID && in.Provider == provider {
			cp := clonePluginInstallation(in)
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemStore) ListPluginInstallationsByProject(_ context.Context, projectID string) ([]domain.PluginInstallation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.PluginInstallation{}
	for _, in := range m.pluginInstallations {
		if in.ProjectID == projectID {
			out = append(out, clonePluginInstallation(in))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out, nil
}

func (m *MemStore) UpdatePluginInstallation(_ context.Context, in *domain.PluginInstallation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pluginInstallations[in.ID]; !ok {
		return ErrNotFound
	}
	cp := clonePluginInstallation(*in)
	cp.UpdatedAt = time.Now().UTC()
	m.appendPluginCredentialVersionLocked(&cp)
	m.pluginInstallations[in.ID] = cp
	in.CredentialVersionID = cp.CredentialVersionID
	return nil
}

func (m *MemStore) RotatePluginCredentialVersion(_ context.Context, version *domain.PluginCredentialVersion) error {
	if version == nil || version.ID == "" || version.InstallationID == "" || !domain.ValidProviderKind(version.Provider) {
		return fmt.Errorf("rotate plugin credential version: invalid version")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.pluginCredentialVersions[version.ID]
	if !ok || current.InstallationID != version.InstallationID || current.Provider != version.Provider {
		return ErrNotFound
	}
	current.AccessTokenEnc = append([]byte(nil), version.AccessTokenEnc...)
	current.RefreshTokenEnc = append([]byte(nil), version.RefreshTokenEnc...)
	if version.TokenExpiresAt == nil {
		current.TokenExpiresAt = nil
	} else {
		t := *version.TokenExpiresAt
		current.TokenExpiresAt = &t
	}
	m.pluginCredentialVersions[version.ID] = current
	if installation, ok := m.pluginInstallations[version.InstallationID]; ok && installation.CredentialVersionID == version.ID {
		installation.AccessTokenEnc = append([]byte(nil), version.AccessTokenEnc...)
		installation.RefreshTokenEnc = append([]byte(nil), version.RefreshTokenEnc...)
		if version.TokenExpiresAt == nil {
			installation.TokenExpiresAt = nil
		} else {
			t := *version.TokenExpiresAt
			installation.TokenExpiresAt = &t
		}
		installation.UpdatedAt = time.Now().UTC()
		installation.LastHealthError = ""
		healthyAt := installation.UpdatedAt
		installation.LastHealthyAt = &healthyAt
		m.pluginInstallations[installation.ID] = installation
	}
	return nil
}

func (m *MemStore) CountPluginInstallationImpact(_ context.Context, installationID string) (services, automations int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.serviceRepoBindings {
		if b.InstallationID == installationID {
			services++
		}
	}
	for _, a := range m.pluginAutomations {
		if a.InstallationID == installationID {
			automations++
			continue
		}
		if b, ok := m.serviceRepoBindings[a.ServiceID]; ok && b.InstallationID == installationID {
			automations++
		}
	}
	return services, automations, nil
}

func (m *MemStore) UninstallPlugin(_ context.Context, installationID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pluginInstallations[installationID]; !ok {
		return ErrNotFound
	}
	serviceIDs := map[string]bool{}
	for serviceID, binding := range m.serviceRepoBindings {
		if binding.InstallationID == installationID {
			serviceIDs[serviceID] = true
		}
	}
	// Keep the claimed run intact until it has completed.  This mirrors the
	// Postgres uninstall fence and prevents a committed dispatch from losing its
	// run record between claim and Kubernetes Job creation.
	for _, run := range m.runs {
		if serviceIDs[run.ServiceID] && (run.Status == domain.StatusScheduling || run.Status == domain.StatusRunning || run.Status == domain.StatusAwaitingInput) {
			return ErrConflict
		}
	}
	for automationID, a := range m.pluginAutomations {
		if serviceIDs[a.ServiceID] || a.InstallationID == installationID {
			m.deletePluginAutomationLocked(automationID)
		}
	}
	for serviceID := range serviceIDs {
		m.deleteServiceLocked(serviceID)
	}
	m.clearPluginInstallationReferencesLocked(installationID)
	delete(m.pluginInstallations, installationID)
	return nil
}

// deletePluginAutomationLocked mirrors automations_v2's FK cascades and its
// SET NULL references from runs/webhook receipts. Caller must hold m.mu.
func (m *MemStore) deletePluginAutomationLocked(automationID string) {
	delete(m.pluginAutomations, automationID)
	delete(m.pluginSCMTriggers, automationID)
	delete(m.pluginKanbanTriggers, automationID)
	delete(m.pluginCronTriggers, automationID)
	for key, action := range m.pluginSCMActions {
		if action.AutomationID == automationID {
			delete(m.pluginSCMActions, key)
		}
	}
	for key, claim := range m.pluginKanbanClaims {
		if claim.AutomationID == automationID {
			delete(m.pluginKanbanClaims, key)
		}
	}
	for runID, run := range m.runs {
		if run.OriginAutomationID == automationID {
			run.OriginAutomationID = ""
			m.runs[runID] = run
		}
	}
	for key, receipt := range m.webhookReceipts {
		if receipt.MatchedAutomationID == automationID {
			receipt.MatchedAutomationID = ""
			m.webhookReceipts[key] = receipt
		}
	}
}

// clearPluginInstallationReferencesLocked implements the ON DELETE SET NULL
// references that deliberately preserve receipts and immutable audit entries.
// Caller must hold m.mu.
func (m *MemStore) clearPluginInstallationReferencesLocked(installationID string) {
	for key, receipt := range m.webhookReceipts {
		if receipt.InstallationID == installationID {
			receipt.InstallationID = ""
			m.webhookReceipts[key] = receipt
		}
	}
	for auditID, event := range m.pluginAuditEvents {
		if event.InstallationID == installationID {
			event.InstallationID = ""
			m.pluginAuditEvents[auditID] = event
		}
	}
}

func (m *MemStore) GetServiceRepositoryBinding(_ context.Context, serviceID string) (*domain.ServiceRepositoryBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.serviceRepoBindings[serviceID]
	if !ok {
		return nil, ErrNotFound
	}
	return &b, nil
}

func (m *MemStore) UpsertServiceRepositoryBinding(_ context.Context, b *domain.ServiceRepositoryBinding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	svc, ok := m.services[b.ServiceID]
	if !ok {
		return ErrNotFound
	}
	installation, ok := m.pluginInstallations[b.InstallationID]
	if !ok {
		return ErrNotFound
	}
	if err := validateServiceRepositoryBinding(&svc, &installation); err != nil {
		return err
	}
	for serviceID, existing := range m.serviceRepoBindings {
		if serviceID != b.ServiceID && existing.InstallationID == b.InstallationID && existing.ProviderRepoID == b.ProviderRepoID {
			return ErrAlreadyExists
		}
	}
	cp := *b
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	cp.UpdatedAt = time.Now().UTC()
	m.serviceRepoBindings[b.ServiceID] = cp
	return nil
}

func (m *MemStore) DeleteServiceRepositoryBinding(_ context.Context, serviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.serviceRepoBindings[serviceID]; !ok {
		return ErrNotFound
	}
	delete(m.serviceRepoBindings, serviceID)
	return nil
}

func (m *MemStore) CreatePluginAutomation(_ context.Context, a *domain.PluginAutomation, scm *domain.SCMTrigger, actions []domain.SCMAction, kanban *domain.KanbanTrigger, cron *domain.CronTrigger) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	svc, ok := m.services[a.ServiceID]
	if !ok {
		return ErrNotFound
	}
	if a.InstallationID != "" {
		installation, ok := m.pluginInstallations[a.InstallationID]
		if !ok {
			return ErrNotFound
		}
		if installation.ProjectID != svc.ProjectID {
			return fmt.Errorf("automation installation project mismatch")
		}
	}
	if err := validatePluginAutomationAggregate(a, scm, actions, kanban, cron); err != nil {
		return err
	}
	if err := m.validatePluginAutomationInstallationLocked(a, &svc, kanban); err != nil {
		return err
	}
	for _, action := range actions {
		key := action.ServiceID + "|" + action.EventFamily + "|" + action.Action
		if _, exists := m.pluginSCMActions[key]; exists {
			return ErrAlreadyExists
		}
	}
	cp := *a
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	cp.UpdatedAt = cp.CreatedAt
	m.pluginAutomations[cp.ID] = cp
	if scm != nil {
		m.pluginSCMTriggers[cp.ID] = *scm
	}
	if kanban != nil {
		m.pluginKanbanTriggers[cp.ID] = *kanban
	}
	if cron != nil {
		m.pluginCronTriggers[cp.ID] = *cron
	}
	for _, action := range actions {
		m.pluginSCMActions[action.ServiceID+"|"+action.EventFamily+"|"+action.Action] = action
	}
	return nil
}

func (m *MemStore) GetPluginAutomation(_ context.Context, id string) (*domain.PluginAutomation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.pluginAutomations[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &a, nil
}

func (m *MemStore) GetPluginAutomationSpec(_ context.Context, id string) (*domain.PluginAutomationSpec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.pluginAutomations[id]
	if !ok {
		return nil, ErrNotFound
	}
	spec := &domain.PluginAutomationSpec{Automation: a}
	if v, ok := m.pluginSCMTriggers[id]; ok {
		cp := v
		spec.SCM = &cp
	}
	if v, ok := m.pluginKanbanTriggers[id]; ok {
		cp := v
		spec.Kanban = &cp
	}
	if v, ok := m.pluginCronTriggers[id]; ok {
		cp := v
		spec.Cron = &cp
	}
	for _, v := range m.pluginSCMActions {
		if v.AutomationID == id {
			spec.Actions = append(spec.Actions, v)
		}
	}
	sort.Slice(spec.Actions, func(i, j int) bool {
		return spec.Actions[i].EventFamily+spec.Actions[i].Action < spec.Actions[j].EventFamily+spec.Actions[j].Action
	})
	return spec, nil
}

func (m *MemStore) ListPluginAutomationsByProject(_ context.Context, projectID string) ([]domain.PluginAutomation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.PluginAutomation{}
	for _, a := range m.pluginAutomations {
		if svc, ok := m.services[a.ServiceID]; ok && svc.ProjectID == projectID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) ListPluginAutomationsForEvent(_ context.Context, provider domain.ProviderKind, repositoryID string, family, action string) ([]domain.PluginAutomation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.PluginAutomation{}
	for _, a := range m.pluginAutomations {
		x, ok := m.pluginSCMActions[a.ServiceID+"|"+family+"|"+action]
		if !ok || x.AutomationID != a.ID || !a.Enabled {
			continue
		}
		b, ok := m.serviceRepoBindings[a.ServiceID]
		if !ok || b.ProviderRepoID != repositoryID {
			continue
		}
		in, ok := m.pluginInstallations[b.InstallationID]
		if !ok || in.Provider != provider || in.Status != domain.PluginStatusEnabled {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) UpdatePluginAutomation(_ context.Context, a *domain.PluginAutomation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.pluginAutomations[a.ID]
	if !ok {
		return ErrNotFound
	}
	// Mirror PG: this lightweight update deliberately cannot mutate aggregate
	// identity (Service, Installation, trigger kind, creator, created time).
	current.Name = a.Name
	current.PromptTemplate = a.PromptTemplate
	current.Enabled = a.Enabled
	current.IgnoreJCode = a.IgnoreJCode
	current.LastTriggeredAt = a.LastTriggeredAt
	current.LastRunID = a.LastRunID
	current.LastError = a.LastError
	current.UpdatedAt = time.Now().UTC()
	m.pluginAutomations[a.ID] = current
	return nil
}

func (m *MemStore) ReplacePluginAutomationSpec(_ context.Context, a *domain.PluginAutomation, scm *domain.SCMTrigger, actions []domain.SCMAction, kanban *domain.KanbanTrigger, cron *domain.CronTrigger) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pluginAutomations[a.ID]; !ok {
		return ErrNotFound
	}
	if err := validatePluginAutomationAggregate(a, scm, actions, kanban, cron); err != nil {
		return err
	}
	svc, ok := m.services[a.ServiceID]
	if !ok {
		return ErrNotFound
	}
	if a.InstallationID != "" {
		installation, ok := m.pluginInstallations[a.InstallationID]
		if !ok {
			return ErrNotFound
		}
		if installation.ProjectID != svc.ProjectID {
			return fmt.Errorf("automation installation project mismatch")
		}
	}
	if err := m.validatePluginAutomationInstallationLocked(a, &svc, kanban); err != nil {
		return err
	}
	for _, x := range actions {
		key := x.ServiceID + "|" + x.EventFamily + "|" + x.Action
		if current, ok := m.pluginSCMActions[key]; ok && current.AutomationID != a.ID {
			return ErrAlreadyExists
		}
	}
	for k, x := range m.pluginSCMActions {
		if x.AutomationID == a.ID {
			delete(m.pluginSCMActions, k)
		}
	}
	delete(m.pluginSCMTriggers, a.ID)
	delete(m.pluginKanbanTriggers, a.ID)
	delete(m.pluginCronTriggers, a.ID)
	cp := *a
	cp.UpdatedAt = time.Now().UTC()
	m.pluginAutomations[a.ID] = cp
	if scm != nil {
		m.pluginSCMTriggers[a.ID] = *scm
		for _, x := range actions {
			m.pluginSCMActions[x.ServiceID+"|"+x.EventFamily+"|"+x.Action] = x
		}
	}
	if kanban != nil {
		m.pluginKanbanTriggers[a.ID] = *kanban
	}
	if cron != nil {
		m.pluginCronTriggers[a.ID] = *cron
	}
	return nil
}

// validatePluginAutomationInstallationLocked mirrors the installation/service
// guards in migration 0044. Caller must hold m.mu.
func (m *MemStore) validatePluginAutomationInstallationLocked(a *domain.PluginAutomation, svc *domain.Service, kanban *domain.KanbanTrigger) error {
	switch a.TriggerKind {
	case "scm":
		binding, ok := m.serviceRepoBindings[svc.ID]
		if !ok || a.InstallationID == "" || binding.InstallationID != a.InstallationID {
			return fmt.Errorf("scm automation must use its service repository plugin")
		}
	case "kanban":
		installation, ok := m.pluginInstallations[a.InstallationID]
		if !ok || kanban == nil || kanban.InstallationID != a.InstallationID || installation.Provider != domain.PluginJType {
			return fmt.Errorf("kanban automation must use its jtype plugin")
		}
	case "cron":
		if a.InstallationID != "" {
			return fmt.Errorf("cron automation must not carry a plugin installation")
		}
	}
	return nil
}

func (m *MemStore) DeletePluginAutomation(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pluginAutomations[id]; !ok {
		return ErrNotFound
	}
	m.deletePluginAutomationLocked(id)
	return nil
}

func (m *MemStore) ListEnabledCronAutomations(_ context.Context) ([]domain.PluginAutomationSpec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.PluginAutomationSpec{}
	for id, automation := range m.pluginAutomations {
		if !automation.Enabled || automation.TriggerKind != "cron" {
			continue
		}
		cron, ok := m.pluginCronTriggers[id]
		if !ok {
			continue
		}
		out = append(out, domain.PluginAutomationSpec{Automation: automation, Cron: &cron})
	}
	return out, nil
}

func (m *MemStore) AdvancePluginCronAutomation(_ context.Context, id string, previous, firedAt *time.Time, lastError string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cron, ok := m.pluginCronTriggers[id]
	if !ok {
		return false, ErrNotFound
	}
	if (cron.LastFiredAt == nil) != (previous == nil) ||
		(cron.LastFiredAt != nil && !cron.LastFiredAt.Equal(*previous)) {
		return false, nil
	}
	cron.LastFiredAt = firedAt
	cron.LastError = lastError
	m.pluginCronTriggers[id] = cron
	if automation, ok := m.pluginAutomations[id]; ok {
		automation.LastError = lastError
		m.pluginAutomations[id] = automation
	}
	return true, nil
}

func (m *MemStore) ListEnabledKanbanAutomations(_ context.Context) ([]domain.PluginAutomationSpec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.PluginAutomationSpec{}
	for id, automation := range m.pluginAutomations {
		if !automation.Enabled || automation.TriggerKind != "kanban" {
			continue
		}
		trigger, ok := m.pluginKanbanTriggers[id]
		if ok {
			out = append(out, domain.PluginAutomationSpec{Automation: automation, Kanban: &trigger})
		}
	}
	return out, nil
}

func pluginKanbanClaimKey(automationID, documentID string) string {
	return automationID + "|" + documentID
}

func (m *MemStore) EnsurePluginKanbanClaim(_ context.Context, automationID, documentID, documentPath string) (*domain.PluginKanbanClaim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := pluginKanbanClaimKey(automationID, documentID)
	claim, ok := m.pluginKanbanClaims[key]
	if !ok {
		claim = domain.PluginKanbanClaim{AutomationID: automationID, DocumentID: documentID, DocumentPath: documentPath, CreatedAt: time.Now().UTC()}
		m.pluginKanbanClaims[key] = claim
	}
	copy := claim
	return &copy, nil
}

func (m *MemStore) SetPluginKanbanClaimRun(_ context.Context, automationID, documentID, runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := pluginKanbanClaimKey(automationID, documentID)
	claim, ok := m.pluginKanbanClaims[key]
	if !ok {
		return ErrNotFound
	}
	if claim.RunID != "" {
		return ErrAlreadyExists
	}
	claim.RunID = runID
	m.pluginKanbanClaims[key] = claim
	return nil
}

func (m *MemStore) ListPluginKanbanRunsAwaitingWriteback(_ context.Context) ([]PluginKanbanWriteback, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []PluginKanbanWriteback{}
	for _, claim := range m.pluginKanbanClaims {
		if claim.RunID == "" || claim.WritebackAt != nil {
			continue
		}
		run, ok := m.runs[claim.RunID]
		if !ok || !run.Status.Terminal() {
			continue
		}
		automation, ok := m.pluginAutomations[claim.AutomationID]
		if !ok {
			continue
		}
		trigger, ok := m.pluginKanbanTriggers[claim.AutomationID]
		if !ok {
			continue
		}
		out = append(out, PluginKanbanWriteback{Automation: automation, Trigger: trigger, Claim: claim, Run: run})
	}
	return out, nil
}

func (m *MemStore) MarkPluginKanbanWriteback(_ context.Context, automationID, documentID string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := pluginKanbanClaimKey(automationID, documentID)
	claim, ok := m.pluginKanbanClaims[key]
	if !ok {
		return false, ErrNotFound
	}
	if claim.WritebackAt != nil {
		return false, nil
	}
	claim.WritebackAt = &at
	m.pluginKanbanClaims[key] = claim
	return true, nil
}

func (m *MemStore) ClaimWebhookReceipt(_ context.Context, r *domain.WebhookReceipt) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := string(r.Provider) + "|" + r.DeliveryID
	if _, ok := m.webhookReceipts[key]; ok {
		return false, nil
	}
	cp := *r
	if cp.ReceivedAt.IsZero() {
		cp.ReceivedAt = time.Now().UTC()
	}
	m.webhookReceipts[key] = cp
	return true, nil
}

func (m *MemStore) CompleteWebhookReceipt(_ context.Context, r *domain.WebhookReceipt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := string(r.Provider) + "|" + r.DeliveryID
	if _, ok := m.webhookReceipts[key]; !ok {
		return ErrNotFound
	}
	m.webhookReceipts[key] = *r
	return nil
}

func (m *MemStore) CreateRunPluginSnapshots(_ context.Context, snapshots []domain.RunPluginSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Validate every referenced aggregate before changing any existing snapshot;
	// this matches the all-or-nothing PG transaction semantics.
	runIDs := map[string]struct{}{}
	for _, snap := range snapshots {
		if _, ok := m.runs[snap.RunID]; !ok {
			return ErrNotFound
		}
		if _, ok := m.pluginInstallations[snap.InstallationID]; !ok {
			return ErrNotFound
		}
		runIDs[snap.RunID] = struct{}{}
	}
	staged := make(map[string]map[string]domain.RunPluginSnapshot, len(runIDs))
	for runID := range runIDs {
		run, ok := m.runs[runID]
		if !ok {
			return ErrNotFound
		}
		staged[runID] = map[string]domain.RunPluginSnapshot{}
		for installationID, snapshot := range m.runPluginSnapshots[runID] {
			staged[runID][installationID] = snapshot
		}
		if run.Status != domain.StatusQueued {
			continue
		}
		for installationID := range staged[runID] {
			installation, ok := m.pluginInstallations[installationID]
			cfg, configOK := m.providerConfigs[installation.Provider]
			if !ok || installation.ProjectID != run.ProjectID ||
				installation.Status != domain.PluginStatusEnabled ||
				installation.LastHealthError != "" ||
				!configOK || !cfg.PluginEnabled || cfg.ConfigRevision != installation.ConfigRevision ||
				installation.CredentialVersionID == "" ||
				(installation.Provider == domain.PluginGitHub && installation.GitHubInstallID == "") ||
				(installation.Provider != domain.PluginGitHub && !installation.TokenSet()) {
				delete(staged[runID], installationID)
			}
		}
	}
	for _, snap := range snapshots {
		run, ok := m.runs[snap.RunID]
		if !ok {
			return ErrNotFound
		}
		installation, ok := m.pluginInstallations[snap.InstallationID]
		if !ok {
			return ErrNotFound
		}
		cfg, configOK := m.providerConfigs[installation.Provider]
		if installation.ProjectID != run.ProjectID ||
			installation.Status != domain.PluginStatusEnabled ||
			installation.LastHealthError != "" ||
			!configOK || !cfg.PluginEnabled || cfg.ConfigRevision != installation.ConfigRevision ||
			installation.CredentialVersionID == "" ||
			(installation.Provider == domain.PluginGitHub && installation.GitHubInstallID == "") ||
			(installation.Provider != domain.PluginGitHub && !installation.TokenSet()) {
			continue
		}
		if snap.CreatedAt.IsZero() {
			snap.CreatedAt = time.Now().UTC()
		}
		// Persist references only. Immutable versions are joined by the reader.
		snap.Provider = installation.Provider
		snap.ProviderConfigRevision = installation.ConfigRevision
		snap.CredentialVersionID = installation.CredentialVersionID
		snap.ProviderBaseURL, snap.ProviderClientID, snap.ProviderClientSecretEnc = "", "", nil
		snap.ProviderAppID, snap.ProviderAppPrivateKeyEnc = "", nil
		snap.GitHubInstallID, snap.AccessTokenEnc, snap.RefreshTokenEnc, snap.TokenExpiresAt = "", nil, nil, nil
		staged[snap.RunID][snap.InstallationID] = snap
	}
	for runID, next := range staged {
		m.runPluginSnapshots[runID] = next
	}
	return nil
}

func (m *MemStore) ClearQueuedRunPluginSnapshots(_ context.Context, runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[runID]
	if !ok {
		return ErrNotFound
	}
	if run.Status == domain.StatusQueued {
		delete(m.runPluginSnapshots, runID)
	}
	return nil
}

func (m *MemStore) ListRunPluginSnapshots(_ context.Context, runID string) ([]domain.RunPluginSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.RunPluginSnapshot{}
	for _, snap := range m.runPluginSnapshots[runID] {
		out = append(out, m.hydrateRunPluginSnapshotLocked(snap))
	}
	return out, nil
}

func (m *MemStore) CreatePluginAuditEvent(_ context.Context, event *domain.PluginAuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *event
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	m.pluginAuditEvents[cp.ID] = cp
	return nil
}

func (m *MemStore) ListPluginAuditEvents(_ context.Context, projectID string, limit int) ([]domain.PluginAuditEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.PluginAuditEvent{}
	for _, event := range m.pluginAuditEvents {
		if event.ProjectID == projectID {
			out = append(out, event)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemStore) ListPluginInstallationAuditEvents(_ context.Context, projectID, installationID string, limit int) ([]domain.PluginAuditEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.PluginAuditEvent{}
	for _, event := range m.pluginAuditEvents {
		if event.ProjectID == projectID && event.InstallationID == installationID {
			out = append(out, event)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func claimKey(linkID, documentID string) string { return linkID + "|" + documentID }

func cloneKanbanLink(l domain.KanbanLink) domain.KanbanLink {
	cp := l
	if l.TokenEnc != nil {
		cp.TokenEnc = append([]byte(nil), l.TokenEnc...)
	}
	if l.TokenExpiresAt != nil {
		t := *l.TokenExpiresAt
		cp.TokenExpiresAt = &t
	}
	if l.EventSequence != nil {
		sequence := *l.EventSequence
		cp.EventSequence = &sequence
	}
	return cp
}

func (m *MemStore) CreateKanbanLink(_ context.Context, l *domain.KanbanLink) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.kanbanLinks {
		if e.WorkspaceID == l.WorkspaceID && e.BoardRef == l.BoardRef {
			return fmt.Errorf("create kanban link: %w", ErrAlreadyExists)
		}
	}
	cp := cloneKanbanLink(*l)
	if cp.BoardStatus == "" {
		cp.BoardStatus = domain.KanbanBoardOK // mirror the pg DEFAULT
	}
	m.kanbanLinks[l.ID] = cp
	return nil
}

func (m *MemStore) GetKanbanLink(_ context.Context, id string) (*domain.KanbanLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.kanbanLinks[id]
	if !ok {
		return nil, ErrNotFound
	}
	// Deep-copy the token blob + pointer fields so a caller can't mutate the
	// stored row through the returned copy (matches GetClusterKanbanConfig).
	cp := cloneKanbanLink(l)
	return &cp, nil
}

func (m *MemStore) ListKanbanLinks(_ context.Context) ([]domain.KanbanLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.KanbanLink, 0, len(m.kanbanLinks))
	for _, l := range m.kanbanLinks {
		out = append(out, cloneKanbanLink(l))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) ListKanbanLinksByProject(_ context.Context, projectID string) ([]domain.KanbanLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.KanbanLink, 0)
	for _, l := range m.kanbanLinks {
		if l.ProjectID == projectID {
			out = append(out, cloneKanbanLink(l))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) ListEnabledKanbanLinks(_ context.Context) ([]domain.KanbanLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.KanbanLink
	for _, l := range m.kanbanLinks {
		if l.Enabled {
			out = append(out, cloneKanbanLink(l))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) SetKanbanLinkToken(_ context.Context, id string, tokenEnc []byte, expiresAt *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.kanbanLinks[id]
	if !ok {
		return ErrNotFound
	}
	// Copy the blob defensively (callers may reuse their buffer); nil clears.
	if tokenEnc == nil {
		l.TokenEnc = nil
	} else {
		l.TokenEnc = append([]byte(nil), tokenEnc...)
	}
	// token_expires_at follows the token: nil (manual paste/clear) => NULL; a
	// device-flow expiry copies the value (D28).
	if expiresAt == nil {
		l.TokenExpiresAt = nil
	} else {
		t := *expiresAt
		l.TokenExpiresAt = &t
	}
	l.UpdatedAt = time.Now().UTC()
	m.kanbanLinks[id] = l
	return nil
}

func (m *MemStore) SetKanbanLinkBoardStatus(_ context.Context, id, status, canonicalRef, title string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.kanbanLinks[id]
	if !ok {
		return ErrNotFound
	}
	l.BoardStatus = status
	if canonicalRef != "" {
		l.BoardRef = canonicalRef
	}
	if title != "" {
		l.BoardTitle = title
	}
	l.UpdatedAt = time.Now().UTC()
	m.kanbanLinks[id] = l
	return nil
}

func (m *MemStore) AdvanceKanbanLinkEventSequence(_ context.Context, id string, sequence int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.kanbanLinks[id]
	if !ok {
		return ErrNotFound
	}
	if l.EventSequence == nil || sequence > *l.EventSequence {
		value := sequence
		l.EventSequence = &value
		l.UpdatedAt = time.Now().UTC()
		m.kanbanLinks[id] = l
	}
	return nil
}

func (m *MemStore) DeleteKanbanLink(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.kanbanLinks[id]; !ok {
		return ErrNotFound
	}
	delete(m.kanbanLinks, id)
	// Cascade: drop claims belonging to the removed link.
	for k, c := range m.kanbanClaims {
		if c.LinkID == id {
			delete(m.kanbanClaims, k)
		}
	}
	return nil
}

func (m *MemStore) EnsureKanbanClaim(_ context.Context, linkID, documentID, documentPath string) (*domain.KanbanClaim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := claimKey(linkID, documentID)
	if c, ok := m.kanbanClaims[key]; ok {
		cp := c
		return &cp, nil
	}
	c := domain.KanbanClaim{
		ID:           domain.NewID(),
		LinkID:       linkID,
		DocumentID:   documentID,
		DocumentPath: documentPath,
		ClaimedAt:    time.Now().UTC(),
	}
	m.kanbanClaims[key] = c
	cp := c
	return &cp, nil
}

func (m *MemStore) SetKanbanClaimRun(_ context.Context, linkID, documentID, runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := claimKey(linkID, documentID)
	c, ok := m.kanbanClaims[key]
	if !ok {
		return ErrNotFound
	}
	if c.RunID == "" {
		c.RunID = runID
		m.kanbanClaims[key] = c
	}
	return nil
}

func (m *MemStore) MarkKanbanNotConfiguredNotified(_ context.Context, linkID, documentID string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := claimKey(linkID, documentID)
	c, ok := m.kanbanClaims[key]
	if !ok {
		return false, ErrNotFound
	}
	if c.NotifiedNotConfiguredAt != nil {
		return false, nil
	}
	t := at
	c.NotifiedNotConfiguredAt = &t
	m.kanbanClaims[key] = c
	return true, nil
}

func (m *MemStore) ListKanbanRunsAwaitingWriteback(ctx context.Context) ([]KanbanWriteback, error) {
	m.mu.Lock()
	claims := make([]domain.KanbanClaim, 0)
	for _, c := range m.kanbanClaims {
		if c.RunID != "" && c.WritebackAt == nil {
			claims = append(claims, c)
		}
	}
	linkByID := map[string]domain.KanbanLink{}
	for _, l := range m.kanbanLinks {
		linkByID[l.ID] = l
	}
	m.mu.Unlock()

	sort.Slice(claims, func(i, j int) bool { return claims[i].ClaimedAt.Before(claims[j].ClaimedAt) })
	var out []KanbanWriteback
	for _, c := range claims {
		run, err := m.GetRun(ctx, c.RunID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		if !run.Status.Terminal() {
			continue
		}
		link, ok := linkByID[c.LinkID]
		if !ok {
			continue // link removed; nothing to write back to
		}
		out = append(out, KanbanWriteback{Claim: c, Run: *run, Link: link})
	}
	return out, nil
}

func (m *MemStore) MarkKanbanWriteback(_ context.Context, linkID, documentID string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := claimKey(linkID, documentID)
	c, ok := m.kanbanClaims[key]
	if !ok {
		return false, ErrNotFound
	}
	if c.WritebackAt != nil {
		return false, nil
	}
	t := at
	c.WritebackAt = &t
	m.kanbanClaims[key] = c
	return true, nil
}

// --- Schedules (F11 / D24) --------------------------------------------------

func (m *MemStore) CreateSchedule(_ context.Context, sc *domain.Schedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.schedules[sc.ID] = *sc
	return nil
}

func (m *MemStore) GetSchedule(_ context.Context, id string) (*domain.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sc, ok := m.schedules[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := sc
	return &cp, nil
}

func (m *MemStore) ListSchedulesByService(_ context.Context, serviceID string) ([]domain.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Schedule, 0)
	for _, sc := range m.schedules {
		if sc.ServiceID == serviceID {
			out = append(out, sc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) ListEnabledSchedules(_ context.Context) ([]domain.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Schedule
	for _, sc := range m.schedules {
		if sc.Enabled {
			out = append(out, sc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) UpdateSchedule(_ context.Context, sc *domain.Schedule, resetWindow bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.schedules[sc.ID]
	if !ok {
		return ErrNotFound
	}
	// Only the owner-editable fields change; last_error stays poller-owned.
	// resetWindow (cron changed / re-enabled) moves last_fired_at to NOW so the
	// next fire is computed from the edit instant — never a backfill of a
	// boundary that predates the edit (C1; mirrors the pg CASE expression).
	cur.CronExpr = sc.CronExpr
	cur.Prompt = sc.Prompt
	cur.Enabled = sc.Enabled
	now := time.Now().UTC()
	if resetWindow {
		t := now
		cur.LastFiredAt = &t
	}
	cur.UpdatedAt = now
	m.schedules[sc.ID] = cur
	sc.LastFiredAt = cur.LastFiredAt
	sc.UpdatedAt = cur.UpdatedAt
	return nil
}

func (m *MemStore) DeleteSchedule(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.schedules[id]; !ok {
		return ErrNotFound
	}
	delete(m.schedules, id)
	return nil
}

func (m *MemStore) AdvanceSchedule(_ context.Context, id string, prevFired *time.Time, newFired time.Time, lastErr string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sc, ok := m.schedules[id]
	if !ok {
		return false, ErrNotFound
	}
	// Conditional claim: the row's current last_fired_at must equal prevFired
	// (both nil, or same instant) — the SQL `IS NOT DISTINCT FROM` semantics. A
	// racing advance that already moved it loses here (won=false).
	if !timePtrEqual(sc.LastFiredAt, prevFired) {
		return false, nil
	}
	t := newFired
	sc.LastFiredAt = &t
	sc.LastError = lastErr
	sc.UpdatedAt = time.Now().UTC()
	m.schedules[id] = sc
	return true, nil
}

func (m *MemStore) SetScheduleLastError(_ context.Context, id, lastErr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sc, ok := m.schedules[id]
	if !ok {
		return ErrNotFound
	}
	sc.LastError = lastErr
	sc.UpdatedAt = time.Now().UTC()
	m.schedules[id] = sc
	return nil
}

// --- PR review Automations --------------------------------------------------

func cloneAutomation(a domain.Automation) domain.Automation {
	a.Events = append([]domain.AutomationEvent(nil), a.Events...)
	return a
}

func (m *MemStore) CreateAutomation(_ context.Context, a *domain.Automation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.automations[a.ID]; exists {
		return ErrAlreadyExists
	}
	m.automations[a.ID] = cloneAutomation(*a)
	return nil
}

func (m *MemStore) GetAutomation(_ context.Context, id string) (*domain.Automation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.automations[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := cloneAutomation(a)
	return &cp, nil
}

func (m *MemStore) ListAutomationsByService(_ context.Context, serviceID string) ([]domain.Automation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Automation, 0)
	for _, a := range m.automations {
		if a.ServiceID == serviceID {
			out = append(out, cloneAutomation(a))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) UpdateAutomation(_ context.Context, a *domain.Automation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.automations[a.ID]; !ok {
		return ErrNotFound
	}
	m.automations[a.ID] = cloneAutomation(*a)
	return nil
}

func (m *MemStore) DeleteAutomation(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.automations[id]; !ok {
		return ErrNotFound
	}
	delete(m.automations, id)
	return nil
}

func (m *MemStore) RecordAutomationDispatch(_ context.Context, id string, at time.Time, runID, lastErr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.automations[id]
	if !ok {
		return ErrNotFound
	}
	a.LastTriggeredAt = &at
	a.LastRunID = runID
	a.LastError = lastErr
	a.UpdatedAt = time.Now().UTC()
	m.automations[id] = a
	return nil
}

func (m *MemStore) UpsertWebhookBinding(_ context.Context, b *domain.WebhookBinding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := *b
	if current, ok := m.webhookBindings[b.ServiceID]; ok {
		// Match the Postgres upsert: reconciling the provider hook updates sync
		// state without erasing the last delivery observation.
		next.LastDeliveryAt = current.LastDeliveryAt
		next.LastDeliveryStatus = current.LastDeliveryStatus
	}
	m.webhookBindings[b.ServiceID] = next
	return nil
}

func (m *MemStore) GetWebhookBinding(_ context.Context, serviceID string) (*domain.WebhookBinding, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.webhookBindings[serviceID]
	if !ok {
		return nil, ErrNotFound
	}
	return &b, nil
}

func (m *MemStore) DeleteWebhookBinding(_ context.Context, serviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.webhookBindings[serviceID]; !ok {
		return ErrNotFound
	}
	delete(m.webhookBindings, serviceID)
	return nil
}

func (m *MemStore) RecordWebhookDelivery(_ context.Context, serviceID string, at time.Time, status, lastErr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.webhookBindings[serviceID]
	if !ok {
		return ErrNotFound
	}
	b.LastDeliveryAt = &at
	b.LastDeliveryStatus = status
	b.LastError = lastErr
	b.UpdatedAt = time.Now().UTC()
	m.webhookBindings[serviceID] = b
	return nil
}

// --- API keys (F12 / D24) ---------------------------------------------------

func (m *MemStore) CreateAPIKey(_ context.Context, k *domain.APIKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.apiKeys[k.ID] = *k
	return nil
}

func (m *MemStore) GetAPIKey(_ context.Context, id string) (*domain.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.apiKeys[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := k
	return &cp, nil
}

// GetAPIKeyByHash excludes revoked rows — mirrors PGStore's `revoked_at IS
// NULL` filter so ErrNotFound uniformly covers "unknown hash" and "revoked
// key" (see the Store interface doc).
func (m *MemStore) GetAPIKeyByHash(_ context.Context, keyHash string) (*domain.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range m.apiKeys {
		if k.KeyHash == keyHash && k.RevokedAt == nil {
			cp := k
			return &cp, nil
		}
	}
	return nil, ErrNotFound
}

func (m *MemStore) ListAPIKeysByProject(_ context.Context, projectID string) ([]domain.APIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.APIKey, 0)
	for _, k := range m.apiKeys {
		if k.ProjectID == projectID {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *MemStore) UpdateAPIKeyLastUsed(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.apiKeys[id]
	if !ok {
		return ErrNotFound
	}
	t := at
	k.LastUsedAt = &t
	m.apiKeys[id] = k
	return nil
}

// RevokeAPIKey is idempotent (mirrors PGStore): a missing id or an
// already-revoked key is a silent no-op, not an error.
func (m *MemStore) RevokeAPIKey(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.apiKeys[id]
	if !ok || k.RevokedAt != nil {
		return nil
	}
	t := time.Now().UTC()
	k.RevokedAt = &t
	m.apiKeys[id] = k
	return nil
}

// timePtrEqual reports whether two *time.Time are both nil or point to the same
// instant — the in-memory analogue of Postgres `IS NOT DISTINCT FROM` for the
// conditional schedule advance.
func timePtrEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

var _ Store = (*MemStore)(nil)
