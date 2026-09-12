package cuberuntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	cube "github.com/tencentcloud/CubeSandbox/sdk/go"
)

type VM interface {
	ID() string
	Run(context.Context, string, time.Duration) (string, error)
	Write(context.Context, string, []byte) error
}
type Backend interface {
	Create(context.Context, string, map[string]string) (VM, error)
	Connect(context.Context, string) (VM, error)
	Find(context.Context, string, string) ([]string, error)
	Delete(context.Context, string) error
	Owned(context.Context, string) ([]OwnedVM, error)
}

type OwnedVM struct{ ID, JobName string }

type SDKBackend struct {
	client *cube.Client
	cfg    cube.Config
	http   *http.Client
}

func NewSDKBackend(cfg cube.Config) *SDKBackend {
	// Control/data endpoints are administrator configuration. Ignore ambient
	// workstation proxy variables and preserve CubeProxy's virtual Host routing.
	h := &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{MaxIdleConns: 32, MaxIdleConnsPerHost: 8, IdleConnTimeout: time.Minute}}
	return &SDKBackend{client: cube.NewClient(cfg, cube.WithHTTPClient(h)), cfg: cfg, http: h}
}

type sdkVM struct{ sandbox *cube.Sandbox }

func (v *sdkVM) ID() string { return v.sandbox.SandboxID }
func (v *sdkVM) Run(ctx context.Context, cmd string, timeout time.Duration) (string, error) {
	cmd = "export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin; " + cmd
	result, err := v.sandbox.Commands().Run(ctx, cmd, cube.CommandOptions{User: "root", Timeout: timeout, Envs: map[string]string{"HOME": "/root", "BASH_ENV": "/dev/null", "ENV": "/dev/null"}})
	if err != nil {
		return "", fmt.Errorf("sandbox command transport failed: %w", err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("sandbox command exited %d", result.ExitCode)
	}
	return result.Stdout, nil
}
func (v *sdkVM) Write(ctx context.Context, path string, b []byte) error {
	return v.sandbox.Files().ForUser("root").Write(ctx, path, b)
}
func (b *SDKBackend) Create(ctx context.Context, template string, meta map[string]string) (VM, error) {
	s, err := b.client.Create(ctx, cube.CreateOptions{TemplateID: template, Metadata: meta, Timeout: cube.DurationPtr(15 * time.Minute), Extra: map[string]any{"lifecycle": map[string]any{"onTimeout": "pause", "autoResume": true}}})
	if err != nil {
		return nil, err
	}
	return &sdkVM{s}, nil
}
func (b *SDKBackend) Connect(ctx context.Context, id string) (VM, error) {
	s, err := b.client.Connect(ctx, id)
	if err != nil {
		return nil, err
	}
	return &sdkVM{s}, nil
}
func (b *SDKBackend) Find(ctx context.Context, owner, name string) ([]string, error) {
	list, err := b.inventory(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, s := range list {
		if s.Metadata["jcloud.owner"] == owner && s.Metadata["jcloud.job"] == name {
			ids = append(ids, s.SandboxID)
		}
	}
	return ids, nil
}
func (b *SDKBackend) Owned(ctx context.Context, owner string) ([]OwnedVM, error) {
	list, err := b.inventory(ctx)
	if err != nil {
		return nil, err
	}
	var out []OwnedVM
	for _, s := range list {
		if s.Metadata["jcloud.owner"] == owner {
			out = append(out, OwnedVM{s.SandboxID, s.Metadata["jcloud.job"]})
		}
	}
	return out, nil
}
func (b *SDKBackend) inventory(ctx context.Context) ([]cube.SandboxInfo, error) {
	u, err := url.Parse(b.cfg.APIURL + "/v2/sandboxes")
	if err != nil {
		return nil, err
	}
	// v0.7.0-rc2 applies metadata filters AFTER its global limit. Fetch a
	// bounded inventory and filter locally; fail closed if the bound is reached.
	// Using limit=1 with metadata would falsely miss older live jobs.
	q := u.Query()
	q.Set("limit", "10000")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if b.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg.APIKey)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("CubeSandbox lookup returned HTTP %d", resp.StatusCode)
	}
	var list []cube.SandboxInfo
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	if len(list) >= 10000 {
		return nil, fmt.Errorf("CubeSandbox inventory exceeded the safe lookup bound; refusing ambiguous resource cleanup")
	}
	return list, nil
}
func (b *SDKBackend) Delete(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, b.cfg.APIURL+"/sandboxes/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	if b.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.cfg.APIKey)
	}
	r, err := b.http.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode == 200 || r.StatusCode == 204 || r.StatusCode == 404 {
		return nil
	}
	return fmt.Errorf("CubeSandbox delete returned HTTP %d", r.StatusCode)
}
