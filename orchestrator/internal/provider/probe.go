package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
	"github.com/cnjack/jcloud/internal/safehttp"
)

// ConfigProbeInput contains decrypted credentials only for the duration of a
// cluster-admin provider probe. Callers must never persist or serialize it.
type ConfigProbeInput struct {
	Provider      domain.ProviderKind
	BaseURL       string
	AppID         string
	AppPrivateKey string
}

// ConfigProbeResult is safe to persist in provider_configs. Version is the
// provider-reported release where one is exposed; GitHub is a hosted API and
// therefore reports its pinned API revision instead of a server release.
type ConfigProbeResult struct {
	Version string
}

// ProbeConfiguration verifies the configured origin plus the strongest
// cluster-owned credential check each provider exposes without impersonating a
// human or creating a persistent provider resource. OAuth client credentials
// are deliberately NOT "validated" by submitting a fabricated authorization
// code: OAuth does not guarantee that invalid_grant proves the client valid.
// Project grants are verified by their own connect/resource-discovery flows.
func ProbeConfiguration(ctx context.Context, in ConfigProbeInput) (ConfigProbeResult, error) {
	base := strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if base == "" {
		return ConfigProbeResult{}, fmt.Errorf("provider base URL is required")
	}
	switch in.Provider {
	case domain.PluginGitHub:
		return probeGitHubApp(ctx, in)
	case domain.PluginGitLab:
		version, err := probeGitLabVersion(ctx, base)
		if err != nil {
			return ConfigProbeResult{}, err
		}
		return ConfigProbeResult{Version: version}, nil
	case domain.PluginGitea:
		version, err := probeJSONVersion(ctx, base+"/api/v1/version", base, "version")
		if err != nil {
			return ConfigProbeResult{}, err
		}
		return ConfigProbeResult{Version: version}, nil
	case domain.PluginJType:
		// JType publishes neither a public version endpoint nor a cluster-owned
		// credential. Health is the only safe cluster-level check. A Project grant
		// later proves its own access through workspace discovery/MCP initialize.
		if err := probeJTypeHealth(ctx, base); err != nil {
			return ConfigProbeResult{}, err
		}
		return ConfigProbeResult{}, nil
	default:
		return ConfigProbeResult{}, fmt.Errorf("unsupported provider %q", in.Provider)
	}
}

// ProbeAuthenticatedVersion performs capability discovery with a short-lived
// Project OAuth grant. It closes the deliberate cluster-probe gap for GitLab
// installations that protect /version with authentication: a 401 from the
// anonymous cluster check is partial, but a successful Project connection can
// now discover the real version and enable only supported SCM actions.
//
// The supplied access token is used only in this request and is never retained
// by this package or returned to a caller.
func ProbeAuthenticatedVersion(ctx context.Context, kind domain.ProviderKind, baseURL, accessToken, scheme string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	accessToken = strings.TrimSpace(accessToken)
	if base == "" || accessToken == "" {
		return "", fmt.Errorf("provider base URL and access token are required")
	}
	endpoint := ""
	switch kind {
	case domain.PluginGitLab:
		endpoint = base + "/api/v4/version"
	case domain.PluginGitea:
		endpoint = base + "/api/v1/version"
	default:
		return "", fmt.Errorf("authenticated version discovery is unsupported for %q", kind)
	}
	if strings.TrimSpace(scheme) == "" {
		scheme = "Bearer"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", strings.TrimSpace(scheme)+" "+accessToken)
	resp, err := safehttp.NewProviderClient(base, 10*time.Second).Do(req)
	if err != nil {
		return "", fmt.Errorf("authenticated provider version request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("authenticated provider version request returned HTTP %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return "", fmt.Errorf("decode authenticated provider version response: %w", err)
	}
	version, _ := body["version"].(string)
	version = strings.TrimSpace(version)
	if version == "" {
		return "", fmt.Errorf("provider version response did not include %q", "version")
	}
	return version, nil
}

// probeGitLabVersion accepts a 401 as an operationally partial result. Some
// GitLab installations protect /version; without a Project OAuth grant Cloud
// must not invent an access token or mark a valid cluster setup as broken.
func probeGitLabVersion(ctx context.Context, baseURL string) (string, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/api/v4/version"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := safehttp.NewProviderClient(baseURL, 10*time.Second).Do(req)
	if err != nil {
		return "", fmt.Errorf("provider version request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return "", nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("provider version request returned HTTP %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return "", fmt.Errorf("decode provider version response: %w", err)
	}
	version, _ := body["version"].(string)
	version = strings.TrimSpace(version)
	if version == "" {
		return "", fmt.Errorf("provider version response did not include %q", "version")
	}
	return version, nil
}

func probeGitHubApp(ctx context.Context, in ConfigProbeInput) (ConfigProbeResult, error) {
	issuer, err := NewGitHubAppIssuer(in.AppID, []byte(in.AppPrivateKey))
	if err != nil {
		return ConfigProbeResult{}, err
	}
	if err := issuer.Verify(ctx); err != nil {
		return ConfigProbeResult{}, err
	}
	return ConfigProbeResult{Version: "2022-11-28"}, nil
}

func probeJSONVersion(ctx context.Context, endpoint, baseURL, field string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	resp, err := safehttp.NewProviderClient(baseURL, 10*time.Second).Do(req)
	if err != nil {
		return "", fmt.Errorf("provider version request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("provider version request returned HTTP %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return "", fmt.Errorf("decode provider version response: %w", err)
	}
	version, _ := body[field].(string)
	version = strings.TrimSpace(version)
	if version == "" {
		return "", fmt.Errorf("provider version response did not include %q", field)
	}
	return version, nil
}

func probeJTypeHealth(ctx context.Context, baseURL string) error {
	endpoint := strings.TrimRight(baseURL, "/") + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := safehttp.NewProviderClient(baseURL, 10*time.Second).Do(req)
	if err != nil {
		return fmt.Errorf("jtype health request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("jtype health request returned HTTP %d", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return nil
}
