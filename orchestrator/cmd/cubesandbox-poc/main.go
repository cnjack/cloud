// cubesandbox-poc verifies the real provider before production cutover.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/cnjack/jcloud/internal/objstore"
	"io"
	"os"
	"sigs.k8s.io/yaml"
	"time"

	cube "github.com/tencentcloud/CubeSandbox/sdk/go"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	configFile := flag.String("config", "", "private JSON environment file (optional)")
	template := flag.String("template", "", "existing READY template")
	envdOutput := flag.String("export-envd", "", "export the template envd binary to this path and skip volume checks")
	sandboxID := flag.String("sandbox", "", "inspect an existing sandbox without deleting it")
	inspectCommand := flag.String("command", "id; ps -eo pid,comm", "command for --sandbox")
	cloudSecret := flag.String("cloud-secret", "", "Cloud Secret YAML with S3 configuration")
	volumeMode := flag.Bool("volume", false, "diagnose the optional S3 FUSE volume instead of checkpoint persistence")
	securityMode := flag.Bool("security", false, "verify non-root confinement and envd isolation in a disposable VM")
	archivePath := flag.String("archive", "", "upload and verify an existing workspace archive in a disposable Cube VM")
	checkpointKey := flag.String("checkpoint-key", "", "destination object key for --archive")
	expectedFingerprint := flag.String("expected-fingerprint", "", "source workspace fingerprint for --archive")
	printFingerprint := flag.Bool("print-fingerprint-command", false, "print the read-only source fingerprint command")
	flag.Parse()
	if *printFingerprint {
		_, err := io.WriteString(os.Stdout, workspaceFingerprintCommand+"\n")
		return err
	}
	if *configFile != "" {
		b, err := os.ReadFile(*configFile)
		if err != nil {
			return err
		}
		var values map[string]string
		if err := json.Unmarshal(b, &values); err != nil {
			return err
		}
		for k, v := range values {
			if err := os.Setenv(k, v); err != nil {
				return err
			}
		}
	}
	cfg := cube.NewConfigFromEnv()
	client := cube.NewClient(cfg)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if *securityMode {
		sb, err := client.Create(ctx, cube.CreateOptions{TemplateID: *template, Timeout: cube.DurationPtr(4 * time.Minute), Metadata: map[string]string{"jcloud.owner": "poc-security"}, Network: cube.NetworkOptions{AllowOut: []string{"192.168.10.236/32"}}})
		if err != nil {
			return err
		}
		defer sb.Kill(context.Background())
		command := `set -eu; export http_proxy=http://192.168.10.236:7890 https_proxy=http://192.168.10.236:7890; apt-get update -qq; apt-get install -y --no-install-recommends iptables >/tmp/packages.log; iptables -w -A OUTPUT -p tcp --dport 49983 -m owner --uid-owner 10001 -j REJECT; ip6tables -w -A OUTPUT -p tcp --dport 49983 -m owner --uid-owner 10001 -j REJECT; mkdir -p /run/jcloud/config; mount -t tmpfs -o size=4m,mode=0755 tmpfs /run/jcloud/config; echo proof > /run/jcloud/config/proof; chmod 444 /run/jcloud/config/proof; setpriv --reuid=10001 --regid=10001 --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs /bin/bash -c 'set -eu; test "$(id -u)" = 10001; test "$(cat /run/jcloud/config/proof)" = proof; if echo altered > /run/jcloud/config/proof 2>/dev/null; then exit 1; fi; if curl --noproxy "*" -fsS --max-time 3 http://127.0.0.1:49983/health; then exit 1; fi; if curl --noproxy "*" -g -fsS --max-time 3 http://[::1]:49983/health; then exit 1; fi; echo nonroot-and-envd-isolation-ok'`
		r, err := sb.Commands().Run(ctx, command, cube.CommandOptions{User: "root", Timeout: 3 * time.Minute})
		if err != nil {
			return err
		}
		fmt.Print(r.Stdout)
		if r.ExitCode != 0 {
			return fmt.Errorf("security PoC exit=%d: %s", r.ExitCode, r.Stderr)
		}
		return nil
	}
	if *sandboxID != "" {
		sb, err := client.Connect(ctx, *sandboxID)
		if err != nil {
			return err
		}
		result, err := sb.Commands().Run(ctx, *inspectCommand, cube.CommandOptions{User: "root", Timeout: 30 * time.Second})
		if err != nil {
			return err
		}
		fmt.Print(result.Stdout)
		fmt.Fprint(os.Stderr, result.Stderr)
		if result.ExitCode != 0 {
			return fmt.Errorf("command exited %d", result.ExitCode)
		}
		return nil
	}
	if *envdOutput != "" {
		sb, err := client.Create(ctx, cube.CreateOptions{TemplateID: *template, Timeout: cube.DurationPtr(time.Minute), Metadata: map[string]string{"jcloud.owner": "poc-envd"}})
		if err != nil {
			return err
		}
		defer sb.Kill(context.Background())
		b, err := sb.Files().ForUser("root").Read(ctx, "/usr/bin/envd")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*envdOutput, []byte(b), 0755); err != nil {
			return err
		}
		fmt.Println("exported envd bytes:", len(b))
		return nil
	}
	var objects *objstore.Client
	if *cloudSecret != "" {
		b, err := os.ReadFile(*cloudSecret)
		if err != nil {
			return err
		}
		var secret struct {
			StringData map[string]string `json:"stringData"`
			Data       map[string]string `json:"data"`
		}
		if err := yaml.Unmarshal(b, &secret); err != nil {
			return err
		}
		v := secret.StringData
		if v == nil {
			v = map[string]string{}
		}
		for k, x := range secret.Data {
			b, err := base64.StdEncoding.DecodeString(x)
			if err != nil {
				return err
			}
			v[k] = string(b)
		}
		objects, err = objstore.New(objstore.Config{Endpoint: "https://storage.scgzyun.com", Bucket: "jcode", Region: "us-east-1", ForcePathStyle: true, AccessKey: v["S3_ACCESS_KEY"], SecretKey: v["S3_SECRET_KEY"]})
		if err != nil {
			return err
		}
	}
	if !*volumeMode && objects == nil {
		return fmt.Errorf("--cloud-secret is required for checkpoint PoC")
	}
	if *archivePath != "" {
		return migrateArchive(ctx, client, objects, *template, *archivePath, *checkpointKey, *expectedFingerprint)
	}
	name := fmt.Sprintf("jcloud-poc-%d", time.Now().Unix())
	var mounts []cube.VolumeMount
	if *volumeMode {
		vol, err := client.CreateVolume(ctx, cube.CreateVolumeOptions{Name: name, Driver: "s3"})
		if err != nil {
			return err
		}
		mounts = []cube.VolumeMount{{Name: vol.VolumeID, Path: "/poc"}}
		defer func() {
			if err := client.DeleteVolume(context.Background(), vol.VolumeID); err != nil {
				fmt.Fprintln(os.Stderr, "volume cleanup:", err)
			}
		}()
		fmt.Println("volume created:", vol.VolumeID)
	}
	key := "cubesandbox/poc/" + name + ".tar.gz"
	if objects != nil {
		defer objects.Delete(context.Background(), key)
	}
	var ids []string
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, id := range ids {
			if sb, err := client.Connect(ctx, id); err == nil {
				if err := sb.Kill(ctx); err != nil {
					fmt.Fprintln(os.Stderr, "cleanup sandbox:", err)
				}
			}
		}
	}()
	for i := 0; i < 2; i++ {
		started := time.Now()
		sb, err := client.Create(ctx, cube.CreateOptions{TemplateID: *template, Timeout: cube.DurationPtr(2 * time.Minute), Metadata: map[string]string{"jcloud.owner": "poc", "jcloud.job": name}, VolumeMounts: mounts, Network: cube.NetworkOptions{AllowOut: []string{"192.168.10.0/24"}}})
		if err != nil {
			return fmt.Errorf("create sandbox: %w", err)
		}
		ids = append(ids, sb.SandboxID)
		fmt.Println("sandbox created:", sb.SandboxID, "in", time.Since(started).Round(time.Millisecond))
		command := `set -eu; id; command -v git; command -v setpriv; command -v mount; command -v timeout; uname -m; test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token; mkdir -p /poc/repo; cd /poc/repo; git init -q; git config user.name PoC; git config user.email poc@localhost; printf 'persistent\n' > proof; chmod 755 proof; ln -s proof link; git add .; git commit -qm proof; git status --porcelain; sync; curl -fsS --max-time 15 https://cloud.j-code.net/ -o /dev/null; echo network-ok`
		if i == 1 {
			command = `set -eu; cd /poc/repo; test "$(cat proof)" = persistent; test -x proof; test "$(readlink link)" = proof; test -z "$(git status --porcelain)"; git log -1 --format=%s; echo persistent-workspace-ok`
		}
		env := map[string]string{}
		if objects != nil {
			if i == 0 {
				url, err := objects.PresignPut(key, 10*time.Minute)
				if err != nil {
					return err
				}
				env["CHECKPOINT_URL"] = url
				command += `; tar -C / -czf /tmp/checkpoint.tgz poc; curl --fail --silent --show-error --max-time 60 -X PUT --data-binary @/tmp/checkpoint.tgz "$CHECKPOINT_URL"; echo checkpoint-uploaded`
			} else {
				url, err := objects.PresignGet(key, 10*time.Minute)
				if err != nil {
					return err
				}
				env["CHECKPOINT_URL"] = url
				command = `set -eu; curl --fail --silent --show-error --max-time 60 "$CHECKPOINT_URL" -o /tmp/checkpoint.tgz; tar -C / -xzf /tmp/checkpoint.tgz; ` + command
			}
		}
		result, err := sb.Commands().Run(ctx, command, cube.CommandOptions{User: "root", Timeout: 90 * time.Second, Envs: env})
		if err != nil {
			return fmt.Errorf("command: %w", err)
		}
		fmt.Print(result.Stdout)
		if result.ExitCode != 0 {
			return fmt.Errorf("command exit=%d: %s", result.ExitCode, result.Stderr)
		}
		if err := sb.Kill(ctx); err != nil {
			return fmt.Errorf("kill: %w", err)
		}
		ids = ids[:len(ids)-1]
		fmt.Println("sandbox deleted:", sb.SandboxID)
	}
	list, err := client.ListV2(ctx)
	if err != nil {
		return err
	}
	for _, s := range list {
		if s.Metadata["jcloud.job"] == name {
			return fmt.Errorf("PoC sandbox remains: %s", s.SandboxID)
		}
	}
	fmt.Println("PASS: execution, git semantics, checkpoint persistence across fresh VMs, network and sandbox cleanup")
	return nil
}
