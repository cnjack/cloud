# 30 · 开源 Agent Workflow 产品对标

调研日期：2026-08-01
用途：约束 `docs/28-workflow-contract-and-deterministic-review-prd.md` 与
`docs/29-workflow-contract-and-review-design.md` 的产品、架构和 UI 决策。

## 1. 结论

**主产品对标选择 [OpenHands Agent Canvas](https://github.com/OpenHands/OpenHands)。**
它在调研时有 82,724 stars、MIT license，已经把“对话、Agent Profile、LLM
Profile、Backend、Automation、Run history”组织成一个自托管控制台，并直接提供
PR Review、Repository Monitor、schedule/webhook trigger 与 Kubernetes backend。
它与 jcode Cloud 的产品边界最接近，也足够高星。

但 jcode 不应复制任何一个项目的全部实现。最终参考组合是：

| 层 | 主要参考 | 借鉴点 |
| --- | --- | --- |
| 产品与 UI | OpenHands Agent Canvas | Profile/LLM/Backend 分层，Automate 控制台，proven workflow，会话式配置，运行历史 |
| Workflow authoring 与安全 | GitHub Agentic Workflows (`gh-aw`) | Markdown + frontmatter、编译产物、只读默认、typed safe outputs、权限/网络/供应链 guardrails |
| JType pickup 与长任务运行 | OpenAI Symphony | tracker polling、单一调度权威、per-work-item workspace、reconcile、retry、continuation |
| PR Review | Alibaba OpenCodeReview | 确定性文件选择/分组/规则/定位 + Agent 判断，覆盖统计，精确行评论与幂等发布 |

所以 jcode 的定位不是另一个通用 DAG engine，而是：

> 面向软件工作的多后端 Agent control plane：把来自 Conversation、JType、SCM、
> Cron 与 API 的请求编译为不可变 Workflow Contract，在具备能力证明的 Runner
> 上执行，并由控制面安全交付 Review、PR、Card、Report 等结构化结果。

## 2. 样本与活跃度快照

星数来自 GitHub repository API，仅作为 2026-08-01 的活跃度快照，不是质量评分。

| 项目 | Stars | Forks | License | 与 jcode 的距离 |
| --- | ---: | ---: | --- | --- |
| [OpenHands/OpenHands](https://github.com/OpenHands/OpenHands) | 82,724 | 10,637 | MIT | 最接近完整产品：自托管控制台 + 多 Agent backend + Automation |
| [openai/symphony](https://github.com/openai/symphony) | 26,356 | 2,667 | Apache-2.0 | 最接近 JType/看板任务持续 pickup |
| [alibaba/open-code-review](https://github.com/alibaba/open-code-review) | 17,036 | 1,158 | Apache-2.0 | 最接近确定性 PR Review 子系统 |
| [github/gh-aw](https://github.com/github/gh-aw) | 4,841 | 473 | MIT | 最成熟的 repository-owned Workflow contract 与 safe output 参考 |

`gh-aw` 的星数不是四者最高，但它是 GitHub 官方、功能距离最近的 Workflow
authoring/security 参考；OpenHands 则满足“高星主对标”的要求。

## 3. OpenHands Agent Canvas：主产品对标

### 3.1 它怎么做

- Canvas 只负责 UI、backend/profile selection 和 API adapter；Agent Server 负责
  conversation，Automation Server 负责 schedule/event run，sandbox/workspace
  不由前端承担。
- Agent Profile 与 LLM Profile 是两个对象。前者选择 OpenHands 或 ACP Agent
  （Claude Code、Codex、Gemini、自定义 ACP），后者只管理 provider/model/credential。
- Automation 有 schedule 或 event trigger、prompt、repository、branch、model、
  plugin、notification、timeout、enabled 与 run history。
- UI 提供 proven workflow。用户从 PR Review 或 Repository Monitor 模板进入一段
  setup conversation，由 Agent 追问 repository、event、output、CI 和 ignore rule，
  然后创建 Automation。
- Automation 可以导出为 versioned JSON；导入时校验 schema，并默认 disabled，
  由用户复核后启用。
- 当前前端类型把默认 timeout 描述为 10 分钟、服务端上限 30 分钟，正好覆盖
  jcode 首批需要持续至少三十分钟的 POC 场景。

### 3.2 借鉴

1. Console 分开呈现 **Agent Profile、LLM Profile、Runner Backend**，避免角色、模型
   和执行环境混成一个下拉框。
2. 首批提供 proven workflows，而不是先做任意 DAG：
   - GitHub PR Review；
   - JType Developer pickup；
   - Competitor research → JType Card；
   - Weekly repository report/changelog。
3. Automation detail 至少展示 prompt、trigger、profile、repository/branch、timeout、
   enabled、最近 Run 和 activity log。
4. 配置可以从 Conversation 产生，但 publish/enable 前必须变成 typed definition，
   经过 preview 与 readiness，而不是直接把整段对话当运行合同。
5. Import 默认 disabled；profile/workflow edit 只影响未来 Run，历史 Run 固定旧 revision。

### 3.3 不照搬

OpenHands 的 PR Review 快速路径让 Agent 通过 GitHub MCP 使用具备 Contents、Issues、
Pull Requests 写权限的 token。这个方式部署简单，但 Agent、prompt injection 与写权限
在同一信任域，且文档中的最小权限仍比纯 Review 所需更宽。

jcode 保持更强边界：Runner/Agent 只拿 source bundle 和 read-only/allowlisted tools；
Agent 输出结构化 intent；Run SCM Grant 留在 control plane，由 provider adapter 校验
并发布评论或 PR。这样 Agent Profile、MCP 与 credential selection 彼此独立。

## 4. GitHub Agentic Workflows：Workflow 与安全参考

### 4.1 它怎么做

- 用户维护 Markdown 文件：YAML frontmatter 定义 trigger、permission、engine、MCP、
  safe outputs 与预算，正文是自然语言任务。
- `gh aw compile` 把可读源文件编译成 `.lock.yml`；两者都提交到 repository。
- trigger 覆盖 manual、issue/comment、PR/review、push 与人类友好的 schedule；编译器
  把 fuzzy schedule 转成确定 cron，并为不同 workflow 分散执行时间。
- Agent 默认只读。创建 issue/comment/PR 等写操作不是 Agent 直接执行，而是 buffered
  typed safe output，经过数量/格式/secret/threat policy 后才由独立阶段落地。
- 编译期做 schema validation、expression allowlist、action SHA pinning 与 security
  scan；运行时再做 sandbox、network firewall、tool allowlist、rate limit 和 approval。
- PR 触发按 PR/ref 做 concurrency group，新 commit 可取消旧 review run。

### 4.2 借鉴

1. 将 **Workflow Definition** 与 **Execution Contract** 分开：前者可读、可编辑、
   可版本控制；后者是每次 Run 的 compiled immutable snapshot。
2. R2 支持 repository-owned `.jcode/workflows/*.md`：frontmatter 管 trigger/profile/
   requirements/timeout/output，正文管理 prompt；Cloud UI 编辑同一 schema，不另造语义。
3. Delivery 使用 allowlisted typed outputs，例如 `provider_review`、`create_pull_request`、
   `create_jtype_card`、`update_changelog`、`publish_report`。
4. `enable` 等价于 publish：只有 schema、capability、permission 和 output policy 都通过
   才能生效。配置修改产生新 revision，不热改 in-flight Run。
5. 受信 compiler/resolver 决定阶段和权限；Agent 只能建议 intent，不能扩大 contract。

### 4.3 不照搬

`gh-aw` 绑定 GitHub Actions。jcode 需要支持 GitHub/GitLab/Gitea、JType、持久 workspace、
长 session 与 Kubernetes Runner，因此不能把 `.lock.yml` 或 Actions job 当核心运行模型。
Cloud Store 中的 Workflow Contract 才是跨 provider 的权威；source-controlled Markdown
只是一个 authoring adapter。

## 5. OpenAI Symphony：JType pickup 与恢复参考

### 5.1 它怎么做

Symphony 是长期运行的 tracker reader/orchestrator。它从 repository-owned
`WORKFLOW.md` 读取 YAML frontmatter 和 prompt，轮询 Linear，确定 eligibility，按全局和
state concurrency 派发，在每个 issue 的隔离 workspace 中启动 Agent。每个 tick 先
reconcile 再 dispatch；issue 进入非 active/terminal 状态会停止 worker，失败按指数
退避重试，正常 turn 后可以在同一 thread/workspace 继续。

它强调：Orchestrator 是 claim/running/retry 的单一写入权威；workspace path 必须可预测
且限制在 root；hook 有 timeout；配置 reload 只影响未来 dispatch，不要求中断 in-flight
session。其参考实现重启后依赖 tracker + filesystem 恢复，没有 durable scheduler DB。

### 5.2 借鉴

1. JType adapter 只负责 receipt/claim/eligibility，Run Store 是 dispatch/retry/reconcile
   的单一权威。
2. 同一 Work Item 的 continuation 保持同一 workspace 与 session lineage；Card 状态变化
   会取消或停止不再 eligible 的 Run。
3. 每个 trigger tick 先 reconcile，再 dispatch；必须有 stall timeout、retry backoff、
   global/profile/runner concurrency。
4. Workflow revision reload 只影响未来 Run；in-flight Run 继续消费 frozen contract。
5. jcode 已有 PostgreSQL，不接受 Symphony “重启不保留 retry/session”这一预览级限制；
   retry、occurrence、checkpoint 与 delivery ledger 必须持久化。

## 6. Alibaba OpenCodeReview：Review 参考

### 6.1 它怎么做

OpenCodeReview 明确反对仅靠通用 Agent prompt 做 Review。工程层确定文件筛选、相关文件
分组、规则匹配、coverage、评论定位和二次 reflection；Agent 负责动态判断与按需读取
上下文。大 diff 被拆成隔离 review units，可并发执行。

输出包含 path、start/end line、category、severity、existing/suggestion code；GitHub
publisher 固定 PR head commit、按路径/行发布 inline comment，对无有效行号或策略降级的
finding 放到 summary。发布端还有稳定排序、批次上限、run/review/comment marker 与重试
reconciliation，避免 provider 5xx 后重复评论。

### 6.2 借鉴

1. Review Plan 必须在模型前完成 file/hunk selection、skip reason 与 changed-line index。
2. finding 只能命中本次 diff 的 right-side changed line，且带 severity/category。
3. `0 finding` 不代表 coverage complete；coverage 与结果数量分开显示。
4. 发布固定 head SHA，并使用 deterministic ordering、bounded batch 与 idempotency marker。
5. R4 的多 Agent 仅用于 plan 产生的互斥 review units；每个 shard 有隔离 budget，最终由
   deterministic reducer 去重/反思，不开放 Agent 自主递归委派。

## 7. 对四个目标场景的落地

| 场景 | Trigger adapter | Profile | Required capabilities | Typed outputs | 关键完成条件 |
| --- | --- | --- | --- | --- | --- |
| PR Review | SCM verified event | Reviewer | source.read、git、scm.review.write | provider_review | fixed SHA、coverage、valid anchors、idempotent publish |
| 竞品研究 → JType | Manual/Cron | Product Manager | web/MCP fetch、jtype.create-card | create_jtype_card、publish_report | sources、request schema、Card receipt |
| JType request → PR | JType receipt/claim | Developer | source.read/write、toolchain、scm.pr.write | create_pull_request | tests、commit、Ready PR、Card backlink |
| 周报/Changelog | Cron occurrence | Developer 或 PM | source.read、scm.read、optional write | publish_report/update_changelog | fixed time window、dedupe、report/PR receipt |

四者不需要四套 engine。它们需要不同的 trigger verification、idempotency key 和
writeback adapter，但共享 Workflow Definition → Compile/Resolve → Readiness → Immutable
Run Contract → Runner → Validated Output → Delivery Ledger。

## 8. 因对标而改变的设计决定

1. 在 Workflow Contract 中新增 `workflow.id/revision/source/definition_hash`，不再只靠
   profile 和 trigger 推断“执行的是哪个 workflow”。
2. `execution.timeout_seconds` 成为合同字段；Ship-R1 冻结与实际
   `activeDeadlineSeconds` 相同的唯一 effective value，POC 对该值要求不低于 1800
   秒而不引入第二个默认值；Ship-R2 才由
   Runner profile 上限和 readiness 阻塞不兼容请求。
3. R2 增加 portable Workflow Definition；UI 和 repository file 使用同一 versioned
   schema，导入默认 disabled。
4. Delivery Contract 改为 allowed typed outputs；Draft/Ready 是 PR output 的 lifecycle
   policy，不是独立 workflow。
5. Automation/Profile edit 只影响 future Run；Run detail 分开显示 workflow/profile
   revision、LLM selection、effective timeout/未来 Runner digest 与 SCM grant revision，
   不伪造 Ship-R1 尚不存在的 LLM/Runner Profile revision。
6. Ship-R2 UI 提供四个 proven workflows 与 conversation-assisted setup；高级 DAG 继续不在
   范围内。
7. Review publisher 的幂等与批次 proof 纳入 R1 测试，而不是只验证 JSON 形状。

## 9. 参考资料

- [OpenHands repository 与 Agent Canvas architecture](https://github.com/OpenHands/OpenHands)
- [OpenHands Automations](https://docs.openhands.dev/openhands/usage/agent-canvas/managing-automations)
- [OpenHands Agent Profiles](https://docs.openhands.dev/openhands/usage/agent-canvas/agent-profiles)
- [OpenHands PR Review Assistant](https://docs.openhands.dev/openhands/usage/agent-canvas/prebuilt/github-pr-review)
- [GitHub Agentic Workflows: How They Work](https://github.github.com/gh-aw/introduction/how-they-work/)
- [GitHub Agentic Workflows: Security Architecture](https://github.github.com/gh-aw/introduction/architecture/)
- [OpenAI Symphony specification](https://github.com/openai/symphony/blob/main/SPEC.md)
- [Alibaba OpenCodeReview](https://github.com/alibaba/open-code-review)
