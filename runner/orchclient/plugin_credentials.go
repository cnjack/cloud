package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// pluginCredential is the runner-side view of the internal endpoint. Keep this
// intentionally small and independent from orchestrator packages: orchclient is
// a static stdlib-only binary built into the runner image.
type pluginCredential struct {
	Provider    string `json:"provider"`
	BaseURL     string `json:"base_url"`
	AccessToken string `json:"access_token"`
	Scheme      string `json:"scheme"`
}

type pluginCredentialResponse struct {
	Credentials []pluginCredential `json:"credentials"`
}

// syncPluginCredentials polls the run-scoped credential endpoint and writes
// tool configuration to a shared tmpfs. A first-sync error is fatal (so the
// pod fails visibly); after a successful sync it keeps retrying forever so a
// transient control-plane outage never invalidates a still-running job's last
// good config.
func (c *client) syncPluginCredentials(dir string, interval time.Duration, once bool, stopFile string) error {
	first := true
	for {
		if stopFile != "" {
			if _, err := os.Stat(stopFile); err == nil {
				return nil
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("check stop file: %w", err)
			}
		}
		err := c.syncPluginCredentialsOnce(dir)
		if err == nil {
			first = false
			if once {
				return nil
			}
		} else if first || once {
			return err
		} else {
			// Never include an HTTP response body or token in this log line.
			fmt.Fprintln(os.Stderr, "[orchclient] plugin credential refresh failed; keeping last good config:", err)
		}
		if stopFile == "" {
			time.Sleep(interval)
			continue
		}
		// Polling the lifecycle file lets the sync container leave promptly
		// after the runner exits instead of delaying Job completion for a full
		// credential refresh interval.
		timer := time.NewTimer(interval)
		ticker := time.NewTicker(time.Second)
		stopped := false
		for !stopped {
			select {
			case <-timer.C:
				stopped = true
			case <-ticker.C:
				if _, err := os.Stat(stopFile); err == nil {
					ticker.Stop()
					if !timer.Stop() {
						<-timer.C
					}
					return nil
				} else if !os.IsNotExist(err) {
					ticker.Stop()
					if !timer.Stop() {
						<-timer.C
					}
					return fmt.Errorf("check stop file: %w", err)
				}
			}
		}
		ticker.Stop()
	}
}

func (c *client) syncPluginCredentialsOnce(dir string) error {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(c.base, "/")+"/internal/v1/runs/"+url.PathEscape(c.runID)+"/plugins/credentials", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request credentials: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("credential endpoint returned HTTP %d", resp.StatusCode)
	}
	var body pluginCredentialResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("decode credentials: %w", err)
	}
	return writePluginConfigs(dir, body.Credentials)
}

// writePluginConfigs writes each file through temp+rename. The runner only sees
// the shared volume read-only, so it cannot race or modify these files. Files
// containing tokens use 0600 and child directories use 0700. The root is a
// Kubernetes-managed tmpfs mount whose ownership and group-write permissions
// come from fsGroup; a non-root init container must not chmod the mount point.
func writePluginConfigs(dir string, credentials []pluginCredential) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create credential dir: %w", err)
	}
	byProvider := map[string]pluginCredential{}
	for _, credential := range credentials {
		if credential.AccessToken == "" || credential.BaseURL == "" {
			return fmt.Errorf("credential for %q is incomplete", credential.Provider)
		}
		if _, duplicate := byProvider[credential.Provider]; duplicate {
			return fmt.Errorf("duplicate credential for provider %q", credential.Provider)
		}
		byProvider[credential.Provider] = credential
	}
	if cred, ok := byProvider["github"]; ok {
		host, err := pluginHost(cred.BaseURL)
		if err != nil {
			return err
		}
		if err := writeSecretFile(filepath.Join(dir, "gh", "hosts.yml"), []byte(host+":\n    user: jcloud\n    oauth_token: "+cred.AccessToken+"\n    git_protocol: https\n")); err != nil {
			return err
		}
	} else if err := os.RemoveAll(filepath.Join(dir, "gh")); err != nil {
		return fmt.Errorf("remove stale GitHub config: %w", err)
	}
	if cred, ok := byProvider["gitlab"]; ok {
		host, err := pluginHost(cred.BaseURL)
		if err != nil {
			return err
		}
		if err := writeSecretFile(filepath.Join(dir, "glab", "config.yml"), []byte("hosts:\n    "+host+":\n        token: "+cred.AccessToken+"\n        api_host: "+host+"\n")); err != nil {
			return err
		}
	} else if err := os.RemoveAll(filepath.Join(dir, "glab")); err != nil {
		return fmt.Errorf("remove stale GitLab config: %w", err)
	}
	if cred, ok := byProvider["gitea"]; ok {
		if _, err := pluginHost(cred.BaseURL); err != nil {
			return err
		}
		if err := writeSecretFile(filepath.Join(dir, "tea", "config.yml"), []byte("logins:\n    - name: jcloud\n      url: "+cred.BaseURL+"\n      token: "+cred.AccessToken+"\n      default: true\n")); err != nil {
			return err
		}
	} else if err := os.RemoveAll(filepath.Join(dir, "tea")); err != nil {
		return fmt.Errorf("remove stale Gitea config: %w", err)
	}
	mcpServers := map[string]any{}
	if cred, ok := byProvider["jtype"]; ok {
		mcpServers["jtype"] = map[string]any{
			"url":     strings.TrimRight(cred.BaseURL, "/") + "/mcp",
			"headers": map[string]string{"Authorization": "Bearer " + cred.AccessToken},
		}
	}
	// The file must always exist before Kubernetes starts the runner because it
	// is mounted with subPath over ~/.jcode/mcp.json. An empty server map keeps
	// non-JType runs valid without persisting any configuration outside tmpfs.
	payload, err := json.Marshal(map[string]any{"mcpServers": mcpServers})
	if err != nil {
		return fmt.Errorf("encode jtype mcp config: %w", err)
	}
	if err := writeSecretFile(filepath.Join(dir, "jtype", "mcp.json"), append(payload, '\n')); err != nil {
		return err
	}

	// Git uses its normal credential-store format. The config points only at the
	// tmpfs file; it never rewrites a task's repository-local .git/config.
	providers := make([]string, 0, len(byProvider))
	for provider := range byProvider {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	var gitCredentials bytes.Buffer
	for _, provider := range providers {
		cred := byProvider[provider]
		if provider == "jtype" {
			continue
		}
		u, err := url.Parse(cred.BaseURL)
		if err != nil || u.Host == "" {
			return fmt.Errorf("invalid base URL for %s", provider)
		}
		if provider == "gitea" {
			u.User = url.User(cred.AccessToken)
		} else {
			username := "oauth2"
			if provider == "github" {
				username = "x-access-token"
			}
			u.User = url.UserPassword(username, cred.AccessToken)
		}
		fmt.Fprintln(&gitCredentials, u.String())
	}
	if err := writeSecretFile(filepath.Join(dir, "git", "credentials"), gitCredentials.Bytes()); err != nil {
		return err
	}
	config := "[credential]\n\thelper = store --file=" + filepath.Join(dir, "git", "credentials") + "\n"
	return writeSecretFile(filepath.Join(dir, "git", "config"), []byte(config))
}

func pluginHost(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid plugin base URL %q", baseURL)
	}
	return u.Host, nil
}

func writeSecretFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
