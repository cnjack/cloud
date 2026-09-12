package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
)

func (c *Config) validateRuntime() error {
	switch c.JobLauncher {
	case "kubernetes", "process":
		return nil
	case "cubesandbox":
	default:
		return fmt.Errorf("JOB_LAUNCHER must be kubernetes, process or cubesandbox")
	}
	if c.DisableK8s {
		return nil
	}
	u, err := url.Parse(c.CubeAPIURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("CUBE_API_URL must be an http(s) API endpoint without credentials or query parameters")
	}
	if net.ParseIP(c.CubeProxyHost) == nil || c.CubeProxyPort < 1 || c.CubeProxyPort > 65535 {
		return fmt.Errorf("CUBE_PROXY_NODE_IP and CUBE_PROXY_PORT_HTTP must identify the reachable CubeProxy")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`).MatchString(c.CubeOwner) {
		return fmt.Errorf("CUBE_RUNTIME_OWNER must be a stable deployment identifier")
	}
	if err := json.Unmarshal([]byte(os.Getenv("CUBE_RUNNER_TEMPLATES_JSON")), &c.CubeTemplates); err != nil {
		return fmt.Errorf("CUBE_RUNNER_TEMPLATES_JSON must map every runner profile to its template ID")
	}
	for profile := range c.RunnerProfiles {
		if !regexp.MustCompile(`^tpl-[a-zA-Z0-9-]+$`).MatchString(c.CubeTemplates[profile]) {
			return fmt.Errorf("CUBE_RUNNER_TEMPLATES_JSON is missing an immutable template ID for %s", profile)
		}
	}
	for profile := range c.CubeTemplates {
		if _, ok := c.RunnerProfiles[profile]; !ok {
			return fmt.Errorf("CUBE_RUNNER_TEMPLATES_JSON contains unknown profile %s", profile)
		}
	}
	if c.PersistentWorkspace && !c.ArchiveEnabled() {
		return fmt.Errorf("CubeSandbox persistent workspaces require complete S3 checkpoint configuration")
	}
	return nil
}

func (c *Config) CubeTemplateImages() map[string]string {
	out := make(map[string]string, len(c.CubeTemplates))
	for p, t := range c.CubeTemplates {
		if image, ok := c.ResolveRunnerImage(p); ok {
			out[image] = t
		}
	}
	return out
}
