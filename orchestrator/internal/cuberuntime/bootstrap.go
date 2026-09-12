package cuberuntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/k8s"
)

//go:embed guest-setup.sh
var setupScript string

//go:embed guest-supervise.sh
var supervisorScript string

//go:embed guest-checkpoint.sh
var checkpointScript string

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func encodeEnv(env map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(env))
	for k := range env {
		if !envName.MatchString(k) {
			return nil, fmt.Errorf("invalid environment name %q", k)
		}
		if strings.ContainsRune(env[k], 0) {
			return nil, fmt.Errorf("environment %s contains a NUL byte", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "export %s=%s\n", k, shellQuote(env[k]))
	}
	return []byte(b.String()), nil
}

func (l *Launcher) bootstrap(ctx context.Context, vm VM, spec k8s.JobSpec, restore string) error {
	env := make(map[string]string, len(spec.Env)+8)
	for k, v := range spec.Env {
		env[k] = v
	}
	env["HOME"] = "/home/jcode"
	env["JCODE_RESERVED_SKILLS"] = "github,gitlab,gitea"
	env["CUBE_JOB_TIMEOUT"] = fmt.Sprint(spec.TimeoutSeconds)
	env["CUBE_PLUGIN_PROVIDERS"] = strings.Join(spec.PluginProviders, ",")
	// Task/project environment is only sourced AFTER dropping privileges. Root
	// helpers receive a small trusted contract, never BASH_ENV/LD_PRELOAD/PATH
	// or other task-controlled process settings.
	controlEnv := map[string]string{
		"CUBE_JOB_TIMEOUT": env["CUBE_JOB_TIMEOUT"], "CUBE_PLUGIN_PROVIDERS": env["CUBE_PLUGIN_PROVIDERS"],
		"ORCH_BASE_URL": spec.Env["ORCH_BASE_URL"], "RUN_ID": spec.RunID, "RUN_TOKEN": spec.Env["RUN_TOKEN"],
		"http_proxy": l.cfg.HTTPProxy, "https_proxy": l.cfg.HTTPSProxy, "no_proxy": l.cfg.NoProxy,
	}
	delete(env, "CUBE_RESTORE_URL")
	delete(env, "CUBE_PLUGIN_ENABLED")
	delete(env, "RESTORE_ARCHIVE_URL")
	if restore != "" {
		env["CUBE_RESTORE_URL"] = restore
		delete(env, "RESTORE_ARCHIVE_URL")
	} else if spec.RestoreArchiveURL != "" {
		env["CUBE_RESTORE_URL"] = spec.RestoreArchiveURL
	}
	if spec.PluginCredentials {
		env["CUBE_PLUGIN_ENABLED"] = "1"
		env["JCODE_PLUGIN_CREDENTIALS_DIR"] = "/run/jcloud/plugins"
		env["JCODE_MANAGED_SKILLS_DIR"] = "/run/jcloud/runtime/skills"
		env["PLUGIN_SYNC_STOP_FILE"] = "/run/jcloud/lifecycle/runner-finished"
		controlEnv["CUBE_PLUGIN_ENABLED"] = "1"
	}
	if spec.ModelConfigBase64 != "" {
		env["JCODE_CONFIG"] = "/run/jcloud/config/config.json"
	}
	for _, p := range spec.PluginProviders {
		switch p {
		case "github":
			env["GH_CONFIG_DIR"] = "/run/jcloud/plugins/gh"
		case "gitlab":
			env["GLAB_CONFIG_DIR"] = "/run/jcloud/plugins/glab"
		case "gitea":
			env["XDG_CONFIG_HOME"] = "/run/jcloud/plugins"
		case "jtype":
		default:
			return fmt.Errorf("unsupported CubeSandbox Plugin provider %q", p)
		}
		if p != "jtype" {
			env["GIT_CONFIG_GLOBAL"] = "/run/jcloud/plugins/git/config"
		}
	}
	if len(spec.Attachments) > 0 {
		env["JCODE_ATTACHMENTS_DIR"] = "/run/jcloud/attachments"
	}
	envBytes, err := encodeEnv(env)
	if err != nil {
		return err
	}
	controlEnv["CUBE_RESTORE_URL"] = env["CUBE_RESTORE_URL"]
	delete(env, "CUBE_RESTORE_URL")
	delete(env, "CUBE_PLUGIN_ENABLED")
	delete(env, "CUBE_JOB_TIMEOUT")
	delete(env, "CUBE_PLUGIN_PROVIDERS")
	envBytes, err = encodeEnv(env)
	if err != nil {
		return err
	}
	controlBytes, err := encodeEnv(controlEnv)
	if err != nil {
		return err
	}
	files := map[string][]byte{"control/env.sh": controlBytes, "config/runner-env.sh": envBytes, "control/setup.sh": []byte(setupScript), "control/supervise.sh": []byte(supervisorScript), "control/checkpoint.sh": []byte(checkpointScript)}
	if spec.ModelConfigBase64 != "" {
		b, err := base64.StdEncoding.DecodeString(spec.ModelConfigBase64)
		if err != nil || !json.Valid(b) {
			return fmt.Errorf("invalid model configuration")
		}
		files["config/config.json"] = b
	}
	if spec.PluginCredentials {
		b, err := os.ReadFile(l.cfg.PluginBinary)
		if err != nil {
			return fmt.Errorf("Plugin runtime binary unavailable: %w", err)
		}
		files["control/plugin-runtime"] = b
		for _, p := range spec.PluginProviders {
			if p == "jtype" {
				continue
			}
			cli := map[string]string{"github": "gh", "gitlab": "glab", "gitea": "tea"}[p]
			for _, rel := range []string{"bin/" + cli, "skills/" + p + "/SKILL.md"} {
				b, err := os.ReadFile(filepath.Join(l.cfg.PluginAssets, rel))
				if err != nil {
					return fmt.Errorf("Plugin %s asset unavailable: %w", p, err)
				}
				files["assets/"+rel] = b
			}
		}
	}
	var downloads strings.Builder
	type attachment struct {
		StageID     string `json:"stage_id"`
		DisplayName string `json:"display_name"`
		ContentType string `json:"content_type,omitempty"`
		Size        int64  `json:"size_bytes"`
		Path        string `json:"path"`
	}
	var manifest []attachment
	for _, a := range spec.Attachments {
		if !identifier.MatchString(a.StageID) || a.SizeBytes < 0 {
			return fmt.Errorf("invalid attachment identity or size")
		}
		path := "/run/jcloud/attachments/" + a.StageID
		fmt.Fprintf(&downloads, "curl --fail --silent --show-error --location --max-time 120 %s -o %s\n[ \"$(wc -c < %s | tr -d ' ')\" = %s ]\n", shellQuote(a.URL), shellQuote(path), shellQuote(path), shellQuote(fmt.Sprint(a.SizeBytes)))
		manifest = append(manifest, attachment{a.StageID, a.DisplayName, a.ContentType, a.SizeBytes, path})
	}
	files["control/attachments.sh"] = []byte(downloads.String())
	if len(manifest) > 0 {
		b, err := json.Marshal(manifest)
		if err != nil {
			return err
		}
		files["attachments/manifest.json"] = b
	}
	bundle, err := packFiles(files)
	if err != nil {
		return err
	}
	// The bundle and its credentials never touch the persistent workspace.
	if _, err := vm.Run(ctx, `set -eu; test "$(id -u)" = 0; command -v iptables >/dev/null; command -v ip6tables >/dev/null; command -v setpriv >/dev/null; command -v pkill >/dev/null; mkdir -p /run/jcloud; if ! mountpoint -q /run/jcloud; then mount -t tmpfs -o mode=0755,size=512m tmpfs /run/jcloud; fi; mkdir -p /run/jcloud/control; chmod 700 /run/jcloud/control`, 20*time.Second); err != nil {
		return fmt.Errorf("CubeSandbox template lacks required runtime tools: %w", err)
	}
	if err := vm.Write(ctx, "/run/jcloud/control/bootstrap.tgz", bundle); err != nil {
		return err
	}
	_, err = vm.Run(ctx, `set -eu; tar -xzf /run/jcloud/control/bootstrap.tgz -C /run/jcloud; rm /run/jcloud/control/bootstrap.tgz; chmod 700 /run/jcloud/control; chmod 600 /run/jcloud/control/env.sh; if [ -f /run/jcloud/control/plugin-runtime ]; then chmod 700 /run/jcloud/control/plugin-runtime; fi; /bin/bash /run/jcloud/control/setup.sh; nohup /bin/bash /run/jcloud/control/supervise.sh >/run/jcloud/control/runner.log 2>&1 </dev/null & pid=$!; for i in $(seq 1 100); do [ ! -f /run/jcloud/control/started ] || exit 0; kill -0 "$pid"; sleep 0.05; done; exit 70`, 2*time.Minute)
	return err
}
func packFiles(files map[string][]byte) ([]byte, error) {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := tw.WriteHeader(&tar.Header{Name: k, Mode: 0600, Size: int64(len(files[k]))}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(files[k]); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
