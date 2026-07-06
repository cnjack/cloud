# jcode headless runner (P0)

**Status: PROVEN.** `jcode` runs fully headless (no TTY) inside a Linux
container, clones a repo, executes a coding task through the complete agent
loop (LLM → tool call → tool execution → LLM → done), and emits a `git diff`.

This directory retires the #1 risk of the Cloud Agent initiative: that jcode
could not run unattended without a terminal.

---

## What was proven

Running `./test.sh` builds the image and, inside a container started with **no
TTY** (no `docker run -t`):

1. clones a throwaway git repo into `/workspace` (via `file://` from a
   read-only mount);
2. drives one headless jcode turn against a mock OpenAI-compatible model;
3. the agent makes a tool call (`write` or `execute`), the tool **actually runs
   against the workspace**, the result is fed back, and the agent finishes;
4. the resulting `git diff` is printed to stdout and written to
   `/out/diff.patch`.

All assertions pass: exit 0, non-empty diff containing the scripted change, diff
present on stdout, **no TUI/ANSI escape codes** in the logs, and **both** agent
turns confirmed to have reached the mock (i.e. the full loop ran, not just a
single model call).

---

## Chosen headless mechanism: ACP (and why)

jcode exposes three surfaces that don't require a TTY. We drive **`jcode acp`**
— the Agent Client Protocol, JSON-RPC over stdio (`internal/command/acp.go`).

A tiny Go client, [`acpdrive`](./acpdrive/main.go), launches `jcode acp` as a
subprocess over stdio pipes and performs:

```
initialize → session/new(cwd=/workspace) → session/prompt(TASK_PROMPT)
```

`conn.Prompt(...)` **blocks until the agent loop completes** and returns a
`StopReason`, so the run is synchronous and deterministic.

**Why ACP over the alternatives:**

| Surface | Verdict |
|---|---|
| bare `jcode` / `jcode -p` | ❌ calls `command.RunInteractive` → boots the BubbleTea TUI → hangs/crashes without a TTY. Never used. |
| `jcode web` REST+WS server (`runAutomation`, forced `full_access`, interactive tools dropped) | ✅ genuinely headless and purpose-built for unattended runs — but needs an HTTP port + bearer-token auth, the SPA-stripped build, and **async** polling of `GET /api/automations/{id}` until `LastStatus != running`. More moving parts. |
| `jcode automation` CLI | ❌ only manages definitions in `~/.jcode/automations.json`; actual firing is owned by a running `jcode web` process. Can't execute a task on its own. |
| **`jcode acp`** | ✅ **chosen** — pure stdio, no port, no auth, no SPA, no polling. `Prompt` is synchronous. The ACP client SDK is the same `github.com/coder/acp-go-sdk` jcode embeds, so protocol compatibility is guaranteed. |

**The critical enabler:** the config sets `default_mode: "full_access"`. That
puts jcode's `ApprovalState` into auto mode, where `RequestApproval` returns
`true` immediately (`internal/runner/approval.go:373`) and **never issues a
reverse-direction permission RPC** to the client. Without this, an unattended
run would hang forever waiting for a human to approve the first tool call. Memory
distillation is also disabled (`"memory": {"enabled": false}`) so no background
model calls fire.

`acpdrive`'s reverse-direction methods (`RequestPermission`, fs read/write,
terminal) are implemented as safe fallbacks and are **not** expected to fire in
this configuration; `RequestPermission` auto-allows as a belt-and-suspenders
guard so nothing can stall.

---

## Layout

```
runner/
├── Dockerfile        # debian-slim + git + ca-certs + prebuilt binaries
├── entrypoint.sh     # clone → write config → drive ACP run → emit diff
├── build.sh          # host cross-compile jcode+acpdrive+mockllm → bin/, docker build
├── test.sh           # full local proof (build, sidecar mock, no-TTY run, assert)
├── acpdrive/         # minimal headless ACP client (Go)
├── mockllm/          # table-driven mock OpenAI-compatible server (Go)
├── patches/          # empty — NO patch to jcode was required
└── bin/              # build output (gitignored)
```

## Run it

```bash
cd cloud/runner
./test.sh            # builds everything and runs the full proof
```

Prerequisites on the build host: Docker (tested with v29), Go (tested 1.26),
and the jcode source checkout at `../../jcode`. On Apple Silicon the image is
built for `linux/arm64`, which OrbStack runs natively.

### Running the container directly

Self-contained (bundled mock, no sidecar):

```bash
docker run --rm \
  -v /path/to/seed:/seed:ro -v "$PWD/out:/out" \
  -e REPO_URL=file:///seed \
  -e TASK_PROMPT="Create a file HELLO.txt in the repo root." \
  -e START_MOCKLLM=1 \
  -e MODEL_API_KEY=dummy -e MODEL_NAME=mock/mock-model \
  jcode-runner:local
```

Against a real (or sidecar) model — point `MODEL_BASE_URL` at any
OpenAI-compatible endpoint:

```bash
docker run --rm \
  -e REPO_URL=https://github.com/you/repo.git \
  -e TASK_PROMPT="..." \
  -e MODEL_BASE_URL=https://api.example.com/v1 \
  -e MODEL_API_KEY=$KEY -e MODEL_NAME=openai/gpt-4o \
  jcode-runner:local
```

**entrypoint.sh env:** `REPO_URL`, `TASK_PROMPT`, `MODEL_BASE_URL`,
`MODEL_API_KEY` (required); `RUN_ID`, `MODEL_NAME` (default `mock/mock-model`),
`MODEL_PROVIDER`, `RUN_TIMEOUT` (default `300s`), `START_MOCKLLM`,
`MOCK_SCENARIO` (optional).

---

## The mock model (`mockllm/`)

A single-file Go server speaking the subset of `/v1/chat/completions` that
jcode's model layer (`sashabaranov/go-openai` via eino) actually uses:

- `stream: true` → **SSE** chunks (`choices[].delta{.content,.tool_calls}`,
  final usage-only chunk for `stream_options.include_usage`, then `[DONE]`) —
  this is jcode's primary path (`chatModel.Stream`);
- `stream: false` → single JSON body (`chatModel.Generate`, supported
  defensively);
- `GET /v1/models` → minimal catalog.

It is **table-driven and stateless**. The "turn" is derived from the request:
if the incoming messages already contain a `role: "tool"` result, it's turn 2 →
return a plain assistant message with `finish_reason: stop`; otherwise turn 1 →
return a tool call. Scenarios live in the `scenarios` map:

| `MOCK_SCENARIO` | turn-1 tool | proves |
|---|---|---|
| `write_file` (default) | `write` → `HELLO_FROM_JCODE.txt` | file-write path |
| `bash_write` | `execute` → shell writes `HELLO_FROM_BASH.txt` | shell path |

> Tool names/args match jcode's real schemas: `write{file_path,content}`,
> `execute{command}` (not `write_file{path,...}` — verified against
> `internal/tools/`). Add a map entry to script new proofs.

---

## Build strategy & tradeoff

The image **COPYies prebuilt static binaries** compiled on the host
(`build.sh`) rather than compiling jcode from source in a multi-stage image.

- **Why:** jcode's module graph is large; a clean in-image `go build` needs
  network access and re-downloads the whole graph on every cache miss. The host
  has a warm module cache, so host-compile is faster and hermetic against
  registry hiccups. The final image carries no Go toolchain and no source.
- **Tradeoff:** the build host needs Go + the jcode checkout. For a
  self-contained CI image, swap in a `FROM golang:1.26 AS build` stage that
  `COPY`s the jcode source and runs
  `GOOS=linux CGO_ENABLED=0 go build -tags jcode_headless ./cmd/jcode`. The
  build flags are proven to work (static aarch64 ELF, ~17s cold compile).

The binary is built with `-tags jcode_headless` (strips the embedded SPA — see
the Makefile `desktop-sidecar` target) and `CGO_ENABLED=0` (static; Linux BLE is
pure-Go so no C toolchain needed).

---

## Timings (Apple Silicon / OrbStack, linux/arm64)

| Step | Cold | Warm |
|---|---|---|
| Compile jcode (headless, static) | ~17s | ~1.4s |
| Compile acpdrive + mockllm | ~2s | <1s |
| `docker build` (binaries present) | ~3s | <1s (cached) |
| **Container run** (clone + 2-turn agent loop + diff) | **~0.5s** | ~0.5s |
| **Full `test.sh` end-to-end** | **~20–25s** | **~10s** |

The agent run itself is sub-second against the local mock; cold time is
dominated by the one-time Go compile.

---

## Deferred (not in P0)

- **Real model.** Swap `MODEL_BASE_URL`/`MODEL_API_KEY`/`MODEL_NAME` for any
  OpenAI-compatible provider. No code change — `entrypoint.sh` already
  parameterizes it. (Providers not in jcode's registry need the
  `custom_models` entry the config already writes.)
- **Language toolchains** (go/java/python/node). Deliberately omitted to keep
  the image minimal. Extension point: add `apt-get install` / language installs
  to the Dockerfile, or layer task-specific images `FROM jcode-runner`.
- **Persistent volume (PVC)** for `/workspace` and the output — currently a
  bind mount / ephemeral dir.
- **Event streaming.** ACP already emits `session/update` notifications
  (agent message chunks, tool-call status) that `acpdrive` currently logs to
  stderr; wiring these to a real event bus is the integration step.
- **Multi-turn / resumable sessions**, auth, secret management, and
  cancellation semantics beyond the single-run timeout.

---

## Top gotchas for the integration phase (task #5)

1. **`default_mode: "full_access"` is mandatory.** Any other mode makes the
   agent request tool-call approval via a reverse ACP RPC; an unattended run
   then hangs on the first tool call. This is the single most important config
   line.
2. **Tool names are jcode-specific, not OpenAI-generic.** The write tool is
   `write` with `{file_path, content}` and the shell tool is `execute` with
   `{command}`. A real model's system prompt already knows these (jcode injects
   them), but anything scripting/asserting tool calls must use the real names —
   `write_file`/`path` will fail with `tool ... not found in toolsNode`.
3. **jcode logs to `$HOME/.jcode/debug.log`, and in ACP mode stdout is the
   JSON-RPC channel.** Don't parse stdout for human logs; do redirect/scrape
   `debug.log` for agent-loop diagnostics. Keep `$HOME` writable and
   per-run-isolated so configs/sessions/logs don't collide across concurrent
   runs.
4. **Providers unknown to jcode's registry need a `custom_models` entry** (id +
   `tool_call: true` + `context`) or the model won't be selectable. The
   entrypoint writes this automatically from `MODEL_NAME`; replicate it for real
   providers that aren't in `models.dev`.
5. **Diff capture depends on `git add -N`.** New files created by the agent are
   untracked, so a plain `git diff` misses them; the entrypoint stages intents
   first. Also set a git identity in the container (done) or git commands warn/
   fail. For real repos, decide the diff baseline explicitly (HEAD at clone vs.
   a branch) rather than relying on working-tree state.
```
