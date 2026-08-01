# 28 · Workflow Contract、Runner Readiness 与确定性 Review PRD

状态：Ready for Ship-R1 implementation
优先级：P0
父级研究：`outputs/cloud-workflow-dogfood-report.md`（工作区交付物）
开源对标：`docs/30-open-source-agent-workflow-benchmark.md`
Published issue: https://github.com/cnjack/cloud/issues/24

## Problem

jcode Cloud 已经能从 Console、jtype Card、SCM event/comment、Cron 与 scoped
API 创建 Run，也已经具备 Plugin、provenance、Automation execution ledger、
Usage 和 lifecycle-aware Pull Request delivery。最新主线解决了“能不能执行”
的大部分问题，但还不能可靠回答“这一次究竟按哪份工作流执行”。

当前执行语义分散在 Run 标量字段、Automation prompt/model、Service policy、
Plugin snapshot、Runner shell 与 hard-coded review protocol 中。Profile 或
Automation 改动后，用户无法从历史 Run 看出它采用了哪个版本；retry、resume、
webhook 与 poller 也容易只复制部分字段。Agent 只能从 prompt 和当前环境猜测
角色、交付和验证要求。

私有仓库路径也没有完全冻结为同一项授权事实。Runner clone 已有 run-scoped
credential substrate，Plugin Review writeback 已部分使用 immutable snapshot，
但 source bundle、push、PR lifecycle 和 legacy fallback 仍有读取当前 Service/
binding/credential 的 implementation。同一个 Run 可能在开始和交付阶段看到
不同的 repository identity 或 grant revision。

PR Review 已有 readonly Runner、严格 JSON、最多 8 条 finding、80% confidence
门槛和 GitHub inline fallback，但仍以 branch 名而不是 commit SHA 定位输入，
没有 changed-hunk index、rule revision 或 coverage ledger。大 PR 仍是一次把整份
diff 交给模型；finding 的行号格式合法不等于确实锚定本次改动。

Runner image 当前明确只保证 git、curl、zstd 与 ripgrep，不带 Go、Node、Python
或 Java toolchain。Cloud 没有 Runner Profile catalog，也没有在排队前把 Agent
Profile requirements 与真实 Runner capability 比对。长达三十分钟的任务可能
在启动后才发现无法测试或缺少 MCP/Plugin capability。

这些问题直接影响四类核心场景：

1. PR Review 需要可复现输入、完整覆盖和可信行锚点；
2. PM/竞品研究需要明确的 MCP 与 Work Item 写入 capability；
3. jtype Card pickup 需要固定的 Developer profile、交付和完成条件；
4. Cron repo maintenance 需要固定的时间窗口、验证命令、Changelog/Report 输出。

## Solution

Cloud 建立一条统一但不抹平 trigger 差异的派发链：

> Trigger adapter 只提供来源事实；Workflow Contract Resolver 把 definition、
> facts、Agent Profile revision、Service policy、模型、交付和验证冻结成一个
> contract；Runner 只消费冻结 contract。Ship-R2 再把现有 dispatch prerequisites
> 收敛为可 preview 的单一 Run Readiness evaluator。

首个 vertical slice 以私有 GitHub PR Review 为验收主线，同时交付 Manual
任务与 Review Run 可见的 contract/coverage inspector。它包含三个相互依赖的深模块：

1. **Workflow Contract Snapshot**：每个 Run 冻结 schema version、workflow、profile
   revision、trigger、execution、delivery、verification 和 capability requirement；
2. **Run SCM Grant**：在派发 claim 时冻结安装、凭据版本、provider config、
   repository identity、clone route、default branch 和 acting identity；所有
   source/PR/review implementation 只消费它；
3. **Review Plan / Coverage**：Review Run 单独固定 base/head SHA，建立 changed-hunk
   index，finding 必须命中右侧 changed line；首期保留单模型执行，但持久化
   coverage，为后续 bounded shards 留下稳定 seam。

Ship-R1 不交付 Runner Profile Catalog、Composer live readiness 或 Custom Profile
CRUD。合同中的 timeout 直接冻结当前 Project override 或 Cluster default 的实际
effective value，必须与 Kubernetes `activeDeadlineSeconds` 一致。Ship-R2 引入
operator-owned Runner manifest 和单一 evaluator，再检查 toolchain/capability 并在
创建前返回 typed blocker；任何阶段都不得静默换模型、换 profile、改 output 或降级
为匿名 clone。

开源对标后，产品采用明确的“可编辑定义 → 编译合同”分层。R2 的 Workflow
Definition 可由 Console conversation、表单或 repository-owned
`.jcode/workflows/*.md` 创建，使用同一 versioned schema；publish 时编译/验证并产生
新 revision。每个 Ship-R1 Run 已先冻结 `workflow.id/revision/source/definition_hash`，
因此 Agent 知道 workflow 不是靠猜 prompt，而是 Orchestrator 注入这份平台拥有的
合同。定义变更只影响未来 Run，retry 默认仍使用旧合同。

### 为什么不同 case 不需要四套 Workflow engine

Manual、JType、SCM 与 Cron 的 trigger、幂等和外部回写确实不同，因此保留各自
adapter 和 occurrence/receipt；它们在创建 Run 前收敛到相同的 contract resolver，
并在 Ship-R2 收敛到 readiness interface。这样 trigger 差异是一等事实，但角色、能力、交付和审计
不会各写一套。

### Draft PR decision

不再把“草稿 PR”作为新 Service 的产品默认或 workflow 类型。Pull Request 是
delivery；one-shot 成功直接 Ready，长 session 可在执行期间保持 Draft，并在
最终 bundle 推送后转 Ready。`always_draft` 仅作为历史兼容和 Owner 显式策略，
Cloud 仍不自动 approve、merge 或主动 dispatch CI。

### Multi-agent decision

Developer、PM、Architect、Reviewer 首先是不同 Agent Profile，不要求多 Agent。
首期每个 Run 仍只有一个 primary agent。只有确定性 Review shards、互不重叠的
work units 或显式“提出方案—独立审计—合并结论”工作流才允许受控 fan-out；
没有隔离、budget、merge policy 和 loop limit 时不启用多 Agent。

## User Stories

### Profile 与 Workflow

1. 作为 Project Member，我希望在发起任务前选择 Developer、PM、Architect 或
   Custom profile，以便模型获得明确角色，而不是从自然语言猜测。
2. 作为 Project Owner，我希望创建 Custom profile 并声明 instructions 与
   required capabilities，以便团队复用经过评审的工作方式。
3. 作为 Project Owner，我希望编辑 profile 产生新 revision，以便历史 Run
   继续显示旧版本。
4. 作为 Project Member，我希望在发送前看到将被冻结的 trigger、profile、
   model、permission、delivery 和 verification 摘要，以便确认系统理解正确。
5. 作为 Viewer，我希望在 Run detail 查看完整 Workflow Contract 和 hash，以便
   审计一次执行为何这样工作。
6. 作为 Automation Author，我希望 SCM/Cron Automation 固定一个 profile
   revision；后续编辑 profile 不改变已发生的 execution。
7. 作为 Reviewer，我希望 retry 明确标记“复用原 contract”或“按当前配置重新
   解析”，避免半旧半新的执行。
8. 作为安全审计者，我希望 profile instructions、accountable actor 与 Bot 都不
   参与 credential selection 或 authorization。

### Readiness

9. 作为 Project Member，我希望排队前知道 source、model、Runner、Plugin、
   workspace 和 delivery 是否就绪，以免长任务启动后才失败。
10. 作为 PM profile 用户，我希望缺少 MCP fetch 或 jtype create-card capability
    时看到明确 blocker 和配置入口，而不是 Agent 假装完成研究/入板。
11. 作为 Developer，我希望 profile 要求 Node/Go/Python 时系统核对 Runner
    manifest 的工具及版本，以免“实现完成但无法测试”。
12. 作为 Cluster Admin，我希望 Runner manifest 包含 image digest、architecture、
    tool versions 和 capability，以便 rollout 后可追踪实际执行环境。
13. 作为 Project Member，我希望 preview 与 Create Run 使用同一 evaluator，
    以免 UI 显示 Ready 但提交后走另一套判断。
14. 作为 Operator，我希望 manifest 漂移在 init preflight 中 fail closed，并把
    稳定原因写回 Run，而不是继续运行未知镜像。

### SCM grant 与 Review

15. 作为私有仓库 Owner，我希望一个 Run 的 clone、push、开 PR、读取 PR 状态
    和 review writeback 使用同一 installation/grant revision。
16. 作为 Project Owner，我希望 Run 开始后即使 Plugin reconnect、repository
    rename 或 Service setting 改变，本 Run 仍按冻结 grant 完成或明确失败。
17. 作为安全审计者，我希望 Runner 永远看不到 provider token，grant 仅由
    control-plane adapter 消费。
18. 作为 PR Author，我希望 Review 显示实际 base SHA、head SHA 与 rules revision，
    以便结果可复现。
19. 作为 PR Author，我希望 inline finding 只能落在本次 diff 的右侧 changed
    line，以免评论锚到无关或过期代码。
20. 作为 PR Author，我希望看到 indexed files、input-covered hunks 与 skipped reason，
    以免“0 finding”被误解成“全量检查无问题”。
21. 作为 Reviewer，我希望 provider 不支持 inline placement 时仍收到包含精确
    path/line 的单条 summary fallback。
22. 作为 Automation Author，我希望 synchronized 事件产生的新 Run 使用新的
    head SHA，而同一 delivery replay 仍只产生一次 Run。

### 操作与恢复

23. 作为 Operator，我希望 contract hash、Runner digest、SCM grant revision 和
    review coverage 出现在 Run inspector，而 secrets 永不出现。
24. 作为 Project Member，我希望任务的 effective timeout 在 Run 开始前可见，并能
    区分“排队阻塞”和“执行失败”；POC workflow 的 timeout 不得低于 1800 秒。
25. 作为未来 Workspace recovery 用户，我希望 contract 与 SCM grant 可作为
    checkpoint lineage 的稳定父引用，避免恢复到另一套工作流。
26. 作为 jcode 编辑工具维护者，我希望 Cloud 不假装通过 turn-hook 解决同路径
    lost update；CAS/atomic mutation 留在实际文件 mutation seam。

## Product Requirements

### PR-1 · Versioned Agent Profiles

- Built-in profile 有稳定 ID、版本和只读 instructions/requirements；
- Custom profile 为 Project scope，Owner 可创建、编辑、归档；
- 编辑产生新 revision，旧 revision 只读保留；
- instructions 有长度上限，不允许包含 secret；
- required capability 使用受控枚举，不接受任意“看起来像能力”的字符串；
- role 不影响 RBAC、provider identity 或 Cloud principal。

### PR-2 · Immutable Workflow Contract

- 所有新 Run 在进入 queued 前拥有 contract snapshot；
- snapshot 至少包含 schema version、workflow revision、profile revision、trigger、execution、
  delivery、verification、requirements、resolved-at 和 stable hash；
- snapshot 是 API 可读、Store append-only 的审计事实；
- Runner 接收 contract-derived instructions 与 metadata，不读取 mutable profile；
- legacy Run 明确显示 `contract unavailable`，不伪造高精度 snapshot；
- retry/resume policy 必须显式，首期 retry 默认复用原 contract；来自新 trigger
  的新 Run 总是解析新 contract。
- execution 冻结当前实际 effective timeout；首批 POC workflow 要求该值至少
  1800 秒，但 1800 不是另一套默认值。Ship-R2 超过 Runner Profile 上限时 readiness
  阻塞，不在执行中静默截断；
- delivery 是 allowlisted typed outputs，Agent 不可增加合同之外的写操作。

### PR-3 · Single Run Readiness Evaluator

- Preview 和 Create Run 共用 evaluator；
- 每项 check 返回 `ready | blocked | unavailable`、stable code、message、repair role；
- required check blocked 时不得创建 Run；
- optional capability 缺失可以显示 degraded，但不得改变 delivery/output；
- Runner manifest 由 operator 配置/镜像证明，浏览器不得自行推断；
- capability 版本比较确定且有单元测试。

### PR-4 · Frozen Run SCM Grant

- grant 与 dispatch claim 原子创建；
- grant 引用 immutable provider config/credential version 并冻结 repository facts；
- source bundle、push、PR create/readiness、review read/write 全部消费 grant；
- 当前 Service/binding 只用于新 Run，不得改变现有 grant；
- legacy fallback 必须显式标记，Plugin-bound 新 Run 不允许 fallback；
- terminal secret material 依照现有 GC policy 清理，但 audit identifiers 保留。

### PR-5 · Deterministic Review Plan

- Review input 固定 base/head commit SHA；branch 只作为展示来源；
- changed-hunk index 在模型调用前生成并持久化摘要；
- Review result validation 校验 path、right-side changed line、finding 数量、
  confidence 与字段长度；
- Ship-R1 coverage 是 input coverage，至少记录 changed files、eligible/indexed
  hunks、skipped files/hunks 和 reason；不声称模型语义上审阅了每个 hunk；
- `0 findings` 与 `incomplete coverage` 可同时成立，UI 必须区分；
- provider adapter 只接受 validated result，不理解 prompt。

### PR-6 · UI Contract

- Ship-R1 Composer 只显示 read-only built-in Profile pill 和合同说明，不显示假的
  readiness；Ship-R2 才提供 Profile selector 与 live Readiness strip；
- Run detail 的 Workflow 区域先显示 Profile、Trigger、Delivery、Verification，
  technical detail 再显示 hash、Runner digest、SCM grant revision；
- Review Run 显示 coverage bar、base/head short SHA、rules revision 与 skipped reason；
- blocked 用持久 panel，不以 Toast 替代；
- Ship-R1 键盘可展开 contract；Profile selector 与 preflight 交互属于 Ship-R2；
- 700px 以下所有事实单列，核心 blocker 在 technical identifiers 之前。

### PR-7 · Portable Workflow Definition（Ship-R2 authoring）

- Console、conversation-assisted setup 与 repository file 使用同一 schema；
- file authoring 使用 Markdown frontmatter + prompt body，默认路径
  `.jcode/workflows/*.md`；
- schema 包含 trigger、profile、requirements、timeout、allowed outputs 与 prompt；
- validate/publish 产生新 immutable revision，invalid revision 不替换 last known good；
- import 创建 disabled draft，必须 preview/readiness 后由 Owner 启用；
- 首批提供 PR Review、JType Developer pickup、Competitor research → JType、Weekly
  repository report/changelog 四个 proven workflows；
- 不把 provider-specific GitHub Actions lock file 当跨 provider runtime contract。

### Ship matrix

| Product requirement | Ship-R1 | Ship-R2+ |
| --- | --- | --- |
| PR-1 Agent Profiles | code-owned Developer/Reviewer r1 | PM/Architect、Custom CRUD/revisions |
| PR-2 Workflow Contract | all new Run paths、built-in definitions、immutable snapshot | custom/portable definition revisions |
| PR-3 Readiness | existing dispatch prerequisites only | shared preview/create evaluator、Runner manifest/catalog |
| PR-4 SCM Grant | repository + provider + credential convergence | additional provider policy/capability evidence |
| PR-5 Review Plan | exact revision pair、input coverage、anchor validation | deterministic shards、semantic review coverage |
| PR-6 UI | Run contract inspector、Review input coverage | selector、live readiness、authoring UI |
| PR-7 Definition | code-owned `builtin:implementation-task@1` and `builtin:pull-request-review@1` | UI/repository authoring and import/export |

## Implementation Decisions

### Deep module 1：Workflow Contract Resolver

Resolver 接收已经授权且规范化的 trigger facts、Agent Profile revision、Service
policy、model selection 与请求选项，返回有界、可哈希的 contract。Manual、
JType、SCM、Cron adapter 不复制默认值和 snapshot 逻辑。Contract canonicalization
固定字段顺序和空值规则，使 hash 可重复。

Profile instructions 不是 system authority。Resolver 用平台拥有的 envelope 把
profile 文本标记为 role guidance；Delivery Contract、security invariant 和 Review
protocol 由平台层追加且不可被 profile 覆盖。

### Deep module 2：Run Readiness Evaluator（Ship-R2）

Evaluator 消费 contract 与当前 capability evidence，输出有序 checks。它不执行
provider mutation，不创建 Run，也不改变输入。Create Run 在同一请求中重新计算
并比较 contract hash；preview 已过期时返回 `preflight_stale`，而不是沿用旧结论。

首期 capability taxonomy：source read/write、git、shell、ripgrep、persistent
workspace、SCM read/write/review、JType read/create-card/comment、MCP fetch，以及
Go/Node/Python/Java toolchain。未来新增 capability 只能向后兼容。

### Deep module 3：Run SCM Grant

Run SCM Grant 组合现有 Plugin runtime snapshot 与 repository snapshot。它是所有
provider operation 的窄 interface。Control plane 可用 grant 签发短期 credential、
构建 source bundle、推送 bundle、创建/读取 PR 与发布 Review；Runner 只看到
source adapter 和结果，不看到 grant secret。

Create/preview 阶段只能检查 `scm_install_ready`，因为 Run SCM Grant 尚未存在。
Grant 在 dispatch claim transaction 内冻结。Plugin-bound Run 若找不到匹配 grant
即 fail visible；只有明确标记的 legacy Run 才可走现有 compatibility resolver。
已经 queued 的 Run 在 claim 时发现 drift，终止为 `dispatch_claim_unavailable`；它不
使用新 binding 重试，也不保留一份看似有效的 partial grant。

### Deep module 4：Review Plan / Coverage Ledger

Review Plan 把 immutable commit pair 转为 changed-hunk index 与 input coverage units。
首期仍执行一个 model turn，但 result validator 根据 index 校验 anchor，并把 eligible
hunks 标记为 indexed 或 skipped；它不声称模型语义上逐个审阅。后续多 Agent/shard 只替换 plan executor，不改变
provider adapter 或 result contract。

### Trigger adapter contract

- Manual：direct actor、prompt、selected profile/model/branch/permission；
- JType：claim/occurrence、Card ref、external actor、Automation owner；
- SCM：verified receipt、provider actor、repository/ref/object/action；
- Cron：scheduled fire、rule owner、window、output mode；
- API key：scoped service principal、idempotency key。

每个 adapter 负责自己的 verification 与 idempotency，但必须在创建 Run 前调用
同一个 resolver/evaluator。

### 开源对标约束

- 产品面以 OpenHands Agent Canvas 为主对标，明确拆分 Agent Profile、LLM Profile
  与 Runner Backend；
- Workflow authoring 与安全输出参考 GitHub Agentic Workflows，但执行合同保持
  provider-neutral；
- JType pickup 的 reconcile/retry/workspace lineage 参考 Symphony，并使用 Cloud
  durable Store 而不是进程内状态；
- PR Review 的 file/hunk coverage、定位和幂等发布参考 OpenCodeReview；
- 不复制“把宽写权限 PAT 直接交给 Agent/MCP”的路径，所有外部写入仍由 control
  plane grant + typed delivery adapter 完成。

## Testing

### Contract tests

1. Ship-R1 Developer/Reviewer 两种 code-owned built-in profile round-trip；
2. built-in definition 的语义内容改变但 revision 未递增时测试失败；
3. canonical contract hash 在 Memory/PostgreSQL 和重启后相同；
4. Manual/JType/SCM/Cron 经过各自 adapter 后共享同一个 resolver/canonicalization，
   但保留各自的 profile、delivery default 与 idempotency facts；
5. retry 明确复用 snapshot，new trigger 解析新 revision；
6. profile text 无法覆盖 Delivery Contract 或授权。

### Readiness tests（Ship-R2）

1. 缺模型、branch、Plugin、SCM grant、Runner capability、persistent workspace；
2. optional 与 required capability 的差别；
3. preview/Create Run 共享 cases；
4. stale hash、Runner digest 漂移和 typed repair role；
5. PM profile 缺 MCP/JType、Developer profile 缺 toolchain 的负路径。

### SCM/review tests

1. Plugin reconnect、Service repository rename、provider config change后，旧 Run
   仍消费原 grant；
2. source、push、PR、review 调用的 installation/config/credential/repository revision
   完全一致；
3. base/head branch 移动后 Review 仍使用 frozen SHA；
4. finding 命中 changed right-side line 才通过；context/deleted/out-of-range line 拒绝；
5. coverage complete、partial、skipped、0 finding；
6. GitHub inline batch 与 summary fallback；
7. duplicate synchronized delivery 不重复 Run，新 head SHA 创建新 Run；
8. publisher 稳定排序、bounded batch、retry 后不重复 inline/summary comment。

### UI tests

1. Ship-R1 keyboard contract disclosure；Ship-R2 再覆盖 profile selection 与 preflight；
2. Ship-R1 legacy/partial/invalid/loading states；Ship-R2 再覆盖 readiness stale/unavailable；
3. Run contract inspector 与 legacy empty state；
4. Review coverage complete/partial 与窄屏；
5. Viewer 只读，Member dispatch；Owner profile mutation 属于 Ship-R2。

### Delivery proof

- Orchestrator `go test ./...`；
- Console `pnpm test && pnpm typecheck`；
- Runner shell/Go tests与本地 mock review journey；
- 受影响 Kustomize target render；
- Copilot CLI（优先 claude-sonnet-5；不可用时明确记录实际模型，不静默冒充）、
  Claude CLI（glm-5.2）、Grok CLI 在设计前后和 implementation 后各独立审计；
- GitHub Ready PR、main merge、amd64 images workflow、`wangwenhui@local` 的
  `jcode` namespace rollout 与 live health/migration 验证。

## Out of Scope

- 首期 visual DAG/workflow canvas；
- 默认启用多 Agent 或 Agent 自主互相委派；
- 任意第三方 prompt 变成 system instruction；
- 自动 approve、merge 或显式 dispatch repository CI；
- 把 Draft PR 删除为不兼容状态；
- 普通 Run 的中途 workspace checkpoint；
- 在 Cloud turn-hook 中模拟文件级 CAS；
- 首期并行 Review shards；
- 自动安装语言 toolchain；Ship-R2 缺少时由 readiness 阻塞；
- 复制 jtype Card/PR 为 Cloud 内部 Work Item。

## Further Notes

### Superseded decisions

- 早期 Plugin 文档中“Cloud 不负责 source/bundle/push/PR/comment”已被当前
  run-scoped source 与 control-plane delivery implementation supersede；
- D08 的 Draft PR 默认已被 lifecycle-aware delivery supersede；
- D19 legacy Integration 只作为兼容，不得成为 Plugin-bound 新 Run 的 fallback；
- D04 的 per-issue PVC 描述与实际 per-Service PVC 不一致；新设计使用“Service
  workspace cache”，不把 PVC 描述成 Work Item checkpoint。

### Release sequence

1. **Ship-R1 / 本 PR**：Workflow Contract snapshot、Run SCM Grant convergence、
   deterministic Review commit pair/anchor validation、Run inspector UI；
2. **Ship-R2**：Project Agent Profile revision CRUD、Runner Profile catalog、Composer
   readiness preview、Portable Workflow Definition 与 Automation profile binding；
3. **Ship-R3**：Workspace Checkpoint + sibling jcode mutation CAS；
4. **Ship-R4**：bounded Review shards 与受控 multi-agent execution。

Ship-R1 是 tracer bullet，不把尚未实现的 Ship-R2 UI/CRUD 伪装成已交付。Ship-R1 必须证明
同一个私有 GitHub Review Run 从 source 到 writeback 使用同一 frozen grant，且
Run UI 可解释其 contract 与 review coverage。
