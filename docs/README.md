# jcode Cloud Agent

自托管的**云端编码 agent**平台——把现有的 jcode / jtype / jbrowser 生态拼成一台"自主编码工厂"(GitHub Copilot cloud-agent / OpenAI Codex-cloud 风格),并采用 **OpenAI Symphony 的 SPEC** 作为编排契约。

> 一句话架构:**一个 Go orchestrator(= Kubernetes 风格的 Symphony 控制器)把 jtype 看板当作 tracker,reconcile 出一批长活 worker Job;每个 Job 里跑 jcode headless,本地改代码、开 draft PR、用 jtype MCP 自己回写卡片;人工在 PR 上把关。K8s 替掉了 Symphony 的 Elixir,jcode 提供 agent,jtype 提供控制面数据,jbrowser 提供 K8s 模板。**

**关键判断:整套系统约 70–80% 的积木已经存在于现有仓库,这是一个"编排 + 集成"项目,不是从零搭建。**

---

## 文档地图

| 文档 | 内容 |
|---|---|
| [01-architecture.md](01-architecture.md) | 生态全景、平面架构图、组件拆解、端到端生命周期 |
| [02-decision-log.md](02-decision-log.md) | 全部锁定决策(3 轮 + 补充)与理由、被否方案 |
| [03-storage-memory-sync.md](03-storage-memory-sync.md) | jcode 可插拔 `Store` 改造 RFC:会话回传 / 跨项目记忆 / local↔cloud 同步 |
| [04-byok-and-isolation.md](04-byok-and-isolation.md) | BYOK 的控制面 LLM 代理模型、沙箱/网络/PVC 隔离与护栏 |
| [05-symphony-and-references.md](05-symphony-and-references.md) | Symphony SPEC 深读、Copilot/Codex 参考、Go+K8s ≈ Elixir+BEAM 映射 |
| [06-reuse-roadmap-risks.md](06-reuse-roadmap-risks.md) | 复用记分卡、落地路线图 P0–P4 + 横切 CI/CD、风险与护栏 |
| [15-project-workspace-architecture.md](15-project-workspace-architecture.md) | Project workspace route, component, capability, and scroll architecture |
| [17-jcode-device-relay.md](17-jcode-device-relay.md) | Desktop ↔ Cloud outbound relay、E2EE、配对与移动端控制 |
| [18-device-mesh-dispatch.md](18-device-mesh-dispatch.md) | 设备网格调度设计 |
| [19-account-settings-sync.md](19-account-settings-sync.md) | 多 Desktop Provider 配置 E2EE 同步 + Cloud Provider Proxy |

可视化蓝图(v1,早于存储/BYOK 细化):<https://claude.ai/code/artifact/68743dc8-aa5c-48f1-b712-fb8d974a2902>

---

## 组成生态的五个仓库

| Repo | 语言 | 在本系统中的角色 |
|---|---|---|
| **jcode** | Go | Agent 引擎 + runner:headless 引擎、`Executor` 抽象(Local/SSH/Docker)、session、memory、BYO model |
| **jtype** | Rust/React | 控制面数据(看板 = Symphony tracker + 文档),local-first 同步/MCP 的参考实现 |
| **jbrowser** | Rust | control-plane ↔ outbound-agent + K8s Helm 的现成模板;可作 runner 的浏览器工具后端 |
| **jcode-buddy** | — | NDJSON 状态协议先例(带外状态汇报) |
| **jcode-design** | — | Web/桌面控制台的设计系统 |

---

## 决策速览

| # | 决策点 | 选择 |
|---|---|---|
| 01 | 控制面归属 | 全新独立 **orchestrator**(不绑 jtype-web) |
| 02 | 技术栈 | **Go**(与 jcode agent 同语言 + client-go) |
| 03 | 身份/租户 | 外部 **OIDC**(Keycloak / 企业 SSO) |
| 04 | Runner 隔离/调度 | **K8s Job / active issue**(长活 worker) |
| 05 | 执行模型 | 长活 worker + **per-issue 持久 PVC** |
| 06 | 状态/恢复 | **幂等 reconciler + Postgres**(K8s controller 风格) |
| 07 | 编排契约 | 采用 **Symphony SPEC**;jtype 看板 = Symphony tracker |
| 08 | Git 集成深度 | 默认 **draft PR/MR**,可按项目退只读;不自动 merge/CI |
| 09 | Provider 顺序 | **Gitea 优先** → GitHub + GitLab |
| 10 | BYOK 密钥 | 控制面 **LLM 代理**持真 key;sandbox 只拿短期 **temp token** |
| 11 | 看板角色 | 触发源 **+** 回写 sink 都要 |
| 12 | 会话/记忆存储 | 改造 jcode 成**可插拔 Store**;云后端 = orchestrator 自有 store |
| 13 | local↔cloud 同步 | 在 orchestrator 内**自建**(借鉴 jtype lamport/cursor,不依赖 jtype) |

详见 [02-decision-log.md](02-decision-log.md)。

---

## 下一步(建议)

- **P0**:证明 jcode 能在无 TTY 容器里 headless 跑通(`clone → automation run → 出 diff`)——退掉最大风险。
- **P1**:Gitea 打通最薄闭环:`dispatch → 云端 run → diff/MR → 看板回写`。
- 详见 [06-reuse-roadmap-risks.md](06-reuse-roadmap-risks.md)。

_最后更新:2026-07-07_
