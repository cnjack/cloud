package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/jtype"
	"golang.org/x/text/unicode/norm"
)

// D31 — embed the real jtype board in the console project page. The console
// injects a board-react `client` whose every listDocuments/getDocument/
// saveDocument call hits the proxy below; jcloud resolves the EFFECTIVE jtype
// token server-side and forwards to jtype with a Bearer header, so the token
// NEVER reaches the browser. Successful responses retain their complete JSON
// shape; upstream error bodies and any response that reflects the credential
// are deliberately not forwarded. See ProxyDocumentAPI in internal/jtype/client.go.
//
// Authz: reads AND writes are member+. Write = member+ matches run-dispatch
// authority exactly (a board move via saveDocument is what the poller turns into a
// run, and POST /runs is member+); read is the same threshold because the board is
// a single read+write component (a viewer cannot meaningfully use it without
// attempting a write). The Kanban button therefore never renders for viewers — the
// member+ board/links endpoint 403s them into an empty list. Owner-only link
// management + discovery (kanban.go / kanban_discovery.go) are untouched.

// boardEmbedLinkView is the REDUCED board-link shape that gates the console's
// Kanban button + feeds the modal's link picker. It carries NO credential fields
// (no token_set / credential_status / token_expires_at) so a member never learns
// the link's credential posture — unlike the owner-only kanbanLinkView.
type boardEmbedLinkView struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	BoardRef      string `json:"board_ref"`
	BoardTitle    string `json:"board_title,omitempty"`
	BoardStatus   string `json:"board_status"`
	ServiceID     string `json:"service_id"`
	TriggerColumn string `json:"trigger_column"`
	DoneColumn    string `json:"done_column,omitempty"`
	Enabled       bool   `json:"enabled"`
}

// jtypeBoardProxy is the slice of *jtype.Client the board embed proxy uses to
// forward document reads/writes to jtype verbatim (D31). A test injects a fake
// so the proxy endpoints are exercised without HTTP.
type jtypeBoardProxy interface {
	ProxyDocumentAPI(ctx context.Context, method, path string, body io.Reader) (*http.Response, error)
}

// maxProxyBody caps both the request body streamed to jtype (a save) and the
// upstream body copied back, so neither side can stream unbounded through the
// proxy. Board documents are small markdown/JSON; 8 MiB is generous headroom.
const maxProxyBody = 8 << 20

// handleListBoardEmbedLinks returns the project's kanban links in the reduced,
// credential-free boardEmbedLinkView (viewer+). The console gates the manual
// Kanban button to member+, while execution-output deep links use this read-only
// discovery path for viewers. Non-members still receive 403.
func (s *Server) handleListBoardEmbedLinks(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, domain.RoleViewer) {
		return
	}
	automations, err := s.st.ListPluginAutomationsByProject(r.Context(), projectID)
	if err != nil {
		s.log.Error("list board bindings", "project", projectID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list Kanban bindings")
		return
	}
	cfg, cfgErr := s.st.GetProviderConfig(r.Context(), domain.PluginJType)
	out := make([]boardEmbedLinkView, 0)
	for _, a := range automations {
		if a.TriggerKind != "kanban" || !a.Enabled {
			continue
		}
		spec, getErr := s.st.GetPluginAutomationSpec(r.Context(), a.ID)
		if getErr != nil || spec.Kanban == nil {
			continue
		}
		in, getErr := s.st.GetPluginInstallation(r.Context(), spec.Kanban.InstallationID)
		if getErr != nil || cfgErr != nil || !cfg.PluginEnabled ||
			in.Provider != domain.PluginJType || in.Status != domain.PluginStatusEnabled ||
			in.ConfigRevision != cfg.ConfigRevision || in.LastHealthError != "" || in.WorkspaceID == "" {
			continue
		}
		out = append(out, boardEmbedLinkView{ID: a.ID, WorkspaceID: in.WorkspaceID, BoardRef: spec.Kanban.BoardRef, BoardStatus: domain.KanbanBoardOK, ServiceID: a.ServiceID, TriggerColumn: spec.Kanban.TriggerColumn, DoneColumn: spec.Kanban.DoneColumn, Enabled: a.Enabled})
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out})
}

// resolveBoardProxy is the shared gate for every documents/* handler. It
// authorizes viewer+ reads and member+ writes, enforces the confused-deputy
// workspace-scoping guard, and resolves the effective jtype client + token
// server-side — or writes a typed fail-visible error and returns ok=false (the
// caller must stop). The returned client already carries the resolved token;
// the workspace string is validated.
func (s *Server) resolveBoardProxy(w http.ResponseWriter, r *http.Request) (client jtypeBoardProxy, workspace, credential string, ok bool) {
	projectID := r.PathValue("id")
	requiredRole := domain.RoleViewer
	if r.Method != http.MethodGet {
		requiredRole = domain.RoleMember
	}
	if !s.authorizeProject(r.Context(), w, principalFrom(r.Context()), projectID, requiredRole) {
		return nil, "", "", false
	}

	workspace = strings.TrimSpace(r.URL.Query().Get("workspace"))
	if workspace == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "workspace query parameter is required")
		return nil, "", "", false
	}

	// ---- CONFUSED-DEPUTY GUARD (the security crux) ----
	// Workspace browsing belongs to the Project JType Plugin grant, not to an
	// enabled Service trigger. This preserves useful JType access before Kanban
	// is enabled while still rejecting every foreign workspace before any
	// upstream round-trip. Writes are narrowed separately below to cards on an
	// enabled Service board.
	in, err := s.st.GetPluginInstallationForProject(r.Context(), projectID, domain.PluginJType)
	if err == nil && in.Status == domain.PluginStatusEnabled && in.WorkspaceID == workspace {
		cfg, cfgErr := s.st.GetProviderConfig(r.Context(), domain.PluginJType)
		token, tokenOK := s.pluginAccessToken(in)
		if cfgErr != nil || !cfg.PluginEnabled || strings.TrimSpace(cfg.BaseURL) == "" ||
			cfg.ConfigRevision != in.ConfigRevision || in.LastHealthError != "" || !tokenOK {
			writeError(w, http.StatusServiceUnavailable, "jtype_unreachable", "the JType Plugin credential is unavailable; reconnect it")
			return nil, "", "", false
		}
		f := jtype.NewFactory(cfg.BaseURL, 20*time.Second)
		return s.boardProxyFor(f, token), workspace, token, true
	}
	// 403, not 404: do not confirm whether a foreign workspace exists.
	writeError(w, http.StatusForbidden, "workspace_not_linked",
		"this workspace is not linked to this project")
	return nil, "", "", false
}

// forwardBoardAPI issues the built request through the token-bound proxy and
// preserves successful JSON responses while blocking common accidental
// credential reflections and suppressing all upstream error detail. The
// cluster-configured JType instance remains a trusted credential recipient:
// arbitrary provider content cannot be proven free of a deliberately encoded
// secret (see the documented Provider trust boundary).
func (s *Server) forwardBoardAPI(w http.ResponseWriter, r *http.Request, client jtypeBoardProxy, credential, method, path string, body io.Reader) {
	resp, err := client.ProxyDocumentAPI(r.Context(), method, path, body)
	if err != nil {
		// Genuine network / instance-down. The raw error carries jtype's internal
		// URL/host — log it, but return a GENERIC message so a project member can't
		// probe the cluster's jtype address through the proxy.
		s.log.Warn("board proxy: jtype request", "method", method, "path", path, "err", err)
		writeError(w, http.StatusServiceUnavailable, "jtype_unreachable",
			"could not reach jtype")
		return
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxProxyBody+1))
	if err != nil || len(raw) > maxProxyBody {
		s.log.Warn("board proxy: invalid upstream body", "method", method, "path", path, "err", err)
		writeError(w, http.StatusBadGateway, "jtype_invalid_response", "JType returned an invalid response")
		return
	}
	if responseContainsCredential(raw, credential) {
		s.log.Error("board proxy: upstream reflected a credential", "method", method, "path", path)
		writeError(w, http.StatusBadGateway, "jtype_unsafe_response", "JType returned an unsafe response")
		return
	}
	if resp.StatusCode >= http.StatusBadRequest {
		code := "jtype_upstream_error"
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			code = "jtype_unauthorized"
		}
		writeError(w, resp.StatusCode, code, "JType rejected the board request")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(raw)
}

func responseContainsCredential(raw []byte, credential string) bool {
	if credential == "" {
		return false
	}
	encodings := []string{
		credential,
		base64.StdEncoding.EncodeToString([]byte(credential)),
		base64.RawStdEncoding.EncodeToString([]byte(credential)),
		base64.URLEncoding.EncodeToString([]byte(credential)),
		base64.RawURLEncoding.EncodeToString([]byte(credential)),
		hex.EncodeToString([]byte(credential)),
		strings.ToUpper(hex.EncodeToString([]byte(credential))),
		url.QueryEscape(credential),
		url.PathEscape(credential),
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return true // successful board responses must be JSON
	}
	var contains func(any) bool
	contains = func(value any) bool {
		switch v := value.(type) {
		case string:
			for _, candidate := range encodings {
				if candidate != "" && strings.Contains(v, candidate) {
					return true
				}
			}
		case []any:
			for _, item := range v {
				if contains(item) {
					return true
				}
			}
		case map[string]any:
			for key, item := range v {
				if contains(key) || contains(item) {
					return true
				}
			}
		}
		return false
	}
	return contains(decoded)
}

// handleBoardListDocuments proxies board-react's listDocuments(ws) (member+).
func (s *Server) handleBoardListDocuments(w http.ResponseWriter, r *http.Request) {
	client, ws, credential, ok := s.resolveBoardProxy(w, r)
	if !ok {
		return
	}
	// The upstream path is built server-side from the validated workspace only; no
	// client-controlled segment is forwarded.
	path := "/api/v1/workspaces/" + url.PathEscape(ws) + "/documents"
	s.forwardBoardAPI(w, r, client, credential, http.MethodGet, path, nil)
}

// handleBoardGetDocument proxies board-react's getDocument(ws, docId) (member+).
func (s *Server) handleBoardGetDocument(w http.ResponseWriter, r *http.Request) {
	client, ws, credential, ok := s.resolveBoardProxy(w, r)
	if !ok {
		return
	}
	docID := strings.TrimSpace(r.PathValue("docID"))
	if docID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "document id is required")
		return
	}
	path := "/api/v1/workspaces/" + url.PathEscape(ws) + "/documents/" + url.PathEscape(docID)
	s.forwardBoardAPI(w, r, client, credential, http.MethodGet, path, nil)
}

// handleBoardSaveDocument proxies board-react's saveDocument(ws, req) — a card
// create/edit/move (member+). The (bounded) body is buffered so the target path
// can be validated: the embed may create ordinary Cards under `cards/` and may
// update an existing Cloud-managed Automation Card under `jcode-automation/`.
// It can never create in the managed namespace, write a `.board` config JSON,
// or target a traversal/other-type path. jtype's SaveDocument response
// (mergeStatus/contentHash) is copied back verbatim.
func (s *Server) handleBoardSaveDocument(w http.ResponseWriter, r *http.Request) {
	client, ws, credential, ok := s.resolveBoardProxy(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxProxyBody+1))
	if err != nil || len(raw) > maxProxyBody {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read request body")
		return
	}
	var req struct {
		RelativePath    string `json:"relativePath"`
		Content         string `json:"content"`
		BaseContentHash string `json:"baseContentHash"`
	}
	rp := ""
	if json.Unmarshal(raw, &req) == nil {
		rp = strings.TrimSpace(req.RelativePath)
	}
	if rp == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "relativePath is required")
		return
	}
	normalized := strings.ReplaceAll(rp, "\\", "/")
	cleaned := path.Clean(normalized)
	ordinaryCard := strings.HasPrefix(cleaned, "cards/")
	managedAutomationCard := strings.HasPrefix(cleaned, "jcode-automation/")
	if cleaned != normalized || strings.HasPrefix(cleaned, "/") ||
		(!ordinaryCard && !managedAutomationCard) ||
		!strings.HasSuffix(strings.ToLower(cleaned), ".md") {
		writeError(w, http.StatusForbidden, "forbidden",
			"the board embed may only write card documents under cards/ or existing Cloud Automation Cards")
		return
	}
	card := jtype.ParseCard(req.Content)
	if card.Board == "" || card.Status == "" || !s.projectAllowsBoardWrite(r, ws, card.Board) {
		writeError(w, http.StatusForbidden, "forbidden",
			"the document is not a card on an enabled Service board")
		return
	}
	existingBoard, existingHash, exists, err := existingCardTarget(r.Context(), client, ws, cleaned)
	if err != nil {
		s.log.Warn("board proxy: validate existing card", "workspace", ws, "path", cleaned, "err", err)
		writeError(w, http.StatusBadGateway, "jtype_invalid_response", "could not validate the existing JType card")
		return
	}
	if managedAutomationCard && !exists {
		writeError(w, http.StatusForbidden, "forbidden",
			"Cloud Automation Cards must already exist before the board can update them")
		return
	}
	if exists && existingBoard != card.Board {
		writeError(w, http.StatusForbidden, "forbidden",
			"the existing card belongs to a different board")
		return
	}
	if exists && (req.BaseContentHash == "" || req.BaseContentHash != existingHash) {
		writeError(w, http.StatusConflict, "card_changed",
			"the card changed since it was opened; reload before saving")
		return
	}
	path := "/api/v1/workspaces/" + url.PathEscape(ws) + "/documents/save"
	s.forwardBoardAPI(w, r, client, credential, http.MethodPost, path, bytes.NewReader(raw))
}

func existingCardTarget(ctx context.Context, client jtypeBoardProxy, workspace, relativePath string) (board, contentHash string, exists bool, err error) {
	listPath := "/api/v1/workspaces/" + url.PathEscape(workspace) + "/documents"
	resp, err := client.ProxyDocumentAPI(ctx, http.MethodGet, listPath, nil)
	if err != nil {
		return "", "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return "", "", false, errors.New("jtype document list rejected")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxProxyBody+1))
	if err != nil || len(raw) > maxProxyBody {
		return "", "", false, errors.New("invalid jtype document list")
	}
	var docs []struct {
		ID           string `json:"id"`
		RelativePath string `json:"relativePath"`
	}
	if err := json.Unmarshal(raw, &docs); err != nil {
		return "", "", false, err
	}
	for _, doc := range docs {
		if jtypePathKey(doc.RelativePath) != jtypePathKey(relativePath) {
			continue
		}
		getPath := listPath + "/" + url.PathEscape(doc.ID)
		current, err := client.ProxyDocumentAPI(ctx, http.MethodGet, getPath, nil)
		if err != nil {
			return "", "", false, err
		}
		defer current.Body.Close()
		if current.StatusCode >= http.StatusBadRequest {
			return "", "", false, errors.New("jtype document read rejected")
		}
		body, err := io.ReadAll(io.LimitReader(current.Body, maxProxyBody+1))
		if err != nil || len(body) > maxProxyBody {
			return "", "", false, errors.New("invalid jtype document")
		}
		var existing struct {
			Content     string `json:"content"`
			ContentHash string `json:"contentHash"`
		}
		if err := json.Unmarshal(body, &existing); err != nil {
			return "", "", false, err
		}
		board := jtype.ParseCard(existing.Content).Board
		if board == "" || existing.ContentHash == "" {
			return "", "", true, errors.New("existing document is not a valid card")
		}
		return board, existing.ContentHash, true, nil
	}
	return "", "", false, nil
}

// JType stores relative paths under MySQL's utf8mb4_0900_ai_ci collation.
// Mirror its case- and accent-insensitive identity when deciding whether a save
// targets an existing card; byte equality would miss `Other.md`/`other.md`.
func jtypePathKey(value string) string {
	decomposed := norm.NFD.String(strings.ReplaceAll(value, "\\", "/"))
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return unicode.ToLower(r)
	}, decomposed)
}

func (s *Server) projectAllowsBoardWrite(r *http.Request, workspace, boardRef string) bool {
	automations, err := s.st.ListPluginAutomationsByProject(r.Context(), r.PathValue("id"))
	if err != nil {
		return false
	}
	for _, a := range automations {
		if a.TriggerKind != "kanban" || !a.Enabled {
			continue
		}
		spec, err := s.st.GetPluginAutomationSpec(r.Context(), a.ID)
		if err != nil || spec.Kanban == nil || spec.Kanban.BoardRef != boardRef {
			continue
		}
		in, err := s.st.GetPluginInstallation(r.Context(), spec.Kanban.InstallationID)
		if err == nil && in.Provider == domain.PluginJType &&
			in.Status == domain.PluginStatusEnabled && in.LastHealthError == "" &&
			in.WorkspaceID == workspace {
			return true
		}
	}
	return false
}
