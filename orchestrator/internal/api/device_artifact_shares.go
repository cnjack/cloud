package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/store"
)

const (
	artifactShareMaxPlaintext     = int64(25 << 20)
	artifactShareEnvelopeOverhead = int64(28)
	artifactShareMaxCiphertext    = artifactShareMaxPlaintext + artifactShareEnvelopeOverhead
	artifactShareMaxMetadataJSON  = int64(64 << 10)
	artifactShareIntentTTL        = time.Hour
	artifactShareUploadLease      = 5 * time.Minute
	artifactShareUploadURLTTL     = 10 * time.Minute
	artifactShareMaxActiveCount   = 100
	artifactShareMaxActiveBytes   = int64(1 << 30)
	artifactShareMinExpiry        = time.Hour
	artifactShareMaxExpiry        = 30 * 24 * time.Hour
	artifactShareCleanupTimeout   = 30 * time.Second
)

type artifactShareIntentRequest struct {
	Protocol         string `json:"protocol"`
	ArtifactID       string `json:"artifact_id"`
	Revision         int    `json:"revision"`
	CiphertextSize   int64  `json:"ciphertext_size"`
	ExpiresInSeconds int64  `json:"expires_in_seconds"`
}

type artifactShareCompleteRequest struct {
	CiphertextSHA256  string                           `json:"ciphertext_sha256"`
	EncryptedMetadata domain.EncryptedArtifactMetadata `json:"encrypted_metadata"`
}

func (s *Server) handleCreateArtifactShareIntent(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	if s.attachmentStore == nil {
		writeError(w, http.StatusConflict, "artifact_share_unavailable", "object storage is not configured")
		return
	}
	var req artifactShareIntentRequest
	if err := decodeBoundedJSON(w, r, &req, 4096); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid artifact share intent")
		return
	}
	if req.Protocol != domain.ArtifactShareProtocolV1 || !validOpaqueArtifactID(req.ArtifactID) || req.Revision <= 0 ||
		req.CiphertextSize < artifactShareEnvelopeOverhead || req.CiphertextSize > artifactShareMaxCiphertext {
		writeError(w, http.StatusBadRequest, "bad_request", "artifact share intent is invalid")
		return
	}
	expiresIn := time.Duration(req.ExpiresInSeconds) * time.Second
	if expiresIn < artifactShareMinExpiry || expiresIn > artifactShareMaxExpiry {
		writeError(w, http.StatusBadRequest, "bad_request", "artifact share expiry is invalid")
		return
	}
	now := time.Now().UTC()
	share := &domain.ArtifactShare{
		ID: domain.NewID(), UserID: p.deviceUserID, DeviceID: p.deviceID,
		ArtifactID: req.ArtifactID, Revision: req.Revision, Protocol: req.Protocol,
		State: domain.ArtifactSharePending, CiphertextSize: req.CiphertextSize,
		IntentExpiresAt: now.Add(artifactShareIntentTTL), ExpiresAt: now.Add(expiresIn), CreatedAt: now,
	}
	if err := s.st.CreateArtifactShare(r.Context(), share, artifactShareMaxActiveCount, artifactShareMaxActiveBytes); err != nil {
		if errors.Is(err, store.ErrArtifactShareQuotaExceeded) {
			writeError(w, http.StatusTooManyRequests, "artifact_share_quota_exceeded", "artifact share quota exceeded")
			return
		}
		s.log.Error("create artifact share intent", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create artifact share")
		return
	}
	baseURL := strings.TrimRight(s.cfg.ConsoleURL, "/") + "/s/" + share.ID
	writeJSON(w, http.StatusCreated, map[string]any{
		"share_id":     share.ID,
		"upload_url":   "/internal/v1/device/artifact-shares/" + share.ID + "/content",
		"complete_url": "/internal/v1/device/artifact-shares/" + share.ID + "/complete",
		"base_url":     baseURL,
		"expires_at":   share.ExpiresAt,
	})
}

func validOpaqueArtifactID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		char := id[i]
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_') {
			return false
		}
	}
	return true
}

func decodeBoundedJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func (s *Server) handleUploadArtifactShareContent(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	if s.attachmentStore == nil {
		writeError(w, http.StatusConflict, "artifact_share_unavailable", "object storage is not configured")
		return
	}
	now := time.Now().UTC()
	claimID := domain.NewID()
	share, err := s.st.ClaimArtifactShareUpload(r.Context(), r.PathValue("shareID"), p.deviceUserID, claimID, now, now.Add(artifactShareUploadLease))
	if err != nil {
		writeError(w, http.StatusConflict, "artifact_share_conflict", "artifact share upload is unavailable")
		return
	}
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), artifactShareCleanupTimeout)
		defer cancel()
		if s.st.ReleaseArtifactShareUpload(cleanupCtx, share.ID, p.deviceUserID, claimID, share.UploadGeneration) {
			_ = s.attachmentStore.Delete(cleanupCtx, share.ObjectKey)
		}
	}
	if r.ContentLength != share.CiphertextSize {
		cleanup()
		writeError(w, http.StatusBadRequest, "artifact_share_size_mismatch", "content length must equal ciphertext_size")
		return
	}
	putURL, err := s.attachmentStore.PresignPut(share.ObjectKey, artifactShareUploadURLTTL)
	if err != nil {
		cleanup()
		writeError(w, http.StatusBadGateway, "object_store_error", "could not prepare artifact upload")
		return
	}
	body := http.MaxBytesReader(w, r.Body, share.CiphertextSize+1)
	defer func() { _ = body.Close() }()
	hash := sha256.New()
	forward, err := http.NewRequestWithContext(r.Context(), http.MethodPut, putURL, io.TeeReader(io.LimitReader(body, share.CiphertextSize), hash))
	if err != nil {
		cleanup()
		writeError(w, http.StatusBadGateway, "object_store_error", "could not prepare artifact upload")
		return
	}
	forward.ContentLength = share.CiphertextSize
	forward.Header.Set("Content-Type", "application/octet-stream")
	resp, err := (&http.Client{Timeout: artifactShareUploadLease}).Do(forward)
	if err != nil || resp.StatusCode/100 != 2 {
		if resp != nil {
			_ = resp.Body.Close()
		}
		cleanup()
		writeError(w, http.StatusBadGateway, "object_store_error", "artifact upload failed")
		return
	}
	_ = resp.Body.Close()
	var extra [1]byte
	if n, _ := body.Read(extra[:]); n != 0 {
		cleanup()
		writeError(w, http.StatusBadRequest, "artifact_share_size_mismatch", "artifact body exceeds ciphertext_size")
		return
	}
	storedSize, _, err := s.attachmentStore.Stat(r.Context(), share.ObjectKey)
	if err != nil || storedSize != share.CiphertextSize {
		cleanup()
		writeError(w, http.StatusBadGateway, "object_store_error", "artifact upload could not be verified")
		return
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if err := s.st.MarkArtifactShareUploaded(r.Context(), share.ID, p.deviceUserID, claimID, share.UploadGeneration, digest, time.Now().UTC()); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), artifactShareCleanupTimeout)
		defer cancel()
		_ = s.attachmentStore.Delete(cleanupCtx, share.ObjectKey)
		writeError(w, http.StatusConflict, "artifact_share_conflict", "artifact upload lease was superseded")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCompleteArtifactShare(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	var req artifactShareCompleteRequest
	if err := decodeBoundedJSON(w, r, &req, artifactShareMaxMetadataJSON+4096); err != nil || !validArtifactShareComplete(req) {
		writeError(w, http.StatusBadRequest, "bad_request", "artifact share completion is invalid")
		return
	}
	share, err := s.st.CompleteArtifactShare(r.Context(), r.PathValue("shareID"), p.deviceUserID, strings.ToLower(req.CiphertextSHA256), req.EncryptedMetadata, time.Now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "artifact share not found")
			return
		}
		writeError(w, http.StatusConflict, "artifact_share_conflict", "artifact share cannot be completed")
		return
	}
	writeJSON(w, http.StatusOK, ownerArtifactShareView(share))
}

func validArtifactShareComplete(req artifactShareCompleteRequest) bool {
	digest, err := hex.DecodeString(req.CiphertextSHA256)
	if err != nil || len(digest) != sha256.Size || req.EncryptedMetadata.PlaintextLength < 0 {
		return false
	}
	nonce, err := base64.RawURLEncoding.DecodeString(req.EncryptedMetadata.Nonce)
	if err != nil || len(nonce) != 12 {
		return false
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(req.EncryptedMetadata.Ciphertext)
	return err == nil && int64(len(ciphertext)) == req.EncryptedMetadata.PlaintextLength+16
}

func ownerArtifactShareView(share *domain.ArtifactShare) map[string]any {
	return map[string]any{
		"share_id": share.ID, "artifact_id": share.ArtifactID, "revision": share.Revision,
		"protocol": share.Protocol, "state": share.State, "ciphertext_size": share.CiphertextSize,
		"ciphertext_sha256": share.CiphertextSHA256, "expires_at": share.ExpiresAt,
		"completed_at": share.CompletedAt, "revoked_at": share.RevokedAt, "created_at": share.CreatedAt,
	}
}

func (s *Server) handleListArtifactShares(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	shares, err := s.st.ListArtifactSharesForUser(r.Context(), p.deviceUserID, r.URL.Query().Get("artifact_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not list artifact shares")
		return
	}
	views := make([]map[string]any, 0, len(shares))
	for i := range shares {
		views = append(views, ownerArtifactShareView(&shares[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": views})
}

func (s *Server) handleRevokeArtifactShare(w http.ResponseWriter, r *http.Request) {
	p := s.requireDevice(w, r)
	if p == nil {
		return
	}
	if _, err := s.st.RevokeArtifactShare(r.Context(), r.PathValue("shareID"), p.deviceUserID, time.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "artifact share not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not revoke artifact share")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func setPublicArtifactHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func (s *Server) handleGetSharedArtifact(w http.ResponseWriter, r *http.Request) {
	setPublicArtifactHeaders(w)
	share, err := s.st.GetPublicArtifactShare(r.Context(), r.PathValue("shareID"), time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "shared artifact not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"share_id": share.ID, "protocol": share.Protocol, "artifact_id": share.ArtifactID,
		"revision": share.Revision, "encrypted_metadata": share.EncryptedMetadata,
		"ciphertext_size": share.CiphertextSize, "ciphertext_sha256": share.CiphertextSHA256,
		"expires_at": share.ExpiresAt,
	})
}

func (s *Server) handleGetSharedArtifactContent(w http.ResponseWriter, r *http.Request) {
	setPublicArtifactHeaders(w)
	share, err := s.st.GetPublicArtifactShare(r.Context(), r.PathValue("shareID"), time.Now().UTC())
	if err != nil || s.attachmentStore == nil {
		writeError(w, http.StatusNotFound, "not_found", "shared artifact not found")
		return
	}
	getURL, err := s.attachmentStore.PresignGet(share.ObjectKey, artifactShareUploadURLTTL)
	if err != nil {
		writeError(w, http.StatusBadGateway, "object_store_error", "shared artifact content is unavailable")
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, getURL, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "object_store_error", "shared artifact content is unavailable")
		return
	}
	resp, err := (&http.Client{Timeout: artifactShareUploadLease}).Do(request)
	if err != nil || resp.StatusCode/100 != 2 {
		if resp != nil {
			_ = resp.Body.Close()
		}
		writeError(w, http.StatusBadGateway, "object_store_error", "shared artifact content is unavailable")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmtInt64(share.CiphertextSize))
	w.WriteHeader(http.StatusOK)
	_, _ = io.CopyN(w, resp.Body, share.CiphertextSize)
}

func fmtInt64(v int64) string {
	return strconv.FormatInt(v, 10)
}
