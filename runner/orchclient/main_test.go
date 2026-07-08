package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// captured holds the body a fake orchestrator received.
type captured struct {
	events []map[string]any
}

func fakeOrch(t *testing.T, sink *captured) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body struct {
			Events []map[string]any `json:"events"`
		}
		_ = json.Unmarshal(b, &body)
		sink.events = append(sink.events, body.Events...)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"accepted":1}`))
	}))
}

// TestReportGitUsesReservedSeq is the regression for the seq-collision bug: the
// acpdrive emitter posts runner events with client seqs 1..N, and the
// orchestrator dedupes runner events by (run_id, "runner", client_seq). If
// report-git reused seq=1 it would be silently deduped away (the run row would
// still update via applyRunGit, but the durable/streamed run.git event would
// vanish — exactly what broke J4). report-git must post its RESERVED high seq.
func TestReportGitUsesReservedSeq(t *testing.T) {
	var sink captured
	srv := fakeOrch(t, &sink)
	defer srv.Close()

	c := &client{base: srv.URL, runID: "r1", token: "tok", http: &http.Client{Timeout: 5 * time.Second}}
	c.reportGit("agent/run-r1", "deadbeef", 1_000_002)

	if len(sink.events) != 1 {
		t.Fatalf("posted %d events, want 1", len(sink.events))
	}
	ev := sink.events[0]
	if ev["type"] != "run.git" {
		t.Fatalf("type=%v want run.git", ev["type"])
	}
	// seq must be the reserved high value, NOT in acpdrive's 1..N range.
	seq, _ := ev["seq"].(float64)
	if seq < 1_000_000 {
		t.Fatalf("run.git client seq=%v collides with acpdrive's 1..N stream (must be reserved-high)", seq)
	}
	payload, _ := ev["payload"].(map[string]any)
	if payload["branch"] != "agent/run-r1" || payload["commit_sha"] != "deadbeef" {
		t.Fatalf("payload = %v", payload)
	}
}

// TestReportFailureUsesDistinctReservedSeq ensures report-failure's reserved seq
// differs from report-git's, so a run that reports BOTH (push produced a diff
// then push_failed) does not have one deduped against the other.
func TestReportFailureUsesDistinctReservedSeq(t *testing.T) {
	var sink captured
	srv := fakeOrch(t, &sink)
	defer srv.Close()

	c := &client{base: srv.URL, runID: "r1", token: "tok", http: &http.Client{Timeout: 5 * time.Second}}
	c.reportFailure("push_failed", "remote rejected", 1_000_001)
	c.reportGit("agent/run-r1", "sha", 1_000_002)

	if len(sink.events) != 2 {
		t.Fatalf("posted %d events, want 2", len(sink.events))
	}
	s0, _ := sink.events[0]["seq"].(float64)
	s1, _ := sink.events[1]["seq"].(float64)
	if s0 == s1 {
		t.Fatalf("report-failure and report-git share a client seq (%v) — they must be distinct", s0)
	}
}

// TestReportResultPostsNoChanges proves report-result posts a run.result event
// carrying the outcome, under a RESERVED high client seq (so it never collides
// with acpdrive's 1..N stream) that is DISTINCT from report-failure/report-git
// (a run could report both a result and, later, a failure).
func TestReportResultPostsNoChanges(t *testing.T) {
	var sink captured
	srv := fakeOrch(t, &sink)
	defer srv.Close()

	c := &client{base: srv.URL, runID: "r1", token: "tok", http: &http.Client{Timeout: 5 * time.Second}}
	c.reportResult("no_changes", 1_000_003)
	c.reportFailure("agent_error", "boom", 1_000_001)
	c.reportGit("agent/run-r1", "sha", 1_000_002)

	if len(sink.events) != 3 {
		t.Fatalf("posted %d events, want 3", len(sink.events))
	}
	ev := sink.events[0]
	if ev["type"] != "run.result" {
		t.Fatalf("type=%v want run.result", ev["type"])
	}
	seq, _ := ev["seq"].(float64)
	if seq < 1_000_000 {
		t.Fatalf("run.result client seq=%v collides with acpdrive's 1..N stream (must be reserved-high)", seq)
	}
	payload, _ := ev["payload"].(map[string]any)
	if payload["outcome"] != "no_changes" {
		t.Fatalf("payload = %v want outcome=no_changes", payload)
	}
	// All three reserved seqs must be distinct so none deduped against another.
	seen := map[float64]bool{}
	for _, e := range sink.events {
		s, _ := e["seq"].(float64)
		if seen[s] {
			t.Fatalf("reserved client seq %v reused across entrypoint reports", s)
		}
		seen[s] = true
	}
}
