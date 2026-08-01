# 31 · Workflow case conversations 与 Video Prompt Studio POC

状态：Ship-R1 验收用例 / Ship-R2 输入
日期：2026-08-01

## 结论

不同场景需要不同的 trigger adapter、幂等键、输入事实和外部回写，但不需要复制
四套执行引擎。它们都应收敛为同一种不可变 `WorkflowContract`，再由一个 Runner
执行。Agent 知道自己应当做什么，不是靠猜对话，而是因为 Cloud 在创建 Run 时冻结
了 workflow、profile、模型、权限、timeout、delivery、verification 和 requirements。

`Developer / PM / Architect / Reviewer` 首先是 Profile，不等同于多 Agent。只有任务能
安全切成互不重叠的 work unit，并且存在预算、隔离、合并规则和 loop limit 时，才应
fan-out。普通实现、研究或架构任务使用一个明确角色的 Agent，更容易解释和追责。

## End-user conversations

每个 case 的有效执行 timeout 不低于 1800 秒。这里的“30 分钟”是可运行预算，不要求
Agent 人为等待；提前满足验收条件可以提前完成。

| Case | 用户会怎么说 | Trigger / Profile | 系统应完成什么 | 建议预算 |
| --- | --- | --- | --- | --- |
| PR Review | “检查这个 PR 的并发和权限问题，只报确定的问题。” | SCM PR / Reviewer | 冻结 base/head SHA，建立 changed-line plan，发布可定位 finding 与 coverage；不改代码 | 30–45m |
| 竞品研究入板 | “抓取这些竞品最近的 Video Agent 能力，整理成可执行 request 放进 JType。” | Manual/API / PM | 用 allowlisted MCP fetch，保留来源，去重并写入 JType；输出 research report 和 Card IDs | 45–90m |
| JType pickup | “把 Ready 列里的下一张卡实现掉。” | JType occurrence / Developer | 原子领取，冻结 Card receipt 和仓库授权，实现、测试、创建 Ready PR，回写 Run/PR 链接并移动卡片 | 30–120m |
| Weekly repo care | “每周总结最近改动，处理安全的小任务并更新 changelog。” | Cron / Developer | 固定时间窗口和 last-success cursor；生成 report；只有满足变更政策才提交 PR；幂等回写 changelog | 30–60m |
| Architecture proposal | “根据现有 ADR 给出队列恢复方案，不要直接改生产代码。” | Manual / Architect | 读取 CONTEXT/ADR，输出 alternatives、trade-off、failure model 和 ADR 草稿；delivery 为 document/diff-only | 45–90m |
| Video product dogfood | “做一个 MiniMax 视频 prompt 管理库，并能发起视频生成。” | JType/Manual / Developer | 从空仓库建立产品、测试 provider adapter、创建 Ready PR；API key 未配置时提供 mock mode，不伪造真实视频 | 60–180m |

### PR Review 对话

用户只期待三件事：审的是当前这次改动、评论落在正确的行、没有问题时能区分“真的
检查完”与“输入没覆盖完”。因此 Conversation 顶部应先显示 `base → head`、rules
revision 和 coverage，结论其次。Review workflow 永远是 read-only，不应因为 Service
之后改成可写就打开 PR。

### PM research → JType 对话

PM 期待的是带出处的 decision-ready request，而不是浏览摘要。Cloud 需要先做
readiness：MCP fetch 与 JType write capability 缺一项就不入队；写 Card 前用
`source URL + normalized title + board` 产生幂等键；结果同时保留来源、产品差距、
建议、验收条件与新建 Card ID。该 case 不需要多 Agent；当来源很多时可做受控 research
shards，主 Agent 只合并结构化 evidence。

### JType pickup 对话

用户期待“卡片被谁领取、现在在哪一步、最终 PR 是什么”清楚可见。Cloud 先创建
occurrence receipt，再冻结 Developer contract 和 SCM grant；失败时 Card 保持可修复
状态，重复 delivery 不产生第二个 Run。成功默认创建 Ready PR；Draft 只用于长 session
尚未结束或 Owner 显式保留的历史策略。

### Cron 对话

Cron 的关键差异不是 prompt，而是窗口和重放。每次 Run 要冻结 scheduled time、
`since/until`、上次成功 cursor 和 coalesce key。无安全变更时只发布 report；有改动时
仍走相同 PR delivery，绝不直接 merge。Changelog 更新必须由测试与 diff policy 验证。

## Video Prompt Studio 产品 POC

### API 名称校正

“MiniMax H3”按当前官方 API 应实现为 `MiniMax-Hailuo-2.3`，不是硬编码一个 `H3`
模型名。正式 Video Generation API 是异步三段式：创建任务、查询任务、取得文件。
旧的 `/v1/video_template_generation` Video Agent 模板接口在官方 reference 已标记
deprecated，因此产品首版不把它作为核心依赖，只保留未来 provider compatibility seam。

### 用户价值

团队可以把经过验证的镜头 prompt 当作可版本化资产，而不是散落在聊天记录中；可以
用变量生成最终 prompt，选择 Text-to-Video 或 Image-to-Video，并在同一处查看异步任务
状态、输入版本和结果。API key 只保留在服务端环境变量 `MINIMAX_API_KEY`，前端和日志
不得显示。

### MVP 范围

- Prompt library：title、description、tags、mode、prompt template、variables、version；
- Prompt compiler：变量 schema、必填校验、2000 字符限制、camera command preview；
- Generation：`MiniMax-Hailuo-2.3`，T2V/I2V，6/10 秒与 768P/1080P 合法组合校验；
- Job ledger：local ID、provider task ID、prompt version、status、error、result URL、timestamps；
- Provider interface：真实 MiniMax adapter + deterministic fake adapter；
- 无 key 时的 mock mode 和显式 banner；绝不声称 mock result 来自 MiniMax；
- 单元测试、provider contract test、README、本地 compose；不实现计费、多人审批或素材库。

### Cloud dogfood 验收

1. 新建独立 GitHub repository；
2. 在 Cloud 建立 Service，Git delivery 使用 lifecycle-aware Ready PR；
3. 用 60–180 分钟 Developer Run 完成 tracer-bullet；
4. Run detail 能看到冻结 contract、模型、timeout、SCM grant 和最终 PR；
5. 没有 `MINIMAX_API_KEY` 时 fake provider 的 create→poll→success 流程通过；
6. 有 key 时才允许显式 opt-in 的 smoke generation，避免测试意外产生费用；
7. PR Review workflow 对生成的 PR 再跑一次确定性 Review；
8. CI 通过后合并；Cloud 不自动 approve 或 merge。

## Ship-R1 与 Ship-R2 边界

Ship-R1 提供不可变合同、Developer/Reviewer built-in Profile、SCM grant、确定性 Review
和 inspector，因此可以安全观察上述 case。PM/Architect/Custom Profile、MCP readiness、
repository-owned workflow authoring、Video provider capability 和多 Agent shards 属于
Ship-R2。当前 UI 不应提前展示一个实际上不能配置的 Profile selector。
