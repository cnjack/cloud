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

### D17 · 看板双向打通 → durable sequence 轮询（非 webhook/SSE）+ claim 幂等 + claim 承载回写

架构愿景（docs/01）："拖卡片到指定列 = 派一个 AI run；run 完成后回写卡片"。落地时的关键取舍：

**(a) 入站用 durable sequence pull，不用 webhook/SSE。** jtype 出站 webhook 有 SSRF 防护（target 强制 https 且拒内网 IP），orchestrator 在内网收不到；board SSE 事件流只放行 `full`-scope token（不接受 mcp PAT）。jtype v22 起提供 `GET /workspaces/{ws}/boards/{board}/events/pull?afterSequence=…`：orchestrator 为每个 link 在 `kanban_links.event_sequence` 持久保存最后**成功处理**的 sequence，按最老事件优先拉取；事件 N 失败时只提交 N 之前的成功前缀，N 与后续事件下一 tick 重放，进程重启也从 DB 游标继续。事件只给 card path，只有页内出现 trigger 状态时才做一次文档列表查询把 path 映射为 document id，再由 `kanban_claims` 兜住重放幂等。迁移 `0028_kanban_event_cursor` 让存量 link 的游标先为 NULL：首次做一次 level scan，保证事件日志上线前已经停在 trigger 列的卡不丢；全部候选处理成功后提交 0 并永久切到 sequence pull。一个 mcp-scope PAT（editor 权限）可完成拉取、读文档和回写。"移动到某列" 仍是改卡片 frontmatter `status`（`POST .../documents/save`），但列变化现在有事务内持久事件，不再用进程内 `updatedClock` 水位猜测。

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

### D27 · jtype 集群配置从 env-only 改为 DB + console 设置（env 保留回退）

kanban 的集群级 jtype 配置（base_url + 可选 cluster fallback token）从"只能改 orchestrator env"改为 **cluster-admin 在 console Cluster 页可设**，落 DB 单行表 `cluster_kanban_config`（仿 `cluster_model_config`：base_url 明文、token AES-256-GCM 用 AUTH_TOKEN_KEY 加密、明文永不回读）。解析顺序 **DB > env**（env `JTYPE_BASE_URL/JTYPE_TOKEN` 保留作兼容回退，延续 D25）；cluster fallback token 与 base_url **同源绑定**（DB 配置只用 DB token，env 配置只用 env token），避免为 env 实例签发的 PAT 静默用于 DB 指向的另一实例。关键改造：新增 `kanbancfg.Resolver`（仿 `modelcfg.Resolver`，TTL + Invalidate，API/poller/reconciler 共享一份），poller 与 writeback **常驻按 tick 解析生效配置**，未配置即可见 no-op —— 于是 console 存下 base_url 后**无需重启**即生效（否则就是静默无效，违反 fail-visible 红线）。GET /api/v1/system 的 kanban 快照改为反映**生效值 + 来源**；token 写入无 cipher → 409 `cipher_not_configured`；DB 有 token 但无 AUTH_TOKEN_KEY → 显式报错，绝不静默回退 env。

- **被否**：只加 DB 表 + PUT 路由而不动 boot 接线（存了不生效，静默 no-op，违反 D14 fail-visible）；cluster token 全局"DB token OR env token"混用（跨实例 PAT 泄漏面）；把 base_url 也留在 env、只把 token 上收（一半在 env 一半在 DB，运维心智割裂）。

### D28 · jtype 凭据接入从手贴 PAT 改为 OAuth 设备流（device flow）"一键连接"

kanban 的 jtype 凭据（集群 fallback token 与 per-link token）从"网页里手动生成 PAT 再粘贴"改为 console 上的 **"Connect with jtype" 设备流按钮**（RFC 8628）。orchestrator 新增 `internal/jtypeoauth` 客户端，只调用 jtype 的两个**免鉴权**端点：`POST /api/oauth/device_authorization`（起流，拿 `device_code`+6 位 `user_code`+`verification_uri_complete`）与 `POST /api/oauth/token`（device_code grant 轮询）。浏览器那一腿（登录+批准）完全由 jtype 现成的 `/oauth/device` 页承担，jcloud 不碰。铸出的 token 与手工 scoped PAT **同族**（`create_scoped_session` scope=`mcp`，90 天，无 `refresh_token`），可直接作 Bearer 打文档 API。

**关键取舍：**
- **不做动态客户端注册**：jtype 的 device grant 完全忽略 `client_id`（`mcp/oauth.rs:247-249,310-367`），故无需 `/oauth/register`、无需持久化 `client_id`、`cluster_kanban_config` 不加相关列。
- **谁来轮询：console 驱动的无状态代理 + 进程内 pending 登记表**，不起后台轮询协程。`device_code` 是可铸 token 的**机密**，绝不下发浏览器；jcloud 用不透明 `connect_id` 在内存里持有它，console 每次 poll 触发**至多一次**对 jtype 的 token 轮询（受 `interval` 闸 + `slow_down` 退避约束）。被放弃的流零服务端开销。**重启即丢流**：下次 poll 返回 `connect_expired`，重连一次即可（10 分钟窗口）；**刻意不把 device_code 落库**。
- **成功即服务端落库，明文永不回浏览器**：poll 命中 `complete` 时 orchestrator 立刻 AES-256-GCM 封存 token、算出 `token_expires_at`、写入 `cluster_kanban_config.token_enc` / `kanban_links.token_enc`（集群侧再 `resolver.Invalidate()` 免重启生效），poll 响应只回 `{status, token_set, token_expires_at}`。比手贴路径更安全。
- **per-link 走"先建链接再连接"**：连接作用在已存在的 link 上（复用 D25 的 per-link token 轮换写路径），目标为**生效的集群 base URL**；建链接表单的明文 token 字段保留（沿用 D25，可接受但非首选）。集群 fallback token 的连接**要求先存好 DB base_url**（D27 同源绑定），未存则 `409 base_url_not_configured`。
- **迁移 0023 只加 `token_expires_at`**（两表，可空）：设备流 token 无 refresh、90 天到期，存到期时间用于 console 主动提示"N 天后过期 / 已过期请重连"；手贴 PAT / env token 记 NULL（未知，诚实）。
- **fail-visible**：base URL 未配 → 按钮禁用给理由；旧版 jtype 无 OAuth 路由 → 类型化 `jtype_oauth_unsupported`，退回手贴，绝不静默；过期/未批准 → 显式 `status`；无 `AUTH_TOKEN_KEY` → 起流即 `cipher_not_configured`。全程无 mock 路径。

- **被否**：动态注册客户端并给 `cluster_kanban_config` 加 `client_id` 列（device grant 根本不校验 client，纯属无用复杂度）；orchestrator 后台轮询 + `oauth_device_flows` 落库（把可铸 token 的 `device_code` 静态存盘、加迁移与清理协程、为被弃流跑忙循环）；把 `device_code`/铸出的 token 回给浏览器由前端轮询（明文过浏览器）；给 per-link 连接做"连接产出 token 塞进建链接表单"（又把明文引回浏览器 state）。

### D29 · console 下拉全面换 Headless UI Listbox（引入首个第三方 UI 组件库）

原生 `<select>` 的弹出层由操作系统渲染、不吃 CSS，暗色主题下观感割裂且无法与设计 token 对齐。console 引入 `@headlessui/react`（v2，唯一第三方 UI 依赖），新增 `components/Select.tsx`（Listbox 触发器 + anchored/portal 选项面板，逃逸 modal 的 overflow 裁剪），`SelectField` 改包装它（children `<option>` API → `options` 数组 API），全部 9 处原生 select 切换。两个行为差异已抹平：Listbox 重选当前项也会发 onChange（原生不会），Select 内部吞掉同值重选，否则角色/默认模型这类 mutation-wired 下拉会发多余写请求；Modal 自动聚焦的查询补上 `button[aria-haspopup="listbox"]`。样式覆盖沿用既有惯例：页面 module class 传 `className` 落在触发器上、靠 bundle 顺序覆盖基础 `.trigger`。代价：bundle +~26 KB gzip（Listbox 静态引 floating-ui / react-virtual）。

- **被否**：Headless UI 的 `Select` 组件（仍是原生弹出层，白改）；自研 listbox（ProjectSwitcher 式手搓，键盘/焦点管理全要自己维护）；`:where()` 降基础类特异性（会被 global.css 的 `button { font: inherit }` 元素选择器穿透）。

### D30 · 修复 kanban link 死链：GetBoard 按名解析 + BoardRef 规范化为 config.id + 死锁软建 + 级联选择器

人类视角实测发现 kanban link 对真实 jtype 根本建不起来、卡片触发跑 run 的闭环全程不可达。四处根因合治（后端 orchestrator，与 console 级联选择器一并落地）：

1. **GetBoard 按名解析（任意路径）**。`jtype/client.go` 原来硬查 `boards/<ref>.board`，而真实 jtype 的 board 是根目录（或任意目录）下的 `<name>.board`、卡片在 `<name>/*.md`、frontmatter 带 `board:<config.id>`。改为镜像 jtype-board-react 的 `resolveBoard.ts`（新增纯函数 `resolveBoardDoc`，可脱 HTTP 单测）：按 basename 大小写不敏感匹配 `<ref>.board`，容忍 `./` 前缀与 `.board` 后缀，精确路径优先于 basename，多命中→类型化 `ErrBoardAmbiguousError`（列候选），零命中→`ErrDocNotFound`。

2. **BoardRef 规范化为 config.id（真正让闭环可达的一步）**。卡片 frontmatter 的 `board:` 是 board `.board` JSON 里那个**随机 `b_xxxxxxxx`**（jtype-web 建板时 `id: b_${random}`），poller 用 `card.Board == link.BoardRef` 字符串比对。因此建链接时把用户填/选的**名字**解析成 board，取 `Board.ID`（`b_…`）存进 `BoardRef`；否则存了名字、poller 永远比不中、卡片永不触发。顺带存 `board_title` 供 console 显示，不让用户面对 `b_…`。

3. **错误分型**。建链接时 board 找不到/歧义→`400 board_not_found`/`board_ambiguous`（指名 ref 与候选），token 无效→`400 jtype_unauthorized`，workspace 不存在→`400 workspace_not_found`，真正网络/5xx 才保留 `503 jtype_unreachable`。原来一律 503，把"名字打错"误导成"网络故障"。凭 `jtype.Error.StatusCode` 与 `ErrDocNotFound`/`ErrBoardAmbiguousError` 区分。

4. **打破死锁（软建 + 运行时 fail-visible 复核）**。原来建链接硬要 token 校验列，而 per-link 设备流（D28）只能挂到**已存在**的 link 上——先有鸡还是先有蛋，全新集群无 env fallback 时首个 link 永远建不出。改为:**有凭据**（per-link 或 cluster fallback）→ 照旧硬校验（列打错仍 400）;**无任何凭据**→ 仍建链接、打 `board_status="unvalidated"`，由 owner 随后对该 link 走设备流拿 token 引导。poller 在拿到 token 后对未校验/失效的 link 做运行时复核：能解析就规范化 BoardRef+校验列、置 `ok` 并正常派 run;解析失败（board 没了/改名/列变了、含 4xx auth）置 `invalid` 并 notify-once;5xx/传输错保持原状下一 tick 重试。软建的错链接在 console 里响亮报错，绝不静默空转（红线 #1）。

5. **级联选择器（傻瓜 UI）**。新增 owner 授权的发现端点 `GET /projects/{id}/kanban/jtype/workspaces` 与 `.../boards?workspace=`（用生效 cluster token，**绝不下发 token**；集成关→`409 kanban_not_configured`、无 token→`503`，皆 fail-visible）。`jtype.Client` 加 `ListWorkspaces`。boards 端点对每个 `.board` 取 id/title/columns（上限 100 防扇出）。console 把"手打 workspace UUID + board ref + 列名"换成 workspace→board→trigger/done 列的级联选择器（board 选项带 columns），保留手动兜底;board_ref 提交 relativePath，服务端解析并规范化。

迁移 `0024_kanban_link_board`：`kanban_links` 加可空 `board_title` 与 `board_status TEXT NOT NULL DEFAULT 'ok'`（存量按 ok 回填，错的会在下次 poll 运行时复核里变 invalid，诚实）。新增 `store.SetKanbanLinkBoardStatus`（一条 UPDATE，非空 canonicalRef/title 才覆写，失效转移保留 last-known ref）。

- **被否**：只修 GetBoard 路径而不规范化 BoardRef（board 能校验通过、卡片仍永不触发，闭环照样死）；把 board 找不到继续报 503（误导 owner 去查网络）；继续硬要 token 才能建 link（死锁无解）；让 console 手打 `b_…` config id（用户根本找不到、且违背傻瓜 UI）；发现端点回吐生效 token（明文泄漏面）。
- **注**：本条实现时 D29 号已被"console 下拉换 Headless UI Listbox"占用，故顺延为 D30（设计稿原拟 D29）。

### D31 · 在 console project 页嵌入真实 jtype 看板（服务端代理 + 工作区范围收口，token 永不进浏览器）

project 页头新增 **Kanban 按钮**（仅当本 project 有 ≥1 个 kanban link 时显示），点开 modal 渲染已发布的 `jtype-board-react@0.1.0` 真实看板（列 + 卡片 + 拖拽移动）。安全基线是本 feature 的核心：**jtype token 绝不下发浏览器**——console 用 board-react 的 `client` 注入位，所有 `listDocuments/getDocument/saveDocument` 都打到 jcloud 的**服务端代理**，代理在服务端用 `kanbancfg.Resolver.Factory` + `jtype.ResolveToken`（与 poller 完全同一条解析:per-link token > 集群 fallback，`poller.go:122,154`）解析生效 token 打 Bearer，响应**逐字透传** jtype 原生报文。

后端落地（本条只做 orchestrator;console 由并行 agent 落地）：

1. **jtype 新增最小 seam `Client.ProxyDocumentAPI(ctx, method, path, body) (*http.Response, error)`**（`internal/jtype/client.go`）。原样透传:调用方用已校验的组件在服务端拼 upstream path，seam 只负责打 Bearer（空 token 不打头，与 `do` 一致）+ 透传 status/body。**不走类型化 `Doc/Document` 重序列化**——那些结构体丢掉了 board-react 依赖的 `isPublished/versionId`（save 的 `mergeStatus` 更是 `SaveDocument` 压根不解析），逐字段重映射会在未来 jtype 加字段时**静默丢字段 = 静默降级**（红线 #1）。`SaveDocument` 因不回响应体不可复用，save 走 `ProxyDocumentAPI(POST, .../documents/save, body)`。

2. **五个 member+ 端点**（`internal/api/kanban_board.go`，注册在 `api.go` kanban 路由块）：
   - `GET  …/kanban/board/links` — 只回 `boardEmbedLinkView`（`id/workspace_id/board_ref/board_title/board_status/service_id/trigger_column/done_column/enabled`，**无任何 token/credential 字段**），用于按钮显隐 + modal 选择器。
   - `GET  …/kanban/board/documents?workspace=` — 代理 `listDocuments`。
   - `GET  …/kanban/board/documents/{docId}?workspace=` — 代理 `getDocument`。
   - `POST …/kanban/board/documents/save?workspace=` — 代理 `saveDocument`（请求体流式透传，有上限）。
   - `DELETE …/kanban/board/documents/{docId}?workspace=` — 代理 `deleteDocument`（P2，可选）。

**关键取舍：**

- **工作区范围收口（confused-deputy 防护，安全核心）**：每个 documents/* 端点的 `?workspace=` 必须等于**本 project 自己某条 link 的 `workspace_id`**（`ListKanbanLinksByProject` 校验），否则 `403 workspace_not_linked`（用 403 不用 404，不确认外部 workspace 是否存在），**且在任何 jtype 往返之前拒绝**。否则集群/per-link token 就成了越权读写任意 jtype workspace 的混淆代理——那 token 能读它授权的每个 workspace 的每篇文档。范围粒度为 workspace（link 既有信任边界，与 poller 一致）；不做卡片级写收口（会在代理里重复 jtype 模型，收益不抵复杂度）。有测试证明 A 的 member 无法读 B 项目 link 的 workspace（且零 upstream 调用）。
- **谁能读/写:一律 member+**。写 = member+ 对齐派 run 权限（`POST /runs` 即 member+`runs.go:50`，拖卡=触发 run，同一权限阈值）；读也 member+（读写同体的组件，单阈值避免读写授权漂移，viewer 不显示按钮——member+ 的 board/links 对 viewer 403 → 空列表 → 无按钮）。owner-only 的 link 管理/发现端点（`kanban.go:129`、`kanban_discovery.go:49`）不动。*备选(记录未做)*:若产品日后要 viewer 只读看板，把 board/links + 读端点降到 viewer+ 并按角色传 `readOnly`，写端点仍 member+。
- **失败一律 fail-visible 类型化错误**：workspace 缺参 `400 bad_request`;集成关 `409 kanban_not_configured`;无可用凭据（`ErrNoToken/ErrNoCipher`/解密失败）`503 jtype_unreachable`（绝不静默跳过）;jtype 传输失败 `503 jtype_unreachable`。jtype 自身的 4xx/5xx（含 409 并发编辑）**逐字透传**，由 board-react 自己的错误面板渲染。
- **不代理 SSE**：PR #45 后 live WS/SSE 只认 full-scope session token，mcp-scope 一律 403;故不实现 `subscribeBoardEvents`，board 自动退回**可见轮询**（`JTypeBoard.tsx:209`），console 传 `live={false}` 去掉"live 不可用"误导提示。绝不假 live。
- **board_ref 名字/id 落差**：link 存的 `board_ref` 是 poller 匹配用的 config id（`b_…`，`domain.go:883`），而 `<JTypeBoard boardRef>` 要的是名字/relativePath。console 侧用 member+ 代理 `listDocuments` + 解析各 `.board` 文档取 `config.id === board_ref` 那份的 `relativePath` 传给组件（不改已发布包）;解析不到 → 醒目报错，不给空 modal（红线 #1）。此步纯 console，后端无改动。

**token 纪律（红线 #1/#2）**：生效 token 服务端解析（`ResolveToken`）、在 `ProxyDocumentAPI` 内打 Bearer 头，**绝不进任何响应体/日志行/JSON view**（`boardEmbedLinkView` 无 token 字段，`Effective.ClusterToken` 不导出）。有 no-leak 测试扫 list/get/save/**error** 四类响应体 + board/links 均不含 token。

- **被否**：把 token 或 baseUrl 下发浏览器让 board-react 直连 jtype（明文过浏览器，违背基线）;类型化逐字段重序列化代理（丢字段=静默降级，红线 #1）;不做 workspace 收口（集群 token 越权读任意 workspace）;代理 SSE（只会转发 403）;用 owner-only 的 links/discovery 端点撑 member+ 的按钮与解析（403 或泄露凭据态）。

- **对抗审查后的收口（同批实现）**：① 只有 **enabled** 的 link 才授予 board 访问（禁用 link = 断嵌入,对齐 poller 只扫 enabled;board/links 也只列 enabled → viewer/禁用态无按钮）。② save 代理**限定 `.md` 卡片路径**（buffer 请求体、校验 relativePath 以 `.md` 结尾且无 `..`）——挡住 member 借代理改 `.board` 配置或覆写工作区任意非卡片文档;残留:member 仍可写该已链接 workspace 内任意 `.md`,与其"能派 run"的信任面相当,可接受;更紧的 board-folder 收口留作后续。③ 删掉未用的 DELETE 代理端点(console client 本就不实现,减攻击面)。④ 传输失败 503 只回**通用文案**,不回显 jtype 内部 host/IP。⑤ 软建(unvalidated)link 的 `board_ref` 是名字而非 `b_` id → console 直接透传给 `<JTypeBoard>`(它按名解析),不再因 id 不匹配而打不开健康的板。

### D32 · Project 路由是完整 workspace，不是全局页壳里的内容卡

`/projects/:projectId` 采用 route-scoped `ProjectWorkspaceShell`：Project rail 是唯一的 Service 选择器，active `service` 与 `tab` (`tasks` / `automations` / `settings`) 写入 URL query，避免同一执行目标在 rail 和 composer 两处各有一份 local state。Project route 隐藏 `AppShell` 的全局 topbar，并把必要的身份/会话 chrome 收入 workspace utility bar，不再让两层 chrome 争夺视觉层级。

Task composer 只派发一次 run 的 prompt、per-run model 与 permission mode；Service default model 属于 Service settings。Project-wide settings（members、bot integrations、Kanban、API keys）只从 Project utility bar 打开，不能混进 selected Service 的 Settings surface。Recent tasks 用 activity row 而非管理型 table，仍链接到 route-owned Run task workspace。

Automation 的 webhook setup 采用显式 `POST /services/{id}/webhook`：member 用自己已连接的同 provider OAuth 授权同步，绝不借 integration bot credential 或 cluster PAT。raw repo、OAuth/receiver 缺失、provider 拒绝都以 typed visible state 呈现；成功只表示 provider 接受了注册，不伪造 delivery health。OAuth callback 可以安全回到原来的 Service Automation URL，并自动执行一次幂等同步；手动 Sync 仍可用于重新配置。Kanban 保持 D31 的真实服务端代理，不降级成假按钮。

完整 component、capability、scroll 和测试约束见 [15-project-workspace-architecture.md](15-project-workspace-architecture.md)。

---
### D33 · 时间线文本合并对系统行宽容 + runner 事件先于 turn-complete 落库（修正 D18 合并契约）

两处 run 详情页渲染/事件定序修复，同源：agent.text 由 runner 批量 emitter 上报，`run.status(awaiting_input)` 由 orchestrator 在 turn-complete 时写入——两个写入方、两条链路，尾部文本批次可能晚于 status 落库。

- **console**：`grouping.ts` 的文本合并改为只被**内容流事件**打断（tool_call/tool_result、permission_request/resolved、user.message）；系统行（run.status、run.session、artifact/git/result/failure 等）不再劈开消息气泡，改为渲染在合并块之后。D18 的"任何非文本事件都打断"契约废止。
- **runner**：`acpdrive` 的 Emitter 新增阻塞式 `Flush()`,session 循环在 POST turn-complete 前先冲刷事件队列，从源头保证同写入方的事件 seq 先于状态写。
- **被否**:只在 runner 侧修定序——orchestrator 直写状态与 runner 上报之间无全局时钟，跨写入方乱序不可根除，存量事件也需要 console 侧宽容才能正确渲染。

---
### D34 · runner 镜像预热 → 常驻 prewarm DaemonSet + console 手动同步

run 首次启动的冷拉取(数百 MB 镜像)由 **jcloud-runner-prewarm DaemonSet** 兜住:每节点一个 `sleep infinity` sleeper pod(requests 1m/16Mi),常驻缓存 `RUNNER_IMAGE`。console Cluster 页新增"同步最新镜像"按钮 → `POST /system/runner-image/prewarm`(cluster-admin)→ 创建/对齐 DaemonSet 并**删除 prewarm pod 强制重建**,配合 `imagePullPolicy: Always` 覆盖"同 tag 重推 `:latest`"场景;`GET /system` 的 `runner.prewarm` 回显 desired/ready/last_sync。launcher 无集群能力(process/disabled)时 `supported:false` + `409 prewarm_not_supported`,console 直接隐藏按钮(D14)。
- **被否**:run pod 设 `imagePullPolicy: Always` 代替预热——每次 run 启动仍串行付拉取等待,预热意义尽失;构建期预热(节点镜像 bake)——节点组由公司集群统一管理,不可控;registry 内网 mirror——加速有效但同样不消除"节点上没有"的首次拉取,可与本方案叠加而非替代。

---
### D35 · Project 级模型管理:providers/models 加可空 project_id,与集群目录并存,resolver 不变

每个 **project 拥有自己的 model providers + models**(对齐 sibling jcode 的 provider 管理器),供该 project 所有 service 自动使用;**集群级目录**(cluster-admin 的 providers/models + `model_grants`)完全保留。为将来 jcode 直连 cloud 共用能力,schema/DTO 按 jcode 超集建模。

- **归属轴(可空 `project_id`)**:`model_providers`/`model_configs` 各加可空 `project_id`(NULL=集群全局,今日行为不变;非空=项目自有,随项目删除 `ON DELETE CASCADE`),照搬 `integrations`(0018)的项目自有资源模式,**不建平行表**(否则 FK/resolver 全要改,收益不抵)。全局 `UNIQUE(name)` 放宽为 `UNIQUE(COALESCE(project_id,''), name)`,让集群与各项目可同名。迁移 `0029`,幂等。
- **可用集合(唯一改动点)**:`ListModelsForProject` 扩成 **项目自有 `enabled` 模型 ∪ 集群 grant 给它的模型**(单 SELECT 的 OR,天然去重;`enabled` 仅过滤项目自有)。这是定义 project 可用模型集的**唯一**查询——`modelcfg.SelectModel`/`ResolveModel`、`services.default_model_id` 校验、LLM 反代**全部不变**,自动覆盖项目自有模型。env `MODEL_*` 兜底仍仅当全局 `CountModels()==0`。
- **RBAC**:`/api/v1/projects/{id}/model-providers*` —— list=member+,写=owner;每个 `{pid}`/`{mid}` handler **断言行 `project_id`==路径项目否则 404**(集群全局与他项目行不可达)。集群 `/system/model-*` 端点原样保留。
- **jcode 对齐**:`kind`=runtime 前缀、`provider/model` 寻址、能力位(reasoning/tools/image)+`context_window`、自定义模型、**每模型 `enabled` 开关**、**provider 级自定义 headers**。凭据纪律不变:`api_key_enc`/`headers_enc` AES-256-GCM、`json:"-"`、绝不序列化(只回显 `api_key_set`/`headers_set`);runner pod 零凭据,LLM 反代注入真 key。
- **对抗审查后的收口(同批实现)**:① headers 曾"存了从不发送"(fail-visibly 违规)→ 打通 `model_configs.headers_enc`(create 时从 provider 快照,provider 改 header 时 sibling-sync 回灌)→ `resolveModel` 解密进 `Resolved.Headers`(cipher nil/解密失败=类型化 fail-visible,绝不静默丢)→ LLM 反代 + `/models` 探测应用;顺序为 drop RUN_TOKEN → set 自定义 headers(跳过 Host/hop-by-hop)→ **最后**对有 key 的 provider set 受管 `Bearer`(受管 key 永远胜出;keyless 的自定义 Authorization 得以保留)。② 项目 verify/catalog 曾给**任意登录用户**对内网发起 SSRF 探测 → `s.modelProviderHTTP` 加 **DialContext 拦截**(按解析后 IP 判环回/link-local/含 169.254.169.254 元数据/ULA/RFC1918/multicast,防 DNS-rebind),集群路径同享;测试经 `allowPrivateModelHosts` 钩子放行 httptest 环回。③ `handleGrantModel` 拒绝授权项目私有模型(`409 model_not_grantable`,只集群全局可 grant)。④ 编辑 `service_identity` provider 的模型不再把 auth_type 静默降级为 `none`。
- **console**:把 `ClusterModelsPage` 的 provider/model 富组件抽成 scope 参数化的 `ModelsCatalog`(集群=grant 管理;项目=`enabled` 开关+编辑+删除+自定义 headers 高级表单),集群页重构成薄壳复用,原测试保持绿;项目 `模型` 分区 owner 可管理 / member 只读并列出可用模型。自定义 headers 高级表单**仅项目 scope 显示**(集群端点 `DisallowUnknownFields` 会拒 `headers`,避免展示会报错的控件——headers 暂为项目特性)。
- **被否**:平行 `project_model_*` 表(FK/resolver 皆需改);把 headers 当 no-op 存着或直接拒收(违 jcode 对齐/fail-visibly);cluster-provider 也上 headers UI(列已存在但本轮不接线);project 模型对 member 可管理(管理属 owner;member 经 run composer 已能看可用模型);把 project 设置入口对 member 放开(既有 owner-only 门 `projectSettingsOpen=canManage&&…` 属跨切面 pre-existing UX,另议)。
