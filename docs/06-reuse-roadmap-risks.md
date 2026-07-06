# 06 · 复用记分卡 · 路线图 · 风险

## 复用记分卡

你的 5 个点子 + 探索中补上的 2 个必需项,逐一映射:🟢 现成 / 🟡 部分 / 🟣 新建。

| 点子 / 领域 | 状态 | 现成积木(含路径) | 净新建 |
|---|---|---|---|
| 登录 / 租户 / 项目 | 🟡 部分 | OIDC 接入;jtype 有 workspace/member RBAC 可借鉴 | project↔repo 实体、run 状态机、run-scoped token |
| jcode 桌面跑云端 | 🟡 部分 | `web/…/apiBase.ts` seam、`authToken.ts`、jcode-auth WS 子协议 | 远端引擎 URL 配置、wss/TLS、指向路由化后端 |
| Bring-your-own-key | 🟢 现成 | jcode `ProviderConfig`(api_key/base_url/headers)· model factory | 控制面 LLM 代理 + temp token(见 [04](04-byok-and-isolation.md)) |
| jtype doc + 看板集成 | 🟢 现成 | notes/kanban MCP 工具;jcode 本身是 MCP client | Symphony 状态机工具、card↔repo 绑定 |
| Git webhook(GitLab/GitHub/Gitea) | 🟡 部分 | jtype 出站 webhook 引擎(HMAC+重试)可参考 | 入站验签、repo clone、PR/MR 创建 ×3、@mention 派发 |
| 钉钉 / 飞书 | 🟣 新建 | WS/SSE 扇出、jcode-buddy NDJSON 状态先例 | bot 适配器(入站指令 + 出站状态卡) |
| Runner 镜像(Go/Java/Py/Node) | 🟣 新建 | jcode `jcode_headless` 静态二进制;jtype 多阶段 Dockerfile 模板 | 多语言基座、两段式 entrypoint、镜像缓存 |
| K8s 扩 runner 池 | 🟡 部分 | jbrowser outbound-agent + Helm(已部署 K8s) | client-go 调度器、per-issue PVC、NetworkPolicy、生命周期 |
| 部署到 K8s | 🟡 部分 | jtype Helm chart + Dockerfile + GHCR CI | orchestrator chart、scheduler RBAC、迁移 Job、Secret 化 |
| CI / CD | 🟡 部分 | jcode 跨平台发布矩阵;jtype GHCR 流水线;agent-eval 评测台 | 多架构构建 runner+orchestrator 镜像;用 runner 当 agent PR 的 CI |
| ⊕ 持久任务队列 / 状态 | 🟣 补漏 | jcode session JSONL 可 resume;jtype MySQL + cron loop | 幂等 reconciler + Postgres run 状态、僵尸回收 |
| ⊕ 硬化沙箱 / 出口管控 | 🟣 补漏 | DockerExecutor 进程树 kill;agent-eval 逃逸金丝雀 | NetworkPolicy egress、资源限额、墙钟、PVC 租户擦除 |
| ⊕ 会话/记忆存储 + 同步 | 🟣 补漏 | jcode Recorder / memory(project+global 两 scope) | 可插拔 Store + orchestrator 后端 + 蒸馏迁移 + 同步游标(见 [03](03-storage-memory-sync.md)) |

---

## 落地路线图

尊重优先级:先退最大风险(headless),Gitea 先行,Symphony 化随后,横切一路带上 CI/CD。

### P0 · 证明 headless(退掉第一大风险)
- 最小 runner 镜像(jcode headless + Go/Node),entrypoint:`clone → 非交互 automation/web run → 出 diff`。
- 确认无 TTY、interactive 工具被 drop(`internal/web/automation_run.go` 是最近的现成形态)。

### P1 · 最薄闭环(Gitea 先)
- Go orchestrator:`projects`/`runs` + Postgres + 幂等 reconciler。
- client-go 起单个 worker Job + 事件流持久化(Store sink)+ 控制台 Run/CLI 触发。
- Gitea `clone → draft MR` + jtype kanban MCP 回写卡片。
- 打通:`dispatch → 云端 run → diff/MR → 看板更新`。

### P2 · Symphony 化 + 多 provider
- 在 Go 实现 Symphony SPEC:claim/run 状态机、退避、stall、blocked、reconcile。
- jtype 看板 = Symphony tracker,拖卡 = 派 run(落地 `agent-orchestration-design.md`)。
- 补 GitHub + GitLab 验签 + PR/MR 适配;接 Keycloak OIDC(device flow)。

### P3 · 执行模型 + 安全硬化
- 长活 worker + per-issue PVC + NetworkPolicy egress 白名单 + 墙钟/`max_turns` + 跨租户 PVC 擦除。
- 控制面 LLM 代理 + temp token(BYOK);记忆蒸馏挪到控制面侧。

### P4 · 入口扩展 + 规模 + 同步
- 钉钉/飞书 bot;桌面 jcode 指远端引擎(`apiBase.ts` seam)。
- jcode 可插拔 Store 落地 + local↔cloud 同步;warm-pool / 镜像缓存优化;dashboard + token 计量。

### 横切 · CI/CD & 部署(贯穿始终)
- 多架构构建 + 发布 runner 镜像 & orchestrator 镜像。
- Helm:orchestrator + scheduler RBAC ServiceAccount + NetworkPolicy + 迁移 Job。
- 用 runner 自身当 agent PR 的 CI executor。

---

## 风险与护栏

| 风险 | 级别 | 护栏 |
|---|---|---|
| **headless TTY** —— `jcode -p` 仍 boot TUI,真正无 TTY 面是 web/automation | 🟠 高 | P0 先证 automation/web run 在无 TTY 容器里干净退出 |
| **沙箱隔离不由 agent 强制** —— agent-eval F3/F4:无 runner 级超时、文件/exec 边界不强制 | 🟠 高 | pod + NetworkPolicy + 墙钟 + PVC 租户隔离兜底,隔离交给 K8s 而非 agent |
| **temp token 滥用** —— 真 key 永不出控制面,sandbox 被攻陷只有短期 proxy token | 🟡 中 | temp token 限流 + 短 TTL + 可撤销;egress 白名单防源码外传。风险从"泄 key"降级为"限额内滥用" |
| **跨项目 memory 投毒** —— 不可信 sandbox 若直写共享记忆可污染全租户 | 🟡 中 | sandbox 只写自己 run 的 session;memory 由控制面侧蒸馏 pipeline 生成/校验 |
| **多 provider 验签 / PR ×3** —— Gitea/GitHub/GitLab 各不同 | 🟡 中 | Gitea 先(GitHub 式 API,较简单),抽象 provider 适配接口 |
| **reconcile 幂等 / 僵尸回收** —— claim 去重、崩溃 worker 回收、可见性 | 🟡 中 | 照抄 Symphony 的 stall/退避/释放语义;单一 reconcile tick 收敛 |
| **PVC 隔离(仅当池化才有风险)** —— 专属 per-issue PVC 天生隔离 | 🟢 低 | 每 issue 专属 PVC、用完删、不池化;命名空间/StorageClass 按租户隔离 |
| **看板 MCP N+1 / last-write-wins** —— 自动 agent + 人并发编辑规模化时冲突 | 🟢 低 | 服务端 kanban 索引 + 3-way merge(规模化后再上) |
| **jtype 运维缺口** —— 迁移进程内跑(多副本竞态)、Helm DB 明文 | 🟢 低 | 迁移改 Job/hook,DB 凭据走 K8s Secret / external-secrets |
