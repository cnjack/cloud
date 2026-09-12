package config

import (
	"strings"
	"testing"
)

func cubeEnv(t *testing.T) {
	t.Helper()
	baseEnv(t)
	t.Setenv("DISABLE_K8S", "0")
	t.Setenv("JOB_LAUNCHER", "cubesandbox")
	t.Setenv("RUNNER_IMAGE", "runner:one")
	t.Setenv("PLUGIN_RUNTIME_IMAGE", "orchestrator:one")
	t.Setenv("ORCH_BASE_URL", "https://cloud.example")
	t.Setenv("CUBE_API_URL", "http://192.0.2.1:31000")
	t.Setenv("CUBE_PROXY_NODE_IP", "192.0.2.1")
	t.Setenv("CUBE_RUNNER_TEMPLATES_JSON", `{"default":"tpl-one"}`)
}
func TestCubeRuntimeConfiguration(t *testing.T) {
	cubeEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.CubeTemplateImages()["runner:one"] != "tpl-one" {
		t.Fatal("profile/template bridge missing")
	}
}
func TestCubeRuntimeRejectsUnusableContracts(t *testing.T) {
	for _, tc := range []struct{ key, value, want string }{{"CUBE_RUNNER_TEMPLATES_JSON", `{}`, "default"}, {"CUBE_RUNNER_TEMPLATES_JSON", `{"default":"latest"}`, "immutable"}, {"CUBE_API_URL", "http://secret@host", "CUBE_API_URL"}, {"CUBE_PROXY_NODE_IP", "wrong", "CUBE_PROXY"}, {"PERSISTENT_WORKSPACE", "1", "S3"}, {"JOB_LAUNCHER", "cubee", "JOB_LAUNCHER"}} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			cubeEnv(t)
			t.Setenv(tc.key, tc.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v want %s", err, tc.want)
			}
		})
	}
}
