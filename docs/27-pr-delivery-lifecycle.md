# PRD：Agent 交付工作流与 Pull Request 生命周期

## Problem

jcode Cloud 已经能够让 runner 在隔离工作区中修改代码、生成提交与 bundle，再由可信 orchestrator 使用项目凭据推送分支并创建草稿 PR。这个安全边界是正确的，但当前产品把项目能力直接显示成“草稿 PR”，同时文档与 Agent 上下文没有清楚表达谁负责提交、什么时候创建 PR、任务结束后 PR 是否进入可评审状态。

这会造成四类问题：

1. 用户把“草稿 PR”理解为永久交付结果，不知道成功任务是否还要手动点击 Ready for review。
2. Agent 可能重复执行 `git commit`、`git push` 或创建 PR，也可能不知道平台会在它退出后接管交付。
3. 单次任务和多轮会话共用同一标签，却有不同的合理生命周期：单次任务已经完成时没有必要再停留在草稿；多轮会话在用户点击 Finish 前又确实需要草稿承接增量更新。
4. GitHub、Gitea、GitLab 对草稿状态的表达不同。如果没有统一状态与幂等规则，重试、崩溃恢复和控制台展示会互相矛盾。

目标是参考 Multica 的“Agent 明确知道仓库工作流”体验，同时保留 Cloud 多租户环境更严格的信任边界：Agent 负责实现、验证和描述交付意图；可信控制面负责提交收口、推送和 PR 状态变化。项目页展示的是稳定的交付能力“Pull request → 仓库”，而不是某个瞬时状态“草稿 PR”。

## Solution

为每次 Agent 运行注入一份平台管理的 Delivery Contract，明确仓库约束、Agent 职责和控制面职责。Delivery Contract 与仓库内的 `AGENTS.md` / `CLAUDE.md` 一起进入 Agent 上下文，但平台安全约束优先，项目说明只能补充工程实践，不能向 runner 下发 Provider 凭据或改变推送信任边界。

项目级 Git 交付模式在产品语言中收敛为：

- **Pull request**：Agent 产出变更后，由可信控制面推送隔离分支并维护 PR。
- **Diff only**：只保存可下载的差异产物，不向远端仓库写入。

Pull request 模式采用生命周期感知的状态机：

- 单次任务成功且存在代码变更：推送后直接创建 Ready PR。
- 多轮会话产生第一份代码变更：立即创建 Draft PR，后续轮次只快进更新同一分支和同一 PR。
- 多轮会话成功结束：仅在最后一份 bundle 已推送后，把 Draft PR 标记为 Ready for review。
- 多轮会话失败、取消或仍在等待输入：保留 Draft，避免把不完整变更误报为可评审。
- 没有代码变更：不创建 PR，并给出明确的 `no_changes` 结果。
- Provider 写入失败：运行结果和代码产物不丢失；交付状态显示具体失败原因并安全重试，不静默降级成“已完成”。

为避免对历史项目造成静默行为破坏，Pull request 模式增加完成策略：

- **Always draft**：兼容策略，所有新 PR 保持 Draft，用户手动转 Ready。
- **Lifecycle aware**：单次成功直接 Ready，多轮会话按 Draft → Ready 生命周期交付。

已有 `draft_pr` 项目迁移为 Always draft；新建 Pull request 项目默认 Lifecycle aware。项目 Owner 可以显式切换，控制台必须说明切换会影响评审通知与仓库 workflow。该设置只影响未来创建或完成的 PR，不批量改写历史 PR。

控制台项目页始终展示“Pull request → owner/repo”；运行详情展示该次交付的实时状态，例如 Draft、Ready、Closed 或 Merged。UI 不再把“草稿 PR”当作项目永久模式。

### Normative lifecycle

每个 run 维护单调交付状态：`none → branch_pushed(revision) → pr_ensured(draft|ready) → ready_ensured`。失败不会倒退已完成步骤；关闭或合并会冻结自动交付。执行状态与交付状态分开表达，代码执行成功不等于远端交付成功。

PR 创建采用持久化 claim/lease，确保同一 run 同一时刻只有一个控制器能执行远端 create。持有 claim 的控制器在 create 前再次按 head branch 查询；远端创建成功但本地落库前崩溃时，租约到期后的重试必须先查询并接管已有 PR。若同一 head 存在多个 open PR，进入 `delivery_conflict`，不自动关闭或任选其一。

会话的 `delivery_head_revision` 是最后一份已接受代码 bundle 的 revision。Finish 只表达终止意图，不直接触发 Ready；只有 run 已成功、没有待上传 bundle、`pushed_revision == delivery_head_revision`、PR 仍 open 且项目使用 Lifecycle aware 时才能 Ready。最后一轮没有代码变更时，可基于先前已推送 revision Ready；最后一轮失败或取消时保持 Draft 并记录阻塞原因。

人工状态以 Provider 为准：人工提前转 Ready 后 Cloud 不会重新转 Draft；人工关闭或合并后 Cloud 停止 Ready 和分支更新；人工把 Cloud 已 Ready 的 PR 转回 Draft 时 Cloud 不会再次改回 Ready；外部 force push 导致非快进时进入冲突而不是强推覆盖。

## User Stories

1. 作为项目维护者，我希望项目页显示“Pull request → 仓库”，以便理解这是交付能力，而不是所有任务最终都会停留在草稿状态。

2. 作为发起单次任务的开发者，我希望成功并产生变更的任务直接得到 Ready PR，以便马上进入正常评审，不再多做一次无意义的手动转换。

3. 作为使用多轮会话的开发者，我希望第一轮有效变更后就能看到 Draft PR，以便在任务进行中查看增量代码，又不会误以为实现已经完成。

4. 作为结束多轮会话的开发者，我希望系统先确认最后一轮代码已推送，再把 PR 标为 Ready，以便评审看到的内容与 Agent 最终结论一致。

5. 作为评审者，我希望失败、取消或尚未 Finish 的会话继续保持 Draft，以免收到不完整代码的正式评审信号。

6. 作为 Agent，我希望运行开始时获得明确的 Delivery Contract，知道自己应修改代码、运行测试并总结结果，但不应持有 Provider token、直接推送或自行创建 PR。

7. 作为仓库维护者，我希望 `AGENTS.md`、`CLAUDE.md` 和项目 Agent Instructions 仍可定义测试命令、编码规范、PR 描述要求等工作流细节，以便不同仓库保持自己的工程习惯。

8. 作为平台安全管理员，我希望项目说明不能覆盖“runner 不持有 Provider 凭据、禁止 force push、禁止自动合并”等硬约束，以便任意仓库内容都不能突破租户隔离边界。

9. 作为平台操作者，我希望 PR 创建、分支更新和 Ready 转换都可安全重试，以便控制器重启或网络抖动不会创建重复 PR，也不会重复发布状态事件。

10. 作为 GitHub、Gitea 或 GitLab 用户，我希望看到符合各自平台习惯的 Draft/Ready 状态，同时 Cloud 内部使用统一语义，以便跨 Provider 行为一致。

11. 作为控制台用户，我希望运行详情区分“代码执行成功”和“远端交付成功”，并显示可操作的错误，以便凭据失效、分支保护或 Provider API 故障不会被绿色成功状态掩盖。

12. 作为项目维护者，我希望可以选择 Pull request 或 Diff only，并设置默认目标分支，以便在安全审查严格的仓库中禁用远端写入。

12.1 作为已有项目维护者，我希望升级后仍保持 Always draft，只有主动选择 Lifecycle aware 才改变评审通知行为。

13. 作为维护者，我希望 PR 标题和正文包含任务摘要、运行链接、验证结果与 Agent 身份归因，以便评审者无需返回日志就能理解变更来源。

14. 作为维护者，我希望平台不主动合并 PR，也不承诺“不会触发 CI”，而是准确说明 Cloud 不主动 dispatch CI、仓库自身的 push/PR workflow 仍可能运行，以便正确评估自动化影响。

## Implementation Decisions

1. 保留现有内部 Git 模式值作为兼容层；公开 API 暂不强制已有客户端迁移。新增独立完成策略区分 Always draft 与 Lifecycle aware。已有服务回填 Always draft，新服务默认 Lifecycle aware；后续版本可通过版本化 API 将内部模式正式重命名为 `pull_request` 与 `diff_only`。

2. Delivery Contract 由平台在每次运行时生成并注入，包含目标分支、交付模式、会话类型、职责边界和禁止事项。仓库指令和项目 Agent Instructions 可以增加工程步骤，但不能覆盖平台安全约束。

3. Agent 不获得 Git Provider 凭据。Agent 只修改工作树、运行验证并总结；runner 的受信任 hook 生成规范本地提交和 bundle，orchestrator 负责远端 push、PR 创建/更新。runner 环境不包含 Provider token，外连策略也不允许把仓库内容变成远端写凭据。

4. Lifecycle aware 下，单次任务的 PR 创建请求显式携带 Ready 状态，多轮会话的首次 PR 创建显式携带 Draft 状态；Always draft 下两者都创建 Draft。`session` 布尔值在 run 创建时锁定交付生命周期，不支持运行中把 one-shot 转成 session。

5. 运行记录持久化 PR 是否仍为 Draft、进入 Ready 的时间、创建 claim/lease 和交付错误。控制器只有在状态为成功、PR 已存在、没有待上传 bundle、最新 bundle revision 已推送时才执行 Ready 转换。

6. Ready 转换必须幂等：如果 Provider 已经是 Ready，视为成功并修正本地状态；如果 PR 已关闭或合并，不重新打开，也不覆盖人工状态。Cloud 完成一次 Ready 后，人工重新转 Draft 也不会被自动改回。

7. 会话的分支更新继续坚持 fast-forward only，禁止 force push。非快进冲突必须显式失败并提示人工处理。

8. 每个运行最多对应一个隔离分支和一个 PR。通过持久化 claim/lease、create 前二次 head 查询和远端创建后的接管规则避免控制器并发或崩溃重试导致重复 PR；多个 open PR 被视为显式冲突。

9. PR 交付状态通过现有运行状态流实时刷新。控制台用状态字段决定 Draft/Ready 文案，不从标题前缀猜测。

10. Provider 凭据错误、权限不足、分支保护和 API 不支持等错误使用稳定类别呈现：`credentials_invalid`、`permission_denied`、`branch_protection`、`non_fast_forward`、`provider_unsupported`、`rate_limited`、`conflict_multiple_prs`。重试型错误指数退避；永久配置错误进入 parked，凭据轮换、配置变化或 Owner 手动 Retry delivery 时解除。

11. GitHub 使用原生 Draft/Ready 能力；Gitea 与 GitLab 使用各自原生能力或兼容的标题标记，并在适配层内规范化，业务层不依赖 Provider 特有字段。

12. 自动化边界保持不变：不自动 approve、不自动 merge、不显式 dispatch CI。由 push 或 PR 事件触发的仓库原生 workflow 属于仓库配置的正常副作用。

13. 交付状态独立于执行状态，至少包含 `none`、`diff_only`、`pushing`、`draft`、`ready`、`push_failed`、`pr_failed`、`parked`、`conflict`、`closed`、`merged` 与 `unknown`。旧记录的空状态映射为 `unknown`，不能猜成 Draft。

14. PR 正文创建时包含任务摘要、run 链接、触发者归因和验证摘要；多轮会话仅在 Ready 前更新一次最终摘要。所有 Agent 文本按普通不可信内容处理并执行 secret redaction。

### Provider capability matrix

| Provider | 创建 Draft | 创建 Ready | Draft → Ready | 状态来源 |
|---|---|---|---|---|
| GitHub | REST 原生 `draft` | REST 原生非 draft | GraphQL `markPullRequestReadyForReview` | Pull Request `draft/state/merged` |
| Gitea | `WIP:` / 实例配置的 WIP 前缀 | 普通标题 | 更新标题并仅移除 Cloud 自己添加的前缀 | PR state + 标题前缀 |
| GitLab | `Draft:` 标题语义 | 普通标题 | 更新标题并仅移除 Cloud 自己添加的前缀 | Merge Request `draft/state` |

Gitea/GitLab 的标题前缀属于各平台正式支持的 Draft/WIP 语义，但控制面必须保存由 Cloud 生成的规范标题，避免删除用户自己添加或编辑的前缀。Provider 不支持可靠检测或转换时返回 `provider_unsupported`，不假装 Ready。

### Compatibility and rollout

读取层把旧 `draft_pr` 显示为 Pull request，把 `readonly` 显示为 Diff only。新增字段允许旧 runner/console 在滚动升级窗口内忽略；空交付状态显示 Unknown。部署顺序为数据库迁移、orchestrator、runner、console；活跃 run 按创建时快照的完成策略执行，避免升级中途改变语义。

先在开关后部署并观察重复 PR、parked、Ready 失败和非快进指标，再对新项目启用 Lifecycle aware 默认。回滚时保留向后兼容的新增列并关闭 Lifecycle aware；不回滚已由 Provider 接受的 Ready 状态。上线 smoke 必须分别覆盖 one-shot Ready、session Draft、session Finish → Ready 和 Always draft。

## Testing Decisions

1. Provider 合约测试覆盖 Ready PR 创建、Draft PR 创建、Draft → Ready 幂等转换、已经 Ready、已关闭/已合并以及 API 错误。

2. Reconciler 测试覆盖单次成功直接 Ready、多轮首次 Draft、后续快进更新、Finish 后等待最新 revision 推送、成功后 Ready、失败/取消保留 Draft、重试不重复创建。

2.1 竞态测试覆盖两个控制器并发 create、远端 create 后本地落库前崩溃、Finish 与最后 bundle 上传并发、最后一轮 `no_changes`、同一 head 多个 open PR。

3. Store 的内存与 PostgreSQL 套件都覆盖 Draft 状态、Ready 时间、首次写入获胜和并发幂等性；迁移需在空库和已有数据上验证。

4. Runner 测试断言 Delivery Contract 在单次与会话运行中均被注入，且包含“Agent 不 push/不创建 PR、控制面接管交付”的边界说明。

5. Console 测试覆盖项目级 Pull request/Diff only 文案、运行级 Draft/Ready 状态和失败可见性，并确保旧 API 值仍可正确渲染。

5.1 手工漂移测试覆盖人工提前 Ready、Ready 后重新 Draft、关闭、合并、删除分支和外部 force push；Provider 合约按三家能力分别测试，而不是只依赖统一 fake。

6. 完成实现后使用两个独立模型进行对抗评审，重点寻找凭据泄漏、状态竞争、重复 PR、最后一轮未推送即 Ready、人工 PR 状态被覆盖以及跨 Provider 语义偏差。

7. 合并前运行 Go 全量测试、Console 单测与类型检查、Runner shell 测试，并渲染部署清单；部署后验证数据库迁移、两个 Deployment rollout 和公开健康检查。

## Out of Scope

- 自动 approve、自动 merge、自动关闭 PR 或绕过分支保护。
- 把 Provider token、App private key 或长期凭据注入 Agent/runner。
- 在本期移除旧的内部 `draft_pr` / `readonly` API 值。
- 自定义任意多阶段工作流 DSL、可编程审批链或 CI 编排器。
- 自动修复第三方仓库自身失败的 CI。
- 对历史运行批量创建 PR；历史 PR 状态仅在被读取或继续执行时按需校正。

## Notes

- Multica 的关键可借鉴点不是“让 Agent 拿 token 直接 push”，而是让 Agent 明确知道交付工作流，并允许仓库说明补充该工作流。Cloud 的多租户安全模型要求外部写入继续由可信控制面执行。
- “草稿 PR 不需要了”只对已经结束的单次成功任务成立。多轮会话仍需要 Draft 作为进行中变更的安全容器，因此应删除的是项目级永久“草稿 PR”标签，而不是 Draft 生命周期本身。
- 工作流可改变，但要分层：产品配置可改变交付模式和目标分支；仓库说明可改变工程步骤；只有平台版本升级才能改变凭据、推送、幂等和自动合并等安全策略。
