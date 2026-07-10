# 02 · 决策账本

三轮 + 补充对话锁定的架构决策。每条含:决策、选择、理由、被否方案。

---

## 第一轮 —— 骨架

### D01 · 控制面归属 → 全新独立 orchestrator
不绑在 jtype-web 上,自建独立控制面,拥有 `projects`/`runs`/调度,向下用 HTTP+MCP 驱动 jcode runner,向侧用 MCP/webhook 和 jtype 看板双向打通。
- **被否**:扩展 jtype-web(Rust)——虽继承现成租户/OAuth/MCP,但被判定为耦合过重;扩展 jcode Go web(无多租户)。

### D02 · 技术栈 → Go
与 jcode agent 同语言 → 直接复用 `internal/config`、`internal/model`、`internal/session`、`handler` 接口;K8s 调度用官方 client-go;团队一套技能。**副作用:orchestrator 与 agent 同语言,原本最大的跨语言运维面几乎消失**,只剩"和 jtype 用 MCP/HTTP 对接"。
- **被否**:Rust(可搬 jtype-web 的 OAuth/租户/webhook,但与 agent 跨语言、K8s 生态弱);Node/TS。

### D03 · 身份/租户 → 外部 OIDC(Keycloak / 企业 SSO)
orchestrator 作 OIDC relying party;device flow 从 Keycloak 来给 CLI/runner;天然打通钉钉/飞书 SSO。OIDC org/group → orchestrator 的 tenant/project。
- **被否**:复用 jtype 当 IdP;控制面自建 auth。

### D04 · Runner 隔离/调度 → K8s Job per active issue
每个 active issue 一个(长活)worker Job;pod 边界 = 租户/安全边界。**K8s 本身即 supervision tree**(替掉 Symphony 的 Elixir/BEAM)。
- **被否**:常驻 outbound runner 热池(jbrowser 模式,启动快但跨租户需擦除、有状态泄露风险);复用 jcode DockerExecutor attach(只 attach、不解决供给/隔离)。

### D08 · Git 集成深度 → 默认 draft PR/MR,可退只读
默认 clone → 推 namespaced 分支(`agent/*`)→ 开 draft PR/MR,人工 review 迭代;敏感项目可配置为只返回 diff。**硬把关:不自动 merge、不自动 CI、密钥隔离**(与 Copilot/Codex/Symphony 三家一致)。draft PR 同时是唯一工作追踪产物。
- **被否**:只读(Codex apply 模式);全写不给退路。

### D11 · 看板角色 → 触发源 + 回写 sink 都要
API/webhook 是规范触发源,看板是众多"触发 + 回写"适配器之一。agent 用 jtype MCP 自己回写卡片。你的 `agent-orchestration-design.md` 作为"看板即队列"适配器落地。

---

## 第二轮 —— 独立控制面打开的新分叉

### D02(细化)· 见上。

### D03(细化)· 见上 —— 选了外部 OIDC。

### D10 · BYOK 密钥 → 控制面 LLM 代理 + sandbox 拿短期 temp token
> 初版曾是"明文 env 注入 agent 阶段(+ egress 白名单补救)",第三轮细化后**修订**为下:

控制面跑一个 **LLM 代理**,持有真 key + endpoint;按 run 签发**短期 temp token** 给 sandbox。agent 在 sandbox 里用 temp token 调代理,**真 key 与 endpoint 永不出控制面**。即使 sandbox 被彻底攻陷,也只有一个限流、可撤销、短 TTL 的代理 token,不是真 key。因此不需要 engine/sandbox 双 pod 拆分——单 sandbox pod 即可。
- **被否**:明文 env 注入 sandbox(key 待在跑生成代码的 pod 里);独立可信 engine pod 驱动 sandbox(2 pod/run,过重);vault 实时拉取。

### D09 · Provider 顺序 → Gitea 优先
Gitea 先(GitHub 式 API,较简单),然后 GitHub + GitLab 一起补。

### 首批集成优先级(多选)
Gitea 优先 → GitHub + GitLab;控制台 Run 按钮 / CLI;Git webhook @mention(先 GitHub 之外亦 Gitea);jtype 看板卡片拖动;钉钉 / 飞书 bot。落地顺序见 [06-reuse-roadmap-risks.md](06-reuse-roadmap-risks.md)。

---

## 第三轮 —— Symphony 重新打开的分叉

### D07 · 编排契约 → 采用 Symphony SPEC
在 Go 里实现 [Symphony](https://github.com/openai/symphony) 协议,把 **jtype 看板做成 Symphony 兼容 tracker**(看板列 = `active_states`/`terminal_states`)。拿到 OpenAI 内部验证过的状态机 / 退避 / stall / blocked / reconcile 语义,并可与生态互操作。你自己的 `agent-orchestration-design.md` 基本是它的子集。
- **被否**:借鉴但自定义;直接跑 Elixir 参考实现(与 Go+K8s 割裂)。

### D06 · 状态/恢复 → 幂等 reconciler + Postgres
像 K8s controller:run/claim 落库(Postgres),但逻辑设计成可靠 reconcile,重启靠重读看板 + DB 恢复。兼顾 Symphony 的韧性,又给 API/webhook/CLI 触发的 run 一个落脚处。
- **被否**:Symphony 式无 DB(全内存,重启丢 retry timer/live session、非看板触发的 run 无处存);DB 支撑的租约队列。

### D05 · 执行模型 → 长活 worker + per-issue 持久 PVC
每个 active issue 一个长活 worker Job,连跑多轮 turn(Symphony 续跑);workspace 用 **per-issue PVC** 跨 pod 重启存活,恢复最快、最省 clone。
- **代价/护栏**:要管 PVC 生命周期 + 跨租户擦除。**注意:PVC 是运行期工作副本,不是权威副本**(权威副本在控制面 Store,见 D12)。
- **被否**:纯 ephemeral(一 Job 一轮,续跑慢);长活 worker + pod 本地盘(崩溃重排要重 clone)。

---

## 补充 —— 存储 / 记忆 / 同步

### D12 · 会话/记忆存储 → 改造 jcode 成可插拔 Store,云后端 = orchestrator 自有 store
把 jcode 写死在 `~/.jcode/` 的 `session.Recorder` 与 `internal/memory` 抽成可插拔 `Store`(`LocalStore` / `RemoteStore` 两实现)。云端后端 = **orchestrator 自有 store(Postgres + 对象存储)**,不依赖 jtype。memory 两 scope(`project (tenant,proj)` + `global (tenant)`)存租户级;会话 transcript 进对象存储、控制面建索引;PVC 仅运行期工作副本。**机制上选 pluggable storage,而非外挂 hook**(单一写路径;实时 UI 走已有 `AgentEventHandler`)。详见 [03-storage-memory-sync.md](03-storage-memory-sync.md)。
- **被否**:memory 存进 jtype 复用其 local-first 同步(更缝生态,但引入外部依赖);先全进 orchestrator 后期迁 jtype。

### D13 · local↔cloud 同步 → 在 orchestrator 内自建
借鉴 jtype 的 lamport clock / sync cursor 概念,但在 orchestrator 内自建、不依赖 jtype。session 是 append-only 日志(按 `(session_id, seq)` 取并集,近乎无冲突);memory 是小结构化 notes(按 note key LWW + 控制面蒸馏 pipeline 当合并权威)。同一个 Store seam 支撑本地/云端一致。

---

## 补充 —— 产品体验红线

### D14 · 未配置依赖 → fail-visible,禁止静默 mock
任何未配置的依赖(LLM/provider/webhook)都是**一等公民状态**:API 返回带类型错误(`model_not_configured`),UI 禁用对应操作并给出去向提示,自动化路径可见地回帖原因。mock 实现只允许在测试/显式 e2e rig 中由脚手架显式接线,**永不作为产品 manifest 的默认兜底**。LLM 配置支持管理员在 Cluster 页自助填写(DB 存储,key 用 AUTH_TOKEN_KEY 加密),env 仅作显式覆盖。
- **被否**:base configmap 默认指 mockllm(生产静默跑假 agent,用户误判 AI 已生效——真实事故);"没配就报 500"(不可操作,无去向)。

---

## 补充 —— Feature B 项目护栏

### D15 · project 护栏落地 → 前缀保留 env + PATCH presence 语义
让 project 级护栏(`max_concurrent_runs` / `run_timeout_secs` / `provider_allowlist` / `injected_env`)真正在 reconciler 与 API 生效时,定了两个 precedent 决策:

**(a) injected_env 黑名单按 NAMESPACE 前缀保留,而非逐 key。** 保留集是前缀族(`RUN_` / `MODEL_` / `GIT_` / `PR_` / `REPO_` / `MOCK_` / `LD_` / `DYLD_`)+ 一批 exact keys(`ORCH_BASE_URL` `TASK_PROMPT` `SOURCE_MODE` `BASE_BRANCH` `BRANCH_NAME` `WORKSPACE` `OUT_DIR` `HOME`,以及执行劫持向量 `PATH` `NODE_OPTIONS` `PYTHONPATH` `PYTHONSTARTUP` `BASH_ENV` `ENV` `SHELLOPTS` `BASHOPTS` `IFS` `PERL5LIB` `RUBYOPT`)。前缀保留使未来同族系统变量自动受护,无需再迁移;两类威胁——契约覆盖(改 `RUN_TOKEN`/`MODEL_NAME` 破坏鉴权/fail-visible 模型闸)与执行劫持(runner 按名调 git/jcode/orchclient,orchclient 持 RUN_TOKEN;`PATH`/`LD_*`/`DYLD_*`/解释器 bootstrap 可换绑二进制)——都被挡在 **PATCH API 层**(400 `reserved_env_key`,指名违规 key),reconciler 注入时再防御性过滤 + log.Warn(双保险)。唯一真源在 Go(`domain/env.go` 的 `ReservedEnvPrefixes`/`ReservedEnvKeys`),console(`src/lib/env.ts`)镜像;两侧由 checked-in golden(`domain/testdata/reserved_env.txt`)+ 双侧测试钉死——改一边不改另一边测试即红。
- **被否**:逐 key 黑名单(漏一个未来 key 即破防);注入时静默丢弃保留 key(违反 fail-visible 红线,用户不知道自己设的 key 没生效);injected_env 值对所有角色可见(泄漏 owner 存的密钥给 viewer/member——已改为**仅 owner** 在 project view 拿到 injected_env value)。

**(b) PATCH /projects presence 语义:omitted=不变,显式 null/≤0=清空回落继承。** 请求体解成 `map[string]json.RawMessage` 做存在性判定——省略的字段保持不变(改名 PATCH 绝不误清护栏),显式发 `null` 或 ≤0 的数值把该护栏清回"继承集群默认"(view 里 omit,console 显示 "cluster default" 占位)。字段名匹配大小写不敏感(沿用旧 stdlib struct decoder 语义,`{"Name":...}` 仍改名),但未知字段仍 400 拒绝(repo 配置只走 service 端点)。
- **被否**:整体覆盖语义(改名会连带清空未发送的护栏);sentinel 空串/零值区分不清 null 与"未发"。

**顺带的护栏红线取舍**:`run_timeout_secs` 同时驱动 runner 内部 `RUN_TIMEOUT` 与 Job `activeDeadlineSeconds`,但两者**不相等**——Job 硬截止 = RUN_TIMEOUT + grace(`max(120, timeout/10)` 秒),因为 activeDeadlineSeconds 从 pod 启动计时(含 clone/setup)且要给 runner 自身优雅超时留窗,否则 k8s 会在 runner 内部超时前 SIGKILL,丢掉 `timeout` 失败分类与 diff.patch/REVIEW.md。`provider_allowlist` 里 raw 仓用显式 `"raw"` 标识;闸点状态码统一:建 service 用 400(可改的输入),已有 service 的 run/retry/review 派发用 403(既有状态上的策略拒绝),webhook 路径可见回帖原因。reconciler 里 project 加载失败**不静默降级**——不启动、下 tick 重试(与调度处一致)。

---

## 补充 —— Feature D LLM 反向代理(落地 D10 / O5 第一半)

### D16 · 真实 API key → 控制面进程内反向代理,key 永不进 pod
runner 的 `MODEL_BASE_URL` 不再指真实 LLM,改指 orchestrator 进程内的反向代理端点 `/internal/v1/runs/{id}/llm/{rest...}`(复用 `s.runToken` 中间件鉴权——path 的 `{id}` 与 token 绑定);`MODEL_API_KEY` 即 `RUN_TOKEN`。代理在转发时复用 Feature A 的 `modelcfg.Resolver` 缓存解析生效模型配置、注入真实 `Bearer <realkey>`、用 `httputil.ReverseProxy`(`FlushInterval=-1` 逐 chunk flush 透传 SSE)把请求转发给真实 LLM。**真实解密后的 key 只存在于 orchestrator 进程内存 + 加密的 `cluster_model_config` 表**,永不进 pod env——仓里的 prompt 注入无法偷走它。这是 D10 架构意图(O5)的"控制面 LLM 代理"第一半;D10 的"沙箱拿短期 temp token"路线留作后续(见下方 TODO)。
- **始终走代理,不加 `LLM_PROXY` 开关**:严格更安全、少一个旋钮;e2e rig 也经代理打到 mockllm(多一跳内网,可忽略)。被否:加开关默认 on(多一条静默降级的退化路径,违背 fail-visible 红线)。
- **`/v1` 归一方案(稳)**:runner 的代理 base **不含 `/v1`**(`${ORCH}/internal/v1/runs/{id}/llm`);entrypoint 统一给 `MODEL_BASE_URL` **末尾补 `/v1`**(已以 `/v1` 结尾则原样)再写进 jcode config——代理与非代理(`START_MOCKLLM` / standalone)路径同一套规则。jcode 按"base 已含 /v1"约定只追加 `/chat/completions`,于是请求落到 `.../llm/v1/chat/completions`;代理从 path 取 rest(=`v1/chat/completions`,含 `/v1`),转发目标 = `stripTrailingV1(真实 model.BaseURL) + "/" + rest`——真实 base 末尾的 `/v1` 被剥、rest 带回 `/v1`,无论 admin 把 base 配成带不带 `/v1` 都不双 `/v1`、都对。`stripTrailingV1` 对不带 `/v1` 的 base 是 no-op,完全透明。该方案由 `TestLLMProxyForwardsBaseWithV1` / `...WithoutV1` 双向钉死。
- **fail-visible 运行期闸**:`model_not_configured` → 类型化 **503**(不假装成功);resolve err → 502;upstream 不可达 → 502。`createJob` 的 Feature A 闸门保留(排队期间配置被清→MarkFailed),代理是运行期兜底(run 已起跑后配置被清→代理 503→runner 报错退出→收敛 failed)。`ORCH_BASE_URL` 空时**不启动、留队列**(生产由 `config.Load` 强制非空;dev/API-only 防御)。
- **安全细节**:入站 `Authorization`(RUN_TOKEN)在 `Rewrite` 里**先删后设真实 key**,绝不透传;`http.Transport` 只限 dial/header 超时,**不限响应体超时**(SSE 可流式数分钟);日志只记 method/run/status,**绝不**记 key 或 body。
- **本期不做(留 TODO,对应 O5 temp-token 路线)**:代理不记用量/审计、不签发独立短期 temp token(`MODEL_API_KEY` 暂复用 RUN_TOKEN)、不做 per-run 速率/配额。后续做 temp-token 化时,把 `jobEnv` 的 `MODEL_API_KEY` 换成代理签发的短期凭据,代理侧凭据表加 TTL + 用量采集即可。
- **被否(整体)**:继续直接注入真实 key(prompt 注入可偷,违背 O5);sidecar 代理(多一个进程/镜像/故障面,而进程内代理零新增部署成本)。

---

## 补充 —— Feature E jtype 看板集成（落地 D11 "看板 = 触发源 + 回写 sink"）

### D17 · 看板双向打通 → 轮询（非 webhook/SSE）+ claim 幂等 + claim 承载回写

架构愿景（docs/01）："拖卡片到指定列 = 派一个 AI run；run 完成后回写卡片"。落地时的关键取舍：

**(a) 入站用轮询，不用 webhook/SSE。** jtype 出站 webhook 有 SSRF 防护（target 强制 https 且拒内网 IP），orchestrator 在内网收不到；board SSE 事件流只放行 `full`-scope token（不接受 mcp PAT）。所以 orchestrator 主动按 `updatedClock` 增量轮询 jtype 文档列表（`GET /workspaces/{ws}/documents`）—— level-based、幂等、重启无缝恢复（claim 表 + 内存游标，游标仅为优化，重启从 0 重扫由 claim 去重兜底），契合现有 reconciler 哲学。一个 mcp-scope PAT（editor 权限）覆盖该实例所有 workspace 的全部读写。"移动到某列" = 改卡片 frontmatter `status`（`POST .../documents/save`）；列变更无独立事件，轮询天然覆盖。

**(b) 幂等单元 = `kanban_claims(link_id, document_id)`，且 `run_id` 留空可重试。** UNIQUE(link_id, document_id) 保证一张卡每个 link 至多派一次 run。`run_id` 在派发成功后才 stamp，于是"LLM 未配置"时（fail-visible 闸）：claim 已建立但 `run_id` 为空 → 不派 run、只发**一次**"LLM 未配置"卡片评论（`notified_not_configured_at` 节流）→ 下个 tick 继续重试 → 管理员配上模型后**自动补派**，且不刷屏。这同时满足 fail-visible + 可恢复 + 零垃圾评论，比"永久 claim 掉"（不可恢复）或"cooldown"（重启丢、重复评论）都好。回写幂等由 `writeback_at`（first-writer-wins）保证，模式沿用 reconcileReviews（AddComment 后 stamp；DB 错误导致的极小概率重复评论被接受）。

**(c) 文档 id/path 放在 claim，不给 runs 表加列。** 回写需按 run 找卡片：`ListKanbanRunsAwaitingWriteback` join claims→runs→links，claim 已持 document_id/document_path，无需改 runs schema（仅扩 origin 枚举到 `'kanban'`，origin 列早由 0008 存在）。`Run.Origin=kanban` 仅作可追溯性标记 + console 展示。

**(d) 回写策略：succeeded 才自动移列，failed/canceled 只评论不动卡。** done_column 的语义是"完成、无需再看"——失败的 run 需要人介入，自动推进到 done/review 会把失败藏起来；故只有 `StatusSucceeded && done_column!=""` 才 MoveCard，failed/cazard 仅贴结果评论、卡留原地。

**(e) PAT 存 env（集群级），不存 DB。** 本期一个集群一个 PAT（对 jtype 实例所有 workspace 有效）进 `orchestrator-secret`（gitignored）；多 link 共享该 PAT（每 link 自己的 workspace/board）。不做 per-link token。

**被否**：jtype webhook（SSRF 挡内网 orchestrator，收不到）；board SSE（需 full-scope token，不接受 mcp PAT）；给 runs 表加 OriginDocID/OriginDocPath 列（claim 已承载，避免无谓改 schema）；失败也移列到 done（藏住失败，反 fail-visible）。

---

## 补充 —— Cloud v2 设计（对标 Claude Code 云端形态，D18–D26）

> 落地顺序、实体模型细节、API 端点草案见 [14-cloud-v2-design.md](14-cloud-v2-design.md)。

### D18 · run 结果语义与时间线渲染

agent run **空 diff 不再等同失败**：runner 退出码 0 但改动为空时，上报 `run.result{outcome:"no_changes"}` 事件，runs 表新落 **result** 列，console 时间线显示"无代码变更"徽标而非报错——agent 以文本回答收尾（对话/分析类任务）本就是合法产出。真失败（agent 异常退出、超时、clone 失败）语义不变；review run 仍强制要求产出 REVIEW.md，不受此放宽。同期 console 时间线做两处渲染修正：连续的 `agent.text` chunk 合并为一个流式消息块（不再一行一气泡），`agent.tool_call`/`agent.tool_result` 按 `call_id` 配对做富渲染。
- **被否**：维持"空 diff = agent_error"——与云端对话形态冲突，用户发一句 "hi" 就会看见一条失败 run。

### D19 · Integration（git 集成）

Integration 是 **project 级实体**，owner 可增删。凭据是 **org 级服务凭据**（Gitea org PAT / GitLab group token，GitHub 先用 PAT），凭据结构带 `type` 字段为将来 `github_app` 留抽象位，加密存储、不可回读。git 操作一律以 **机器人身份** 执行（PR 正文标注真实触发者，保留可追溯性）。service 创建时绑定一个 integration，该 service 的所有 run 都走这份凭据；存量 service 保持"触发者个人 OAuth"路径兼容，不强制迁移。集群级 `GITEA_TOKEN` env 迁移为一条显式 integration 后废弃。member 可基于 project 已有的 integration 直接添加 repo 建 service——建 service 的权限从 owner-only 放开到 member。
- **已知升级面**：member 可以借 bot 凭据触达自己在 git 主机本无权限的 repo。当前公司内网信任环境下接受；多租户化时用 per-integration 开关收紧（呼应 D20 的治理闸口方向）。
- **被否**：共享个人 OAuth token（离职即断、审计混乱）；cluster 目录 + 授权模式（不如 owner 自治贴近实际使用动线）。

### D20 · 治理闸门上收 cluster（部分回退 D15）

D15 定的 project 级 `provider_allowlist` 废弃——owner 自设的约束管不住 owner 自己，形同虚设。改为 **cluster 级 git host 白名单**：cluster-admin 配置允许的 host 列表，integration 创建时校验，空列表 = 不限制。
- **被否**：继续用 project 级 `provider_allowlist`（D15）兜底——owner 既是设约束的人也是被约束的人，闸门不成立。

### D21 · 模型目录 + project 授权 + 简单选择器

`cluster_model_config` 单行表演进为 **多行模型目录**（cluster-admin 管理增删改）；新增 **model↔project 授权表**，被授权的 project 只读可用、不可改、不可见 key。模型选择不做"任务类型 × 模型"矩阵，就是一个 select：service 可设默认模型，composer 派 run 时可从授权列表里临时切换。迁移：存量单行配置 → 目录第一条 + 默认对所有 project 授权。D16 的反向代理架构不变（真 key 永不进 pod），仅 `modelcfg.Resolver` 改为按 **run 所属 project** 解析生效配置。
- **被否**：按任务类型分别配置模型的矩阵式选择器（当前用量撑不起这份复杂度，simple select 够用）。

### D22 · 多轮 session（Job 保活路线，实现 D05 本意，推翻 runner README 的 multi-turn non-goal）

状态机加一个非终态 `awaiting_input`。`POST /api/v1/runs/{id}/messages`（member+ 权限）投递后续消息；runner 侧 `acpdrive` 用 `RUN_TOKEN` 长轮询 `GET /internal/v1/runs/{id}/next-prompt`，拿到新消息就在**同一个 ACP session** 上反复 `session/prompt`，不重开 session。每轮结束照常算 diff/推 bundle（复用既有 update-push 逻辑持续更新同一 PR 分支）；空 diff 的轮次即"纯对话轮"（D18 语义天然覆盖）。新增 project 护栏 `max_live_sessions`（计 `running`+`awaiting_input` 状态的 run 数）与 idle 超时。permission 面：只有 session 模式下把 ACP `RequestPermission` 转发成一个 run 事件，由 console 交互式审批（超时默认拒绝）；单轮 headless（webhook/kanban/schedule 触发）维持现状 `full_access`，不改。
- **被否**：`runner/README.md` 既定的"multi-turn / resumable sessions 不做"non-goal——本决策显式推翻它，是 D05"长活 worker 连跑多轮 turn"本意时隔多轮后的落地。

### D23 · 休眠/恢复三层 + 转录归属（解决 D12 张力）

三层：①**保活**——pod 常驻等 follow-up（D22 的 `awaiting_input`）；②**idle 回收**——超时杀 pod、留 PVC，session 转录**不再擦除**（改掉 entrypoint 现行为），恢复时走 ACP `session/load`（jcode 已支持）重建；③**长期归档**——长时间不用则把 PVC tar 打包送对象存储（S3/MinIO）、删 PVC，恢复时还原展开。转录本身持续同步进控制面 store（D13 的 append-only log），**authoritative 副本在控制面**，PVC/对象存储只是工作副本与冷备——这维持而非推翻 D12"PVC 是运行期工作副本、不是权威副本"的原则。对象存储是一等公民依赖：未配置时归档功能整体禁用并明确提示（D14 fail-visible），不静默跳过。
- **被否**：idle 回收时继续擦除转录（逼所有恢复退化成重新 clone，浪费 D22 刚建好的 session 保活能力）。

### D24 · 触发器扩展

新增 `schedules` 表（service 级 cron 表达式 + prompt 模板）+ 一个 poller tick（仿 D17 kanban poller 的轮询/幂等哲学）。新增 **project 级 scoped API key**（可撤销、hash 存储，权限限定在本 project 内派 run），替代目前 `CONSOLE_TOKEN` 被外部脚本借用、权限过粗的用法。补齐 GitHub/GitLab 的 webhook 接收端，对齐现有 Gitea 实现（§8 的验签/映射/去重模式平移）。
- **被否**：继续用集群级 `CONSOLE_TOKEN` 顶 project 级自动化凭证（一旦泄漏波及整个集群，且不可单独撤销）。

### D25 · jtype link 下放 project 级

kanban link 的管理权从 cluster-admin 下放到 **project owner**；jtype 凭据从集群单一 env 改为 **per-link 加密存储**，集群级 `JTYPE_TOKEN` env 保留作兼容回退（未配置 per-link 凭据时使用）。
- **被否**：继续 cluster-admin 独占 kanban link 管理权（每加一个看板集成都要走 admin，拖慢 project 自助节奏，与 D19 integration 下放 owner 的方向不一致）。

### D26 · 聊天 UI 统一路线 → console 消费已发布的 jcode-ui 包

`jcode-ui` / `jcode-ui-core` 已作为 npm 包发布后，console 不再维护第二套消息、工具卡片、markdown 与 composer 实现：`runview` 只保留 Cloud SSE → `ThreadItem` 的纯适配层，渲染使用 package 的 headless `Thread` + styled `Message` / `ToolCallCard`，多轮 live follow-up 与终态 Continue 都使用 package `ChatInput`。运行中的 Enter 映射为 Cloud durable queue，package Stop 映射为立即 Cancel；`Finish session` 仍是 host action，因为它是“优雅收尾并 succeeded”的 Cloud 产品语义，不等同于 Stop。

**暂留两个显式 host renderer seam**：ACP 请求带任意 `option_id/name/kind`，而 `jcode-ui@0.1.1` 的 `ApprovalBanner` 只有固定布尔 allow/deny；多用户 `user.message.by` 也不能交给 package `Message`（其 generic user 标题固定写成 “You”）。console 通过 headless Thread 扩展点继续渲染 lossless options 并原样回传 option_id，同时用 package markdown/style class 渲染带真实作者名的 user row；待 package 暴露 arbitrary approval options + general author label 后再移除这两个 seam。package 全局 CSS 先于 console tokens 引入，并在 Run 页容器做 scoped token bridge，避免 npm 包重绘整个 Cloud shell。

- **被否**：继续维护 console 自有 Message/ToolCard/ChatInput（重复实现持续漂移）；为追求“100% package 组件”把 ACP option_id 强转成 allow/deny（破坏 wire contract，违反 fail-visible）；把 package 默认橙色 token 直接覆盖到整个 console（污染现有主题）。
