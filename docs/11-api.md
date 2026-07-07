# 11 · API 契约 (orchestrator)

> 本文件是 **控制面 orchestrator** 的 HTTP/SSE 契约,是 React 控制台、CLI、runner
> 三个并行 agent 的**唯一对接来源**。以本文件为准,逐字实现。
>
> 实现:`cloud/orchestrator/`(Go module `github.com/cnjack/jcloud`)。
> 范围锁定见 [10-prd.md](10-prd.md);状态机语义源自 Symphony,见
> [05-symphony-and-references.md](05-symphony-and-references.md)。
>
> _最后更新:2026-07-07(runner↔orchestrator 事件流水线接线:服务端 seq 分配、
> MODEL_NAME、SSE access_token)_

---

## 0 · 通用约定

- **Base path**:`/api/v1`(console/CLI 面);`/internal/v1`(runner 面)。
- **Content-Type**:请求与响应体均为 `application/json; charset=utf-8`,除
  SSE 流(`text/event-stream`)与 diff 下载(`text/plain`)。
- **时间**:所有时间戳为 RFC3339 / ISO-8601 UTC(如 `2026-07-07T12:34:56Z`)。
- **ID**:`project.id` / `run.id` 为 32 位十六进制字符串(不透明,勿解析)。
- **未知字段**:请求体启用严格解码——**多余字段会 400**。请勿发送契约外字段。
- **错误信封**:所有非 2xx 响应形如:

  ```json
  { "error": { "code": "not_found", "message": "run not found" } }
  ```

  `code` 取值:`bad_request` · `unauthorized` · `not_found` · `conflict` ·
  `internal`。UI 应展示 `message`,按 `code` 决定交互(如 `conflict` 提示重试)。

### 0.1 认证

| 面 | Header | 校验 |
|---|---|---|
| Console / CLI (`/api/v1/*`) | `Authorization: Bearer <CONSOLE_TOKEN>` | 与环境变量 `CONSOLE_TOKEN` 常量时间比较 |
| Runner (`/internal/v1/*`) | `Authorization: Bearer <RUN_TOKEN>` | 与该 run 存储的 token 哈希常量时间比较;路径 `{id}` 必须匹配该 token 所属 run |
| `GET /healthz` | 无 | 公开 |

- 缺失/错误 token → `401 unauthorized`。
- 用 console token 打 `/internal/v1/*` → `401`(反之亦然)。
- 单租户 MVP:无 OIDC、无用户概念(见 PRD OUT-1)。

---

## 1 · 数据模型

### 1.1 Project

```json
{
  "id": "9f2c...",
  "name": "demo",
  "repo_url": "https://gitea.internal/acme/app.git",
  "default_branch": "main",
  "created_at": "2026-07-07T12:00:00Z",
  "git_mode": "readonly",
  "provider": "gitea",
  "provider_url": "http://gitea.jcloud.svc.cluster.local:3000",
  "provider_repo": "jcloud/seed"
}
```

- **`git_mode`** (ST-1; decision D08): `readonly` (default) | `draft_pr`.
  - `readonly` — today's behavior: a successful run ends in a **diff artifact
    only**. Nothing is pushed and no PR is opened. J1-J3 use this.
  - `draft_pr` — after a successful run with a **non-empty diff**, the runner
    pushes an `agent/run-<id>` branch and the orchestrator opens a **draft PR**
    on the provider. **Never auto-merges, never triggers CI** (hard gate).
- **`provider`** (`gitea` only in the MVP; decision D09), **`provider_url`**
  (Gitea base URL), **`provider_repo`** (`owner/name`): required together when
  `git_mode == draft_pr`; ignored/empty for `readonly`. If `git_mode == draft_pr`
  and `provider` is omitted it defaults to `gitea`. `provider_repo` is
  **required** for `draft_pr` (400 otherwise). The provider **token** is not a
  project field — it comes from the orchestrator env `GITEA_TOKEN` (single-tenant
  MVP), injected to the runner for the push and used by the orchestrator for the
  PR API.

### 1.2 Run

```json
{
  "id": "1a2b...",
  "project_id": "9f2c...",
  "prompt": "在 README 末尾加一行 Hello",
  "status": "running",
  "phase": "StreamingTurn",
  "error": "",
  "k8s_job_name": "jcloud-run-1a2b...",
  "retried_from": null,
  "failure_reason": "",
  "failure_message": "",
  "attempt": 1,
  "created_at": "2026-07-07T12:00:01Z",
  "started_at": "2026-07-07T12:00:09Z",
  "finished_at": null,
  "job_cleaned_at": null,
  "git_branch": "",
  "commit_sha": "",
  "pr_url": "",
  "pr_number": 0
}
```

- `status` ∈ 见 §1.3。`phase` 是人读的细节标签(源自 Symphony run 阶段),
  仅供展示,勿据其做逻辑判断——**逻辑一律看 `status`**。
- `retried_from`:若本 run 由 Retry 生成,指向原 run 的 id;否则 `null`。
  (⚠️ 字段名是 `retried_from`,不是 `retry_of`。)
- `failure_reason` / `failure_message`:仅当 `status == "failed"` 非空;
  `failure_message` 保证非空且人类可读(见 §1.4)。
- `started_at`:转入 `running` 时置。`finished_at`:进入任一终态时置。
- **`k8s_job_name` 供运维排障**;UI 可不展示。**该字段是 run 历史记录的一部分,
  终态后也永不清空**(审计 + e2e 按名核对各 run 独立 worker Job)。
- `job_cleaned_at`(可空,省略即 `null`):reconciler 确认该终态 run 的 Job 已
  从集群删除(回收孤儿 Job)后打的时间戳;`k8s_job_name` 保持不变。仅供审计/
  排障,console 无需展示或依赖。(迁移 `0003_job_cleaned_at`;新增可空字段,
  非破坏性变更。)
- **`git_branch` / `commit_sha` / `pr_url` / `pr_number`(ST-1,迁移
  `0004_gitea_draft_pr`;新增字段,非破坏性)**:仅 `draft_pr` 模式 run 会填充。
  - `git_branch` / `commit_sha`:runner 推送 `agent/run-<id>` 分支后经
    **`run.git` 事件**上报(见 §4)。`git_branch` 是 orchestrator 开 PR 时的
    **幂等键**(先按 head 分支查已存在 PR 再创建)。
  - `pr_url` / `pr_number`:orchestrator 开(或找到)draft PR 后写入;`pr_number`
    为 `0` 表示尚无 PR。console 有 `pr_url` 时展示「Draft PR #N ↗」链接徽章。
  - `readonly`(默认)模式这四个字段恒为空/`0`——**与 J1-J3 行为一致,不受影响**。
- 服务器**从不**把 run token 序列化给 console 客户端。

### 1.3 Run 状态徽章体系(单一事实源)

| status | 语义 | 终态? | 本期可达 | 建议色 |
|---|---|---|---|---|
| `queued` | 已创建,等待调度 worker | 否 | ✅ | 灰 |
| `scheduling` | 已创建 K8s Job,尚未观察到 pod 运行 | 否 | ✅ | 蓝(脉冲) |
| `running` | worker pod 活跃,agent 连跑中 | 否 | ✅ | 蓝(动效) |
| `succeeded` | 正常结束,diff 产物就绪 | ✅ | ✅ | 绿 |
| `failed` | clone/setup/agent/timeout 失败,含可读原因 | ✅ | ✅ | 红 |
| `canceled` | 操作者取消 | ✅ | ✅ | 灰 |
| `blocked` | 需人工输入(Symphony 一等公民)。**本期建模+展示,`full_access` runner 不产生** | 否 | ⚠️ | 黄 |

> 与 PRD §6 徽章表一致。PRD 用户旅程只提 `queued→running→succeeded/failed`;
> `scheduling` 是 `queued` 与 `running` 之间的可见细分态(调度中),UI 可与
> `running` 同色处理,也可单独展示。`canceled` 服务于取消端点。

### 1.4 failure_reason 枚举

| 值 | 含义 | 谁来定 |
|---|---|---|
| `clone_failed` | 仓库 clone 失败 | runner 报 `run.failure` 事件精化 |
| `setup_failed` | 项目 setup 阶段失败 | runner 报 `run.failure` 事件精化 |
| `agent_error` | agent 报错 / 通用 Job 失败(**兜底**) | 集群状态推断,或 runner 报 |
| `timeout` | 超过 `activeDeadlineSeconds` 墙钟上限 | orchestrator 从 Job DeadlineExceeded 推断 |
| `push_failed` | **(ST-1)** `draft_pr` 模式下已产出 diff,但推送 `agent/run-<id>` 分支到 provider 失败(token/网络/受保护分支) | runner 报 `run.failure` 事件 |

**归类规则**:orchestrator 从 K8s Job 状态推断——Job 失败 → `agent_error`;
Job DeadlineExceeded → `timeout`。仅凭集群状态**无法**区分 clone/setup,故 runner
可主动 POST 一个 `run.failure` 事件(`{reason,message}`)来**精化**;若 runner 已上报,
orchestrator 的兜底分类**不覆盖**它。

### 1.5 RunEvent

```json
{ "seq": 7, "ts": "2026-07-07T12:00:10Z", "type": "agent.tool_call", "payload": { "tool": "edit", "args": { "path": "README.md" } } }
```

- `seq`:该 run 内单调递增、从 1 起、唯一,**由服务端权威分配**(见 §5.1)。
  `(run_id, seq)` 唯一。runner 上报时携带的 `seq` 仅作按来源的幂等键,非最终 `seq`。
- `type` 取值见 §4 事件类型表。`payload` 是与 type 对应的自由 JSON 对象。

### 1.6 RunArtifact

```json
{ "run_id": "1a2b...", "kind": "diff", "content": "--- a/README.md\n+++ b/README.md\n@@ ...", "created_at": "2026-07-07T12:01:00Z" }
```

- 本期 `kind` 仅 `diff`。`content` 为完整 unified diff 文本。

---

## 2 · Console / CLI 端点(`/api/v1`)

所有端点要求 `Authorization: Bearer <CONSOLE_TOKEN>`。

### 2.1 Projects

#### `POST /api/v1/projects` — 创建 project

请求:

```json
{
  "name": "demo",
  "repo_url": "https://gitea.internal/acme/app.git",
  "default_branch": "main",
  "git_mode": "draft_pr",
  "provider": "gitea",
  "provider_url": "http://gitea.jcloud.svc.cluster.local:3000",
  "provider_repo": "jcloud/seed"
}
```

- `name`(必填)、`repo_url`(必填);`default_branch` 缺省 `main`。
- **(ST-1)** `git_mode` 缺省 `readonly`。取 `draft_pr` 时:`provider` 缺省
  `gitea`(只支持 `gitea`,否则 `400`),`provider_repo`(`owner/name`)**必填**
  (否则 `400`);`provider_url` 可选(缺省用 orchestrator 的 `GITEA_URL`)。
  `readonly` 时后三者可省略。`PATCH` 同样接受这些字段(只更新提供的字段)。

响应 `201 Created`:完整 Project 对象(见 §1.1)。
错误:`400`(缺 name/repo_url;`git_mode` 非法;`draft_pr` 缺 `provider_repo`
或 `provider` 非 gitea)。

#### `GET /api/v1/projects` — 列出 projects

响应 `200`:

```json
{ "projects": [ { "id": "...", "name": "demo", "...": "..." } ] }
```

空态返回 `{ "projects": [] }`。

#### `GET /api/v1/projects/{id}` — 取单个 project

响应 `200`:Project 对象。错误:`404`。

#### `PATCH /api/v1/projects/{id}` — 更新 project

请求(全部可选,仅提供的字段被更新):

```json
{ "name": "demo2", "repo_url": "https://...", "default_branch": "dev" }
```

响应 `200`:更新后的 Project。错误:`404`。

#### `DELETE /api/v1/projects/{id}` — 删除 project

响应 `204 No Content`(级联删除其 runs/events/artifacts)。错误:`404`。

### 2.2 Runs

#### `POST /api/v1/projects/{id}/runs` — 创建并入队 run

请求:

```json
{ "prompt": "在 README 末尾加一行 Hello" }
```

- `prompt`(必填,非空白)。

响应 `201 Created`:完整 Run 对象,`status` = `queued`。
错误:`400`(空 prompt)、`404`(project 不存在)。

> 创建即入队;reconciler 下一 tick(默认 3s 内)按并发上限起 K8s Job。

#### `GET /api/v1/projects/{id}/runs` — 列出某 project 的 runs

#### `GET /api/v1/runs` — 列出所有 runs

查询参数:`limit`(默认 100,上限 500)。
响应 `200`:

```json
{ "runs": [ { "id": "...", "status": "running", "...": "..." } ] }
```

按 `created_at` 降序。空态 `{ "runs": [] }`。

#### `GET /api/v1/runs/{id}` — 取单个 run

响应 `200`:Run 对象。错误:`404`。刷新/回放时读此端点取终态与元数据。

#### `POST /api/v1/runs/{id}/cancel` — 取消 run

- 删除 K8s Job(best-effort),标记 `status = canceled`,置 `finished_at`。
- 响应 `200`:更新后的 Run。
- 错误:`404`;`409 conflict`(run 已在终态)。

#### `POST /api/v1/runs/{id}/retry` — 重试 run

- **生成一条新 run**(同 project、同 prompt),`status = queued`,
  `retried_from` = 原 run id,`attempt` = 原 + 1。
- **只有终态 run 可 retry**。
- 响应 `201 Created`:**新** Run 对象(`id` ≠ 原)。
- 错误:`404`;`409 conflict`(run 未结束)。

> **与 Symphony 的分歧**:Symphony 在同一 claim 上 `RetryQueued→Running` 原地重试;
> 本系统以 Job-per-run 模型 + REST 触发,retry = 新 run + `retried_from` 链接,
> 更易推理。Symphony 退避公式 `min(10000·2^(n-1), 300000)ms` 已实现并随
> `attempt` 携带,供未来**自动**重试;MVP 为**手动**重试,不强制退避。

### 2.3 Events(拉取 + 流式)

#### `GET /api/v1/runs/{id}/events` — 拉取事件(增量)

查询参数:`after_seq`(默认 0,只返回 `seq > after_seq`)、`limit`(默认 1000)。
响应 `200`:

```json
{ "events": [ { "seq": 1, "ts": "...", "type": "run.status", "payload": { "status": "queued", "phase": "Queued" } } ] }
```

用途:非流式轮询,或流断线后补齐。

#### `GET /api/v1/runs/{id}/stream` — SSE 实时流(replay-then-live)

查询参数:`after_seq`(默认 0)、`access_token`(可选,见下)。

- **认证(仅本端点)**:除标准 `Authorization: Bearer <CONSOLE_TOKEN>` 外,
  本端点**额外**接受 `?access_token=<CONSOLE_TOKEN>` 查询参数(常量时间比较,
  与 header 等价)。原因:浏览器原生 `EventSource` **无法**设置自定义 header,
  故控制台以查询参数携带 token。**仅**此只读流端点开放该方式;所有写端点仍
  只认 header。二者择一即可;都提供时以 header 优先。
- **响应头**:`Content-Type: text/event-stream`。
- **语义**:先**回放** `seq > after_seq` 的持久化事件,**再切到实时**。订阅在
  回放前建立,保证无缝、无漏、无重(`seq` 单调,重复自动去重)。
- **断线重连**:客户端记住最后见到的 `seq`,重连时带 `after_seq=<lastSeq>`,
  从断点续流(见 §3 示例)。
- **心跳**:每 15s 一行 SSE 注释(`: heartbeat`),防中间层断闲连接。
- **结束**:run 进入终态后,流补发终态 `run.status` 事件,再发一行注释
  `: run terminal; stream complete`,随后**服务器关闭连接**。客户端据此停止重连。
- **优雅停机**:orchestrator 收到 SIGTERM 优雅停机时,会给每个在连的流补发一行
  注释 `: server shutting down` 并关闭连接(非终态)。这是一条普通 SSE 注释,
  客户端按「注释行忽略」处理即可;若 run 尚未终态,客户端应带 `after_seq=<lastSeq>`
  重连以续流。契约无破坏性变更(仍是 replay-then-live、`seq` 单调、注释行可忽略)。

**SSE 帧格式**(每帧三行 + 空行):

```
event: agent.tool_call
id: 7
data: {"seq":7,"ts":"2026-07-07T12:00:10Z","type":"agent.tool_call","payload":{"tool":"edit"}}

```

- `event:` = 事件 type(供 `EventSource.addEventListener(type, ...)`)。
- `id:` = `seq`(浏览器 `EventSource` 会自动带 `Last-Event-ID` 头重连;本服务
  同时支持 `after_seq` 查询参数,二者择一即可)。
- `data:` = 单行 JSON,字段 `{seq, ts, type, payload}`。
- 注释行以 `:` 开头,客户端应忽略。

### 2.4 Artifact

#### `GET /api/v1/runs/{id}/artifact` — 取产物

查询参数:`kind`(默认 `diff`)、`download`(`1` 时返回原文下载)。

- 默认(JSON)响应 `200`:RunArtifact 对象(见 §1.6)。
- `?download=1`:响应 `200`,`Content-Type: text/plain`,
  `Content-Disposition: attachment; filename="<run-id>.diff"`,body 为原始 diff。
- 错误:`404`(产物尚不存在)。

### 2.5 System / admin

#### `GET /api/v1/system` — 集群管理只读快照

集群管理员(cluster-admin)视图的数据源:一次性返回容量、护栏、provider、
runner、版本等运行时快照。**只读**,console bearer token 鉴权(同其它
`/api/v1` 端点)。

**安全不变量:永不返回任何 secret** —— 不含 `GITEA_TOKEN`、`CONSOLE_TOKEN`、
`DATABASE_URL`(DSN)或任何令牌。`provider.gitea_enabled` 是从 `GITEA_TOKEN`
是否非空派生出的**布尔信号**,token 本身绝不序列化。

响应 `200`:

```json
{
  "version":   { "version": "1.4.0", "commit": "abc1234" },
  "capacity":  {
    "max_concurrent_runs": 4,   // MAX_CONCURRENT_RUNS(0 = 无限)
    "running": 1,               // 跨所有 project 的 running 计数
    "queued": 2,                // 跨所有 project 的 queued 计数
    "scheduling": 1             // 跨所有 project 的 scheduling 计数
  },
  "guardrails": {
    "run_timeout_seconds": 1800,  // RUN_TIMEOUT_SECONDS(Job activeDeadlineSeconds)
    "job_ttl_seconds": 3600       // JOB_TTL_SECONDS(完成后 Job 回收)
  },
  "provider":  {
    "gitea_enabled": true,        // = GITEA_TOKEN 非空(token 不返回)
    "gitea_url": "http://gitea.jcloud.svc.cluster.local:3000"
  },
  "runner":    { "image": "ghcr.io/acme/runner:v1" },  // RUNNER_IMAGE
  "namespace": "jcloud",         // K8S_NAMESPACE
  "launcher":  "kubernetes"      // JOB_LAUNCHER:kubernetes | process | disabled(DISABLE_K8S)
}
```

- 容量计数由 store 单次查询(`CountRunsByStatus`,只读、跨所有 project)。
- 错误:`401`(缺/错 token)、`500`(容量查询失败)。
- 鉴权口径与运行时可见性映射到 console 的两种角色(见 console `VITE_ROLE`);
  当前为单租户 MVP,尚无 OIDC/RBAC,角色仅是**展示层**信号,并非服务端授权。

---

## 3 · 前端集成示例(SSE)

```js
// 详情页:先 GET run 拿终态元数据,再开 SSE 流从头回放并转直播。
const es = new EventSource(
  `/api/v1/runs/${runId}/stream?after_seq=0`,
  // EventSource 不支持自定义 header;若走浏览器原生 EventSource,
  // console token 需经同源 cookie / 反代注入。CLI/fetch 流则可直接带 Bearer。
);
let lastSeq = 0;
for (const type of ["run.status","agent.text","agent.tool_call","agent.tool_result","run.artifact","run.failure"]) {
  es.addEventListener(type, (e) => {
    const frame = JSON.parse(e.data);   // {seq, ts, type, payload}
    lastSeq = frame.seq;
    render(frame);
  });
}
// 服务器在终态后关闭连接;若需自动重连中间断线,用 after_seq=lastSeq 重开。
```

> 注:浏览器原生 `EventSource` 无法设自定义 Header。两条可行路径:
> (a) 用 `?access_token=<CONSOLE_TOKEN>` 查询参数(本 stream 端点专门支持,见
> §2.3 认证);(b) 用 `fetch()` + `ReadableStream` 手动解析 SSE 并带 Bearer;
> 或由同源反代注入 Authorization。控制台当前走 (a)。

---

## 4 · 事件类型分类(taxonomy)

runner 通过 `POST /internal/v1/runs/{id}/events` 上报;orchestrator 也会内生
`run.status` / `run.failure`。所有类型经同一 SSE 流下发。

| type | 方向 | payload 形状 | 说明 |
|---|---|---|---|
| `run.status` | orchestrator 内生 | `{ "status": "running", "phase": "StreamingTurn", "failure_reason": "", "failure_message": "", "pr_url": "", "pr_number": 0 }` | 每次状态转移发一条;`failure_*` 仅 failed 时含;**`pr_url`/`pr_number` 仅 draft PR 开好后(ST-1)含,orchestrator 会在开 PR 后补发一条带 `pr_url` 的 `run.status`,让在连的 console 无需重取即可显示链接** |
| `agent.text` | runner | `{ "text": "我先读一下 README" }` | agent 的自然语言输出增量 |
| `agent.tool_call` | runner | `{ "tool": "edit", "args": { "path": "README.md" }, "call_id": "c1" }` | agent 发起一次工具调用 |
| `agent.tool_result` | runner | `{ "call_id": "c1", "ok": true, "output": "...", "exit_code": 0 }` | 对应工具调用的结果(命令输出等) |
| `run.artifact` | orchestrator 内生 | `{ "kind": "diff", "bytes": 214 }` | 产物已就绪的信号(内容经 §2.4 取) |
| `run.failure` | runner(可选) | `{ "reason": "clone_failed", "message": "fatal: repository not found" }` | runner 主动精化失败原因(见 §1.4) |
| `run.git` | runner(**ST-1**,`draft_pr` 模式) | `{ "branch": "agent/run-<id>", "commit_sha": "abcd..." }` | runner 推送 `agent/run-<id>` 分支后上报;orchestrator 据此(以 `branch` 为幂等键)开 draft PR。首个非空 `branch` 生效(first-writer-wins),重发为幂等空操作 |

- `payload` 除上述约定键外可含额外键,客户端应容忍未知键。
- `agent.tool_call` 与 `agent.tool_result` 建议以 `call_id` 关联配对渲染。

---

## 5 · Runner 面(`/internal/v1`)

要求 `Authorization: Bearer <RUN_TOKEN>`;路径 `{id}` 必须为该 token 所属 run。

### 5.1 `POST /internal/v1/runs/{id}/events` — 批量上报事件

请求:

```json
{
  "events": [
    { "seq": 1, "type": "agent.text",      "payload": { "text": "clone 完成" } },
    { "seq": 2, "type": "agent.tool_call", "payload": { "tool": "edit", "args": { "path": "README.md" }, "call_id": "c1" } },
    { "seq": 3, "type": "agent.tool_result","payload": { "call_id": "c1", "ok": true } }
  ]
}
```

- 每个事件需 `seq > 0` 且 `type` 非空。
- **runner 的 `seq` 只是「按来源的幂等键」,不是最终落库/SSE 的 `seq`。**
  runner 从 1 单调自增即可(用于安全重发去重),**不需要**关心 orchestrator
  内生事件的 seq。
- **服务端分配 seq(修复了原 seq 冲突隐患)**:ingest 时,orchestrator 在一个
  事务内(对该 run 行加锁)为每条**新**事件分配全局单调递增的 `seq`
  (= 该 run 当前 `max(seq)+1`),并按 `(run_id, source='runner', client_seq)`
  去重。因此:
  - runner 事件与 orchestrator 内生事件(`run.status` / `run.artifact` /
    `run.failure`,`source='internal'`)**共享同一条单调 `seq` 序列但永不冲突**,
    不会再有事件被静默丢弃。
  - 重发同一批(相同 `client_seq`)是幂等空操作,不再消耗新的 `seq`。
- 对 console 的 SSE 契约不变:`seq` 仍是该 run 内从 1 起、单调递增、唯一的整数
  ——只是其**权威分配方从客户端移到了服务端**(见迁移 `0002_event_seq_alloc`)。
- 上报 `run.failure` 可精化失败分类(见 §1.4)。

响应 `200`:

```json
{ "accepted": 3 }
```

`accepted` = 本次**新插入**的事件数(按 `client_seq` 去重后)。注意返回的
`accepted` 与事件最终的 `seq` 无关;runner 无需据此推断 seq。

### 5.2 `POST /internal/v1/runs/{id}/artifact` — 上报产物

请求:

```json
{ "kind": "diff", "content": "--- a/README.md\n+++ b/README.md\n@@ ..." }
```

- `kind` 缺省 `diff`。按 `(run_id, kind)` upsert(重复上报覆盖)。

响应 `201 Created`:

```json
{ "kind": "diff", "bytes": 214 }
```

- 上报后 orchestrator 内生一条 `run.artifact` 事件推给 SSE 订阅者。

---

## 6 · Runner Job 环境变量(runner-integration agent 对接清单)

orchestrator 的 reconciler 为每个 run 起一个 K8s Job(`backoffLimit: 0`,
`restartPolicy: Never`,`activeDeadlineSeconds` = `RUN_TIMEOUT_SECONDS`,
`TTLSecondsAfterFinished` = `JOB_TTL_SECONDS`,label `jcloud.run-id=<run.id>`),
注入以下环境变量到 runner 容器:

| Env | 来源 | 说明 |
|---|---|---|
| `RUN_ID` | run.id | 本 run 唯一 id;上报事件/产物时用于路径 `{id}` |
| `TASK_PROMPT` | run.prompt | 任务描述,喂给 agent |
| `REPO_URL` | project.repo_url | 要 clone 的仓库 |
| `REPO_BRANCH` | project.default_branch | 基线分支(契约扩展项;runner 可用可忽略) |
| `MODEL_BASE_URL` | 环境 `MODEL_BASE_URL` | OpenAI 兼容 provider base URL |
| `MODEL_API_KEY` | 环境 `MODEL_API_KEY` | 模型 key(MVP 直注入;P3 换 LLM 代理 + temp token) |
| `MODEL_NAME` | 环境 `MODEL_NAME`(默认 `mock/mock-model`) | jcode 的 `provider/model` 标识;runner 据此为未知 provider 写 `custom_models` 配置项 |
| `ORCH_BASE_URL` | 环境 `ORCH_BASE_URL` | orchestrator 基址,runner 回传事件/产物用 |
| `RUN_TOKEN` | 每 run 随机生成 | Bearer,仅本 run 有效;打 `/internal/v1/runs/{RUN_ID}/*` |
| `GIT_MODE` | project.git_mode(缺 token/host 不匹配时降级) | **(ST-1)** `readonly`(默认,diff-only)\| `draft_pr`(推分支) |
| `GIT_BRANCH` | `agent/run-<RUN_ID>` | **(ST-1)** `draft_pr` 时要创建/推送的命名分支 |
| `GIT_PUSH_URL` | `provider_url`(或 `GITEA_URL`)+ `provider_repo` | **(ST-1)** https 推送 origin,如 `http://gitea.../owner/repo.git` |
| `GIT_TOKEN` | 环境 `GITEA_TOKEN` | **(F1/ST-1)** provider token,作为 https userinfo。用于 **(a) CLONE 私有仓库**(`readonly` **和** `draft_pr` 都注入——私有仓库要能被 clone 才能被 READ)**和 (b) `draft_pr` 推分支**。仅命令行传参,不落盘,**不打日志**(clone/push URL 与 git stderr 均脱敏后才输出) |
| `GIT_BASE_BRANCH` | project.default_branch | **(ST-1)** PR base 分支(信息性;PR base 实际由 orchestrator 设) |

> **(F1 · GIT_TOKEN 注入的 host-match 规则)** 当 orchestrator 配了 `GITEA_TOKEN`
> **且** `REPO_URL` 是 http(s) **且**其 host(host:port,大小写不敏感)与所配
> provider host(优先 `project.provider_url`,否则 orchestrator 的 `GITEA_URL`)
> **一致**时,注入 `GIT_TOKEN`——`readonly` 与 `draft_pr` **两种模式都注入**,这样
> 私有仓库在只读模式下也能被 clone 读取。**绝不**为 `file://`(或任何非 http(s))
> 仓库注入,也**绝不**在 `REPO_URL` host 与 provider host 不一致时注入——避免把
> Gitea token 泄露给无关的 git host。host 不匹配的 `draft_pr` 项目降级为纯 diff
> (`GIT_MODE=readonly`,不推分支)。`GIT_MODE` 语义不变:`readonly` 永不推送/开 PR。
>
> **(ST-1)** 仅当 project `git_mode=draft_pr`、配了 `GITEA_TOKEN` **且**上述 host
> 匹配时才额外注入 `GIT_BRANCH`/`GIT_PUSH_URL`/`GIT_BASE_BRANCH` 并置
> `GIT_MODE=draft_pr`。缺 token / 缺 provider 配置 / host 不匹配时
> `GIT_MODE=readonly`——**readonly 默认路径(公有仓库 / 集群内匿名 gitseed)完全不变**。
>
> **(F2 · clone/push 失败诊断)** `git clone`/`git push` 失败时,runner 把 git 的
> stderr **脱敏**(剥离 `token@` 与任何 userinfo)后取尾部若干字符,拼进
> `run.failure` 的 `reason=clone_failed`/`push_failed` 消息里,详情页因此能显示真实
> 原因(auth vs not-found vs network),而不再是一句无信息的 `git clone … failed`。

**契约要点(runner agent 必读)**:

1. 入口两段式:`SETUP`(有网:clone `REPO_URL`、装依赖)→ `AGENT`(headless
   `full_access` 跑 `TASK_PROMPT`)→ 出 diff。
2. 无 TTY 下干净退出:成功 `exit 0`;失败非 0(orchestrator 从 Job 状态判 `failed`)。
3. 回传:边跑边 `POST /internal/v1/runs/{RUN_ID}/events`(带 `RUN_TOKEN`);
   跑完 `POST /internal/v1/runs/{RUN_ID}/artifact` 交 diff。
4. 失败精化(可选但推荐):clone/setup 失败时先发一条 `run.failure` 事件带
   `reason ∈ {clone_failed, setup_failed}`,再让进程非 0 退出——这样详情页失败
   原因才能精确到 clone/setup,而非兜底的 `agent_error`。
5. `RUN_TOKEN` 只在本 run 的 Job env 里出现,orchestrator 只存其哈希;勿外泄。

**当前实现(已接线并端到端验证)**:

- **事件流水线**:`acpdrive` 把 jcode 的 ACP `session/update` 通知映射为事件——
  `AgentMessageChunk → agent.text`;`ToolCall(初始) → agent.tool_call{name,args,call_id}`;
  `ToolCallUpdate(终态 completed/failed) → agent.tool_result{call_id,output,is_error}`
  ——经**有缓冲、不阻塞 agent loop** 的发射器批量 POST(500ms 或 10 条触发一次
  flush;5xx/网络错误按退避重试;缓冲满时丢最旧并补一条 `agent.text` 丢弃计数)。
- **失败/产物上报**:`entrypoint.sh` 在非 0 退出前调 `orchclient report-failure`
  (clone_failed / setup_failed / agent_error);成功时 `orchclient upload-artifact`
  上传 diff(基线 = clone 时 HEAD,`git add -N .` 纳入未跟踪文件)。`orchclient`
  是一个仅依赖标准库的小工具(base 镜像无 curl/wget);当 `ORCH_BASE_URL/RUN_ID/
  RUN_TOKEN` 任一缺失时为安全空操作,保证 runner 可独立运行。

---

## 6a · Gitea draft-PR 闭环(ST-1)

> 决策 D08(默认 draft PR / 可退只读)+ D09(Gitea 优先)。**硬把关:永不自动
> merge、永不自动触发 CI。** 失败降级:任一步失败仍产出 diff 产物,不因缺 PR
> 出口而回退——除**推送失败**外(见下)。

**职责划分**:runner 拥有 checkout,负责**推分支**;orchestrator 拥有 provider
适配器,负责**幂等开 PR**。

1. **runner(推送)**:`git_mode=draft_pr` 时,一次成功且 diff 非空的 run,runner
   把改动提交到分支 `agent/run-<RUN_ID>` 并用 `GIT_TOKEN` 经 https 推到 `GIT_PUSH_URL`,
   然后上报 **`run.git` 事件** `{branch, commit_sha}`(见 §4)。
   - **推送失败**:runner 发 `run.failure{reason:push_failed, message}` 再非 0 退出。
     此时 run 判 `failed(push_failed)`——把「run 跑通但分支发布不了」显式暴露,而非
     静默吞掉。
2. **orchestrator(开 PR)**:reconciler 观察到一个 **succeeded** 且 project 为
   `draft_pr`、已上报 `git_branch`、但尚无 `pr_url` 的 run →
   - **幂等**:先 `GET /repos/{owner}/{repo}/pulls?state=open` 按 head 分支查已存在
     PR(覆盖「开了 PR 但持久化前崩溃」与人工已开的情况);已存在则**采用**其
     url/number,不再新建。
   - 否则 `POST /repos/{owner}/{repo}/pulls`,title=`WIP: [jcode] <prompt 首行>`
     (Gitea 无独立 draft 字段,**WIP 前缀即 draft/工作草稿**),body 关联 run id,
     `base` = project.default_branch,`head` = `agent/run-<id>`。
   - 用 **`MarkPRCreated`**(first-writer-wins,仅在 `pr_url` 为空时写)持久化
     `pr_url`/`pr_number`,并补发一条带 `pr_url` 的 `run.status`,让在连 console
     无需重取即可显示「Draft PR #N ↗」。
   - **绝不 merge、绝不触发 CI。**
   - provider 未配(orchestrator 无 `GITEA_URL`/`GITEA_TOKEN`)→ 该步为空操作,run
     停在 diff-only(不失败)。provider 瞬时报错 → 本 tick 不写 `pr_url`,run 留在
     待开 PR 扫描里,下个 tick 重试。

**幂等性总结**:`run.git`(按 branch,first-writer-wins)、`FindOpenPRByHead`
(建前先查)、`MarkPRCreated`(`pr_url` 空才写)三处叠加,保证**并发/重试/崩溃恢复**
下每个 run 至多一个 draft PR,且 `pr_url` 一旦记录不被覆盖。

**新增/变更清单**:迁移 `0004_gitea_draft_pr`(projects 加 `git_mode/provider/
provider_url/provider_repo`;runs 加 `git_branch/commit_sha/pr_url/pr_number`);
`failure_reason` 枚举加 `push_failed`;事件加 `run.git`;`run.status` payload 增可选
`pr_url/pr_number`。均为非破坏性新增。

---

## 7 · 与 PRD 验收标准的对应

| AC | 本契约支撑点 |
|---|---|
| AC-3 项目 CRUD 落库 | §2.1 |
| AC-4 触发 run → 起 Job | §2.2 `POST .../runs` + §6 Job env |
| AC-5 状态机落库 | §1.3 status;`queued→scheduling→running→succeeded/failed` |
| AC-6 事件 ≤2s 实时 | §2.3 SSE stream(replay-then-live) |
| AC-7 刷新/重连回放 | §2.3 `after_seq` + §2.2 `GET run` 取终态 |
| AC-8 diff 产物可看/下载 | §2.4 + §5.2 |
| AC-9 failure_reason + message | §1.4 + `run.failure` 事件 |
| AC-10 retry 关联 retried_from | §2.2 retry |
| AC-11 并发隔离 | 每 run 独立 Job + 独立 `(run_id,seq)` 事件空间 |
| AC-12 墙钟超时判 failed(timeout) | §6 `activeDeadlineSeconds` / `RUN_TIMEOUT_SECONDS` |
| AC-13 CLI 等价 | 同一 `/api/v1` 端点,CLI 与控制台共用 |
