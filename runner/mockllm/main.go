// mockllm is a tiny OpenAI-compatible chat-completions server used to drive
// jcode's agent loop end-to-end WITHOUT a real model or API key. It speaks the
// subset of the /v1/chat/completions contract that jcode's model layer
// (github.com/sashabaranov/go-openai, via eino) actually exercises:
//
//   - POST /v1/chat/completions with {"stream": true}  → SSE streaming chunks
//     (this is jcode's primary path: chatModel.Stream sets stream_options
//     include_usage and reads choices[].delta{.content,.tool_calls})
//   - POST /v1/chat/completions with {"stream": false} → single JSON body
//     (jcode's chatModel.Generate path; supported defensively)
//   - GET  /v1/models                                  → minimal catalog
//
// Scenario selection is TABLE-DRIVEN (see scenarios below). The active scenario
// is chosen by the MOCK_SCENARIO env var (default "write_file"). Within a
// scenario, the "turn" is derived from the request itself: if the incoming
// messages already contain a tool result (role=="tool"), we are on the SECOND
// turn (the agent fed our tool call's result back), so we answer with a plain
// assistant message and finish_reason=stop. Otherwise we are on the FIRST turn
// and answer with a tool call. This makes the mock stateless and safe under the
// agent's ret/summarize/parallel behavior.
//
// Extending: add a Scenario to the scenarios map. Each scenario is just the
// first-turn tool call (name + JSON arguments) and the final assistant text.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// ---- OpenAI wire types (only the fields we read/emit) ----

type chatReq struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
	Tools    []any     `json:"tools,omitempty"`
}

type message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Index    *int         `json:"index,omitempty"`
	Function toolCallFunc `json:"function"`
}

type toolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Non-streaming response.
type chatResp struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []respChoice `json:"choices"`
	Usage   usage        `json:"usage"`
}

type respChoice struct {
	Index        int     `json:"index"`
	Message      message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Streaming chunk.
type chatChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *usage        `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int     `json:"index"`
	Delta        message `json:"delta"`
	FinishReason *string `json:"finish_reason,omitempty"`
}

// ---- Scenario table ----

// Scenario describes the two-turn behavior that proves the full agent loop:
// turn 1 emits a tool call that mutates the workspace; turn 2 (after the tool
// result comes back) emits a final assistant message.
type Scenario struct {
	// ToolName is the jcode tool to invoke on turn 1. jcode exposes (among
	// others): write_file, edit_file, execute (bash), read_file, grep.
	ToolName string
	// ToolArgs is the JSON arguments object for that tool call.
	ToolArgs string
	// FinalText is the assistant message returned on turn 2 (finish_reason=stop).
	FinalText string
}

// scenarios is the extension point: add entries here to script new proofs.
// Argument shapes mirror jcode's tool schemas (write_file{path,content};
// execute{command}).
// NOTE: tool names and argument shapes MUST match jcode's real tool schemas
// (internal/tools/): the write tool is named "write" with args {file_path,
// content}; the shell tool is "execute" with args {command}. A relative
// file_path is resolved against the session working directory (/workspace).
var scenarios = map[string]Scenario{
	// Default: write a brand-new file into the workspace via the "write" tool.
	"write_file": {
		ToolName:  "write",
		ToolArgs:  `{"file_path":"HELLO_FROM_JCODE.txt","content":"jcode ran headless in a container and wrote this file.\n"}`,
		FinalText: "Done. I created HELLO_FROM_JCODE.txt in the repository root.",
	},
	// Alternative: mutate the tree via a bash command (execute tool). Useful to
	// prove the shell path also works headless.
	"bash_write": {
		ToolName:  "execute",
		ToolArgs:  `{"command":"printf 'written by jcode via bash\\n' > HELLO_FROM_BASH.txt","description":"create a file"}`,
		FinalText: "Done. I created HELLO_FROM_BASH.txt using a shell command.",
	},
	// Review: the M3 review channel. Turn 1 writes a fixed, reasonable review to
	// REVIEW.md (conclusion + bulleted findings, markdown); turn 2 finishes. This
	// scenario is selected automatically when a request's messages contain the
	// "[review]" marker (see scenarioForRequest), regardless of MOCK_SCENARIO.
	"review": {
		ToolName:  "write",
		ToolArgs:  `{"file_path":"REVIEW.md","content":"needs-work\n\n- The change is missing test coverage for the new branch.\n- Consider handling the empty-input edge case explicitly.\n- Overall the diff is focused and the naming is clear.\n"}`,
		FinalText: "Review complete. I wrote my findings to REVIEW.md.",
	},
}

func activeScenario() (string, Scenario) {
	name := os.Getenv("MOCK_SCENARIO")
	if name == "" {
		name = "write_file"
	}
	sc, ok := scenarios[name]
	if !ok {
		log.Printf("[mockllm] unknown MOCK_SCENARIO=%q, falling back to write_file", name)
		name = "write_file"
		sc = scenarios["write_file"]
	}
	return name, sc
}

// hasToolResult reports whether the conversation already contains a tool result,
// which means the agent has executed our turn-1 tool call and fed the result
// back — i.e. we are now on turn 2 and should finish.
func hasToolResult(msgs []message) bool {
	for _, m := range msgs {
		if m.Role == "tool" {
			return true
		}
	}
	return false
}

// reviewMarker is the literal token the entrypoint embeds in a review prompt so
// the mock (and any real prompt routing) can identify a review turn.
const reviewMarker = "[review]"

// messageText extracts the textual content of a message. OpenAI content is
// either a plain string or an array of {type,text} parts; we read both.
func messageText(m message) string {
	switch c := m.Content.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, part := range c {
			if pm, ok := part.(map[string]any); ok {
				if t, ok := pm["text"].(string); ok {
					b.WriteString(t)
					b.WriteByte(' ')
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

// scenarioForRequest chooses the scenario for a request: the "review" scenario
// when any message carries the review marker, otherwise the env-selected default.
// The write_file scenario personalises the file content with the request's task
// text so DIFFERENT prompts produce DIFFERENT diffs — without this, a second
// run on a branch that already has HELLO_FROM_JCODE.txt is a no-op and fails
// with empty_diff (hit live by the @jcode update-push flow, M7).
func scenarioForRequest(msgs []message) (string, Scenario) {
	for _, m := range msgs {
		if strings.Contains(messageText(m), reviewMarker) {
			return "review", scenarios["review"]
		}
	}
	name, sc := activeScenario()
	if name == "write_file" {
		if fp, excerpt := lastUserFingerprint(msgs); fp != "" {
			// A per-prompt FILENAME (not just content): jcode's write tool
			// refuses to overwrite an existing file it hasn't read, so a fixed
			// path silently no-ops on any branch that already carries the file
			// (M7 live find — @jcode update runs always produced empty diffs).
			args, _ := json.Marshal(map[string]string{
				"file_path": "JCODE_TASK_" + fp + ".txt",
				"content": "jcode ran headless in a container and wrote this file.\n" +
					"Task: " + excerpt + "\n",
			})
			sc.ToolArgs = string(args)
		}
	}
	return name, sc
}

// lastUserFingerprint hashes the ENTIRE last user message (agent prompts share
// a fixed template preamble, so any single line can be identical across runs —
// only the full text is guaranteed distinct) and returns a short fingerprint
// plus a one-line excerpt for readability.
func lastUserFingerprint(msgs []message) (fp, excerpt string) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		full := messageText(msgs[i])
		if strings.TrimSpace(full) == "" {
			return "", ""
		}
		sum := sha256.Sum256([]byte(full))
		fp = hex.EncodeToString(sum[:])[:12]
		excerpt = strings.Join(strings.Fields(full), " ")
		if len(excerpt) > 100 {
			excerpt = excerpt[:100]
		}
		return fp, excerpt
	}
	return "", ""
}

func main() {
	addr := os.Getenv("MOCK_ADDR")
	if addr == "" {
		addr = ":8081"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", handleModels)
	mux.HandleFunc("/v1/chat/completions", handleChat)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	name, _ := activeScenario()
	log.Printf("[mockllm] listening on %s, scenario=%s", addr, name)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("[mockllm] server error: %v", err)
	}
}

func handleModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"id": "mock-model", "object": "model", "owned_by": "mockllm"},
		},
	})
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	name, sc := scenarioForRequest(req.Messages)
	turn2 := hasToolResult(req.Messages)
	log.Printf("[mockllm] scenario=%s stream=%v msgs=%d turn2=%v", name, req.Stream, len(req.Messages), turn2)

	model := req.Model
	if model == "" {
		model = "mock-model"
	}

	if req.Stream {
		streamResponse(w, model, sc, turn2)
		return
	}
	jsonResponse(w, model, sc, turn2)
}

// jsonResponse handles the non-streaming (Generate) path.
func jsonResponse(w http.ResponseWriter, model string, sc Scenario, turn2 bool) {
	var choice respChoice
	if turn2 {
		choice = respChoice{
			Index:        0,
			Message:      message{Role: "assistant", Content: sc.FinalText},
			FinishReason: "stop",
		}
	} else {
		idx := 0
		choice = respChoice{
			Index: 0,
			Message: message{
				Role: "assistant",
				ToolCalls: []toolCall{{
					ID:       "call_mock_1",
					Type:     "function",
					Index:    &idx,
					Function: toolCallFunc{Name: sc.ToolName, Arguments: sc.ToolArgs},
				}},
			},
			FinishReason: "tool_calls",
		}
	}
	writeJSON(w, http.StatusOK, chatResp{
		ID:      "chatcmpl-mock",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []respChoice{choice},
		Usage:   usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	})
}

// streamResponse handles the SSE (Stream) path. It emits, in order:
//   - a role chunk (assistant),
//   - either tool-call delta chunks (turn 1) or content chunks (turn 2),
//   - a finish_reason chunk,
//   - a final usage-only chunk (stream_options.include_usage),
//   - the [DONE] sentinel.
func streamResponse(w http.ResponseWriter, model string, sc Scenario, turn2 bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	send := func(c chatChunk) {
		b, _ := json.Marshal(c)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	base := func() chatChunk {
		return chatChunk{
			ID:      "chatcmpl-mock",
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   model,
		}
	}

	// 1. role chunk
	c := base()
	c.Choices = []chunkChoice{{Index: 0, Delta: message{Role: "assistant"}}}
	send(c)

	if turn2 {
		// content chunk(s)
		c = base()
		c.Choices = []chunkChoice{{Index: 0, Delta: message{Content: sc.FinalText}}}
		send(c)
		fr := "stop"
		c = base()
		c.Choices = []chunkChoice{{Index: 0, Delta: message{}, FinishReason: &fr}}
		send(c)
	} else {
		// tool-call delta: name+args delivered in one delta (go-openai
		// accumulates by index; a single complete delta is valid).
		idx := 0
		c = base()
		c.Choices = []chunkChoice{{Index: 0, Delta: message{
			ToolCalls: []toolCall{{
				ID:       "call_mock_1",
				Type:     "function",
				Index:    &idx,
				Function: toolCallFunc{Name: sc.ToolName, Arguments: sc.ToolArgs},
			}},
		}}}
		send(c)
		fr := "tool_calls"
		c = base()
		c.Choices = []chunkChoice{{Index: 0, Delta: message{}, FinishReason: &fr}}
		send(c)
	}

	// final usage-only chunk (include_usage). Empty choices is valid.
	c = base()
	c.Choices = []chunkChoice{}
	c.Usage = &usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	send(c)

	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
