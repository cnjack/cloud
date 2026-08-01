# Workflow Platform Ship-R1 audit

日期：2026-08-01
分支：`codex/workflow-platform`
范围：Workflow Contract、Run SCM Grant、确定性 PR Review、Console inspector、Runner protocol

## 工具与实际模型

| 工具 | 请求模型 | 实际模型 | 说明 |
| --- | --- | --- | --- |
| GitHub Copilot CLI | Sonnet 5 | `auto` | 当前 CLI/账户不接受 `claude-sonnet-5`；没有把 auto 冒充 Sonnet 5 |
| Claude CLI | `glm-5.2` | `glm-5.2` | 精确模型，read-only plan permission |
| Grok CLI | `grok-4.5` | `grok-4.5` | 精确模型，禁止网络与编辑 |

所有审计命令都被明确限制为 read-only；没有让外部 CLI 编辑、commit、push 或部署。

## 实现前审计

三类意见最终收敛到同一组 release invariant：

1. 每个新 Run 必须在创建时冻结完整 execution contract；
2. 私有仓库的 clone、push、PR 和 review 必须共用 claim 时冻结的 SCM grant；
3. Review 必须固定 base/head SHA，模型前先生成 changed-line plan；
4. structured finding 必须命中 plan 的右侧 changed line；
5. Viewer projection 不暴露 Profile instructions、credential 或 private anchor；
6. Ready PR 是新流程默认语义，Draft 只作为 session/兼容策略。

## 实现后第一轮

- Copilot：没有发现可复现 P0；建议把 review result 的 legacy/structured 分支写得更明确，已重构。
- Claude/GLM：`GO`，验证 migration/scan、所有创建路径、retry、anchor、idempotency、SCM consumer 和 UI projection；提出 model display、GitLab base label、marshal-error 三项 P2。
- Grok：`NO-GO`，指出 4 项 P1：runtime 仍读 live GitMode、incomplete grant fallback、部分 Review 入口晚失败、contract Review 仍接受 text/plain。

处理结果：4 项全部修复并增加 regression。Plugin grant 的 claim 校验在 PG/Mem 中要求 repository id/path/clone/default branch/acting principal 全部存在，否则在 `queued → scheduling` 前原子失败。

## 实现后第二轮

- Claude/GLM：`GO`，4 项 blocker 全部清除，无 P0/P1。
- Grok：确认 4 项 blocker 全部清除，但发现 GitLab MR webhook 常缺
  `diff_refs.base_sha`，自动 Review 没有补查 PR，判定为新 P1。

处理结果：统一 Plugin Review 在 revision 不完整时用 Service binding 的 provider
credential 调用 `PRByNumber`，拿到完整 pair 后才创建 Run；增加真实形状的 GitLab MR
webhook regression（输入不含 `diff_refs`，断言 provider lookup 恰好一次且 Run 冻结
base/head SHA）。

## 最终复核

Grok `grok-4.5` 最终结论：`GO`。

复核证据：

- GitLab no-`diff_refs` webhook 经 bound provider lookup 后可正常入队；
- one-shot、update-push、session push/ready 与 Runner `GIT_MODE` 均优先读取 immutable contract；
- Plugin-bound dispatch 在 claim transaction 内验证完整 frozen grant；
- Manual、legacy webhook、comment mention、unified Plugin webhook 和 Store safety net
  均在 queue 前拒绝缺失/非法 SHA；
- Contract Review 拒绝 text/plain，缺 plan 返回 `review_plan_required`，finding 经
  `ValidateAgainst` 校验；
- 没有剩余可复现 P0/P1。

额外采取的 P2 hardening：retry 复制 `PRReadyPolicy`；runtime Ready/Draft 决策优先读取
contract 的 frozen ready policy；创建时校验 SHA 是 7–64 位十六进制；Viewer contract
projection 对 slice 做防御性 copy；Runner README 改为 `REVIEW.json`/Review Plan 协议。

## 验证记录

- Orchestrator：`go test ./...` 通过；
- PostgreSQL migration/round-trip：migration 0064、contract/hash、plan/anchor 与 snapshot
  round-trip 通过（`JCLOUD_TEST_DATABASE_URL` gated integration）；
- Console：76 test files / 606 tests 通过；TypeScript typecheck、production build、design
  token lint 通过；
- Runner Go modules：`acpdrive`、`orchclient`、`mockllm` 全部通过；
- Runner shell：syntax、delivery contract、session scrub matrix 通过；
- Runner container：credential-free source fetch、exact-SHA Review Plan、structured
  `REVIEW.json` upload 通过；standalone no-TTY full loop 通过（最终复跑记录见交付）。

Console 测试仍打印既有 React `act(...)` 和 Router future-flag warning；Vite 仍提示大
chunk。这些没有导致失败，也不是本次 Ship-R1 引入的 release blocker。
