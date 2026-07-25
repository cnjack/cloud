package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// HTTPStatusError is the only upstream detail surfaced from a failed Provider
// REST request. In particular, it intentionally excludes the response body:
// an upstream proxy or compromised provider can reflect Authorization headers,
// and callers must never serialize or log those opaque fragments.
type HTTPStatusError struct {
	Method     string
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("provider %s request returned HTTP %d", e.Method, e.StatusCode)
}

// IsProviderHTTPStatusError lets API boundaries preserve the stable status
// detail while treating every other upstream error as an opaque failure.
func IsProviderHTTPStatusError(err error) (int, bool) {
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		return 0, false
	}
	return statusErr.StatusCode, true
}

// doJSON performs one authenticated JSON request against url and decodes a 2xx
// body into out (out may be nil to discard). authHeader is the full
// Authorization header value (e.g. "Bearer <tok>"). It is the shared request
// primitive behind the github/gitlab clients so each host client is just a thin
// set of path builders — matching the orchestrator's std-lib-first posture.
func doJSON(ctx context.Context, hc *http.Client, method, url, authHeader, accept string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal %s %s: %w", method, url, err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return fmt.Errorf("build %s %s: %w", method, url, err)
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPStatusError{Method: method, StatusCode: resp.StatusCode}
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, url, err)
		}
	}
	return nil
}
