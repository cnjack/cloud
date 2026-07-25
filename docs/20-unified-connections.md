# 20 · 统一 Connection 概念(kanban / gitea / github / gitlab)需求分析与对抗审查

状态:需求分析(未实现)。触发:D37 把 kanban 建链接改成"先 Connect 再点选"之后,
自然的问题——git 侧(gitea/github/gitlab)和看板侧能不能收敛成**同一个
connection 概念**(参考 ChatGPT/Codex 的 connector:安装 → 授权 → 已连接 →
资源一律下拉选择 → 连接对外暴露技能)。本文先定义需求,再逐条对抗审查。

---

## 1. 现状:四套凭据流程,四种 UX

| 面 | 凭据在哪 | 怎么获得 | 资源怎么选 |
|---|---|---|---|
| kanban link(D25/D36/D37) | `kanban_links.token_enc`(per-link) | 手贴 PAT / link 设备流 / project 设备流(D37 不落库,密文 blob 过渡) | workspace→board→列 级联选择器(D30),不可用时手动录入 |
| git integration(D19/F5) | `integrations.token_enc`(per-project,bot 身份) | 手贴 PAT,创建时验证连通性回填 `bot_username` | repo_owner/repo_name **手填**;webhook 用 member 自己的 OAuth 同步(D32) |
| 个人 OAuth 身份(M 系列) | 用户级 linked identity(gitea/github) | OAuth redirect 授权 | 仅用于 webhook sync 授权证明,不做资源枚举 |
| 个人 API key / 调度(F11/F12) | 无外部凭据 | — | — |

痛点:

1. **凭据重复录入**:同一个 gitea 实例,integration 要贴一次 PAT;member 做
   webhook 还要自己 OAuth 一遍;kanban 每个 link 还要各自贴/连。用户视角是
   "我不是已经连过了吗"。
2. **ID 手填**:repo_owner_name、workspace UUID 都要用户自己找、自己抄,
   错了只能在运行期炸(fail-visible 救回的是"可见",不是"顺手")。
3. **生命周期不一致**:kanban 有设备流 + 到期徽标;integration 只有
   "验证连通性";OAuth 身份没有健康态。没有一个统一的"已连接 / 待续期 /
   已失效"语义。
4. **能力不可发现**:连接能提供什么(能列哪些 repo、能写哪些 board)隐含在
   各处代码里,没有一个"这个连接暴露了哪些 capability"的一等模型。

## 2. 参考概念(Codex connector,截图所引)

- 插件市场:每个 provider 一个 connector,安装动作 = 授权(OAuth,多账号可挑)。
- 连接态一等可见:已连接 / 管理 / 卸载。
- 授权后**所有资源都是选择器**(Codex 任务页挑 repo、挑 branch),用户从不抄 ID。
- connector 暴露"技能"(它能做什么),宿主按技能挂载能力。

## 3. 需求定义(候选 D38)

### 3.1 实体:Connection(project 级)

```
Connection {
  id, project_id, provider    // jtype | gitea | github | gitlab
  host                        // 实例地址(jtype base URL / git host;见 §4.4 层级问题)
  credential { kind: pat|oauth|device, token_enc, expires_at?, identity }
  status: connected | expiring | expired | error(reason)   // 派生,永不静默
  capabilities[]              // 见 §3.3,按 provider 声明 + 按凭据实况降级
  created_by, created_at, updated_at
}
```

- 一个 project 同一 provider 可有**多个 connection**(多账号/多实例,如截图
  "choose which account");每个 connection 有唯一 `identity`(bot_username /
  oauth 登录名)用于消歧。
- 生命周期操作:`connect`(device flow 优先,OAuth redirect 其次,PAT 手贴兜底)、
  `reconnect`(旋转/续期)、`disconnect`(级联策略见 §4.7)。

### 3.2 统一的授权编排(connect-first)

所有需要外部凭据的表单(integration、kanban link、webhook setup)遵循同一顺序:
**① 选/建 Connection → ② 选择器挑资源 → ③ 提交**。没有可用 connection 时,
表单的第一控件就是"连接"(设备流/授权按钮),而不是一串待填的 ID。

### 3.3 Capabilities(连接的"技能")

Connection 声明并可被查询的能力集,消费者按需调用、按实况降级:

| capability | 提供者 | 消费者 |
|---|---|---|
| `repo:list` | gitea/github/gitlab | service 仓库选择器、webhook setup |
| `repo:webhook` | 同上 | automation webhook 注册/同步状态 |
| `board:list` / `board:columns` | jtype | kanban link 选择器 |
| `board:proxy` | jtype | 内嵌看板(D31,token 永不进浏览器) |
| `identity:whoami` | 全部 | 连接卡上的身份/健康态 |

能力**实况降级**(对应"skills 现在可能不健壮"):某 provider/实例缺某个
list API 时,capability 标记 `unavailable(reason)`,UI 回退手动录入 —
降级是显式状态,不是空白或静默失败。

### 3.4 凭据纪律(沿用现有红线)

- token 一律 `token_enc`(AES-256-GCM,JCLOUD_MASTER_KEY),API 永不回读明文;
  读取/枚举一律**服务端代理**(沿用 D31 模式),浏览器只见元数据。
- 个人 OAuth 身份与 bot connection **保持两个物种**(见 §4.2):webhook
  之类的"以触发者身份"操作继续用 member 自己的 OAuth;service 的
  clone/push/PR 继续用 bot connection。Connection 概念统一的是
  **bot/service 凭据这一侧**,不把个人身份吸进来。

## 4. 对抗审查

### 4.1 "这复活了 D37 刚刚否掉的 project 级常驻凭据"

属实。D37 被否条原文:project 级常驻 jtype 凭据会"重新引入一层共享凭据及
其轮换/失效语义,而 99% 场景首个 link 建完后 blob 即被消费"。审查结论:
- D37 的场景判断**对 kanban 仍然成立**——一个 project 的 jtype 往往就一
  个 workspace/board,per-link 凭据 + blob 过渡已经够轻。
- 但 git 侧不一样:repo 是多对多(一个 project 多个 service 可能跨多个
  repo/org),而且 integration 本来就是 project 级常驻凭据(D19)。
- **结论:不强制统一存储层。** Connection 是**概念层 + UX 层**的统一
  (授权编排、连接态、capabilities、选择器),存储层保持现状:
  git → `integrations`,kanban → `kanban_links.token_enc`。kanban link
  可以在表单层"复用 project connection 的授权结果"(就是 D37 的 blob
  路径),不必真建 connection 行。概念统一 ≠ 数据模型硬合并。

### 4.2 "个人 OAuth 和 bot connection 合并会不会破坏 D19/D32 的权限边界"

会,所以不能合并。D19 的核心是 service 的 git 操作以**机器人身份**执行
(人走了不塌、审计主体单一);D32 的核心是 webhook 注册用**触发者自己的
OAuth**,绝不借 integration bot credential(借了就变成 bot 权限内的
confused deputy)。两条都是红线。Connection 只统一 bot/service 凭据侧;
个人 linked identity 继续独立,webhook 编排照旧。这条必须在实现时写成
测试级 invariant(webhook 端点绝不接受 connection 凭据)。

### 4.3 "self-hosted 实例没法预注册 OAuth app,connect-first 会不会不成立"

部分成立。github.com/gitlab.com 可以注册固定 OAuth app;但 self-hosted
gitea/gitlab 的 OAuth app 是**每实例**注册的,cluster 不可能预知。结论:
- 授权方式按 provider×host 三态协商:**device flow > 预注册 OAuth > PAT
  手贴**。PAT 永远是 first-class escape hatch,不是二等公民(自建实例
  是主场景之一)。
- connect-first 的 UX 语义改为"先建立凭据(任意一种方式)再点选",
  而非"必须 OAuth"。

### 4.4 "jtype 的 endpoint 是 cluster 级(D27/D36),git host 是 project 级
(integration.host)——Connection.host 放哪层"

- jtype:endpoint 留 cluster 级(基础设施事实,只有一个实例),project
  connection 不复制它,只持有凭据。
- git:host 继续 per-connection(现状如此,且受 cluster git-host allowlist
  约束,D20)。
- 结论:Connection.host 对 git 必填、对 jtype 省略(或冗余回显 cluster
  值)。不为了字段齐整把 jtype base URL 下放——那是 D36 方案 B,已被否。

### 4.5 "capabilities 不完整时会不会变成第二个静默失败面"

要求是:capability 的不可用必须是**枚举出来的显式状态**
(`unavailable + reason`,如 "gitea 版本不支持按 org 列 repo"),UI 据此
回退手动录入并给出原因——和 kanban discovery 的 fail-visible 自动降级同
一套语义。实现时每 capability 一个探活/探测端点,结果进 connection 的
status,不做缓存谎言(探测失败 ≠ 能力不存在,要区分)。

### 4.6 "多 connection 会不会把每处消费点都搞复杂"

每个消费点(service 选 repo、link 选 board)当前只有一个凭据来源,引入
多 connection 后需要"选哪个 connection"这一步。缓解:消费点记住上次选
择(per project per surface),单 connection 时自动跳过选择步。复杂度
可控,但**第一阶段只允许单 connection/provider/project**,多连接等真实
需求(多账号、多实例)出现再开。

### 4.7 "disconnect 的级联是什么"

不能静默留下死 link/死 service。disconnect 时必须:列出受影响消费者
(links/services/webhooks)→ 显式确认 → 消费者进入 fail-visible 的
"missing credential"态(沿用 kanban link 的 `credential_status: missing`
语义),而不是级联删除用户资产。

### 4.8 "与 jcode 的关系"

本轮明确**不让 jcode 参与**(owner 要求)。jcode 侧有自己的 provider/
connection 模型,未来如要打通(本地 jcode 复用 cloud 的 connections),
Connection 的 schema 按"可导出超集"设计(provider/host/identity/
capabilities 都是中性字段),但现在不做任何 jcode 端工作。

### 4.9 "为什么不是直接实现"

风险集中在 4.1(存储层合不合并)和 4.2(权限边界),这两点没有真实用户
压力验证前不宜动数据模型。建议按 §5 分阶段,先做零迁移的概念层。

## 5. 分阶段建议

- **P0(本轮,纯文档)**:本文 + 决策日志挂链。明确"Connection 是概念层
  统一"的定位。
- **P1(UX 收敛,无 schema 变更)**:把 D37 的"connect-first + 选择器"
  模式推广到 integration 表单:git connection 建立(PAT 粘贴即"连接",
  带验证 + identity 回填)→ service repo 字段改选择器(`repo:list`
  capability,gitea 先行);kanban 侧不动(D37 已完成)。
- **P2(capability 模型)**:`GET /projects/{id}/connections` 视图层(聚合
  integrations + kanban links + oauth 身份的连接态)+ capabilities 探测
  端点 + 实况降级。UI 出一个统一的"连接"页(项目设置里的第四个 tab)。
- **P3(按需)**:多 connection/provider、OAuth redirect 授权(git.com
  系)、jcode 对齐。

## 6. 验收标准(概念层)

1. 任何一个需要外部凭据的表单,用户都不需要离开 console 去"找 ID"。
2. 任何一个凭据的失效/降级,都能在 console 看到一个显式状态而不是运行期
   报错。
3. token 明文不出现在任何 API 响应、日志、浏览器存储(现有红线,沿用)。
4. webhook 授权路径不接受任何 bot connection 凭据(测试级 invariant)。
