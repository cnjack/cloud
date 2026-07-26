package api

import (
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

const (
	attachmentMaxFiles    = 10
	attachmentMaxBytes    = int64(25 << 20)
	attachmentMaxRunBytes = int64(100 << 20)
	attachmentNameMax     = 180
	attachmentStageTTL    = 10 * time.Minute
)

type attachmentIntentReq struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

// handleCreateAttachmentIntent creates an opaque-key, short-lived Cloud-proxied
// upload intent. The browser-visible filename is display-only; it cannot
// influence object addressing or the runner's filesystem path.
func (s *Server) handleCreateAttachmentIntent(w http.ResponseWriter, r *http.Request) {
	svc, err := s.st.GetService(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "service not found")
		return
	}
	p := principalFrom(r.Context())
	if !s.authorizeProject(r.Context(), w, p, svc.ProjectID, domain.RoleMember) {
		return
	}
	if p == nil || p.user == nil {
		writeError(w, http.StatusForbidden, "attachment_user_required", "attachments require a signed-in user")
		return
	}
	if s.attachmentStore == nil {
		writeError(w, http.StatusConflict, "attachments_unavailable", "object storage is not configured for attachments")
		return
	}
	var req attachmentIntentReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, 400, "bad_request", "invalid JSON: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.ContentType = strings.TrimSpace(req.ContentType)
	if req.Name == "" || len(req.Name) > attachmentNameMax || filepath.Base(req.Name) != req.Name || strings.ContainsAny(req.Name, "\\/\x00\r\n") {
		writeError(w, 400, "bad_request", "attachment name is invalid")
		return
	}
	if req.SizeBytes <= 0 || req.SizeBytes > attachmentMaxBytes {
		writeError(w, 400, "bad_request", "attachment size must be between 1 byte and 25 MiB")
		return
	}
	if len(req.ContentType) > 120 || !allowedAttachmentContentType(req.ContentType) {
		writeError(w, 400, "bad_request", "attachment content type is invalid")
		return
	}
	now := time.Now().UTC()
	stage := &domain.AttachmentStage{ID: domain.NewID(), ProjectID: svc.ProjectID, CreatedBy: p.user.ID, ObjectKey: "run-inputs/" + svc.ProjectID + "/" + domain.NewID(), DisplayName: req.Name, ContentType: req.ContentType, SizeBytes: req.SizeBytes, CreatedAt: now, ExpiresAt: now.Add(attachmentStageTTL)}
	if err := s.st.CreateAttachmentStage(r.Context(), stage); err != nil {
		if errors.Is(err, store.ErrAttachmentQuotaExceeded) {
			writeError(w, http.StatusTooManyRequests, "attachment_quota_exceeded", "too many unconsumed attachment uploads; wait for expiry or create a run")
			return
		}
		s.log.Error("create attachment stage", "err", err)
		writeError(w, 500, "internal", "could not create attachment upload")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"stage": stage, "upload_url": "/api/v1/services/" + svc.ID + "/attachments/" + stage.ID + "/content", "expires_at": stage.ExpiresAt})
}

func allowedAttachmentContentType(v string) bool {
	if v == "" || strings.HasPrefix(v, "text/") || strings.HasPrefix(v, "image/") {
		return true
	}
	switch v {
	case "application/json", "application/pdf", "application/zip", "application/gzip", "application/x-tar", "application/octet-stream":
		return true
	}
	return false
}

// handleUploadAttachmentContent proxies a bounded browser upload. We do not
// expose a direct presigned PUT because that cannot portably constrain object
// size before storage is consumed.
func (s *Server) handleUploadAttachmentContent(w http.ResponseWriter, r *http.Request) {
	svc, err := s.st.GetService(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "not_found", "service not found")
		return
	}
	p := principalFrom(r.Context())
	if !s.authorizeProject(r.Context(), w, p, svc.ProjectID, domain.RoleMember) {
		return
	}
	if p == nil || p.user == nil || s.attachmentStore == nil {
		writeError(w, 409, "attachments_unavailable", "attachment upload is unavailable")
		return
	}
	now := time.Now().UTC()
	stage, err := s.st.ClaimAttachmentStageUpload(r.Context(), r.PathValue("stage"), svc.ProjectID, p.user.ID, now)
	if err != nil {
		writeError(w, 409, "attachment_unavailable", "attachment upload is unavailable or expired")
		return
	}
	if r.ContentLength != stage.SizeBytes {
		s.st.ReleaseAttachmentStageUpload(r.Context(), stage.ID)
		writeError(w, 400, "attachment_size_mismatch", "attachment content length must equal the upload intent")
		return
	}
	putURL, err := s.attachmentStore.PresignPut(stage.ObjectKey, attachmentStageTTL)
	if err != nil {
		s.st.ReleaseAttachmentStageUpload(r.Context(), stage.ID)
		writeError(w, 502, "object_store_error", "could not prepare attachment upload")
		return
	}
	body := http.MaxBytesReader(w, r.Body, stage.SizeBytes+1)
	defer body.Close()
	forward, err := http.NewRequestWithContext(r.Context(), http.MethodPut, putURL, io.LimitReader(body, stage.SizeBytes+1))
	if err != nil {
		s.st.ReleaseAttachmentStageUpload(r.Context(), stage.ID)
		writeError(w, 502, "object_store_error", "could not prepare attachment upload")
		return
	}
	forward.ContentLength = stage.SizeBytes
	if stage.ContentType != "" {
		forward.Header.Set("Content-Type", stage.ContentType)
	}
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(forward)
	if err != nil || resp.StatusCode/100 != 2 {
		if resp != nil {
			resp.Body.Close()
		}
		if s.st.ReleaseAttachmentStageUpload(r.Context(), stage.ID) {
			_ = s.attachmentStore.Delete(r.Context(), stage.ObjectKey)
		}
		writeError(w, 502, "object_store_error", "attachment upload failed")
		return
	}
	resp.Body.Close()
	if err := s.st.MarkAttachmentStageUploaded(r.Context(), stage.ID, stage.SizeBytes, now); err != nil {
		if s.st.ReleaseAttachmentStageUpload(r.Context(), stage.ID) {
			_ = s.attachmentStore.Delete(r.Context(), stage.ObjectKey)
		}
		writeError(w, 409, "attachment_unavailable", "attachment upload expired before finalization")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// finalizeRunAttachmentStages accepts only stages the bounded Cloud upload has
// already finalized; it never trusts a browser-side completion signal.
func (s *Server) finalizeRunAttachmentStages(r *http.Request, svc *domain.Service, ids []string) error {
	if len(ids) > attachmentMaxFiles {
		return &attachmentHTTPError{http.StatusBadRequest, "bad_request", "at most 10 attachments are allowed"}
	}
	p := principalFrom(r.Context())
	if len(ids) > 0 && (p == nil || p.user == nil) {
		return &attachmentHTTPError{http.StatusForbidden, "attachment_user_required", "attachments require a signed-in user"}
	}
	if len(ids) > 0 && s.attachmentStore == nil {
		return &attachmentHTTPError{http.StatusConflict, "attachments_unavailable", "object storage is not configured for attachments"}
	}
	seen := make(map[string]struct{}, len(ids))
	var totalBytes int64
	now := time.Now().UTC()
	for _, id := range ids {
		if id == "" {
			return &attachmentHTTPError{http.StatusBadRequest, "bad_request", "attachment stage id is required"}
		}
		if _, ok := seen[id]; ok {
			return &attachmentHTTPError{http.StatusBadRequest, "bad_request", "attachment stage ids must be unique"}
		}
		seen[id] = struct{}{}
		stage, err := s.st.GetAttachmentStage(r.Context(), id)
		if err != nil || stage.ProjectID != svc.ProjectID || stage.CreatedBy != p.user.ID || !stage.ExpiresAt.After(now) {
			return &attachmentHTTPError{http.StatusConflict, "attachment_unavailable", "attachment upload is unavailable or expired"}
		}
		if stage.UploadedAt == nil {
			return &attachmentHTTPError{http.StatusConflict, "attachment_not_uploaded", "attachment upload has not completed"}
		}
		totalBytes += stage.SizeBytes
		if totalBytes > attachmentMaxRunBytes {
			return &attachmentHTTPError{http.StatusBadRequest, "attachment_total_too_large", "attachments total must not exceed 100 MiB"}
		}
	}
	return nil
}

type attachmentHTTPError struct {
	status        int
	code, message string
}

func (e *attachmentHTTPError) Error() string { return e.message }
