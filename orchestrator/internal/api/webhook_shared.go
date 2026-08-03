package api

// Shared webhook intake helpers used by the Plugin platform receiver
// (plugin_webhook.go). The legacy per-provider receivers were removed with the
// old webhook/Automation API.

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net/http"
	"strings"

	"github.com/cnjack/jcloud/internal/domain"
)

// maxWebhookBytes caps a webhook body (blueprint §8: read once, ≤1MiB).
const maxWebhookBytes = 1 << 20

// originCommentKey builds the run's origin_comment_id (de-dup key). Gitea keeps
// the BARE numeric id for backward compatibility with M7 runs already stored that
// way; github/gitlab are PREFIXED with the provider so numerically-equal comment
// ids from different hosts (e.g. github note 42 vs gitlab note 42) never de-dup
// against each other, or against a gitea id.
func originCommentKey(prov domain.GitProvider, rawID string) string {
	if prov == domain.ProviderGitea {
		return rawID
	}
	return string(prov) + ":" + rawID
}

// validGiteaSignature verifies X-Gitea-Signature = hex(HMAC-SHA256(body, secret))
// in constant time. An empty secret or signature, or a non-hex signature, fails.
func validGiteaSignature(secret string, body []byte, sigHex string) bool {
	return validHexHMAC(secret, body, strings.TrimSpace(sigHex))
}

// validGitHubSignature verifies X-Hub-Signature-256 = "sha256=" +
// hex(HMAC-SHA256(body, secret)) in constant time (F13). An empty secret/header,
// a missing "sha256=" prefix, or a non-hex digest fails.
func validGitHubSignature(secret string, body []byte, header string) bool {
	const prefix = "sha256="
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	return validHexHMAC(secret, body, header[len(prefix):])
}

// validHexHMAC is the shared core of the gitea/github signature checks: it
// compares sigHex against hex(HMAC-SHA256(body, secret)) in constant time.
func validHexHMAC(secret string, body []byte, sigHex string) bool {
	if secret == "" || sigHex == "" {
		return false
	}
	want, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(want, mac.Sum(nil))
}

// validGitLabToken compares the X-Gitlab-Token header to the shared secret in
// constant time (GitLab does not sign the body — it echoes the token verbatim;
// F13). An empty secret or header fails.
func validGitLabToken(secret, token string) bool {
	if secret == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(secret), []byte(token)) == 1
}

// readWebhookBody reads (and 1MiB-caps) a webhook body. It writes the error
// response itself and returns ok=false on a read error / oversize body.
func readWebhookBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read webhook body")
		return nil, false
	}
	if int64(len(body)) > maxWebhookBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "webhook body exceeds the 1MiB limit")
		return nil, false
	}
	return body, true
}

// writeWebhookOK returns a 200 with a short machine-readable status so the
// provider's delivery log shows why an event was accepted / ignored.
func writeWebhookOK(w http.ResponseWriter, status string) {
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}
