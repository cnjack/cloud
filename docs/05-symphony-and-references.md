# 05 · Symphony SPEC 与参考架构

## Symphony 是什么

OpenAI 开源的**不是框架,而是一份 `SPEC.md`**——把"用 issue tracker 当控制面来编排多个 Codex 编码 agent"写成**语言无关的协议**,配一份 Elixir 参考实现("materialize the spec into any language")。口号:**"管理工作,而不是盯着 agent"**(工程师手动盯 Codex session,"3–5 个就开始上下文切换到崩溃";Symphony 自动监督,内部三周 landed PR +500%)。

- 仓库:<https://github.com/openai/symphony>(根目录 `SPEC.md` + `elixir/` 参考实现)
- 公告:<https://openai.com/index/open-source-codex-orchestration-symphony/>

## 核心机制(来自 SPEC.md)

- **Tracker 即控制面**:一个 issue = 一个专属 workspace(`<root>/<issue_id>`)= 一个 worker;编排器持续 watch 看板,**保证每个 active issue 都有 agent 跑到它离开 active 态**。
- **两套状态机**:
  - claim 态:`Unclaimed → Claimed → Running → RetryQueued → Released`
  - run 阶段:`PreparingWorkspace → BuildingPrompt → LaunchingAgentProcess → InitializingSession → StreamingTurn → Finishing → [Succeeded | Failed | TimedOut | Stalled | CanceledByReconciliation]`
- **reconcile 循环**:轮询 tracker、**stall 检测**(按最后 codex 事件时间戳,`stall_timeout_ms`)、**失败指数退避**(`delay = min(10000 · 2^(attempt-1), max_retry_backoff_ms)`,默认封顶 `300000`/5m)、终态 issue 自动清 workspace。
- **续跑**:一个 worker 在**同一 thread/workspace 连跑多轮 turn**,直到 issue 不再 active(`max_turns` 封顶);正常退出后编排器仍排一个约 1s 的续跑重试去复查 issue 是否仍 active。
- **dispatch 资格**:required fields 齐、state ∈ `active_states`(默认 `["Todo","In Progress"]`)、∉ `terminal_states`、未 running/claimed、全局 + 按 state 并发有余、**Todo blocker 规则**(Todo 有非终态 blocker 则不派)。排序:priority 升序 → created_at 最老 → identifier 字典序。
- **blocked / 需人工输入是一等公民**:保持 `claimed`,在 runtime state / JSON API / dashboard 里暴露成 `blocked`——绝不永久 stall。
- **git / 分支 / PR / CI 明确不归编排器管**:由 coding agent 用它 workflow 里的工具自己做;Symphony 只给 agent 一个 `linear_graphql` 客户端工具让它**自己写 tracker**。
- **有意无数据库**:scheduler 状态全在内存,重启靠"重轮询 tracker + 复用 workspace"恢复(retry timer / live session 不保命)。
- **动态 reload**:`WORKFLOW.md` 变更热加载;prompt 用严格模板(Liquid 语义,未知变量/filter 失败)。
- **JSON API + dashboard**:`GET /api/v1/state`、`GET /api/v1/<issue>`、`POST /api/v1/refresh`;`/` 人读 dashboard。
- **Elixir/BEAM**:因为要 supervision tree 容错地监督成百上千个并发轻量 agent 进程。

## 关键映射:Go + K8s ≈ Elixir + BEAM(减掉 Elixir)

Symphony 用 Elixir 是因为它把 agent 当**进程**跑、要靠 BEAM 重启崩溃的 agent。**但本系统选了「K8s Job per active issue」——Kubernetes 本身就是 supervision tree。** 所以:

```
Go orchestrator (reconcile 循环) + K8s (监督/重启)  ≈  Symphony reconcile + BEAM supervision
```

**不用引入 Elixir,就白拿了 Symphony 用 Elixir 换来的容错。** orchestrator 从"任务队列"心智升级成一个 **Kubernetes 风格的控制器**:把"看板期望态" reconcile 成"应有的 worker Job 集合"。

## Symphony ↔ 本系统

| 维度 | Symphony(参考) | 本系统(融合后) |
|---|---|---|
| 控制面本质 | tracker 即控制面 + reconcile | Go orchestrator = K8s 风格控制器,看板 = 期望态 |
| 容错 / 监督 | Elixir BEAM supervision | **K8s = supervision tree**,免 Elixir |
| 工作单元 | 1 issue = 1 workspace = 1 worker(连跑多轮) | 1 active issue = 1 长活 worker Job + per-issue PVC |
| 状态存储 | 内存无 DB,重轮询恢复 | 幂等 reconciler **+ Postgres**(补非看板触发的 run) |
| git/PR/CI | agent 自己用工具做 | 一致:jcode 用自带 git 工具 + provider API |
| tracker 写入 | agent 用 `linear_graphql` 自写 | 一致:agent 直接调 **jtype kanban MCP** 写卡片(零新代码) |
| 验收关口 | PR + 人工 review | 一致:draft PR + 人工把关,不自动 merge |
| 触发源 | 只看板 | 看板 + webhook + 控制台 + chat 喂进同一 run 模型 |
| 退避 / stall | `min(10s·2^n, 5m)` · 按最后 codex 事件时间 | 照抄进 Go reconciler |

## Copilot / Codex 参考(可迁移的共识)

GitHub Copilot cloud agent 与 OpenAI Codex cloud 都收敛到同一形状:

- 一个任务后端 + 多个瘦控制面。
- 每任务一个**一次性容器**(建在现有 CI/runner 基建上)。
- 一个维护的**多语言基座镜像**,运行时版本由 env-var 选(不是运行时安装,保缓存稳定)。
- 两段式:**setup 有网 → agent 锁网**的安全模型。
- **draft PR** 作为唯一工作追踪产物。
- **硬人工把关**(agent 不能 merge/触发 CI,密钥只在 setup,egress 白名单)。

参考:
- Copilot cloud agent: <https://docs.github.com/en/copilot/concepts/agents/cloud-agent/about-cloud-agent>
- Codex cloud: <https://developers.openai.com/codex/cloud>
