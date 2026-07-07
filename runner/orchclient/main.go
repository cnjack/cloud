// orchclient is a tiny stdlib-only helper the entrypoint uses to talk to the
// orchestrator's internal API. The runner base image is debian-slim with no
// curl/wget, so this single static binary carries the two POSTs the runner
// needs to make from shell:
//
//	orchclient report-failure --reason clone_failed --message "fatal: ..."
//	    → POST /internal/v1/runs/{RUN_ID}/events  {events:[{seq,type:run.failure,payload:{reason,message}}]}
//
//	orchclient report-git --branch agent/run-<id> --commit <sha>
//	    → POST /internal/v1/runs/{RUN_ID}/events  {events:[{seq,type:run.git,payload:{branch,commit_sha}}]}
//
//	orchclient upload-artifact --kind diff --file /out/diff.patch
//	    → POST /internal/v1/runs/{RUN_ID}/artifact  {kind,content}
//
// Config comes from the environment (same vars the orchestrator injects):
//
//	ORCH_BASE_URL, RUN_ID, RUN_TOKEN
//
// If any of those is empty the command is a SUCCESSFUL no-op: the runner must
// still work standalone (e.g. the pure headless proof has no orchestrator), so
// the absence of a control plane is never itself a failure. All network calls
// are best-effort with a short retry; a permanent failure to report is logged to
// stderr and exits 0 so it can't mask the real run outcome the entrypoint is
// trying to surface.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: orchclient <report-failure|upload-artifact> [flags]")
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	base := os.Getenv("ORCH_BASE_URL")
	runID := os.Getenv("RUN_ID")
	token := os.Getenv("RUN_TOKEN")
	if base == "" || runID == "" || token == "" {
		// No control plane wired: nothing to report. Not an error.
		fmt.Fprintln(os.Stderr, "[orchclient] ORCH_BASE_URL/RUN_ID/RUN_TOKEN not all set; skipping "+cmd)
		return
	}
	c := &client{base: base, runID: runID, token: token, http: &http.Client{Timeout: 10 * time.Second}}

	// entrypoint-posted events (run.failure / run.git) use a HIGH, RESERVED client
	// seq range so they never collide with the acpdrive emitter's own 1..N runner
	// stream — the orchestrator dedupes runner events by (run_id, "runner",
	// client_seq), so reusing a seq acpdrive already sent would silently drop the
	// event (the run row would still update, but the durable/streamed event would
	// vanish). Distinct fixed keys keep each entrypoint report idempotent on
	// re-send without clashing with each other or the agent stream.
	const (
		seqReportFailure = 1_000_001
		seqReportGit     = 1_000_002
	)

	switch cmd {
	case "report-failure":
		fs := flag.NewFlagSet("report-failure", flag.ExitOnError)
		reason := fs.String("reason", "agent_error", "failure reason (clone_failed|setup_failed|agent_error|timeout|push_failed)")
		message := fs.String("message", "", "human-readable failure message")
		seq := fs.Int64("seq", seqReportFailure, "client seq (idempotency key; server allocates the durable seq)")
		_ = fs.Parse(args)
		if *message == "" {
			*message = "runner reported a failure"
		}
		c.reportFailure(*reason, *message, *seq)

	case "report-git":
		fs := flag.NewFlagSet("report-git", flag.ExitOnError)
		branch := fs.String("branch", "", "the pushed branch (agent/run-<id>)")
		commit := fs.String("commit", "", "the pushed commit sha")
		seq := fs.Int64("seq", seqReportGit, "client seq (idempotency key; server allocates the durable seq)")
		_ = fs.Parse(args)
		if *branch == "" {
			fmt.Fprintln(os.Stderr, "[orchclient] report-git: --branch is required")
			os.Exit(2)
		}
		c.reportGit(*branch, *commit, *seq)

	case "upload-artifact":
		fs := flag.NewFlagSet("upload-artifact", flag.ExitOnError)
		kind := fs.String("kind", "diff", "artifact kind")
		file := fs.String("file", "", "path to the artifact content file (\"-\" for stdin)")
		_ = fs.Parse(args)
		content, err := readContent(*file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[orchclient] read artifact: %v\n", err)
			os.Exit(1)
		}
		c.uploadArtifact(*kind, content)

	default:
		fmt.Fprintf(os.Stderr, "[orchclient] unknown command %q\n", cmd)
		os.Exit(2)
	}
}

func readContent(file string) (string, error) {
	if file == "" || file == "-" {
		b, err := io.ReadAll(os.Stdin)
		return string(b), err
	}
	b, err := os.ReadFile(file)
	return string(b), err
}

type client struct {
	base  string
	runID string
	token string
	http  *http.Client
}

func (c *client) reportFailure(reason, message string, seq int64) {
	body := map[string]any{"events": []map[string]any{{
		"seq":     seq,
		"type":    "run.failure",
		"payload": map[string]any{"reason": reason, "message": message},
	}}}
	c.postJSON("/internal/v1/runs/"+c.runID+"/events", body, "report-failure")
}

func (c *client) reportGit(branch, commit string, seq int64) {
	body := map[string]any{"events": []map[string]any{{
		"seq":     seq,
		"type":    "run.git",
		"payload": map[string]any{"branch": branch, "commit_sha": commit},
	}}}
	c.postJSON("/internal/v1/runs/"+c.runID+"/events", body, "report-git")
}

func (c *client) uploadArtifact(kind, content string) {
	body := map[string]any{"kind": kind, "content": content}
	c.postJSON("/internal/v1/runs/"+c.runID+"/artifact", body, "upload-artifact")
}

// postJSON POSTs body with a short bounded retry on network/5xx errors. It never
// returns a nonzero exit: reporting is best-effort and must not mask the run's
// real outcome.
func (c *client) postJSON(path string, body any, label string) {
	b, err := json.Marshal(body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[orchclient] marshal %s: %v\n", label, err)
		return
	}
	url := c.base + path
	backoff := 200 * time.Millisecond
	const maxAttempts = 5
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ok, retryable := c.post(url, b)
		if ok {
			fmt.Fprintf(os.Stderr, "[orchclient] %s ok\n", label)
			return
		}
		if !retryable || attempt == maxAttempts {
			fmt.Fprintf(os.Stderr, "[orchclient] %s failed after %d attempt(s)\n", label, attempt)
			return
		}
		time.Sleep(backoff)
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

func (c *client) post(url string, body []byte) (ok, retryable bool) {
	ctx, cancel := context.WithTimeout(context.Background(), c.http.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return false, true
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return true, false
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return false, true
	default:
		fmt.Fprintf(os.Stderr, "[orchclient] non-retryable status %d for %s\n", resp.StatusCode, url)
		return false, false
	}
}
