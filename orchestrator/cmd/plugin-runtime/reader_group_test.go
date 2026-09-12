package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootWriterPublishesGroupReadOnlyCredentialsAcrossRefresh(t *testing.T) {
	dir := t.TempDir()
	gid := os.Getgid()
	for _, token := range []string{"first-token", "refreshed-token"} {
		if err := writePluginConfigsForReader(dir, []pluginCredential{{Provider: "jtype", BaseURL: "https://jtype.example", AccessToken: token}}, &gid); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{dir, filepath.Join(dir, "jtype"), filepath.Join(dir, "jtype", "mcp.json")} {
			st, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			want := os.FileMode(0640)
			if st.IsDir() {
				want = 0750
			}
			if st.Mode().Perm() != want {
				t.Fatalf("%s mode=%o want %o", path, st.Mode().Perm(), want)
			}
		}
	}
	private := filepath.Join(t.TempDir(), "private")
	if err := writeSecretFile(private, []byte("private")); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(private)
	if st.Mode().Perm() != 0600 {
		t.Fatal("existing Kubernetes writer permissions changed")
	}
}
