// plugin-runtime is the Orchestrator-owned runtime injector for Project
// Plugins. It runs only as a Kubernetes init/companion container: the generic
// Runner image contains no Provider CLI, Skill, command, or credential-format
// implementation.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const defaultAssetsDir = "/opt/jcloud/plugin-runtime"

type pluginCredential struct {
	Provider    string `json:"provider"`
	BaseURL     string `json:"base_url"`
	AccessToken string `json:"access_token"`
	Scheme      string `json:"scheme"`
}

type pluginCredentialResponse struct {
	Credentials []pluginCredential `json:"credentials"`
}

type client struct {
	base      string
	runID     string
	token     string
	providers map[string]bool
	http      *http.Client
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: plugin-runtime <inject|sync-credentials> [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "inject":
		fs := flag.NewFlagSet("inject", flag.ExitOnError)
		providers := fs.String("providers", "", "comma-separated Providers from the run snapshot")
		dir := fs.String("dir", "", "shared tmpfs destination")
		assets := fs.String("assets", defaultAssetsDir, "Orchestrator-owned asset root")
		_ = fs.Parse(os.Args[2:])
		if *dir == "" {
			fatal("inject: --dir is required")
		}
		if err := injectRuntime(*assets, *dir, *providers); err != nil {
			fatal("inject: " + err.Error())
		}
	case "sync-credentials":
		fs := flag.NewFlagSet("sync-credentials", flag.ExitOnError)
		providers := fs.String("providers", "", "comma-separated Providers from the run snapshot")
		dir := fs.String("dir", "", "shared tmpfs directory for Provider CLI/Git/MCP configuration")
		interval := fs.Duration("interval", 5*time.Minute, "credential refresh interval")
		once := fs.Bool("once", false, "sync once and exit")
		stopFile := fs.String("stop-file", "", "exit when this runner lifecycle file appears")
		_ = fs.Parse(os.Args[2:])
		providerList, err := parseProviders(*providers)
		if err != nil || len(providerList) == 0 {
			fatal("sync-credentials: --providers must contain only supported snapshot Providers")
		}
		if *dir == "" || *interval <= 0 {
			fatal("sync-credentials: --dir and a positive --interval are required")
		}
		base, runID, token := os.Getenv("ORCH_BASE_URL"), os.Getenv("RUN_ID"), os.Getenv("RUN_TOKEN")
		if base == "" || runID == "" || token == "" {
			fatal("sync-credentials requires ORCH_BASE_URL/RUN_ID/RUN_TOKEN")
		}
		allowed := make(map[string]bool, len(providerList))
		for _, provider := range providerList {
			allowed[provider] = true
		}
		c := &client{base: base, runID: runID, token: token, providers: allowed, http: &http.Client{Timeout: 60 * time.Second}}
		if err := c.syncPluginCredentials(*dir, *interval, *once, *stopFile); err != nil {
			fatal("sync-credentials: " + err.Error())
		}
	default:
		fatal("unknown command " + os.Args[1])
	}
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, "[plugin-runtime] "+message)
	os.Exit(1)
}

func injectRuntime(assetsDir, dir, rawProviders string) error {
	providers, err := parseProviders(rawProviders)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create runtime mount: %w", err)
	}
	binDir, skillsDir := filepath.Join(dir, "bin"), filepath.Join(dir, "skills")
	if err := os.RemoveAll(binDir); err != nil {
		return fmt.Errorf("clear runtime bin: %w", err)
	}
	if err := os.RemoveAll(skillsDir); err != nil {
		return fmt.Errorf("clear runtime skills: %w", err)
	}
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(skillsDir, 0o700); err != nil {
		return err
	}
	for _, provider := range providers {
		if provider == "jtype" {
			continue // JType is MCP-only by product decision.
		}
		binary := map[string]string{"github": "gh", "gitlab": "glab", "gitea": "tea"}[provider]
		if err := copyFile(filepath.Join(assetsDir, "bin", binary), filepath.Join(binDir, binary), 0o755); err != nil {
			return fmt.Errorf("inject %s CLI: %w", provider, err)
		}
		skillDir := filepath.Join(skillsDir, provider)
		if err := os.MkdirAll(skillDir, 0o700); err != nil {
			return err
		}
		if err := copyFile(filepath.Join(assetsDir, "skills", provider, "SKILL.md"), filepath.Join(skillDir, "SKILL.md"), 0o600); err != nil {
			return fmt.Errorf("inject %s Skill: %w", provider, err)
		}
	}
	return nil
}

func parseProviders(raw string) ([]string, error) {
	seen := map[string]bool{}
	for _, value := range strings.Split(raw, ",") {
		provider := strings.TrimSpace(strings.ToLower(value))
		if provider == "" {
			continue
		}
		switch provider {
		case "github", "gitlab", "gitea", "jtype":
			seen[provider] = true
		default:
			return nil, fmt.Errorf("unsupported Provider %q", provider)
		}
	}
	providers := make([]string, 0, len(seen))
	for provider := range seen {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	return providers, nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(destination, mode)
}

// syncPluginCredentials writes a first usable configuration or fails visibly,
// then refreshes atomically until the runner lifecycle file appears.
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
			fmt.Fprintln(os.Stderr, "[plugin-runtime] credential refresh failed; keeping last good config:", err)
		}
		if stopFile == "" {
			time.Sleep(interval)
			continue
		}
		timer := time.NewTimer(interval)
		ticker := time.NewTicker(time.Second)
		for {
			select {
			case <-timer.C:
				ticker.Stop()
				goto refresh
			case <-ticker.C:
				if _, err := os.Stat(stopFile); err == nil {
					ticker.Stop()
					if !timer.Stop() {
						<-timer.C
					}
					return nil
				} else if !os.IsNotExist(err) {
					return fmt.Errorf("check stop file: %w", err)
				}
			}
		}
	refresh:
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
	for i := range body.Credentials {
		if !c.providers[body.Credentials[i].Provider] {
			return fmt.Errorf("credential endpoint returned Provider %q outside the run snapshot", body.Credentials[i].Provider)
		}
	}
	return writePluginConfigs(dir, body.Credentials)
}

func writePluginConfigs(dir string, credentials []pluginCredential) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create credential dir: %w", err)
	}
	byProvider := map[string]pluginCredential{}
	for _, credential := range credentials {
		switch credential.Provider {
		case "github", "gitlab", "gitea", "jtype":
		default:
			return fmt.Errorf("unsupported credential Provider %q", credential.Provider)
		}
		if credential.AccessToken == "" || credential.BaseURL == "" {
			return fmt.Errorf("credential for %q is incomplete", credential.Provider)
		}
		if _, duplicate := byProvider[credential.Provider]; duplicate {
			return fmt.Errorf("duplicate credential for Provider %q", credential.Provider)
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
		return err
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
		return err
	}
	if cred, ok := byProvider["gitea"]; ok {
		if _, err := pluginHost(cred.BaseURL); err != nil {
			return err
		}
		if err := writeSecretFile(filepath.Join(dir, "tea", "config.yml"), []byte("logins:\n    - name: jcloud\n      url: "+cred.BaseURL+"\n      token: "+cred.AccessToken+"\n      default: true\n")); err != nil {
			return err
		}
	} else if err := os.RemoveAll(filepath.Join(dir, "tea")); err != nil {
		return err
	}
	mcpServers := map[string]any{}
	if cred, ok := byProvider["jtype"]; ok {
		mcpServers["jtype"] = map[string]any{"url": strings.TrimRight(cred.BaseURL, "/") + "/mcp", "headers": map[string]string{"Authorization": "Bearer " + cred.AccessToken}}
	}
	payload, err := json.Marshal(map[string]any{"mcpServers": mcpServers})
	if err != nil {
		return err
	}
	if err := writeSecretFile(filepath.Join(dir, "jtype", "mcp.json"), append(payload, '\n')); err != nil {
		return err
	}
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
	return writeSecretFile(filepath.Join(dir, "git", "config"), []byte("[credential]\n\thelper = store --file="+filepath.Join(dir, "git", "credentials")+"\n"))
}

func pluginHost(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid Plugin base URL %q", baseURL)
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
