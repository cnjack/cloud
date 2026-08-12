package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// GitHub's repository endpoints return full Repository objects even when a
// caller needs only a handful of fields. Fifty ordinary repositories can
// exceed the former 64 KiB cap, so keep a larger but still bounded allowance.
const maxProviderJSONResponseBytes = 4 << 20

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
	_, err := doJSONResponse(ctx, hc, method, url, authHeader, accept, body, out)
	return err
}

// doJSONResponse is doJSON with a cloned response header for provider APIs
// whose pagination metadata is available only through Link/X-Total-Count.
func doJSONResponse(ctx context.Context, hc *http.Client, method, requestURL, authHeader, accept string, body, out any) (http.Header, error) {
	return doJSONResponseWithLimit(ctx, hc, method, requestURL, authHeader, accept, body, out, maxProviderJSONResponseBytes)
}

// doJSONResponseWithLimit keeps exceptional provider payload classes bounded
// without raising the shared allowance for every REST call.
func doJSONResponseWithLimit(ctx context.Context, hc *http.Client, method, requestURL, authHeader, accept string, body, out any, maxResponseBytes int64) (http.Header, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal %s %s: %w", method, requestURL, err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, rdr)
	if err != nil {
		return nil, fmt.Errorf("build %s %s: %w", method, requestURL, err)
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
		return nil, fmt.Errorf("%s %s: %w", method, requestURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPStatusError{Method: method, StatusCode: resp.StatusCode}
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s %s: %w", method, requestURL, err)
	}
	if int64(len(respBody)) > maxResponseBytes {
		return nil, fmt.Errorf("read %s %s: provider response exceeds %d bytes", method, requestURL, maxResponseBytes)
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return nil, fmt.Errorf("decode %s %s: %w", method, requestURL, err)
		}
	}
	return resp.Header.Clone(), nil
}

// providerLastPage resolves the last available page without trusting a
// provider-supplied URL as a request target. Link is preferred; Gitea's
// X-Total-Count is a fallback for versions that omit Link.
func providerLastPage(header http.Header, perPage int) (int, error) {
	if link := header.Get("Link"); link != "" {
		for _, item := range strings.Split(link, ",") {
			parts := strings.Split(item, ";")
			isLast := false
			for _, param := range parts[1:] {
				rel := strings.TrimSpace(param)
				if rel == `rel="last"` || rel == "rel=last" {
					isLast = true
					break
				}
			}
			if !isLast {
				continue
			}
			target := strings.TrimSpace(parts[0])
			if len(target) < 3 || target[0] != '<' || target[len(target)-1] != '>' {
				return 0, errors.New("invalid provider pagination link")
			}
			u, err := url.Parse(target[1 : len(target)-1])
			if err != nil {
				return 0, errors.New("invalid provider pagination link")
			}
			page, err := strconv.Atoi(u.Query().Get("page"))
			if err != nil || page < 1 {
				return 0, errors.New("invalid provider pagination page")
			}
			return page, nil
		}
	}

	totalValue := header.Get("X-Total-Count")
	if totalValue == "" {
		return 1, nil
	}
	total, err := strconv.Atoi(totalValue)
	if err != nil || total < 0 || perPage < 1 {
		return 0, errors.New("invalid provider pagination total")
	}
	if total == 0 {
		return 1, nil
	}
	return (total + perPage - 1) / perPage, nil
}
