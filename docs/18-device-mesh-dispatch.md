# 18 — Desktop 互见与任务分发（Device Mesh & Dispatch）设计

状态：Draft（本期只设计，不实现；依赖 docs/17 的 M2–M5 已落地）
关联文档：17-jcode-device-relay（relay/E2EE/配对）、11-api（错误信封与 principal 语义）、14-cloud-v2-design（云端 runner 面）

## 1. 概述

让任意一台 jcode desktop（`jcode web` 本地 UI）能看到**同账号下**的其他 desktop/CLI 设备，并向它们分发任务——参考 OpenAI Codex 的 local/cloud 混合体验：任务从一端发起、在另一端执行、有任务列表和状态回传。

核心原则（全部继承 docs/17，不推翻任何已定决策）：

- **云端只做 relay**：desktop 之间不直连，互见与分发全部经过 orchestrator 的 device 命名空间。
- **E2E 加密不变**：账号级一把 CEK（§6.1），A 渲染 B 的会话用同一把 CEK，天然可行，无新密钥分发。
- **会话归本地**：被分发到设备 B 的会话就是 B 的本地会话，B 用户可见、可停止、可删除。
- **不加新指令 kind**：任务分发复用 `chat.send`，差异只在 payload 的 `dispatch` 标记与离线入队语义。

## 2. 现状盘点：desktop A 控制设备 B 缺什么

docs/17 落地后，"任何已授权客户端经云端 relay 控制任意设备"中的**客户端**特指 cloud console（session 鉴权）与手机 app。desktop 要互见，缺口有三：

1. **凭证能力**：desktop 持有的是 device token（`jcd_`，`~/.jcode/cloud.json`）。`resolvePrincipal` 的 device 分支只放行 `/internal/v1/device/*`（本设备上行/长轮询/ack）；读同账号设备列表、发指令要用的 `/api/v1/devices/*`（docs/17 §4.3）当前仅 session 鉴权，device token 调用即 403。
2. **UI**：jcode web UI 没有设备视图——设备列表/会话/控制面只存在于 console（M4 的 `/devices` 路由族）。
3. **本地中转**：device token 不能进前端。jcode web server（`internal/web`）需要一组本地代理路由（如 `/api/cloud/devices*`），用 `cloud.json` 里的 device token 转发云端 API，前端只跟本地 server 说话（与 connector 复用同一份 `cloud.json`，无新增凭证存储）。

## 3. 认证：token 委派方案（核心决策）

desktop 调 `/api/v1/devices/*` 需要 user 级能力。三个候选：

### 3.1 方案对比

| | (a) device token 扩权 | (b) desktop 配对成为"客户端" | (c) 云端另发受限 user-scope token |
|---|---|---|---|
| 做法 | device 分支放行同账号 `/api/v1/devices/*` 的读 + 消息类写 | desktop 走浏览器 OAuth 拿 user session，等价 console | device 登录后再铸一把 `jcdu_` token |
| 安全边界 | 不扩大：设备已持有账号级 CEK（密码学上本就能解密账号全部密文），API 放行只是把"密码学可行"变成"协议可行"；明文侧多暴露的仅是设备清单（名称/hostname/online），设备 register 时本就知道云端存这些 | 同等信任，但引入第二条登录链路（OAuth 回跳 localhost），每台机器两套凭证并存 | 与 (a) 权限集合相同；两 token 同机存储，泄露面相同，无安全收益 |
| 实现成本 | 最小：`resolvePrincipal` device 分支加 scope 判断，handler 的 user 匹配复用现有"本人设备 only"语义；无新表、无新流程 | 高：desktop 新增 OAuth 登录流；配对（§6.3）解决的是 CEK 分发而非 API 鉴权，desktop 已有 CEK，配对对它无增量 | 中：多一个凭证生命周期（颁发/存储/轮换/吊销联动），换来与 (a) 相同的能力 |
| 撤销语义 | 干净：吊销设备（`jcode logout` / console 吊销）→ token 立即失效，互见与分发能力同步消失；CEK 换代（§6.4）挡住历史新内容 | 复杂：吊销设备 vs 登出客户端两条路径，用户要理解"这台机器是设备还是客户端" | 复杂：吊销设备必须联动吊销委派 token，否则留孤儿凭证 |

### 3.2 推荐：(a) device token 扩权

理由：

- **信任边界不扩大**。device token 与 CEK 同在 `cloud.json`，泄露任一即账号内容失守；扩权不实质扩大 blast radius。
- **实现最小**。只是 principal 的 scope 扩展，不动 token 结构、不加端点族、不加表。
- **撤销语义单一**。一个 token = 一台设备的全部云端能力，吊销即全收。

配套约束（扩权的边界，全部 403）：

- device token 调 `/api/v1/devices*` 时 user_id 必须与 token 绑定的 user_id 一致（沿用"他人设备一律 403"语义）。
- device token **不可** `DELETE /api/v1/devices/{id}`（不能吊销别的设备）、**不可**发起/审批配对（pairings 写路径仅 session）、**不可**访问 project 命名空间与 `/api/v1/system*`。
- P5 只放行读（list/detail/sessions/events/stream）；消息类写（messages/stop/approval）P6 才放行。

## 4. 任务分发语义

### 4.1 两类分发

**交互式会话分发（P6）**——现状 `chat.send` 的直接延伸：A 选设备 B、输入 prompt → `POST /api/v1/devices/{id}/sessions/new/messages` 入队 `chat.send`（session_id=null）→ B 本地新建会话执行 → A 订 `GET /api/v1/devices/{id}/stream` 实时跟进。本质是 console M4 已有能力搬进 desktop UI。设备 offline 维持 409 `device_offline`（交互场景即时反馈比排队合理）。

**一次性任务（P7，fire-and-forget，参考 Codex task）**：描述任务 → 远端自动跑到底 → 完成/失败通知 + 结果回看。与交互式两点差异：

- **离线可入队**：offline 不 409，指令留 pending，B 下次 poll 拾取（同 §6.3 `pairing.request` 的语义）。
- **建议自动模式**：默认 `mode:"full_access"`，不等审批跑到底（B 本地权限策略仍可覆盖）。

### 4.2 数据模型决策：不加新表

**task = 带 `dispatch` 标记的 device_session**：

- `chat.send` 信封 payload 增加可选块（密文内，服务端不解析，§6.2 信封判定规则不变）：

  ```json
  { "text": "…", "mode": "full_access",
    "dispatch": { "task_id": "<发起端生成的 uuid>", "title": "修 login redirect 的 flake",
                  "from_device_id": "…", "from_device_name": "dev-mbp-01", "dispatched_at": "…" } }
  ```

- B 收到后建本地会话，session meta 携带 `dispatch` 块，随 `POST /internal/v1/device/sessions` 上行（meta 原样存取）→ 任何持 CEK 的端解密后都能渲染"这是 dev-mbp-01 分发来的任务"。
- **不加 `tasks` 表的理由**：任务的执行实体就是 device_session；状态全部可从 `device_commands.status` + `device_sessions.status` + `device_events` 的 `agent_done`/`task_status` 派生，新表只会引入第二份要与 session 对账的状态。且 E2EE 下服务端只见密文 title，"云端任务中心"的列表也只能由客户端解密渲染，新表收益有限。若未来确需跨发起端聚合视图，再单独评估。

### 4.3 状态机（task 视角，全部派生，无新存储）

| 状态 | 派生自 | 说明 |
|---|---|---|
| `queued` | `device_commands.status=pending` | 已入队（含 B 离线等待） |
| `dispatched` | `status=delivered` | B 已拾取 |
| `running` | ack `ok` + `device_sessions.status=running` | 远端执行中 |
| `done` | durable `agent_done`（`data.error` 空、`stopped` false） | 完成 |
| `error` | `agent_done` 带 `data.error`，或 command ack `failed` | 失败 |
| `stopped` | `agent_done` 带 `stopped:true`（被 B 本地或 A 远端 stop） | 中止 |

发起端本地维护 task registry（task_id → 目标 device_id/session_id/title/时间），是任务列表的唯一"新状态"，存发起端本地不落云。

### 4.4 通知路径

A 订 B 的 SSE（`GET /api/v1/devices/{id}/stream`），按 `dispatch.task_id` 匹配的 session_id 过滤 `session.event`；看到 durable `agent_done` 即收口 → desktop 本地通知 + 任务列表状态翻转。结果回看：`GET /api/v1/devices/{id}/sessions/{sid}/events?after_seq=0` 拉全量，CEK 解密渲染（同一把 CEK，无需新通道）。断线用 `after_seq` 补 durable gap，与 M4 console 重连语义一致。`device_events` 已够，无新增通知基础设施。

## 5. API 增改清单

| # | 变更 | 说明 | 期 |
|---|---|---|---|
| 1 | `resolvePrincipal` device 分支扩 scope | 放行同账号 `GET /api/v1/devices*`（list/detail/sessions/events/stream）；禁 DELETE、禁 pairings 写 | P5 |
| 2 | jcode `internal/web` 新增 cloud 代理路由 | 如 `GET /api/cloud/devices`、`GET /api/cloud/devices/{id}/stream`（SSE 透传）：本地 session 鉴权，device token 留在 server 进程 | P5 |
| 3 | device token 放行消息类写 | `POST /api/v1/devices/{id}/sessions/{sid}/messages|stop|approval`（含 sid=new） | P6 |
| 4 | `chat.send` payload 约定 `dispatch` 块 | 信封内可选字段，三端（jcode/console/手机）统一；meta 携带上行 | P6 |
| 5 | `device_commands` 增加 `initiated_by` 列 | TEXT，`user:<id>` / `device:<id>` / `service:<name>`；审计"谁向谁分发了什么" | P6 |
| 6 | 新增 `POST /api/v1/devices/{id}/tasks` | fire-and-forget 包装：body `{text, mode?, title}` 明文或 `{envelope}`；服务端组 `chat.send`（session_id=null + `dispatch` 块）；**offline 不 409**，入队 pending 返回 202 `{command_id, task_id}` | P7 |

错误信封、`?access_token=` SSE 鉴权、严格 JSON decode 均沿用 docs/11 与 docs/17 既有约定。

## 6. 安全与审计

- **E2EE 渲染**：A 渲染 B 的会话用同一把账号级 CEK；信封带 `key_gen`，换代（§6.4）后历史用旧代、新事件用新代，与 console/手机完全一致。无 CEK 时复用 M4 的配对引导卡片状态。
- **扩权风险与缓解**：device token 泄露 = 同账号全部设备可读可发指令；但同一泄露下 CEK 已失守，扩权不实质扩大损失。缓解 = 吊销即全失效（§3.2 撤销语义）+ CEK 换代挡新内容。
- **审计**：`device_commands.initiated_by` 记录发起人（kind、目标 device_id、initiator、时间戳、ack 结果可查）；指令内容密文不可审计，符合 E2E 原则。被分发端 B 的会话 meta 带 `channel:"device"` + `dispatch.from_device_name`，B 本地 UI 可见来源并可停止/删除（会话归本地原则）。
- **明确边界**：device token 永不能吊销其他设备、不能碰配对写路径、不能碰 project/system 命名空间（§3.2）。

## 7. 客户端

### 7.1 jcode desktop（P5–P7）

- jcode web UI 侧边栏新增"设备"分区：本机置顶（this device 徽章），其余 desktop/CLI 设备带 online 圆点、平台徽章、当前 project（来自 session meta 解密）。
- 设备面板（P5 只读）→ 交互分发（P6：选设备 → prompt → 确认卡 → `sid=new` 分发）→ 一次性任务 + 通知（P7）。
- 远端会话视图与本地会话同一渲染内核，顶部"运行于 <设备名>"横幅 + 停止控制；分发来源在会话内可见。
- 设计原型：`design/desktop-devices-panel.html`、`design/desktop-dispatch.html`、`design/desktop-remote-session.html`。

### 7.2 console / 手机

零改动。desktop 分发出的会话经 meta 的 `dispatch` 块在 console 设备视图同样可识别（渲染"由 dev-mbp-01 分发"badge 为可选增强）。

## 8. 与 Codex 的对照

| Codex 概念 | 本方案对应物 |
|---|---|
| Codex CLI（本地 agent） | 本地 jcode（device） |
| Codex cloud（托管容器跑 task） | jcloud runner（cloud v2 run，docs/11/14）；轻量场景 = peer desktop 设备 |
| 统一的 local/cloud 任务列表 | desktop 设备面板任务区（发起端本地 registry + SSE 状态派生） |
| local → cloud handoff | 任务分发：`POST /api/v1/devices/{id}/tasks` / `messages`（经 relay） |
| cloud environment（镜像 + repo 挂载） | device + project 工作目录（环境就是那台机器本身，会话归本地） |
| ChatGPT Codex web 查看云端任务 | console `/devices/:id/sessions/:sid`（M4 已有）/ desktop 远端会话视图 |
| 任务完成通知 | 发起端订目标设备 SSE 的 `agent_done` → desktop 本地通知 |

差异要点：Codex cloud 的任务跑在 OpenAI 托管的一次性容器里；本方案 P6/P7 的任务跑在**用户自己的另一台设备**上，重负载/隔离需求仍归 jcloud runner 面（docs/14），两者互补不替代。

## 9. 分期

| 期 | 内容 | 依赖 |
|----|------|------|
| P5 | 设备面板只读：principal 扩权（读）+ jcode web 代理路由 + desktop 设备分区 UI | M5 |
| P6 | 交互分发：消息类写放行 + `dispatch` meta + `initiated_by` 审计列 + 远端会话视图 | P5 |
| P7 | 一次性任务 + 通知：`/tasks` 端点（离线入队）+ 发起端 task registry + 完成通知与结果回看 | P6 |

## 10. 明确不做（本期）

- 跨账号分发、设备共享/协作
- 任务模板、定时/周期任务
- 云端任务中心（`tasks` 表，见 §4.2 理由）
- device token 权限分级（只读 device token）
- 离线唤醒、原生推送通知（同 docs/17 §11）

## 11. 与既有文档的关系

- docs/17 已定决策全部不动：纯出站 relay、E2EE 信封与配对、会话归本地、指令 kind 集合（任务复用 `chat.send`，不加新 kind）、409 `device_offline` 语义（仅 `/tasks` 端点例外，§4.1）。
- docs/11：新端点遵循同一错误信封与严格 decode；`initiated_by` 取值沿用 principal 分类。
- docs/03：全量历史同步仍是后续演进，本设计不依赖它。
