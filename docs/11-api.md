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

一个 project 是**纯容器**(名字 + 成员 + 护栏);仓库配置只存在于其 services 上
(§1.1a)。旧的"扁平化 default service 字段"(repo_url/git_mode/provider_repo…)
已随 simple-mode shim 一并移除。

```json
{
  "id": "9f2c...",
  "name": "demo",
  "created_at": "2026-07-07T12:00:00Z",
  "role": "owner",
  "owner_user_id": "u_...",
  "services": [ { "...": "见 §1.1a Service" } ]
}
```

### 1.1a Service(仓库)

```json
{
  "id": "svc...",
  "project_id": "9f2c...",
  "name": "default",
  "repo_kind": "provider",
  "provider": "gitea",
  "repo_owner_name": "jcloud/seed",
  "default_branch": "main",
  "git_mode": "draft_pr",
  "integration_id": "integ...",
  "created_at": "2026-07-07T12:00:00Z"
}
```

- **`integration_id`** (D19 / F5):非空 = 该 service 的所有 git 操作用绑定
  integration 的**机器人凭据**(不看触发者个人 OAuth;PR 正文标注真实触发者)。
  `null`/缺省 = 存量路径(个人 OAuth → `GITEA_TOKEN` 回退)。见 §2.5c。
- **`git_mode`** (ST-1; decision D08): `readonly` (default) | `draft_pr`.
  - `readonly` — a successful run ends in a **diff artifact only**. Nothing is
    pushed and no PR is opened. J1-J3 use this.
  - `draft_pr` — after a successful run with a **non-empty diff**, the
    orchestrator pushes an `agent/run-<id>` branch (proxy-push, M3) and opens a
    **draft PR** on the provider. **Never auto-merges, never triggers CI**.
- **`repo_kind`**: `provider`(`repo_owner_name` = `owner/name`,配合
  `provider`)| `raw`(`raw_repo_url`,只读,不能 `draft_pr`)。provider 基址
  由 orchestrator 配置推导(gitea 用 `GITEA_URL`)。提供者**token** 不是
  service 字段——draft-PR 优先用触发用户的 OAuth token,回退 `GITEA_TOKEN`。

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
- **`session` / `awaiting_since`(F7 / D22,迁移 `0014_session`;新增字段,非破坏
  性)**:`session` 恒序列化(`true` = 多轮 session run;单发 run 为 `false`)。
  `awaiting_since` 仅当前处于 `awaiting_input` 时非空(空闲回收计时起点)。
- **`permission_mode`(F8b / D22,迁移 `0015_permission`;新增字段,非破坏性)**:
  `"approval"` = 交互审批 session(runner 把每个 agent 权限请求转发给用户审批,
  见 §2.2 permission-response、§4 权限事件、§5.5 决议端点);缺省/`""` =
  full_access(现状,自动放行)。仅 `session=true` 的 run 可为 `"approval"`。
- **`acp_session_id` / `resumed_from`(F9b / D23 ①②,迁移 `0016_session_resume`;
  新增字段,非破坏性)**:
  - `acp_session_id`:本 run 驱动的 ACP session id。runner 经 **`run.session` 事件**
    (见 §4)上报,ingest **first-writer-wins** 落库(仅当仍为空才写)。一个续聊
    (resume)run 在创建时就把原 run 的 `acp_session_id` **复制**进来,好让
    reconciler 在 run 发出自己的 `run.session` 之前就能注入 `RESUME_SESSION_ID`;
    runner 随后 emit **同一个** id(`resumed=true`),first-writer-wins 写为 no-op。
    非 session run / 尚未建立 session 时为空。
  - `resumed_from`:若本 run 由 `POST /runs/{id}/resume` 生成,指向被续聊的原
    (终态)session run 的 id(语义仿 `retried_from`);否则 `null`。console 展示
    「resumed from <短 id>」链接回原 run。
- 服务器**从不**把 run token 序列化给 console 客户端。

### 1.3 Run 状态徽章体系(单一事实源)

| status | 语义 | 终态? | 本期可达 | 建议色 |
|---|---|---|---|---|
| `queued` | 已创建,等待调度 worker | 否 | ✅ | 灰 |
| `scheduling` | 已创建 K8s Job,尚未观察到 pod 运行 | 否 | ✅ | 蓝(脉冲) |
| `running` | worker pod 活跃,agent 连跑中 | 否 | ✅ | 蓝(动效) |
| `awaiting_input` | **(F7 / D22)** session run 一轮结束,pod 保活长轮询,等用户下一条消息 | 否 | ✅(仅 `session=true`) | 紫(脉冲) |
| `succeeded` | 正常结束,diff 产物就绪 | ✅ | ✅ | 绿 |
| `failed` | clone/setup/agent/timeout 失败,含可读原因 | ✅ | ✅ | 红 |
| `canceled` | 操作者取消 | ✅ | ✅ | 灰 |
| `blocked` | 需人工输入(Symphony 一等公民)。**本期建模+展示,`full_access` runner 不产生** | 否 | ⚠️ | 黄 |

> 与 PRD §6 徽章表一致。PRD 用户旅程只提 `queued→running→succeeded/failed`;
> `scheduling` 是 `queued` 与 `running` 之间的可见细分态(调度中),UI 可与
> `running` 同色处理,也可单独展示。`canceled` 服务于取消端点。

**状态机转移表(F7 后的完整版;`from == to` 的空转移恒允许,幂等)**:

| from \ to | scheduling | running | awaiting_input | succeeded | failed | canceled |
|---|---|---|---|---|---|---|
| `queued` | ✅ | — | — | — | ✅ | ✅ |
| `scheduling` | — | ✅ | — | ✅(极快 Job) | ✅ | ✅ |
| `running` | — | — | ✅(session 轮结束) | ✅ | ✅ | ✅ |
| `awaiting_input` | — | ✅(消息投递) | — | ✅(finalize 后 runner 退出) | ✅(pod 死亡/超时) | ✅ |
| 终态 | — | — | — | — | — | — |

- `awaiting_input` **只在 `session=true` 的 run 上出现**;单发 headless run
  (webhook/kanban 触发含在内)状态机与 F7 之前完全一致。
- `awaiting_input` 期间 Job 持续 active 是**正常态**(pod 长轮询 next-prompt),
  reconciler 不视为失败;Job 退出才驱动 succeeded/failed。
- SSE 流(§2.3)只在**终态**关闭;`awaiting_input` 不断流。

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
{ "name": "demo" }
```

- `name` 是**唯一**字段。project 是纯容器;仓库随后通过
  `POST /projects/{id}/services` 附加(创建流是两步)。请求体带旧的
  repo 字段(`repo_url` 等)会被**响亮拒绝**(`400`,DisallowUnknownFields),
  不再自动创建 default service。

响应 `201 Created`:完整 Project 对象(见 §1.1,`services` 为空数组)。
错误:`400`(缺 name / 未知字段)。

#### `GET /api/v1/providers/{provider}/repos` — 仓库选择器(Drone 式 onboarding)

`?q=<搜索>&page=<页码>`。列出**调用者凭据**在该 provider 上可见的仓库:登录用户
用其绑定的 OAuth token(列表 = 该用户真实可见范围);service principal /
未绑定时 gitea 回退全局 PAT。响应:

```json
{ "repos": [ { "id": 210003, "full_name": "ai/jcode-cloud-e2e", "default_branch": "main", "private": true, "html_url": "..." } ] }
```

- `id` 是 provider 的数字仓库 id;创建 service 时以 `provider_repo_id` 回传,
  作为防 rename 的仓库身份(迁移 0009)。
- 错误:`400`(未知 provider)、`403`(无该 provider 凭据——console 据此提示
  去绑定账号,并回退手填 URL)。
- 注意 scope:gitea 登录 token(空 scope)可列全部;github 的 `read:user` /
  gitlab 的 `read_user` 登录 scope **列不了私有仓库**,需要升级 scope 重新
  绑定(github `repo` / gitlab `read_api`)。
- 若同时配置了 `WEBHOOK_URL` + `WEBHOOK_SECRET`,创建 service 时会
  **best-effort 自动注册** `@jcode` 评论 webhook(三 provider;幂等,按 URL
  判重;失败仅记日志,不影响创建)。
- **运维注记**:幂等按 URL 判重意味着**轮换 `WEBHOOK_SECRET` 不会更新已注册
  hook 的 secret**——轮换后旧 hook 的投递将全部验签失败(provider 侧投递日志
  可见 401)。轮换后需在 git 主机侧删除旧 hook 再触发重建(或手工更新 secret)。

#### `GET /api/v1/projects` — 列出 projects

响应 `200`:

```json
{ "projects": [ { "id": "...", "name": "demo", "...": "..." } ] }
```

空态返回 `{ "projects": [] }`。

#### `GET /api/v1/projects/{id}` — 取单个 project

响应 `200`:Project 对象。错误:`404`。

#### `PATCH /api/v1/projects/{id}` — 更新 project

请求(重命名 + 护栏;仓库改动走 `PATCH /services/{id}`):

```json
{ "name": "demo2", "max_concurrent_runs": 2, "run_timeout_secs": 900,
  "max_live_sessions": 2, "session_idle_timeout_secs": 900, "session_ttl_secs": 14400 }
```

- **presence 语义(D15b)**:省略的字段不变;数值护栏显式发 `null` 或 ≤0 清回
  「继承集群默认」。
- **`provider_allowlist` 已废弃(D20 / F5)**:请求体带该 key → `400 deprecated_key`
  (文案指向集群级 git-host 白名单 + 项目 integration)。DB 列保留(存量数据),
  但不再可编辑;**5 个派发执行点(run 创建 / retry / resume / review / webhook)**
  的旧校验逻辑已移除(service 创建处的旧 provider 闸亦一并移除)。git-host 策略改由
  集群 `ALLOWED_GIT_HOSTS` 收口:integration 创建/轮换时校验,且**派发时对绑定
  integration 的 service 再次校验**(白名单收紧后存量 integration 立即被拦,
  `403 host_not_allowed`;webhook 路径可见回帖)——见 §2.5c。
- **session 护栏(F7 / D22)**:`max_live_sessions`(project 内 live session 数
  上限,集群默认 `MAX_LIVE_SESSIONS`=2)、`session_idle_timeout_secs`
  (`awaiting_input` 空闲自动收尾,集群默认 900s)、`session_ttl_secs`
  (整个 session 墙钟预算,集群默认 14400s)。
- **明示:live session 总量只受 per-project `max_live_sessions` 闸约束,没有
  集群级上限**——集群级 `MAX_CONCURRENT_RUNS` 只管 scheduling/running 的调度槽,
  `awaiting_input` 的保活 pod 不占用它;project 数量多且并发开 session 时,
  集群总保活 pod 数 = Σ(各 project 的 live session),容量规划按此估算。

响应 `200`:更新后的 Project。错误:`400`(未知字段)、`404`。

#### `DELETE /api/v1/projects/{id}` — 删除 project

响应 `204 No Content`(级联删除其 runs/events/artifacts)。错误:`404`。

### 2.2 Runs

#### `POST /api/v1/services/{id}/runs` — 创建并入队 run

Run 一律**按 service 派发**(旧的项目级 `POST /projects/{id}/runs`——解析
default service 的 shim——已移除;该路径现在只服务 GET,POST 得 `405`)。

请求:

```json
{ "prompt": "在 README 末尾加一行 Hello", "model_id": "…可选…", "session": false, "permission_mode": "" }
```

- `prompt`(必填,非空白)。
- `session`(可选,默认 `false`;**F7 / D22**):`true` = 以**多轮 session**模式派发
  ——runner 保持同一 ACP session,每轮结束 run 进 `awaiting_input` 等待
  `POST /runs/{id}/messages` 的后续消息。仅 `kind=agent`(本端点恒为 agent)。
  webhook/kanban 触发路径首期不发该字段。
- `permission_mode`(可选,默认 `""`;**F8b / D22**):`"approval"` = 交互审批
  session——runner 把每个 agent 权限请求作为 `agent.permission_request` 事件
  转发(§4),等用户经 permission-response(下)决议。**仅与 `session: true`
  搭配合法**(单发 headless run 无人值守可审批),否则 `400`;取值仅
  `""`/`"approval"`,其余 `400`。缺省 = full_access(现状,自动放行)。

响应 `201 Created`:完整 Run 对象,`status` = `queued`(`permission_mode` 回显)。
错误:`400`(空 prompt / 非法 `permission_mode` / `approval` 无 `session`)、
`404`(service 不存在)。

> 创建即入队;reconciler 下一 tick(默认 3s 内)按并发上限起 K8s Job。
> session run 额外受 project 护栏 `max_live_sessions` 闸(见 §2.1 PATCH 字段):
> project 内 live(scheduling/running/awaiting_input)session 数达上限时新
> session 停留 `queued`,腾出名额后自动调度。

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
> retry 保留 run 身份:`kind`、PR 关联、**`session`**(session run 的 retry 仍是
> session)、**`permission_mode`**(F8b:审批 session 的 retry 仍是审批 session,
> 绝不静默降级成 full_access)一并拷贝。

#### `POST /api/v1/runs/{id}/resume` — 续聊已结束的 session run(F9b / D23 ①②)

权限 member+。请求:

```json
{ "prompt": "接着上次的会话继续" }
```

**续聊 = 在一条新 run 上 `session/load` 复用原 session**(retry 是全新 session;
resume 是同一 ACP session)。前置校验按序返回类型化 `409`(fail-visible,
CLAUDE.md 红线①):

- 原 run 必须是 **session run 且已终态**(succeeded / failed / canceled 均可),
  否则 `409 run_not_resumable`——非 session run「没有可恢复的 session」;仍在跑的
  session「用消息框继续,别开新 run」(同一 code,文案区分)。
- 原 run 必须已记录 `acp_session_id`(来自 `run.session` 事件),否则
  `409 session_not_recorded`。
- 集群必须开了**持久化工作区**(`PERSISTENT_WORKSPACE`;转录留在 service 的 PVC 上,
  runner `session/load` 从 `$HOME/.jcode` 读),否则 `409 workspace_not_persistent`
  ——**持久化是集群级开关(Feature C / D05),非 per-service**,所以这里查集群配置。
- 再过模型闸(fail-visible,同 create/retry:优先沿用原 run 的模型,不再授权则
  `403 model_not_granted` / `409 model_not_configured`)。**注:D20/F5 起
  provider_allowlist 派发闸已移除**——git-host 策略改由集群白名单收口:integration
  创建时校验 + 派发时对绑定 integration 的 service 再校验(`403 host_not_allowed`)。
- `max_live_sessions` 护栏**不在此端点校验**——新 run 入队后由 reconciler 现有闸门
  自然生效(F7b 逻辑),超额则新 session 停留 `queued`。

成功 → **生成一条新 run**,`status = queued`、`kind = agent`、`session = true`、
`prompt` = body 的 prompt、`resumed_from` = 原 run id、`permission_mode` 继承原 run、
`acp_session_id` **复制**原 run 的值(供 reconciler 注入 `RESUME_SESSION_ID`)。
响应 `201 Created`:**新** Run 对象(带 `resumed_from` / `acp_session_id`,`id` ≠ 原)。
错误:`400`(空 prompt);`404`;`403`(viewer / provider / 模型未授权);
`409`(`run_not_resumable` / `session_not_recorded` / `workspace_not_persistent` /
`model_not_configured`)。

> 注入路径:reconciler 在 `jobEnv` 里,当 `resumed_from` 非空且**新 run 自身的**
> `acp_session_id` 非空时,注入 `RESUME_SESSION_ID=<acp_session_id>`(读自身字段,
> 不查原 run,避免原 run 被删的边界);entrypoint 在 `RUN_SESSION=1` 且
> `RESUME_SESSION_ID` 非空时给 acpdrive 传 `--resume`,走 ACP `session/load`
> (F9a 已在 main)。普通 session run 在入队时 `acp_session_id` 仍为空,故**不**注入。

#### `POST /api/v1/runs/{id}/messages` — 给 session run 投递后续消息(F7 / D22)

权限 member+。请求:

```json
{ "prompt": "接着把测试补上" }
```

- run 必须是 **session run**(`session=true`)且状态 ∈ `{awaiting_input, running}`
  (`running` 时排队,当前轮结束后被取走);其余状态一律
  **`409 run_not_awaiting`**(fail-visible,不静默丢弃)。
- **正在收尾的 session(finalize 标记已置,来自 finish 端点或空闲超时)一律
  `409 run_finalizing`**——此时 next-prompt 只会回 `410`,收下的消息永远不会被
  处理,所以拒收而不是静默吞掉。
- 消息落 `run_messages` 投递队列(runner 经 §5.4 next-prompt 长轮询取走,
  **两阶段 offer/consume,详见 §5.4**),同时 append 一条 `user.message` 事件进
  时间线(§4),console 渲染为用户消息气泡。

响应 `201 Created`:

```json
{ "id": "…", "run_id": "…", "seq": 1, "prompt": "接着把测试补上", "created_at": "…", "offered_at": null, "consumed_at": null }
```

错误:`400`(空 prompt)、`404`、`403`(viewer)、`409 run_not_awaiting`、
`409 run_finalizing`。

#### `POST /api/v1/runs/{id}/finish` — 结束 session(F7 / D22)

权限 member+。无请求体。置 finalize 标记后:下一次 next-prompt 长轮询立即回
`410` → runner 优雅收尾退出(exit 0)→ Job 成功 → run 收敛 `succeeded`。
**幂等**:重复 finish / 对已终态 run finish 均回 `200` 当前 Run。
首次 finish 会 append 一条 `session.finish {reason:"user"}` 事件。
非 session run:`409 run_not_awaiting`。

> 空闲超时(project 护栏 `session_idle_timeout_secs`,默认继承集群
> `SESSION_IDLE_TIMEOUT_SECONDS`=900s)由 reconciler 走**同一条 finalize 路径**
> 自动触发,并 append `session.finish {reason:"idle_timeout"}` 事件。

#### `POST /api/v1/runs/{id}/permission-response` — 决议权限请求(F8b / D22)

权限 member+(viewer `403`——console 侧按钮同时禁用,后端是权威闸)。仅对
`permission_mode="approval"` 的 session run 有意义。请求:

```json
{ "request_id": "…agent.permission_request 事件里的 request_id…", "option_id": "allow_once" }
```

校验顺序(全部 fail-visible,绝不静默吞掉一次点击):

- `400 bad_request` — `request_id` / `option_id` 缺失;
- `404 not_found` — 该 run 上没有这个 request(事件还没到/发错 run);
- `409 permission_already_resolved` — 已有人决议过(decided),或 runner 已
  自行收尾(resolved,如客户端超时 timeout-deny)——**迟到的决议被记录拒绝,
  绝不覆盖**;并发双击/两人同时点由 store 条件写原子裁决,恰好一人成功;
- `400 invalid_option` — `option_id` 不在该请求 offer 的 options 集合内
  (镜像 runner 侧 acpdrive 的同名防御检查)。

响应 `200`:完整 run_permission 台账行(含 `decided_option_id` / `decided_by` /
`decided_at`)。runner 的决议轮询(§5.5)随即取走;时间线上的最终结果以
`agent.permission_resolved` 事件(§4)回流,console 据此把卡片置为已决议态。

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
    "gitea_url": "http://gitea.jcloud.svc.cluster.local:3000",
    "allowed_git_hosts": ["github.com", "gitea.jcloud.svc.cluster.local"]  // D20/F5;空数组 = 不限制
  },
  "runner":    { "image": "ghcr.io/acme/runner:v1", "persistent_workspace": true },  // RUNNER_IMAGE / PERSISTENT_WORKSPACE
  "archive": {                   // F10 / D23 ③ 持久化工作区归档层
    "enabled": true,             // 对象存储已配 + PERSISTENT_WORKSPACE=1 + ARCHIVE_IDLE_DAYS>0
    "endpoint": "http://minio.jcloud.svc.cluster.local:9000",  // S3_ENDPOINT(非密;仅 enabled 时)
    "bucket": "jcloud-workspaces",                             // S3_BUCKET(仅 enabled 时)
    "idle_days": 14              // ARCHIVE_IDLE_DAYS(仅 enabled 时)
    // 关闭时改为:{ "enabled": false, "reason": "object storage not configured — set S3_ENDPOINT, ..." }
  },
  "namespace": "jcloud",         // K8S_NAMESPACE
  "launcher":  "kubernetes"      // JOB_LAUNCHER:kubernetes | process | disabled(DISABLE_K8S)
}
```

- 容量计数由 store 单次查询(`CountRunsByStatus`,只读、跨所有 project)。
- `archive` 是持久化工作区归档层(F10 / D23 ③)的**只读状态**。对象存储(S3/MinIO)是
  一等公民依赖(D14 fail-visible):`S3_ENDPOINT`/`S3_BUCKET`/`S3_ACCESS_KEY`/`S3_SECRET_KEY`
  四者缺一即 `enabled:false` 并给出 `reason`(明示缺哪一项),而不是静默跳过;归档还要求
  `PERSISTENT_WORKSPACE=1` 与 `ARCHIVE_IDLE_DAYS>0`(否则 `reason` 分别提示"持久工作区未开"/
  "归档 pass 未开")。`endpoint`/`bucket`/`idle_days` 仅在 `enabled` 时返回;S3 的
  access/secret key **绝不序列化**(与其它 secret 一致)。归档/恢复由 reconciler 自动驱动
  (无面向用户的手动触发端点):一次性 K8s Job 打包 PVC → 经**预签名 PUT URL** 传对象存储 →
  删 PVC + 置 `services.archived_at/archive_key`;archived service 的下一个 run 用**预签名 GET URL**
  在 runner 内还原后再跑。真实 S3 凭据只在控制面签发预签名 URL,**永不进 runner pod**(D16)。
  - **已知取舍(恢复标记在 dispatch 时清除)**:reconciler 一旦为 archived service 派出恢复 run
    就清掉 `archived_at/archive_key`(runner 无"恢复完成"回调,那需要动 orchclient——本期不做)。
    因此若还原在 runner 内失败,该 run 会 fail-visible 失败(entrypoint `die setup_failed`),但归档标记
    已清、**该次还原不会自动重试**——下一个 run 直接 re-clone 进(已重建的)空 PVC。这只丢失工作副本:
    权威转录始终在控制面 store(D12),数据安全。tar 对象仍留在对象存储的确定性 key 上(下次归档覆盖)。
  - **Terminating PVC 保护**:归档删 PVC 后若下一个 run 恰在旧同名 PVC 仍 Terminating 时重建,
    `EnsureWorkspacePVC` 探测 `DeletionTimestamp` 返回 transient error,run 留 queued、下 tick 旧 PVC
    清干净后自然重建——绝不把 run 挂到 Terminating PVC 上(否则 pod 永远 Pending 直到 Job 超时)。
- 错误:`401`(缺/错 token)、`500`(容量查询失败)。
- 鉴权口径与运行时可见性映射到 console 的两种角色(见 console `VITE_ROLE`);
  当前为单租户 MVP,尚无 OIDC/RBAC,角色仅是**展示层**信号,并非服务端授权。
- console 侧有一个**运行时登录门**(`src/auth/AuthProvider.tsx`):浏览器先以
  当前 token(localStorage > `VITE_CONSOLE_TOKEN`)探测本端点 —— `200` 进入
  控制台、`401` 进登录页、网络错/5xx 进环境引导页(每 3s 自动重探)。会话中
  任何 `401` 会清掉本地 token 并回登录页。因此"cluster admin"是**经服务端
  验证过的信任级别**,但仍是共享 token,非按用户/按请求授权。

### 2.5a 模型目录 + project 授权(D21)

单行 `cluster_model_config` 演进为**多行模型目录** + **model↔project 授权表**。
生效模型按 **run 所属 project** 解析(见 `internal/modelcfg`);D16 反向代理不变
(真 key 永不进 pod)。**旧的 `GET/PUT/DELETE /api/v1/system/model` 已下线**,
console 全部走以下新端点。api key **只写不读**(回显 `api_key_set` 布尔位),
`base_url`/明文 key **只对 cluster-admin 可见**。

| 端点 | 角色 | 说明 |
|---|---|---|
| `GET /api/v1/system/models` | cluster-admin | 目录全量(含 `api_key_set` + `granted_project_ids`,不含明文 key) |
| `POST /api/v1/system/models` | cluster-admin | `{name, base_url, model_name, api_key?}`;`base_url` 须 http(s),`model_name` 须 `provider/model`,name 唯一(重名 `409`);有 key 但无 `AUTH_TOKEN_KEY` → `409 cipher_not_configured` |
| `PATCH /api/v1/system/models/{id}` | cluster-admin | 字段皆可选(省略=不变);`api_key` 省略=不变、`""`=清空(keyless)、有值=轮换 |
| `DELETE /api/v1/system/models/{id}` | cluster-admin | 删除;grants 级联,`services.default_model_id`/`runs.model_id` 置 NULL |
| `PUT /api/v1/system/models/{id}/grants/{projectId}` | cluster-admin | 授权某 project(幂等);model/project 不存在 → `404` |
| `DELETE /api/v1/system/models/{id}/grants/{projectId}` | cluster-admin | 撤销(幂等) |
| `GET /api/v1/projects/{id}/models` | member+ | 本 project 被授权的模型 `{models:[{id,name,model_name}], env_fallback}`——**绝不含 base_url/key** |
| `PATCH /api/v1/services/{id}` | owner | `{default_model_id?}`:见下方 presence 警示 |
| `POST /api/v1/services/{id}/runs` | member+ | 新增可选 `{model_id?}`(composer 选);见下方解析链 |

> **⚠️ `default_model_id` 的 presence 语义与 `PATCH /projects` 不同(易踩坑)。**
> 该字段用 `*string` 指针解码,故 **JSON 省略字段** 与 **显式 `null`** 都解成 nil =
> **"不变"**(二者无法区分)。要**清除**默认必须显式发 **空串 `""`**(→ `default_model_id`
> 置 NULL);发一个 id 则**设为默认**(须在 project 授权集内,否则 `400 model_not_granted`)。
> 这与 D15b 的 `PATCH /projects`(那里显式 `null` 才是"清空回落继承")**相反** —— 迁移
> 调用方时别把 `null` 当清除。console `updateService` 只发 `default_model_id`,遵循此约定。

**解析链(run dispatch 时按 project 选模型)**:`run.model_id`(composer 显式选)
→ `service.default_model_id` → project 授权集恰好一个 → 落 `runs.model_id` +
`runs.model_name`(provider/model 快照,审计用:模型删除后 `model_id` 被 FK 置 NULL,
但 `model_name` 快照仍在)。典型错误(fail-visible,均不入队 run):

- composer 传的 `model_id` 不在 project 授权集 → `403 model_not_granted`。
- retry/review 复用的原 run 模型已不再授权 → 同 `403 model_not_granted`,但文案面向
  复用场景("原 run 使用的模型已不再授权,请设 service 默认或另选")。
- project 有多个授权且无 service 默认、composer 未选 → `409 model_not_selected`
  (webhook/kanban 亦区分此文案,指向 service owner 设默认)。
- project 零授权(且目录非空)→ `409 model_not_configured`(文案含"联系管理员授权")。
- env `MODEL_*` 回退**仅当目录为空表**时生效(本地 rig 兼容),`runs.model_id` 留 NULL
  ——**目录非空时的 NULL model_id(被删模型)绝不回退到 env**,而是 fail-visible。

reconciler 的 Job-launch 闸与 LLM 反向代理都按 `run.model_id` **物化**同一模型
(模型在入队后被删 → `503 model_not_configured` / `setup_failed`;模型有 key 但
`AUTH_TOKEN_KEY` 未配 → **永久错误** → `setup_failed`,非无限重试;均不静默降级)。

### 2.5b jtype kanban links(Feature E / F6 · D25)

kanban link 把「jtype board 的某一列」绑到 project 的某个 service:卡片拖进
`trigger_column` 派一个 agent run,run 结束把结果回写为卡片评论(`done_column`
非空时把卡片移到该列)。**F6 / D25 起,link 管理权从 cluster-admin 下放到
project owner**,jtype 凭据从「单一集群 env」改为 **per-link 加密存储**
(`token_enc`,AES-256-GCM,与模型 key 同一 `AUTH_TOKEN_KEY`,**只写不读**)。
`JTYPE_BASE_URL` 一项即启用集成;`JTYPE_TOKEN` 降级为**回退凭据**(link 没配
自己的 token 时使用)。

| 端点 | 角色 | 说明 |
|---|---|---|
| `GET /api/v1/system/kanban/links` | cluster-admin | **只读**跨 project 总览(每条含 `project_id` + `token_set`,绝不含 token) |
| `GET /api/v1/projects/{id}/kanban/links` | owner | 本 project 的 links(含 `token_set`) |
| `POST /api/v1/projects/{id}/kanban/links` | owner | `{workspace_id, board_ref, service_id, trigger_column, done_column?, token?}`;`service` 须属本 project(否则 `400`);`token` 可选、**只写**(明文进、绝不回);集成开时用该 token(或集群回退)对 live board 校验列名 |
| `PATCH /api/v1/projects/{id}/kanban/links/{linkId}` | owner | **只轮换/清除 token**:body `{"token":"..."}`(字段必发,缺省 `400`);`""` = 清除转集群回退,非空 = 轮换;**claims 保留**(轮换绝不重派已认领卡片);link 须属本 project(否则 `404`);cipher 缺失 `409`(同 create) |
| `DELETE /api/v1/projects/{id}/kanban/links/{linkId}` | owner | 删除;link 须属本 project(否则 `404`),claims 级联 |

**创建时的 fail-visible 闸(均不静默)**:

- `service` 不存在或不属本 project → `400`。
- 集成开(`JTYPE_BASE_URL` 已配)但既没传 `token` 也没有集群回退 → `400 token_required`
  (无可用凭据的 link 是死 link,拒建而非静默存)。
- `trigger_column`/`done_column` 不是 live board 的真实列 → `400`;jtype 不可达 → `503 jtype_unreachable`。
- 传了 `token` 但 `AUTH_TOKEN_KEY` 未配(无 cipher,无法加密)→ `409 cipher_not_configured`
  (该检查在 board 网络校验**之前**——配置错误不被网络失败掩盖)。

响应 `kanbanLinkView` 恒含 `token_set`(bool)与 `credential_status`
(`"per_link" | "cluster_fallback" | "missing"`,服务端按 token_set 与集群回退
是否配置派生;`missing` = poller/回写 fail-visible 跳过该 link,console 以醒目
错误徽标呈现),**永不含 token**。

**运行期 token 选取(poller 拉取 + reconciler 回写共用同一三态)**:
`token_enc` 非空 → 解密用之;否则集群 `JTYPE_TOKEN` 回退(限频一次性 deprecation
日志);两者皆空 → 该 link **fail-visible 跳过**(不以空凭据打 jtype),回写侧则把
该卡的 writeback **挂起重试**(不丢结果),owner 补上 token 后自动恢复。

`GET /api/v1/system` 的 `kanban` 字段:`enabled`(= `JTYPE_BASE_URL` 已配)、
`base_url`、`poll_interval`、`cluster_token_set`(= 集群回退 token 是否配置,**非** token 本身)。

### 2.5c Integrations(git 集成 · Feature F5 · D19+D20)

**Integration 是 project 级实体**:一个 git host 绑定 + 一份**机器人服务凭据**
(Gitea org PAT / GitLab group token / GitHub PAT)。绑定该 integration 的 service,
其所有 git 操作(clone/push/开 PR/发 review)一律以**机器人身份**执行——不再看触发者
个人 OAuth(PR 正文追加 `Triggered by @<user>` 保留可追溯性)。凭据 AES-256-GCM
加密落库,**只写不可回读**。

| 端点 | 角色 | 说明 |
|---|---|---|
| `GET /api/v1/projects/{id}/integrations` | member+ | 本 project 的 integration 列表(每条含 `token_set`/`bot_username`/`host`/`provider`,**绝不含 token**) |
| `POST /api/v1/projects/{id}/integrations` | owner | `{name?, provider, host, cred_type?, token}`;`token` **只写**;创建时用该 token 调 provider API **验证连通性**并回填 `bot_username`(失败 → `400 integration_unreachable`,带 provider 错误摘要) |
| `PATCH /api/v1/integrations/{iid}` | owner | `{name?, token?}`;`token` 非空 = **轮换**(重新验证连通性 + 刷新 `bot_username`);`token` 为 `""` → `400`(integration 必须有凭据,清除请删除);`host`/`provider` 不可改 |
| `DELETE /api/v1/integrations/{iid}` | owner | 删除;绑定它的 service 的 `integration_id` 置 NULL(回退存量凭据路径);响应含 `services_unbound` 计数 |
| `GET /api/v1/projects/{id}/integrations/{iid}/repos` | member+ | 用 integration 机器人 token 列可见仓库(`?q=`),供 service 建库选择器;整型 `provider_repo_id` 回传 |

**cluster git-host 白名单(D20)**:env `ALLOWED_GIT_HOSTS`(逗号分隔)。比对按
`domain.NormalizeGitHost` 的规范形 `hostname[:port]` 做——**端口计入比较**(SSRF
审查 C1②:`gitea.svc:3000` 与 `gitea.svc:9999` 是不同服务;http/https 的 scheme
默认端口 80/443 折叠掉,所以 `github.com` 匹配 `https://github.com`)。
**空 = 不限制,仅适合封闭部署**(如本仓根 rig:显式接 mockllm 的 e2e 封闭环境);
对外的产品集群应显式配置(company overlay 配为 `git.scgzyun.com`)。校验点:
① integration 创建/轮换 `host` → `400 host_not_allowed`(网络往返**之前**先拒);
② **派发时**(run 创建 / retry / resume / review / webhook)对绑定 integration 的
service 再校验 → `403 host_not_allowed`(webhook 路径可见回帖)——白名单收紧后存量
integration 立即被拦,不必等下次轮换。Cluster 页 `GET /api/v1/system` 的
`provider.allowed_git_hosts` 只读展示。

**host 接线约束(`400 host_mismatch`,本期暂用)**:当前版本的实际 git 操作
(clone URL 推导 + PR 客户端)走**集群配置的 host**——gitea 用 `GITEA_URL`,
github/gitlab 用各自公有主机。因此 integration 的 `host` 必须与之一致(gitea =
`GITEA_URL` 的 host;github = `github.com`;gitlab = `gitlab.com`),否则
`400 host_mismatch`——避免"验证打 A 主机、推送打 B 主机"的静默错配。**多 host
正式接线(per-integration base URL 贯通 clone/PR 路径)为后续项。**

**SSRF 硬化(C1①)**:integration 连通性探测与 repo 列表使用的 provider HTTP
客户端**不跟随任何重定向**——host 是用户输入,30x 反弹到内网地址的请求被拒绝,
3xx 以可见错误浮出。

**service 创建 RBAC 放开(D19)**:`POST /api/v1/projects/{id}/services` 带
`integration_id` 时 **member 即可建**(校验:integration 属同 project;其 host 仍
∈ 集群白名单——纵深防御;目标 repo 在机器人 token **可达集**内,否则
`400 repo_not_reachable`)——service 的 provider 由 integration 决定。不带
`integration_id` 的**裸建库**仍 **owner-only**。

**fail-visible(CLAUDE.md 红线 #1)**:连通性验证失败 `400 integration_unreachable`;
host 不在白名单 `400 host_not_allowed`;`AUTH_TOKEN_KEY` 未配(无法加密)
`409 cipher_not_configured`;repo 不可达 `400 repo_not_reachable`;跨 project 引用
integration `404`。凭据解析(reconciler/webhook 注册/source 拉取共用
`ResolveForService`):绑 integration → **一律**用其 token,**解密失败/cipher 缺失即
fail-visible 报错,绝不静默降级到个人 OAuth**;未绑 → 存量个人 OAuth → `GITEA_TOKEN`
回退(回退处打**进程内一次**的 deprecation 日志)。runner 的 source 拉取遇
integration 凭据错误 → `409 integration_credential_unavailable` 且 run 的
failure_reason/message 带凭据原因(clone 阶段 fail-visible,绝不匿名 clone 顶替)。
reconciler 的 PR 开启/update push/review 回帖/session push 遇同类错误:发**一条**
`run.failure` 时间线事件说明原因与修法,然后**停摆该 run**(进程内标记,后续 tick
跳过,不再无限重试;重启后重查一次——修好即恢复)。

### 2.5d Schedules(定时触发器 · Feature F11 · D24)

schedule 把「一条标准 5 段 cron 表达式 + 一个 prompt」绑到 project 的某个
service:每当 cron 到点,schedule poller 就对该 service 派一条**无头 agent run**
(`origin="schedule"`),模型走 service 的默认(D21/F4 解析链)。哲学仿 D17 kanban
poller——**level-based、幂等、重启无缝**:权威状态是 `schedules.last_fired_at`,用
**条件更新**推进(`WHERE last_fired_at IS NOT DISTINCT FROM $old`),因此既可重启恢
复,也可多实例并发跑而不重派。迁移 `0019_schedules`。

Schedule 对象(`GET`/`POST`/`PATCH` 的响应体):

```json
{
  "id": "sc-1a2b...",
  "service_id": "9f2c...",
  "cron_expr": "0 9 * * 1-5",
  "prompt": "汇总昨天合并的 PR 更新 changelog",
  "enabled": true,
  "last_fired_at": "2026-07-09T09:00:03Z",
  "last_error": "",
  "created_by": "u-abc...",
  "created_at": "2026-07-08T00:00:00Z",
  "updated_at": "2026-07-09T09:00:03Z"
}
```

| 端点 | 角色 | 说明 |
|---|---|---|
| `GET /api/v1/services/{id}/schedules` | member+ | 该 service 的 schedules(`{schedules:[...]}`,含 `last_error`) |
| `POST /api/v1/services/{id}/schedules` | owner | `{cron_expr, prompt, enabled?}`;`enabled` 缺省 `true`;cron 校验见下;返回 `201` + Schedule |
| `PATCH /api/v1/schedules/{sid}` | owner | `{cron_expr?, prompt?, enabled?}`(pointer presence:缺省=不变);传 `cron_expr` 会重校验;`last_error` 由 poller 独占,PATCH 不动;**窗口重置**:`cron_expr` 实际变化、或 `enabled` 由 false→true 时,`last_fired_at` 原子重置为编辑时刻——新节奏/重开的首次触发从编辑时刻起算,**绝不补发编辑前已过去的边界**(与重启不补跑同一哲学);仅改 prompt 不重置 |
| `DELETE /api/v1/schedules/{sid}` | owner | 删除该 schedule(`{deleted:sid}`) |

**cron 校验(create/patch 均 fail-visible,写时即拒,绝不静默到派发时才忽略)**:

- 非法 5 段 cron → `400 invalid_cron`(用 robfig/cron/v3 标准 5 段 parser;
  `@hourly`/`@every`/秒字段一律不接受)。
- **cron 表达式一律按 UTC 求值**(poller 求值前把基准时间归一到 UTC;不支持
  `TZ=` 前缀)——`0 9 * * *` 意为 09:00 UTC,与 orchestrator 容器的本地时区无关。
- **最小间隔护栏**:相邻两次触发间隔 `< 5 分钟` 的表达式 → `400 cron_too_frequent`
  (防 `* * * * *` 之类自伤;取样多个连续触发点取最小间隔,能抓到 `0,1 * * * *`
  这类不规则的高频)。
- `prompt` 为空 → `400 bad_request`。

**派发期 fail-visible 闸(写入 `last_error`,推进该窗口,不派 run)**:到点时若
①模型闸不过(project 无授权模型 / 多个已授权但 service 未设默认)或②service 绑的
集成 host 已不在集群 allowlist(D20)——poller **不派 run**,但仍**原子推进
`last_fired_at`**(该窗口放弃、不无限重试),并把原因写进 `last_error`;下次成功派发
清空。console 以红色徽标呈现 `last_error`(`SchedulesPanel`)。瞬时错误(DB/解析器
抖动)则**不推进**窗口,下 tick 重试。

派生 run 走 `origin="schedule"`(见 §1.2 origin 枚举);触发它的 schedule id **不加
到 runs 表**,而是记在该 run 的首条 `run.status` 事件 payload 的 `schedule_id` 上
(runs 表保持不变)。

env:`SCHEDULE_POLL_INTERVAL`(默认 `30s`,`<=0` 禁用整个 schedule poller)。

> **`origin` 枚举**(run 字段):`"api"`(默认/console)、`"webhook"`(Gitea PR 评论
> `@jcode`)、`"kanban"`(卡片拖入触发列)、`"schedule"`(cron 到点)。console 在 run
> 详情按 origin 展示对应徽标(webhook 链回评论、schedule 显示「scheduled」)。

---

### 2.5e Project-scoped API keys(项目级 API key · Feature F12 · D24)

替代此前外部脚本 / CI 借用集群级 `CONSOLE_TOKEN` 触发 run 的用法——那种用法一旦
泄漏波及整个集群、且不可单独撤销(D24 明确否决)。API key 是**绑定单个
project、可单独撤销**的自动化凭证:`Authorization: Bearer jck_<64 hex>`,
resolvePrincipal 按 `jck_` 前缀识别后以 SHA-256 查表命中,构造一个**权限上限为
该 project 的 Member** 的 scoped principal——既不能碰其它 project,也不能升到
该 project 的 owner 面(含管理 API key 自身)。迁移 `0020_api_keys`。

**凭据纪律(CLAUDE.md fail-visible / 红线 2)**:`api_keys` 表只存
`key_hash`(sha256 hex,与 `sessions.token_hash` / `runs.token_hash` 同一套
单向哈希)和 `prefix`(明文前 8 位,如 `jck_a1b2`,仅供列表辨识、不可用于认证)。
**明文只在创建响应里出现一次**,此后无任何读回路径——即便是持有该 key 的调用方
自己也无法再次查看。

API key 对象(`GET`/`DELETE` 响应体;`POST` 额外带 `key`):

```json
{
  "id": "ak-1a2b...",
  "project_id": "p-9f2c...",
  "name": "ci-bot",
  "prefix": "jck_a1b2",
  "created_at": "2026-07-09T00:00:00Z",
  "last_used_at": "2026-07-09T08:30:00Z",
  "revoked_at": null,
  "key": "jck_a1b2c3d4...(仅 POST 响应体含此字段,且只出现这一次)"
}
```

| 端点 | 角色 | 说明 |
|---|---|---|
| `GET /api/v1/projects/{id}/apikeys` | owner | 该 project 的 key 列表(`{api_keys:[...]}`,含已撤销的,便于查历史/状态);响应体永不含 `key_hash` 或明文 |
| `POST /api/v1/projects/{id}/apikeys` | owner | `{name}`(必填,空/空白 → `400`);服务端生成明文、哈希后落库,`201` + 上面的对象**含明文 `key`**——这是唯一一次能看到明文的机会 |
| `DELETE /api/v1/projects/{id}/apikeys/{keyID}` | owner | 撤销(置 `revoked_at`),**立即生效**——下一次用该 key 认证即 `401`;重复撤销幂等返回 `200`;`{id}` 属于另一 project 时 `404`(不暴露该 id 是否存在于别处) |

**scoped principal 权限矩阵**(`api/principal.go` `effectiveRole` 的核心安全约束):

| 动作 | 结果 |
|---|---|
| 本 project 内的 member 级动作(run create/retry/resume、session messages/finish、review、member 可见的读:runs/services/events/diff) | 允许,等同一个真实 member |
| 任何**其它 project** 的任何动作(读或写) | `403`(不是 `404`——与既有 authorizeProject 对非成员的处理一致) |
| 本 project 的 **owner 级**动作(project 设置/成员/集成/schedule/kanban 管理、service 增删) | `403`——RoleMember 永远不满足 RoleOwner 闸 |
| 集群管理面(`/api/v1/system*`) | `403`——scoped principal 永不是 cluster-admin |
| **管理 API key 自身**(即上面三个 `apikeys` 端点) | `403`——防止 key 自我续期/升权(D24 红线) |
| **枚举全部署用户** `GET /api/v1/users` | `403`——用户目录是跨 project 的(会泄漏全体用户 + 识别 cluster admin);scoped key 无权 |
| **枚举 provider 仓库** `GET /api/v1/providers/{provider}/repos` | `403`——建库/onboarding 面;scoped principal 无 linked identity,放行会落到集群 `GITEA_TOKEN` bot fallback 从而枚举整个 org 的仓库(跨租户凭据泄漏) |
| `GET /api/v1/me` | `200`——返回**最小诚实身份**:`kind:"api_key"`、`scoped_project_id`、`role:"member"`,`user` 为不含 id/非 admin 的占位、`identities:[]`;**不 panic、不伪造 user**。三/四种 principal(user/service/api_key)均 200,仅未认证 401 |
| `GET /api/v1/projects` | 只返回该 key 绑定的那一个 project(不是集群全量、也不是空列表) |

**授权按资源真实 `ProjectID`,不看 URL 形状(anti-IDOR)**:`GET /runs/{id}`、
`POST /services/{id}/runs`、`GET /services/{id}/runs` 等按 URL 里 `{id}` 定位的
run/service,其归属 project 从 store 读出后交给 `authorizeProject`——所以拿 A 的
scoped key 去打 B 的 run/service id 一律 `403`(经典 IDOR-by-id 被真实 ProjectID
挡住,而非 URL 前缀)。

`last_used_at` 在每次成功认证后**尽力**刷新,但做了节流(默认 1 分钟窗口内不重复
写)以避免一个被高频调用的 key 把每个请求都放大成一次数据库写。

`CONSOLE_TOKEN` 行为不变(仍是虚拟 cluster-admin service principal)——**外部 /
CI 集成应改用 project-scoped key**,而不是继续借用集群级 token。

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
| `run.session` | runner(**F9a/F9b**,session) | `{ "acp_session_id": "…", "resumed": false }` | ACP session 建立(`session/new`,`resumed=false`)或重载(`session/load`,`resumed=true`)后发一条。ingest 钩子把 `acp_session_id` 落到 `runs.acp_session_id`(**first-writer-wins**,仅当仍为空才写,故续聊 run 预填的 id / 重发均为幂等空操作),**不改 status**。console 渲染低调系统行「Session established / resumed」。仅 session run(及 resumed 的单发 run)会发 |
| `user.message` | orchestrator 内生(**F7 / D22**,session) | `{ "prompt": "接着把测试补上", "by": "Ada" }` | `POST /runs/{id}/messages` 落队列的同时 append;console 渲染为用户消息气泡,时间线读起来是连续对话。`by` 为触发者 display name(service principal 为空) |
| `session.finish` | orchestrator 内生(**F7 / D22**,session) | `{ "reason": "user", "by": "Ada" }` | session 被收尾:`reason` ∈ `user`(finish 端点)/ `idle_timeout`(reconciler 空闲回收)。紧凑系统行渲染 |
| `agent.permission_request` | runner(**F8a/F8b**,`permission_mode=approval` session) | `{ "request_id": "…uuid…", "tool_call_id": "c2", "title": "Run \`make deploy\`", "options": [ { "option_id": "allow_once", "name": "Allow", "kind": "allow_once" } ] }` | agent 的一次权限请求被转发待审批。runner **同步直发 + at-least-once**(先于决议轮询送达;重发幂等)。ingest 钩子按 `request_id` upsert `run_permissions` 台账行(重复事件**绝不**重置已决议状态);console 渲染 PermissionCard(title + options 按钮组)。**该事件可能先于它引用的 `agent.tool_call` 到达——按 `request_id` 键控配对,勿依赖事件相邻性** |
| `agent.permission_resolved` | runner(**F8a/F8b**,同上) | `{ "request_id": "…", "option_id": "allow_once", "resolution": "user" }` | 请求的最终结果:`resolution` ∈ `user`(用户决议生效)/ `timeout`(无人应答,runner 选 reject 类 option 兜底;`option_id` 可为 `""` = 无 reject option 时的 ACP Cancelled)。ingest 钩子落 `resolved_*` 字段(first-writer-wins);之后 §5.5 对该 request 回 `410` |

- `payload` 除上述约定键外可含额外键,客户端应容忍未知键。
- `agent.tool_call` 与 `agent.tool_result` 建议以 `call_id` 关联配对渲染;
  `agent.permission_request` 与 `agent.permission_resolved` 以 `request_id`
  配对(F8b,console 折叠成一张 PermissionCard)。

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
  **session run(F7)每轮有新变化都会重传累计 diff / bundle,upsert 语义保证
  最新一轮覆盖旧内容**;bundle 上传(`POST .../bundle`)同为 per-run upsert,
  且对 session run 每次上传递增 `bundle_rev`,驱动 reconciler 的逐轮 push
  (首轮开 draft PR,后续轮 ff-only 推进同一分支,绝不新开 PR/force-push)。

响应 `201 Created`:

```json
{ "kind": "diff", "bytes": 214 }
```

- 上报后 orchestrator 内生一条 `run.artifact` 事件推给 SSE 订阅者。

### 5.3 `POST /internal/v1/runs/{id}/turn-complete` — session 轮完成上报(F7 / D22)

仅 session run 有意义(单发 run 调用为无害空操作)。请求:

```json
{ "turn": 1, "stop_reason": "end_turn" }
```

- **消费(两阶段投递的 phase 2)**:把当前 offered 未 consumed 的消息标
  `consumed_at`——这条消息发起的轮已经跑完,投递闭环;无 offered 消息(首轮
  `TASK_PROMPT` 场景)为无害空操作。
- 将 run 从 `running` 置为 `awaiting_input`,首次进入时打 `awaiting_since`
  (空闲回收计时起点);orchestrator append 一条 `run.status(awaiting_input)`。
- **幂等**:重复上报(网络重试)无消息可再消费、不重置 `awaiting_since`,回 `200`。
- run 已并发转终态/取消时同样回 `200`(runner 随后从 next-prompt 的 `410`
  得知收尾),**绝不以 4xx 打死 runner**。

响应 `200`:`{ "status": "awaiting_input", "turn": 1 }`。

### 5.4 `GET /internal/v1/runs/{id}/next-prompt` — 长轮询取下一条消息(F7 / D22)

服务端 **hold ≤ 25s**(必须显著小于 runner 侧单请求超时 35s):

| 状态码 | 含义 | runner 行为(acpdrive) |
|---|---|---|
| `200` | `{ "message_id": "…", "prompt": "…" }` — 取到下一轮输入 | 同一 ACP session 上再发 `session/prompt` |
| `204` | hold 期满暂无消息 | 立即(≥250ms 下限)再次轮询 |
| `410` | session 已 finalize(用户 finish / 空闲超时)或 run 已终态 | **优雅收尾,exit 0**(Job 成功 → run `succeeded`) |

**投递语义(两阶段 offer/consume,服务端单侧保证,runner 契约不变)**:

- **offer(phase 1)**:取到消息即打 `offered_at` 并把 run 置回 `running`
  (append `run.status(running)`)。
- **幂等重发**:只要该消息还未被 consume(§5.3),**每次 re-poll 都原样重发同
  一条**(同 `message_id`/`prompt`)——acpdrive 只在轮与轮之间轮询,re-poll 即
  证明上一次响应在网络上丢了、并没有开出一轮,所以重发绝不会双投;"响应丢失后
  run 卡在 running 而无活跃 turn" 的僵局由此自愈。
- **consume(phase 2)**:下一次 turn-complete 把 offered 消息标 `consumed_at`,
  之后的 poll 才会 offer 队列里的下一条;已 consume 的消息(含 orchestrator
  重启后)绝不重发。
- 消息按 `seq` 升序逐条投递;`running` 期间排队的消息在下一轮 poll 被取走;
  finalize/终态永远优先于队列中的消息(直接 `410`)。
- runner 对 turn-complete / next-prompt 连续失败 >5min 判定控制面丢失,以
  `agent_error` 收尾(F7a 契约)。

### 5.5 `GET /internal/v1/runs/{id}/permissions/{request_id}/decision` — 权限决议轮询(F8a/F8b)

`permission_mode=approval` session 的 runner(acpdrive)为每个转发的权限请求
轮询本端点(客户端 ~250ms 下限,整体受 `PERMISSION_TIMEOUT_SECONDS` 约束;
服务端不 hold,立答):

| 状态码 | 含义 | runner 行为(acpdrive) |
|---|---|---|
| `200` | `{ "option_id": "…" }` — 用户已决议 | 校验 option 在本请求 options 集内后回给 jcode(`agent.permission_resolved {resolution:"user"}`) |
| `204` | pending:尚未决议,**包括未知 `request_id`** | 继续轮询 |
| `410` | 请求已过期:已被 resolve(如 runner 超时先落了 timeout)、或 run 已 finalize/终态 | 等同客户端超时:timeout-deny(选 reject 类 option / Cancelled),**绝不 fail-open** |

> **硬约束(F8a 契约,load-bearing)**:**未知 `request_id` 必须回 `204`
> (pending),绝不 `404`**。`404/410` 只保留给"确实存在过、现已过期/失效"的
> 请求(run finalize/终态、或已 resolve)。acpdrive 只在其
> `agent.permission_request` 事件被 ack(2xx)之后才开始轮询,但
> 404-for-unknown 仍会与服务端内部的任何 ingest 异步竞态,把一个 pending
> 审批瞬间打成 deny——所以由服务端一侧保证。同一约束的另一半:ingest 对
> `agent.permission_request` 的 upsert 失败时**整批回 5xx**(runner 重发幂等),
> 绝不 ack 一个没落库的请求事件。

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
| `REPO_URL` | service.repo(clone url 由 service 推导) | 要 clone 的仓库 |
| `REPO_BRANCH` | service.default_branch | 基线分支(契约扩展项;runner 可用可忽略) |
| `MODEL_BASE_URL` | 生效模型配置(DB 行优先,env `MODEL_BASE_URL` 兜底;见 `internal/modelcfg`) | OpenAI 兼容 provider base URL |
| `MODEL_API_KEY` | 生效模型配置(DB 行加密存储,env 兜底;可为空 = keyless 端点) | 模型 key(MVP 直注入;P3 换 LLM 代理 + temp token) |
| `MODEL_NAME` | 生效模型配置(DB 行优先,env 兜底)。**必填,无 mock 默认**(fail-visible 红线 D14;唯一例外:runner 独立运行时 `START_MOCKLLM=1` 显式声明 mock rig) | jcode 的 `provider/model` 标识;runner 据此为未知 provider 写 `custom_models` 配置项 |
| `ORCH_BASE_URL` | 环境 `ORCH_BASE_URL` | orchestrator 基址,runner 回传事件/产物用 |
| `RUN_TOKEN` | 每 run 随机生成 | Bearer,仅本 run 有效;打 `/internal/v1/runs/{RUN_ID}/*` |
| `RUN_SESSION` | `run.session`(**F7 / D22**) | `1` = 多轮 session 模式:acpdrive 每轮后跑 turn-hook(逐轮 diff/commit/bundle)→ `POST turn-complete` → 长轮询 `GET next-prompt`(§5.3/§5.4)。**单发 run 不注入,行为不变**。session run 的 `RUN_TIMEOUT` 与 Job `activeDeadlineSeconds` 改用 session TTL(project `session_ttl_secs`,缺省集群 `SESSION_TTL_SECONDS`=14400s)——`--timeout` 包住整个 session 含 idle 等待 |
| `RUN_PERMISSION_MODE` | `run.permission_mode`(**F8b / D22**) | `approval` = acpdrive 把每个 jcode 权限请求转发审批(§4 权限事件 + §5.5 决议轮询)。**仅 `permission_mode=approval` 的 session run 注入;其余 run(含 full_access session)两个变量都不注入**——runner 缺省即 full_access,行为不变 |
| `PERMISSION_TIMEOUT_SECONDS` | reconciler 计算(**F8b**) | 单个审批等待预算 = `min(300, session_ttl/4)`(下限 1s,TTL≤0 时取 300)。强制审批超时 **≪** session TTL(F8a 要求:整轮阻塞在 RequestPermission 里,超时过大将把一次没人理的审批烧成整 run 的硬 `RUN_TIMEOUT` 失败)。超时后 runner timeout-deny(reject 类 option / Cancelled),run 继续 |
| `GIT_MODE` | project.git_mode(缺 token/host 不匹配时降级) | **(ST-1)** `readonly`(默认,diff-only)\| `draft_pr`(推分支) |
| `GIT_BRANCH` | `agent/run-<RUN_ID>` | **(ST-1)** `draft_pr` 时要创建/推送的命名分支 |
| `GIT_PUSH_URL` | `provider_url`(或 `GITEA_URL`)+ `provider_repo` | **(ST-1)** https 推送 origin,如 `http://gitea.../owner/repo.git` |
| `GIT_TOKEN` | 环境 `GITEA_TOKEN` | **(F1/ST-1)** provider token,作为 https userinfo。用于 **(a) CLONE 私有仓库**(`readonly` **和** `draft_pr` 都注入——私有仓库要能被 clone 才能被 READ)**和 (b) `draft_pr` 推分支**。仅命令行传参,不落盘,**不打日志**(clone/push URL 与 git stderr 均脱敏后才输出) |
| `GIT_BASE_BRANCH` | service.default_branch | **(ST-1)** PR base 分支(信息性;PR base 实际由 orchestrator 设) |

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
     `base` = service.default_branch,`head` = `agent/run-<id>`。
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

## 6b · `@jcode` 评论 webhook 接收端(三 provider · F13 / D24)

> 决策 D24 + 蓝图 §8(把 M7 的 gitea 验签/映射/去重/回帖语义平移到 github/gitlab)。
> **只在配置了 `WEBHOOK_SECRET` 时注册这三条公开路由**(缺 secret → 三条路由都
> 404,`@mention` 触发关闭,系统照常运行)。三 provider **语义完全一致**,只有传输层
> (验签方式、事件头、payload 字段名)与"回帖凭据来源"不同。

### 端点 / 事件 / 验签矩阵

| provider | 端点 | 验签 | 触发事件(事件头) | PR/评论字段 |
|---|---|---|---|---|
| gitea  | `POST /webhooks/gitea`  | `X-Gitea-Signature` = hex(HMAC-SHA256(body, secret)) | `issue_comment`(`X-Gitea-Event`),`action=created` 且 `issue.pull_request` 非空 | `repository.full_name` / `issue.number` / `comment.id` / `comment.user.id` |
| github | `POST /webhooks/github` | `X-Hub-Signature-256` = `sha256=`+hex(HMAC-SHA256(body, secret)) | `issue_comment`(`X-GitHub-Event`),`action=created` 且 `issue.pull_request` 非空 | 同 gitea(payload 结构一致) |
| gitlab | `POST /webhooks/gitlab` | `X-Gitlab-Token` **恒等比较** secret(GitLab 不签 body) | `Note Hook`(`X-Gitlab-Event`),`object_attributes.noteable_type=MergeRequest` | `project.path_with_namespace` / `merge_request.iid` / `object_attributes.id` / `user.id` |

- **验签失败(签名不符 / token 不符)→ `401`**,是唯一的硬失败;其余一切
  (非目标事件、非 PR/MR 评论、无 `@jcode` 命令、重投递)一律 `200` no-op,delivery
  日志带 `{"status":"ignored: …"}` 说明,重投递永不报错回 provider。
- gitlab 的 `merge_request.iid` 收敛为内部统一的 PR number;`path_with_namespace`
  收敛为 `owner/name`——各 provider 的字段差异在各自解析函数里归一到同一中间结构
  (`webhookMention`),下游派发逻辑三 provider 共用一条代码路径。

### `@jcode` 命令与派发语义(与 M7 gitea 完全一致)

- `@jcode review` → 对该 PR/MR 建 **kind=review** run。
- `@jcode <任务文本>` → **kind=agent** run,基线=PR/MR head 分支,产出推回同一分支
  (update-push,不新开 PR);run 预填 `pr_url`/`pr_number`/`pr_head_branch`。
- **身份映射硬门槛**:评论者 provider uid → `user_identities` → jcloud 用户;映射不上
  → 可见回帖提示"用该 provider 登录 console",不建 run。
- **project member 校验**:非 cluster-admin 须是命中 service 所属 project 的 member+
  (viewer 不够);不满足 → 回帖"找不到你能跑的 project"。
- **host gate / model gate**:与 run 创建路径同源的 fail-visible 回帖(白名单收紧 →
  回帖点名 host;无 LLM / 未选默认模型 → 回帖并指向 console);瞬时错误回帖
  "temporary,请重试",绝不误报为"未配置"。
- **受理成功**:回帖 `🚀 jcode run started — <CONSOLE_URL>/runs/<id>`。

### 去重键(`origin_comment_id`)

- gitea 保持**裸数字** `comment.id`(向后兼容 M7 已落库的 run)。
- github/gitlab **带 provider 前缀**:`github:<id>` / `gitlab:<id>`——避免不同 host 的
  数字评论 id 恰好相等时跨 provider 误判为重复。
- 重投递(同 comment 多次投递)→ 前置 `GetRunByOriginCommentID` 命中即 no-op;并发
  漏检时 `origin_comment_id` 唯一索引在 `CreateRun` 兜底,**至多一条 run、一条回执**。

### 回帖凭据来源(三 provider 的唯一实现差异)

- **gitea**:回帖/读 PR 用全局 PAT(`GITEA_TOKEN`,与仓库无关),故"找不到 project /
  身份映射不上"也能回帖。
- **github/gitlab**:无集群级 PAT,回帖/读 PR 凭据取自**该仓库上某个 service 绑定的
  integration bot token**(取第一个能解出凭据的 service);仓库上无 service / 无可用
  integration 凭据 → **无法回帖 → log+忽略**(诚实降级,绝不假成功)。

### 自动注册(`ensureServiceWebhook`,幂等)

- 同时配置 `WEBHOOK_URL` + `WEBHOOK_SECRET` 时,创建 provider service 会 best-effort
  自动注册评论 webhook(三 provider 都生效;失败仅记日志,不影响创建)。
- **每 provider 的 hook URL 从单个 `WEBHOOK_URL` 推导**:`WEBHOOK_URL` 指向
  `…/webhooks/gitea`(部署约定),github/gitlab 为同一 orchestrator 的兄弟路径,注册时
  把结尾 `/webhooks/<known>` 段替换为 `/webhooks/<prov>`(单 env 部署三 provider 通吃,
  无需改 manifest)。
- **幂等**:先列仓库现有 hooks,已存在同 target URL 的 hook 则跳过(secret/token 读回
  被 provider 掩码,URL 是身份键)。事件:gitea `[issue_comment, pull_request_comment]`;
  github `[issue_comment]`;gitlab `note_events=true`,secret 放 `token` 字段(GitLab
  据此回填 `X-Gitlab-Token`)。注册用 integration bot token(绑定时)或 PAT 回退。

### 测试与 e2e 诚实记录

- 本地 OrbStack rig **无真实 github/gitlab** 可打(只有自托管 gitea);github/gitlab 的
  接收端与注册端以 **httptest 单测**为准(`internal/api/webhook_multiprovider_test.go`、
  `internal/provider/webhookregister_test.go`):覆盖两 provider 各自的验签失败 `401`、
  非 PR/MR 评论忽略、两种 kind、身份映射不上、member 校验拒绝、去重(前缀键)、model
  gate 回帖、注册幂等、payload 字段差异(github `full_name` / gitlab `iid`)。gitea 仍走
  e2e `j6-webhook.sh`(§8 验收)。

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
