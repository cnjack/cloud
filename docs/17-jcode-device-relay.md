# 17 — jcode 设备互联（Device Relay）设计

状态：Draft（M1）
关联文档：03-storage-memory-sync（D12/D13）、11-api、12-e2e-flows、14-cloud-v2-design

## 1. 概述

让本地 jcode（CLI/desktop）登录 jcloud，并把本地会话通过云端 relay 暴露给远程客户端（cloud console、手机 app），实现与 jcode desktop 一致的远程会话体验。

核心原则：

- **云端只做 relay**：本地 jcode 纯出站连接（长轮询 + POST），无入站端口、无 NAT 打洞。
- **E2E 加密**：会话内容与指令密文经过云端，服务端只见路由元数据。
- **复用 runner 契约**：本地 jcode 在协议上伪装成一个"永驻 runner"，复用 `run_events` 只追加日志、消息队列、权限审批、SSE 流的既有模式。
- **会话归本地**：远程发起的会话就是本地会话（出现在本地 sessions 列表），云端是其加密副本 + 控制入口。

## 2. 架构

```
┌─────────────┐  HTTPS+SSE   ┌──────────────────┐  长轮询/POST(出站)  ┌─────────────────┐
│ 手机 app     │ ◄──────────► │  orchestrator    │ ◄─────────────────► │ 本地 jcode       │
│ (Tauri)      │              │  - device 注册    │                     │ - internal/cloud │
├─────────────┤              │  - device 命名空间│                     │   connector     │
│ console      │  客户端解密   │  - 密文事件日志    │                     │ - internal/web   │
│ (设备视图)    │              │  - SSE fanout    │                     │   (控制面,零改动) │
└─────────────┘              └──────────────────┘                     └─────────────────┘
        │                              │                                       │
   CEK 配对获取                   仅存密文+路由元数据                        CEK 持有者/签发者
```

### 2.1 设备命名空间

云端以 **device** 为一等命名空间（与 project 平级，挂在 user 下）：

```
users ──< devices ──< device_sessions ──< device_events (seq 只追加)
                 │                  └──< device_messages (下行指令队列)
                 └──< device_pairings (客户端配对/授权记录)
```

- 一台本地 jcode 安装 = 一个 device（持久 device_id，存 `~/.jcode/cloud.json`）。
- 一个本地 jcode session UUID = 一个 device_session；其事件流 = device_events（`(session_id, seq)` 唯一）。
- 远程客户端看到的会话列表/历史全部来自 device 命名空间；控制指令经 device_messages 队列下行。

## 3. 认证：Device Code 登录（RFC 8628）

### 3.1 端点（orchestrator 新增）

| 方法 | 路径 | 鉴权 | 说明 |
|------|------|------|------|
| POST | `/auth/device/code` | 无 | CLI 请求 device_code；body `{client_name}`；返回 `{device_code, user_code, verification_uri, expires_in, interval}` |
| POST | `/auth/device/token` | 无 | CLI 轮询；body `{device_code}`； pending → 400 `authorization_pending`；批准 → 200 `{access_token, token_type:"device", device_id}` |
| （console 路由） | `/device?user_code=` | session | 浏览器端：输入/确认 user_code 的授权页（console SPA 路由，非 orchestrator 端点） |
| POST | `/auth/device/authorize` | session | 批准/拒绝 user_code。**CONSOLE_TOKEN（service principal）不能批准**——设备必须归属真实 user |

### 3.2 device token

- 新 principal 类型：`device`（user 级，区别于 project 级 `jck_` API key）。token 前缀 `jcd_` + 32B 随机 hex。
- 存储：`device_tokens` 表，仅存 SHA-256 hash；plaintext 仅颁发时返回一次。
- token 绑定 device_id + user_id；`resolvePrincipal` 增加 device 分支（在 API key 之后、session 之前）。
- **一次性兑换**：approved flow 首次轮询铸 token 并消费 flow，再次轮询返回 400 `token_already_redeemed`（丢响应需重新 login）。
- 吊销：用户可在 console 设备管理页吊销（token 失效 + 触发 CEK 换代，见 §6.4）。

### 3.3 jcode 侧

- `jcode login [--cloud <url>]`：默认 `https://cloud.j-code.net`；允许 self-host 域名（**必须 https**；`localhost`/`127.0.0.1` 允许 http，仅开发用）。
- 流程：请求 device_code → 打印 user_code + verification_uri（尝试打开浏览器）→ 轮询 token → 写入 `~/.jcode/cloud.json`（0600）：`{cloud_url, device_id, device_token, device_name, public_key, private_key, key_gen}`（X25519 密钥对，base64；`cek_wrapped` 字段 P3 加密阶段加入）。
- `jcode logout`：本地清除 + 调用吊销端点。
- config 增加 `cloud` 块（`internal/config`）：`{enabled, url, auto_connect}`，`jcode web` 启动时若已登录且 `auto_connect!=false` 则自动启动 connector。

## 4. Relay 协议

### 4.1 上行（jcode → orchestrator，device token 鉴权）

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/internal/v1/device/register` | 注册/心跳设备：`{name, hostname, jcode_version, pubkey}`（device_id 从 token 解析，不在 body）；返回 `{device_id, server_time, heartbeat_interval:30}` |
| POST | `/internal/v1/device/heartbeat` | 30s 心跳维持 online；>90s 无心跳标记 offline |
| POST | `/internal/v1/device/sessions` |  upsert 会话元数据（SessionMeta 镜像，密文 payload） |
| POST | `/internal/v1/device/sessions/{sid}/events` | 批量追加事件：`{events:[{seq, kind, envelope}]}`，幂等（`(sid,seq)` 唯一冲突即跳过） |
| POST | `/internal/v1/device/sessions/{sid}/ephemeral` | 实时流式事件（不落库，仅 SSE 转发） |

### 4.2 下行（orchestrator → jcode，长轮询）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/internal/v1/device/poll?wait=25s` | 长轮询指令；返回 `{commands:[{id, kind, session_id?, envelope}]}`，空则 204 |
| POST | `/internal/v1/device/commands/{id}/ack` | 指令执行结果回执 `{status, envelope?}` |

指令 kind：`chat.send`（新会话/追加消息）、`chat.stop`、`approval.respond`、`session.list.req`、`session.delete`、`session.patch`（pin/archive/title）、`pairing.respond`（§6.3）。

### 4.3 客户端 API（console/手机，session 鉴权）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/v1/devices` | 我的设备列表（含 online 状态） |
| GET | `/api/v1/devices/{id}` | 设备详情 |
| DELETE | `/api/v1/devices/{id}` | 吊销设备 |
| GET | `/api/v1/devices/{id}/sessions` | 会话索引（密文，客户端解密） |
| GET | `/api/v1/devices/{id}/sessions/{sid}/events?after_seq=N` | 历史事件回放 |
| GET | `/api/v1/devices/{id}/stream` | SSE：设备级实时流（ephemeral + durable 通知 + online 状态） |
| POST | `/api/v1/devices/{id}/sessions/{sid}/messages` | 发消息/新会话（入队下行指令） |
| POST | `/api/v1/devices/{id}/sessions/{sid}/stop` | 停止 |
| POST | `/api/v1/devices/{id}/sessions/{sid}/approval` | 审批响应 |
| POST | `/api/v1/devices/{id}/pairings` | 发起配对请求（§6.3） |
| GET | `/api/v1/devices/{id}/pairings` | 配对状态查询 |

### 4.4 事件分类

- **durable**：`user`、`assistant`（完整消息）、`tool_call`、`tool_result`、`approval_request`、`session_state`（busy/idle/done）、`error`。落 `device_events`，离线回放靠它。
- **ephemeral**：`assistant_delta`（token 流）、`progress`。只过 SSE hub，不落库。客户端断线重连用 `after_seq` 补 durable gap，delta 丢失由最终完整消息兜底（与 jcode web WS 重连语义一致）。

## 5. 数据模型（migration 草案）

> 实现注记（0030_device.sql）：主键/外键实际采用 TEXT（`domain.NewID()` hex），对齐现有 `users(id)` 类型；FK 均 `ON DELETE CASCADE`；`devices.pubkey` 允许空串占位（token 颁发时建行，pubkey 待 register 才到）。下方 SQL 为逻辑模型。

```sql
CREATE TABLE devices (
  id            uuid PRIMARY KEY,
  user_id       uuid NOT NULL REFERENCES users(id),
  name          text NOT NULL,           -- 用户可改，默认 hostname
  hostname      text,
  jcode_version text,
  pubkey        text NOT NULL,           -- X25519 公钥 base64
  key_gen       int  NOT NULL DEFAULT 1, -- 当前 CEK 代
  last_seen_at  timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  revoked_at    timestamptz
);

CREATE TABLE device_tokens (
  id uuid PRIMARY KEY, device_id uuid NOT NULL REFERENCES devices(id),
  token_hash text NOT NULL UNIQUE, created_at timestamptz NOT NULL DEFAULT now(),
  revoked_at timestamptz
);

CREATE TABLE device_sessions (
  device_id uuid NOT NULL REFERENCES devices(id),
  session_id uuid NOT NULL,
  meta bytea,              -- E2EE 密文（SessionMeta JSON）
  status text,             -- 明文路由态: idle/running （供列表 UI）
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (device_id, session_id)
);

CREATE TABLE device_events (
  device_id uuid NOT NULL, session_id uuid NOT NULL,
  seq bigint NOT NULL, kind text NOT NULL,   -- kind 明文（路由/渲染骨架需要）
  envelope bytea NOT NULL,                    -- E2EE 密文
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (device_id, session_id, seq)
);

CREATE TABLE device_commands (
  id uuid PRIMARY KEY, device_id uuid NOT NULL REFERENCES devices(id),
  kind text NOT NULL, session_id uuid,
  envelope bytea NOT NULL,                    -- E2EE 密文（指令内容）
  status text NOT NULL DEFAULT 'pending',     -- pending/delivered/acked/failed
  result bytea, created_at timestamptz NOT NULL DEFAULT now(), acked_at timestamptz
);

CREATE TABLE device_pairings (
  id uuid PRIMARY KEY, device_id uuid NOT NULL REFERENCES devices(id),
  requester_label text NOT NULL,              -- "iPhone 15" / "console-web"
  requester_pubkey text NOT NULL,
  status text NOT NULL DEFAULT 'pending',     -- pending/approved/denied/expired
  wrapped_cek bytea,                          -- 批准后由在线设备写入
  created_at timestamptz NOT NULL DEFAULT now(), resolved_at timestamptz
);
```

## 6. E2E 加密

### 6.1 密钥结构

- **CEK**（账号级一把，XChaCha20-Poly1305，32B）：由第一台登录设备生成，永不明文出设备。
- **设备身份密钥对**（X25519）：每个设备/客户端一对，公钥随注册上云（明文，密钥交换所需）。
- **generation**：换钥时 key_gen+1，新事件用新钥；旧代密钥保留用于解密历史；信封带 `key_gen`。

### 6.2 信封格式

```json
{ "enc": "xchacha20poly1305", "key_gen": 1, "nonce": "<base64 24B>", "ct": "<base64>" }
```

明文侧仅保留：device_id、session_id、seq、kind、时间戳、online 状态。

### 6.3 配对流程（新客户端拿 CEK）

1. 客户端（手机/console 浏览器）生成身份密钥对，调 `POST /api/v1/devices/{id}/pairings`（带 requester_pubkey + label）。
2. 云端把 `pairing.request` 指令推给目标设备（若离线则 pending，客户端轮询状态）。
3. 本地 jcode 在 UI（desktop/TUI/web 均可见，复用 approval 交互）弹出审批：显示 requester_label。
4. 批准 → 本地用 requester_pubkey 做 X25519 ECDH → HKDF 派生包裹密钥 → 加密 CEK（全部代）→ `pairing.respond` 上行写入 `wrapped_cek`。
5. 客户端轮询到 approved → 取回 wrapped_cek 解开 → 存系统 keychain（手机）/ IndexedDB（console）。
6. 配对记录 10 分钟过期。

### 6.4 吊销与换代

吊销设备/客户端 → 任一在线持有 CEK 的设备执行：生成 gen N+1 CEK → 逐客户端用其公钥包裹分发（走配对响应同一通道）→ 服务端 `devices.key_gen` 更新。被吊销方无法读新内容。

### 6.5 灾难恢复

CEK 生成时显示 24 词 recovery phrase（BIP39 编码 256bit）。全部设备丢失后，`jcode login --recover` 输入短语重建 CEK。

### 6.6 灰度策略

信封 `enc` 字段缺省 = 明文（P2 阶段）；P3 上线后新事件必须带 `enc`。服务端不校验内容，只透传——明文/密文天然可共存。

## 7. 客户端

### 7.1 cloud console（M4）

- 新路由：`/devices`（设备列表）、`/devices/:id`（welcome：新会话输入 + 会话列表）、`/devices/:id/sessions/:sid`（会话页）。
- 渲染复用 jcode-ui 组件；CEK 在 IndexedDB，未配对时显示配对引导。
- 设备离线：列表/历史可看（缓存），输入禁用 + offline 横幅。

### 7.2 手机 app（M6）

- Tauri 2 mobile（iOS/Android），前端独立 Vite/React SPA（`mobile/`）。
- 首页 = 设备列表 → 设备 welcome（复刻 desktop welcome 交互）→ 会话页。
- 渲染内核与 console 设备视图同一代码（共享包），外壳响应式。
- 配对后 CEK 存 iOS Keychain / Android Keystore。

### 7.3 jcode 本地（M2/M3）

- `jcode login/logout` 子命令。
- `internal/cloud/` connector：随 `jcode web` 启动（`cloud.auto_connect`），心跳 + 长轮询 + 指令转发本地 `internal/web` REST + 订阅本地 WS 事件上行。
- 会话来源标记 `channel: "mobile"|"console"`（复用现有 channel 机制）。

## 8. 部署与配置

- orchestrator 新增 env：无强制新增（device 功能默认可用）；`DEVICE_HEARTBEAT_TTL`（默认 90s）可选。
- jcloud 地址：jcode 默认 `https://cloud.j-code.net`；self-host 必须 https（localhost/127.0.0.1 豁免，仅开发）。
- 部署目标：k8s `ns=jcode`（context `wangwenhui@local`），ingress `cloud.j-code.net`（Kong）。SSE 需 `proxy_buffering off`（已有先例）。

## 9. 分期

| 期 | 内容 | 对应模块 |
|----|------|----------|
| P1 | device code 登录 + 设备注册 | M2 |
| P2 | relay 明文跑通 + console 设备视图 | M3 + M4 |
| P3 | E2EE 上线（CEK/generation/配对） | M5 |
| P4 | 手机 app + in-app 文档 + 部署 | M6 + M7 + M8 |

## 10. 与既有文档的关系

- `docs/03` D12/D13（同步做在 orchestrator、jcode 可插拔 Store）：本设计是其落地形态之一，但**首版不做全量历史同步**，只做"经 relay 的会话事件实时上行 + 会话索引镜像"；全量同步作为后续演进（§11）。
- `docs/11` API 约定：新端点遵循同一错误信封 `{error:{code,message}}` 与严格 JSON decode。

## 11. 明确不做（首版）

- 全量历史会话上传（只同步 relay 期间产生的数据 + 索引）
- 权限分级（只读/可写分离）
- 服务端搜索/统计会话内容（E2E 下不可能）
- 离线唤醒本地 jcode
- 原生推送通知（APNs/FCM，后续迭代）
