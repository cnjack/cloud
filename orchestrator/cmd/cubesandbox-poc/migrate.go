package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/objstore"
	cube "github.com/tencentcloud/CubeSandbox/sdk/go"
)

// Run the identical fingerprint on the read-only source PVC and restored VM.
// File bytes, symlink targets and filesystem modes are checked separately;
// filenames are NUL-delimited so whitespace and Unicode remain unambiguous.
const workspaceFingerprintCommand = `set -euo pipefail
export LC_ALL=C
cd /
paths=(workspace home/jcode/.jcode)
excludes=( -path home/jcode/.jcode/config.json -o -path home/jcode/.jcode/mcp.json -o -path home/jcode/.jcode/skills/github -o -path home/jcode/.jcode/skills/gitlab -o -path home/jcode/.jcode/skills/gitea )
find "${paths[@]}" \( "${excludes[@]}" \) -prune -o -type f -print0 | sort -z | xargs -0 -r sha256sum -z | sha256sum | cut -d' ' -f1
find "${paths[@]}" \( "${excludes[@]}" \) -prune -o -type l -printf '%p:%l\0' | sort -z | sha256sum | cut -d' ' -f1
find "${paths[@]}" \( "${excludes[@]}" \) -prune -o -printf '%m %y %p\0' | sort -z | sha256sum | cut -d' ' -f1`

func migrateArchive(ctx context.Context, client *cube.Client, objects *objstore.Client, template, path, key, expected string) (retErr error) {
	if key == "" || expected == "" || objects == nil {
		return errors.New("--archive requires object storage, --checkpoint-key and --expected-fingerprint")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	// kubectl exec can report success while a large stdout transfer was cut
	// short. Reject an incomplete local backup before publishing any object.
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("invalid workspace gzip: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		_, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			gz.Close()
			return fmt.Errorf("invalid workspace tar: %w", err)
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			gz.Close()
			return err
		}
	}
	if _, err := io.Copy(io.Discard, gz); err != nil {
		gz.Close()
		return fmt.Errorf("incomplete workspace gzip: %w", err)
	}
	gz.Close()
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	put, err := objects.PresignPut(key, time.Hour)
	if err != nil {
		return err
	}
	get, err := objects.PresignGet(key, time.Hour)
	if err != nil {
		return err
	}
	httpClient := &http.Client{Timeout: 3 * time.Minute, Transport: &http.Transport{}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, put, f)
	if err != nil {
		return errors.New("construct archive upload request")
	}
	request.ContentLength = info.Size()
	response, err := httpClient.Do(request)
	if err != nil {
		return errors.New("workspace archive upload connection failed")
	}
	response.Body.Close()
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("workspace archive upload returned HTTP %d", response.StatusCode)
	}
	request, _ = http.NewRequestWithContext(ctx, http.MethodGet, get, nil)
	response, err = httpClient.Do(request)
	if err != nil {
		return errors.New("workspace archive verification connection failed")
	}
	h = sha256.New()
	_, copyErr := io.Copy(h, response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || copyErr != nil || hex.EncodeToString(h.Sum(nil)) != digest {
		return errors.New("uploaded workspace archive failed SHA-256 readback")
	}
	fmt.Printf("archive verified: bytes=%d sha256=%s key=%s\n", info.Size(), digest, key)
	sb, err := client.Create(ctx, cube.CreateOptions{TemplateID: template, Timeout: cube.DurationPtr(5 * time.Minute), Metadata: map[string]string{"jcloud.owner": "jcloud-migration", "jcloud.job": key}})
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := sb.Kill(ctx); err != nil && retErr == nil {
			retErr = fmt.Errorf("migration VM cleanup failed: %w", err)
		}
	}()
	command := `set -euo pipefail
mkdir -p /workspace /home/jcode/.jcode
chown 10001:10001 /workspace /home/jcode /home/jcode/.jcode
curl --fail --silent --show-error --max-time 120 "$DOWNLOAD_URL" -o /tmp/workspace-migration.tar.gz
test "$(sha256sum /tmp/workspace-migration.tar.gz | cut -d' ' -f1)" = "$EXPECTED_SHA"
chmod 644 /tmp/workspace-migration.tar.gz
setpriv --reuid=10001 --regid=10001 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs tar --no-same-owner --same-permissions -xzf /tmp/workspace-migration.tar.gz -C /
` + workspaceFingerprintCommand
	r, err := sb.Commands().Run(ctx, command, cube.CommandOptions{User: "root", Timeout: 3 * time.Minute, Envs: map[string]string{"HOME": "/root", "BASH_ENV": "/dev/null", "DOWNLOAD_URL": get, "EXPECTED_SHA": digest}})
	if err != nil {
		return err
	}
	if r.ExitCode != 0 {
		return fmt.Errorf("workspace restore verification exited %d: %s", r.ExitCode, strings.ReplaceAll(r.Stderr, get, "[checkpoint URL]"))
	}
	if strings.TrimSpace(r.Stdout) != strings.TrimSpace(expected) {
		return fmt.Errorf("restored workspace fingerprint mismatch: source=%s destination=%s", strings.TrimSpace(expected), strings.TrimSpace(r.Stdout))
	}
	fmt.Printf("PASS: native filesystem restore matches file bytes, symlinks and permissions; sandbox=%s\n", sb.SandboxID)
	return nil
}
