# 10 · PRD —— jcode Cloud Agent(MVP)

> 本 PRD 只覆盖 **MVP = 路线图 [P0 + P1] 垂直切片**:证明 headless 跑通 + 控制台/CLI 触发的云端 run(本地 k8s)+ run 详情实时事件流 + diff 产物。多租户 OIDC、看板拖卡、钉钉/飞书、BYOK 代理均为**非目标(本期)**,见 §4。
>
> 锁定的架构决策见 [README](README.md) / [01-architecture](01-architecture.md) / [02-decision-log](02-decision-log.md) / [06-reuse-roadmap-risks](06-reuse-roadmap-risks.md)。本 PRD **不重开**任何已锁决策,只把它们收敛成可测的产品行为。
>
> _最后更新:2026-07-07_

---

## 1 · 产品定位与目标用户

**一句话定位:** jcode Cloud Agent 是一个**自托管的云端编码 agent 平台**——你在浏览器里给一个 git 仓库派一句任务,平台在自己的 k8s 里 headless 跑 jcode,实时把 agent 的每一步(工具调用、文本、命令输出)推给你看,跑完交出一份可 review 的 diff,**模型和源码都不出你的域**。

| 目标用户 | 画像 | 本期为他解决的核心痛点 |
|---|---|---|
| **自托管团队(主)** | 有合规/数据不出域要求,已在内网跑 k8s + 私有 git(Gitea) | 想用 cloud coding agent,但不能把代码/模型 key 交给 SaaS 厂商 |
| **平台工程师** | 给团队提供内部开发者平台,负责部署/运维这套编排 | 需要一个能装进现有 k8s、可观测、可复现的 agent 编排,而非黑盒 SaaS |
| **个人开发者** | 单人跑本地 k8s(kind/minikube/k3d),想异步派活 | 想把长任务丢给云端 agent 异步跑,不占本地终端,回头看 diff |

---

## 2 · 问题陈述与价值

**问题:** GitHub Copilot cloud agent 与 OpenAI Codex cloud 都是**托管 SaaS**——代码进厂商云、模型由厂商定、密钥托管在厂商侧。有数据驻留 / 合规 / BYO-model 要求的团队用不了,或用得提心吊胆。

**价值主张(为什么用本平台,不用 Copilot / Codex cloud):**

| 维度 | Copilot cloud agent / Codex cloud | jcode Cloud Agent(本平台) |
|---|---|---|
| 部署形态 | 厂商托管 SaaS | **自托管**,跑在你自己的 k8s |
| 源码去向 | clone 进厂商云 | **不出域**:clone 只发生在你集群内的 runner pod |
| 模型 | 厂商锁定 | **BYO model**:任意 OpenAI 兼容 provider(复用 jcode `ProviderConfig`) |
| 密钥 | 托管在厂商 | 留在你的控制面(本期:runner 内配置;未来:LLM 代理 + temp token) |
| 可观测 | 厂商给多少看多少 | 事件流 + transcript + diff 全量落你自己的库 |
| 编排语义 | 黑盒 | Symphony SPEC(未来 P2)+ 幂等 reconciler,可复现 |

**MVP 要证明的一件事:** 「在自己的 k8s 里,给一个仓库派任务 → 实时看 agent 干活 → 拿到 diff」这条链路端到端跑得通、可观测、可复现。这是退掉全项目最大风险(headless 是否跑得通)的最小闭环。

---

## 3 · MVP 范围表(In / Out)

功能粒度到「用户能点/能看到的东西」。

### 3.1 In scope(本期做)

| # | 功能 | 粒度说明 | 溯源 |
|---|---|---|---|
| IN-1 | **Headless runner 镜像** | jcode headless(`full_access` + LocalExecutor)+ Go/Node 基座;entrypoint:`clone → 非交互 run → 出 diff`;无 TTY 干净退出 | P0 |
| IN-2 | **Project 实体(单租户)** | 创建/列出/查看 project;字段:name、git repo URL、默认分支、(可选)clone 用凭据引用 | P1 |
| IN-3 | **Run 实体 + 状态机** | 触发 run;状态:`queued → running → succeeded / failed`;`blocked` 状态**建模并展示**(本期 agent 全自动不产生 blocked,但状态徽章体系含它) | P1 |
| IN-4 | **控制台触发** | Web 控制台:project 列表页、新建 project 弹窗、run 列表页、run 详情页、Run 输入框(任务描述) | P1 |
| IN-5 | **CLI 触发** | `cloud run --project <id> "<task>"` 触发一个 run;可 `--follow` 实时打事件、`cloud run list`、`cloud run get <id>` | P1 |
| IN-6 | **实时事件流** | run 详情页事件时间线:工具调用、文本、命令输出增量推送(经 jcode `AgentEventHandler`,WebSocket/SSE);端到端延迟 ≤ 2s | P1 |
| IN-7 | **事件/transcript 持久化** | run_events 落 Postgres;完整 transcript + 日志落对象存储;刷新页面/断线重连可回放 | P1 |
| IN-8 | **Diff 产物** | run 成功后把工作副本 vs 基线的 unified diff 作为**产物**存对象存储;详情页 diff 视图查看、可下载 | P1 |
| IN-9 | **单 worker Job 调度** | orchestrator 用 client-go 在**本地 k8s** 起单个 worker Job 跑 run;注入 repo URL + 任务;run 结束回收 | P1 |
| IN-10 | **失败可见性 + retry** | 失败原因归类可读(clone 失败 / setup 失败 / agent 报错 / 超时);详情页一键 retry(同参数重跑) | J2 需求 |
| IN-11 | **并行多 run** | 同一 project 可并发多个 run,列表各自独立推进,详情页事件流互不串扰 | J3 需求 |
| IN-12 | **墙钟 + max_turns 上限** | 每个 run 有硬性墙钟超时与 `max_turns`,到点标记 `failed`(原因=timeout),不永久 stall | 护栏 |

### 3.2 Out of scope(本期不做,见 §4 非目标)

| # | 明确不做 | 归属阶段 |
|---|---|---|
| OUT-1 | 多租户 / OIDC 登录(本期单租户、单用户、本地信任) | P2 |
| OUT-2 | 看板拖卡触发(J4 只写旅程,不实现) | P2 |
| OUT-3 | Symphony SPEC 完整状态机(claim/退避/stall reconcile) | P2 |
| OUT-4 | GitHub / GitLab provider(本期不接任何 provider 写操作) | P2 |
| OUT-5 | **Gitea draft-MR 创建**——**stretch goal,标记为「MVP 外但紧接下一步」**,详见 §3.3 | P1↔P2 边界 |
| OUT-6 | BYOK LLM 代理 + temp token(本期:key 直接配在 runner) | P3 |
| OUT-7 | per-issue 持久 PVC + NetworkPolicy egress 白名单(本期:ephemeral 工作副本即可) | P3 |
| OUT-8 | 跨项目 memory / 蒸馏 pipeline / local↔cloud 同步 | P3 / P4 |
| OUT-9 | 钉钉 / 飞书 bot;桌面 jcode 指远端引擎 | P4 |
| OUT-10 | 自动 merge / 自动 CI(架构硬约束:永不做) | 永不 |

### 3.3 Stretch goal(MVP 外,但紧接下一步)

> **ST-1 · Gitea draft-MR 出口。** run 成功后,除了 diff 产物,把 `agent/<run-id>` 分支推到 Gitea 并开一个 **draft MR**(不自动 merge、不触发 CI)。
> - **为何是 stretch 而非 In:** In-scope 的验收出口是「diff 产物可看」(IN-8),它不依赖任何 git provider,先把 headless + 事件流 + diff 这条主链路证死。draft-MR 只是把出口从「下载 diff」升级成「推 Gitea 开 MR」,是 P1→P2 的自然延伸。
> - **若本期有余量做:** 只做 Gitea 一家;失败降级为「仍然产出 diff 产物」,不阻断 run 判成功。
> - 对应 User Journey 里以 `(ST)` 标注的可选步骤。

---

## 4 · 非目标(本期)

以下能力**明确不在 MVP**,列出以消除范围歧义。每项都是既定路线图的后续阶段,不是「砍掉」。

| 非目标 | 说明 | 计划阶段 |
|---|---|---|
| 多租户 + OIDC(Keycloak/SSO) | 本期单租户、本地信任、无登录 | P2 |
| 看板拖卡触发(jtype tracker) | 见 J4,只写旅程占位、不实现 | P2 |
| Symphony SPEC 完整语义 | claim/run 状态机、退避、stall、blocked reconcile | P2 |
| GitHub / GitLab provider | 本期连 Gitea 写操作都是 stretch | P2 |
| BYOK LLM 代理 + temp token | 真 key 永不出控制面的隔离模型 | P3 |
| per-issue PVC + egress 白名单 | 本期 ephemeral 工作副本、无网络硬隔离 | P3 |
| memory(project+global)+ 同步 | 可插拔 Store 云后端、蒸馏、local↔cloud 同步 | P3/P4 |
| 钉钉/飞书 bot、桌面指远端 | 入口扩展 | P4 |

---

## 5 · User Journeys(可直接转 e2e 测试)

> 约定:每条 Journey 有**前置条件**;每步是 `Jx-Sn`,含【用户动作】【系统可见反应】【验收断言(machine-checkable)】。断言里 `assert:` 前缀的是 e2e 应直接检查的。UI 触点见 §6。
> 通用前置(所有 Journey):本地 k8s 可用(kind/minikube/k3d)、orchestrator + Postgres + 对象存储已部署、runner 镜像已就绪、控制台可访问、存在一个可 clone 的测试 Gitea 仓库 URL。

### J1 ·「第一次使用」——从零到看到 diff

**前置条件:** 无任何 project;有一个有效的公开/内网可 clone 的 git repo URL(记为 `REPO_OK`)。

| 步骤 | 用户动作 | 系统可见反应 | 验收断言 |
|---|---|---|---|
| **J1-S1** | 打开控制台首页 | 进入 **项目列表页**,空态提示「还没有项目,新建一个」+「新建项目」按钮 | `assert:` 页面渲染空态;存在 `[data-testid=new-project-btn]` |
| **J1-S2** | 点「新建项目」 | 弹出 **新建项目弹窗**,含字段:name、git repo URL、默认分支(默认 `main`) | `assert:` 弹窗可见;name 与 repo URL 为必填 |
| **J1-S3** | 填 name=`demo`、repo URL=`REPO_OK`,提交 | 弹窗关闭;列表出现一张 `demo` 项目卡;跳到 **project 详情/ run 列表页** | `assert:` `POST /projects` 返回 201 且回体含 `id`;列表含 `demo` |
| **J1-S4** | 在 run 列表页点「Run」,输入任务描述=`在 README 末尾加一行 Hello`,提交 | 立即出现一条新 run,状态徽章 `queued`;自动进入(或可点进)**run 详情页** | `assert:` `POST /projects/{id}/runs` 返回 201 且含 `run_id`;run 状态初始 `queued` |
| **J1-S5** | 停留在 run 详情页观察 | 状态徽章 `queued → running`;**事件时间线**开始增量出现:clone 事件、工具调用(read/edit)、文本、命令输出 | `assert:` 状态在 ≤ 30s 内转 `running`;首个事件端到端延迟 ≤ 2s;时间线事件数 > 0 |
| **J1-S6** | 继续观察至结束 | 事件流持续追加,最终出现完成事件;状态徽章 `running → succeeded` | `assert:` 状态终态 `succeeded`;存在 `run.finished_at` |
| **J1-S7** | 切到 run 详情页的 **diff 视图** tab | 展示本次改动的 unified diff(README 多一行 `Hello`);可下载 `.diff` | `assert:` diff 产物存在且非空;diff 含新增行 `Hello`;下载返回 200 |
| **J1-S8** | 刷新页面 | 事件时间线与 diff **完整回放**(来自持久化,非内存) | `assert:` 刷新后事件数与终态与刷新前一致(读自 `GET /runs/{id}` + 对象存储) |
| **J1-(ST)** | (stretch)启用了 Gitea MR | 详情页额外出现「draft MR」链接,指向 Gitea 上 `agent/<run-id>` 分支的 draft MR | `assert:` 若开启,`run.mr_url` 非空且指向 draft;关闭时该区块不出现 |

**J1 成功定义:** 一个新用户从打开控制台到看到第一个成功 run 的 diff,**≤ 10 分钟**,全程无需读文档。

---

### J2 ·「run 失败可见性」——失败清晰 + 一键 retry

**前置条件:** 已有 project `demo`(可用 J1 建);准备一个**无效** repo URL 或让 clone 必然失败的场景(记为 `REPO_BAD`,如不存在的仓库 / 错误凭据)。

| 步骤 | 用户动作 | 系统可见反应 | 验收断言 |
|---|---|---|---|
| **J2-S1** | 新建一个 project,repo URL=`REPO_BAD`(或对已有 project 触发一个注定失败的 run) | run 创建成功,状态 `queued` | `assert:` `POST …/runs` 返回 201 |
| **J2-S2** | 进入 run 详情页观察 | 状态 `queued → running`;事件流出现 clone 阶段的错误输出 | `assert:` 事件流含 stderr/error 类事件 |
| **J2-S3** | 等待 run 收敛 | 状态徽章清晰变 **`failed`**(红);详情页顶部显示**可读失败原因**,归类为「仓库 clone 失败」+ 具体报错摘要 | `assert:` 终态 `failed`;`run.failure_reason` ∈ {`clone_failed`,`setup_failed`,`agent_error`,`timeout`};`run.failure_message` 非空且人类可读 |
| **J2-S4** | 点详情页的 **Retry** 按钮 | 生成一条**新 run**(同 project、同任务描述),状态 `queued`,并跳到新 run 详情 | `assert:` `POST /runs/{id}/retry` 返回 201 且新 `run_id` ≠ 原;新 run `retried_from` = 原 run_id |
| **J2-S5** | (可选)把 project 的 URL 改成有效后再 retry | 新 run 走通 → `succeeded` | `assert:` 修正后 retry 的 run 可达 `succeeded` |

**J2 成功定义:** 失败不静默、不显示裸堆栈——用户 3 秒内知道「哪一步失败、为什么」,并能一键重试。

---

### J3 ·「并行多 run」——两个 run 独立推进、事件互不串扰

**前置条件:** 已有 project `demo`(repo=`REPO_OK`);orchestrator 并发上限 ≥ 2。

| 步骤 | 用户动作 | 系统可见反应 | 验收断言 |
|---|---|---|---|
| **J3-S1** | 在同一 project 快速触发 **run A**(任务=`加一行 A`) | 列表出现 run A,状态 `queued` | `assert:` run A 创建,得 `run_id_A` |
| **J3-S2** | 紧接着触发 **run B**(任务=`加一行 B`) | 列表出现 run B,与 A 并列,状态各自独立 | `assert:` run B 创建,`run_id_B` ≠ `run_id_A`;两者都在列表 |
| **J3-S3** | 在 run 列表页观察 | A、B **各自的状态徽章独立推进**(可同时 `running`);互不覆盖 | `assert:` 存在某时刻 A、B 同为 `running`;各自状态由各自 run 决定 |
| **J3-S4** | 点进 run A 详情 | 只看到 A 的事件流(含 `A`),**不含** B 的任何事件 | `assert:` A 的事件全部 `run_id == run_id_A`;时间线不含 `run_id_B` 的事件 |
| **J3-S5** | 点进 run B 详情 | 只看到 B 的事件流(含 `B`),**不含** A 的事件 | `assert:` B 的事件全部 `run_id == run_id_B` |
| **J3-S6** | 等两者完成 | A、B 各自 `succeeded`;各自 diff 只含自己的改动 | `assert:` A 的 diff 含 `A` 不含 `B`;B 的 diff 含 `B` 不含 `A`;两 run 有独立 worker Job |

**J3 成功定义:** 并发 run 之间**完全隔离**——状态、事件流、diff 三者都不串台。

---

### J4 ·「看板拖卡触发」——【P2 · 未来,仅写旅程,本期不实现】

> ⚠️ **本期不实现。** 仅记录目标形态,供 P2 落地时直接转 e2e。依赖 jtype 看板作为 Symphony tracker(见 [02-decision-log D07/D11](02-decision-log.md))。

**前置条件(未来):** jtype 看板已接入为 tracker;某列被标记为「触发列(active_state)」;卡片与 project↔repo 已绑定。

| 步骤 | 用户动作 | 系统可见反应 | 验收断言(未来) |
|---|---|---|---|
| **J4-S1** | 在看板把一张卡从 `Todo` 拖到 `In Progress`(触发列) | orchestrator reconcile 认到该 active 卡,claim 它 | `assert:` 生成一个 run,`trigger_source=kanban`,绑定该 card_id |
| **J4-S2** | 观察卡片 | agent 用 jtype kanban MCP 回写进度(`report_progress`)到卡片评论 | `assert:` 卡片出现 agent 回写的进度评论 |
| **J4-S3** | run 完成 | 卡片自动流转到 `review` 列;附 draft MR 链接 | `assert:` 卡片状态=`review`;评论含 MR 链接 |

---

## 6 · UI 触点清单(页面 / 组件级)

每个 Journey 步骤引用的页面与组件,e2e 可据此定位选择器。

| 触点 | 类型 | 关键元素 | 被哪些步骤触及 |
|---|---|---|---|
| **项目列表页** | 页面 | 空态占位、项目卡(name/repo)、`新建项目`按钮 `[new-project-btn]` | J1-S1, J1-S3 |
| **新建项目弹窗** | 组件(modal) | 字段 name / git repo URL / 默认分支;提交 / 取消 | J1-S2, J1-S3, J2-S1 |
| **Run 列表页(project 详情)** | 页面 | `Run` 触发按钮 + 任务描述输入框 `[run-input]`;run 表格(id / 任务摘要 / 状态徽章 / 创建时间)| J1-S4, J2, J3-S1..S3 |
| **Run 详情页** | 页面 | 两个 tab:**事件时间线** + **diff 视图**;顶部状态徽章 + 失败原因区 + `Retry` 按钮 + (ST)`draft MR` 链接 | J1-S5..S8, J2-S2..S4, J3-S4..S6 |
| **事件时间线** | 组件 | 按时序增量渲染:工具调用卡、文本块、命令输出块;WS/SSE 实时追加 + 断线重连回放 | J1-S5/S6/S8, J2-S2, J3-S4/S5 |
| **Diff 视图** | 组件 | unified diff 高亮 + 下载 `.diff` | J1-S7, J3-S6 |
| **状态徽章体系** | 组件 | `queued`(灰)· `running`(蓝/动)· `succeeded`(绿)· `failed`(红)· `blocked`(黄,本期建模+展示,agent 不产生)| 全部 Journey |
| **CLI** | 命令行 | `cloud run` / `cloud run --follow` / `cloud run list` / `cloud run get` | J1/J2/J3 的 CLI 等价路径 |

**状态徽章语义表(单一事实源):**

| 徽章 | 语义 | 本期是否可达 | 颜色 |
|---|---|---|---|
| `queued` | 已创建,等待调度 worker | ✅ | 灰 |
| `running` | worker 已起,agent 连跑中 | ✅ | 蓝(动效) |
| `succeeded` | 正常结束,diff 产物就绪 | ✅ | 绿 |
| `failed` | clone/setup/agent/timeout 失败,含可读原因 | ✅ | 红 |
| `blocked` | 需人工输入(未来:Symphony 一等公民) | ⚠️ 建模+展示,本期 `full_access` 不产生 | 黄 |

---

## 7 · 验收标准(可勾选清单)

机器可验证优先。全部勾选 = MVP 达标。

**P0 · headless(退最大风险)**
- [ ] AC-1 runner 镜像在**无 TTY** 容器里跑 `clone → 非交互 run → 出 diff` 并**干净退出**(exit 0);交互工具被 drop、不 boot TUI
- [ ] AC-2 run 崩溃 / 非 0 退出被 orchestrator 捕获为 `failed`,不挂死

**P1 · 控制面 + 闭环**
- [ ] AC-3 `POST /projects` 建 project;`GET /projects` 列出;数据落 Postgres,重启后仍在
- [ ] AC-4 `POST /projects/{id}/runs` 触发 run;orchestrator 用 client-go 在本地 k8s 起 worker Job
- [ ] AC-5 run 状态机走 `queued→running→succeeded|failed`,状态落库
- [ ] AC-6 事件流实时推送到详情页(WS/SSE),**首事件端到端延迟 ≤ 2s**
- [ ] AC-7 run_events 落 Postgres + transcript/日志/diff 落对象存储;**刷新/重连可完整回放**
- [ ] AC-8 成功 run 产出非空 unified diff 产物,详情页可看 + 可下载
- [ ] AC-9 失败 run 有 `failure_reason`(枚举)+ 可读 `failure_message`
- [ ] AC-10 `Retry` 生成新 run 且 `retried_from` 关联原 run
- [ ] AC-11 同 project 两个并发 run:状态、事件流、diff **三者互不串扰**(J3 全绿)
- [ ] AC-12 每个 run 有墙钟 + `max_turns` 上限,超时判 `failed(timeout)`,不永久 stall
- [ ] AC-13 CLI `cloud run` 与控制台触发**功能等价**(能触发、能 follow、能查状态)

**Journey 端到端**
- [ ] AC-14 J1 全流程 e2e 绿(S1→S8)
- [ ] AC-15 J2 全流程 e2e 绿(S1→S4)
- [ ] AC-16 J3 全流程 e2e 绿(S1→S6)

**Stretch(达标不要求)**
- [ ] AC-ST 开启 Gitea 后,成功 run 推 `agent/<run-id>` 分支并开 draft MR;`run.mr_url` 非空;关闭时不影响 AC-8

---

## 8 · 成功指标

| 指标 | 目标值 | 测量方式 |
|---|---|---|
| **从零到第一个成功 run** | ≤ 10 分钟 | 新用户从打开控制台到 J1-S6 `succeeded` 的墙钟(含建 project、派任务、跑完) |
| **事件端到端延迟** | ≤ 2s(p95) | agent 侧 emit → 详情页渲染的时间差 |
| **run 状态收敛正确率** | 100% | 每个终态 run 都落到 `succeeded`/`failed` 之一,无「卡 running」僵尸(靠墙钟兜底) |
| **失败可读率** | 100% | 每个 `failed` run 都有非空枚举 `failure_reason` + 可读 message(J2 断言) |
| **并发隔离正确率** | 100% | J3 断言:并发 run 的事件/diff 零串扰 |
| **刷新回放一致率** | 100% | 刷新前后事件数 + 终态一致(AC-7) |
| **headless 干净退出率** | 100% | runner 在无 TTY 下 exit 0 / 非 0 均被正确归类,无挂死(AC-1/AC-2) |

---

## 附:本 PRD 与既定架构的映射(便于评审)

| PRD 章节 | 溯源决策 / 路线图 |
|---|---|
| §1 定位 / §2 价值 | README 一句话架构;自托管 / BYO model / 数据不出域 |
| IN-1, AC-1/2 | P0 headless;`internal/web/automation_run.go`(现成 headless 形态) |
| IN-3 状态机, §6 徽章 | D06 幂等 reconciler + Postgres;`blocked` 来自 D07 Symphony(本期只建模) |
| IN-6/7 事件流 | jcode `AgentEventHandler`(实时)+ 可插拔 Store sink(D12,持久) |
| IN-8 diff 产物 | D08 默认 draft PR/MR,可退**只返回 diff**——本期取「只返回 diff」这一退路 |
| IN-9 调度 | D04 K8s Job;client-go;本期单 worker、ephemeral(PVC 属 P3) |
| ST-1 Gitea MR | D09 Gitea 优先;D08 draft MR、不自动 merge/CI |
| §4 非目标 | D03 OIDC(P2)、D10 BYOK(P3)、D05 PVC(P3)、D11 看板(P2)、D07 Symphony(P2) |
