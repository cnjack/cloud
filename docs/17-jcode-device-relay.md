# 17 — jcode 设备互联（Device Relay）设计

状态：Draft（M5 — 登录/注册/relay/E2EE（AES-256-GCM + P-256 配对）已实现；手机 app 未上线）
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
| POST | `/auth/device/token` | 无 | CLI 轮询；body `{device_code, fingerprint?}`（M16：fingerprint = 机器指纹的 sha256 hex）； pending → 400 `authorization_pending`；批准 → 200 `{access_token, token_type:"device", device_id, deduped}`（`deduped:true` 表示同 user+同指纹复用了已有 devices 行） |
| （console 路由） | `/device?user_code=` | session | 浏览器端：输入/确认 user_code 的授权页（console SPA 路由，非 orchestrator 端点） |
| POST | `/auth/device/authorize` | session | 批准/拒绝 user_code。**CONSOLE_TOKEN（service principal）不能批准**——设备必须归属真实 user |

### 3.2 device token

- 新 principal 类型：`device`（user 级，区别于 project 级 `jck_` API key）。token 前缀 `jcd_` + 32B 随机 hex。
- 存储：`device_tokens` 表，仅存 SHA-256 hash；plaintext 仅颁发时返回一次。
- token 绑定 device_id + user_id；`resolvePrincipal` 增加 device 分支（在 API key 之后、session 之前）。
- **一次性兑换**：approved flow 首次轮询铸 token 并消费 flow，再次轮询返回 400 `token_already_redeemed`（丢响应需重新 login）。
- 吊销：设备侧 `jcode logout` 调 `POST /internal/v1/device/revoke` 自吊销（立即生效，天然幂等）；用户亦可在 console 设备管理页吊销（token 失效 + 触发 CEK 换代，见 §6.4）。

### 3.2.1 机器指纹幂等（M16）

- **指纹来源**：jcode 解析稳定机器指纹——macOS `IOPlatformUUID`（`ioreg -rd1 -c IOPlatformExpertDevice`）、Linux `/etc/machine-id`（回退 `/var/lib/dbus/machine-id`）、Windows 注册表 `HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid`；都取不到时 fallback 为 `fallback:<hostname>:<随机16B>`（随机数生成一次后存 `cloud.json` 的 `fingerprint` 字段，一旦写入不再变）。**线上只传 sha256(source)（带 domain separator），硬件 id 明文不出机器。**
- **登录幂等**：token 轮询带 `fingerprint` 时，orchestrator 按 `(user_id, fingerprint_hash)` 查非 revoked devices 行——命中则**复用**（更新 name/last_seen，新 token 绑到同一 device_id，响应 `deduped:true`），未命中才建行；并发兜底靠 `devices` 的部分唯一索引（migration 0036：`(user_id, fingerprint_hash) WHERE fingerprint_hash IS NOT NULL AND revoked_at IS NULL`），插入冲突时回退为复用。空指纹（老 CLI）永不参与去重。
- **注册幂等**：`/internal/v1/device/register` 也带 `fingerprint`，给颁发时没带指纹的行**回填**（仅当该行无指纹且同 user 无其他活设备占用），保证下一次登录能去重。

### 3.3 jcode 侧

- `jcode login [--cloud <url>]`：默认 `https://cloud.j-code.net`；允许 self-host 域名（**必须 https**；`localhost`/`127.0.0.1` 允许 http，仅开发用）。
- 流程：请求 device_code → 打印 user_code + verification_uri（尝试打开浏览器）→ 轮询 token（M16 起携带指纹 sha256）→ 写入 `~/.jcode/cloud.json`（0600）：`{cloud_url, device_id, device_token, device_name, public_key, private_key, key_gen, fingerprint}`（P-256 密钥对，base64；`cek_wrapped` 字段 M5 加密阶段加入；`fingerprint` 为 M16 指纹 source，仅存本地）。`jcode login --status` 显示指纹 hash 前 8 位；重复登录命中指纹幂等时提示"已识别此设备"。
- `jcode logout`：本地清除 + 调用吊销端点。
- config 增加 `cloud` 块（`internal/config`）：`{enabled, url, auto_connect}`，`jcode web` 启动时若已登录且 `auto_connect!=false` 则自动启动 connector。

## 4. Relay 协议

### 4.1 上行（jcode → orchestrator，device token 鉴权）

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/internal/v1/device/register` | 注册/心跳设备：`{name, hostname, jcode_version, pubkey, fingerprint?}`（M16：指纹 sha256，回填用；device_id 从 token 解析，不在 body）；返回 `{device_id, server_time, heartbeat_interval:30}` |
| POST | `/internal/v1/device/heartbeat` | 30s 心跳维持 online；>90s 无心跳标记 offline |
| POST | `/internal/v1/device/sessions` | upsert 会话索引：`{sessions:[{session_id, status:"idle"\|"running", meta, last_activity_at?}], replace:true}`（meta 原样存取——M3 明文 JSON，M5 起为密文，服务端不解析）；`last_activity_at` 是 connector 上报的真实本地活动 UTC ISO 时间（优先本地 updated_at，回退 start_time），仅作明文排序路由元数据；缺失的 legacy 行保持 null，绝不以云端镜像时间伪造。`replace:true` 将其作为设备完整快照并删除未列出的 mirror/events（本地删除或关闭同步会收敛到云端）；响应 `{sessions:[{session_id, last_seq}]}`（该 session 当前最大 durable seq，无则 0，供重连续号） |
| POST | `/internal/v1/device/sessions/{sid}/events` | 批量追加 durable 事件：`{events:[{seq, kind, payload}]}`（kind 明文字符串、payload 任意 JSON 原样存，服务端不解析仅透传）；`(device_id, sid, seq)` 冲突幂等跳过；响应 `{accepted:[seq…], conflicted:[seq…], max_seq}` |
| POST | `/internal/v1/device/sessions/{sid}/ephemeral` | 实时流式事件 `{kind, payload}`：不落库，仅推 SSE hub（`session.delta`），响应 202 |
| GET | `/internal/v1/device/pairings?status=pending` | 列出本设备的配对请求（默认 pending）：`{pairings:[{id,label,created_at}]}`；超过 10 分钟的 pending 在此惰性结算为 expired（§6.3） |
| POST | `/internal/v1/device/pairings/{pid}/respond` | 设备审批配对 `{approve, key_gen?, wrap?}`：approve 必须带当前 `key_gen` 与 ECIES 包裹的 CEK（`wrap`，原样存）；同结果重复 respond 幂等，竞争结果/旧代次 → 409；已过期 → 409 `pairing_expired`；他人配对 404 |
| POST | `/internal/v1/device/revoke` | 吊销自己的 device token（`jcode logout`）：204，立即生效（下一个请求即 401，天然幂等） |

### 4.2 下行（orchestrator → jcode，长轮询）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/internal/v1/device/poll?wait=25s` | 长轮询指令：有 pending 指令则标 `delivered` 并 200 返回 `{commands:[{id, kind, session_id, payload}]}`（按 created_at 序，session_id 为 null = 新会话）；没有则 hold 到 wait（默认 25s，上限 30s）后 204。投递为单发（delivered 不重投），以 ack 收口 |
| POST | `/internal/v1/device/commands/{id}/ack` | 指令执行回执 `{status:"ok"\|"error", result?}`：标 `acked`/`failed` + 存 result + acked_at；幂等（重复 ack 为 no-op 200），他人/未知指令一律 404 |

指令 kind：`chat.send`（新会话/追加消息）、`chat.stop`、`approval.respond`、`workspace.browse`（设备侧只读目录浏览）、`session.list.req`、`session.patch`（pin/archive/title）、`pairing.request`（§6.3）。`session.delete` 不再是客户端可下发指令；conversation 的所有权在 desktop，本地删除后由下一次 `replace:true` 快照收敛云端 mirror。

### 4.3 客户端 API（console/手机，session 鉴权，本人设备 only）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/v1/devices` | 我的设备列表（含 `online`：last_seen_at 在 DEVICE_HEARTBEAT_TTL 内）；默认不含已删除（revoked）设备 |
| GET | `/api/v1/devices/{id}` | 设备详情 |
| DELETE | `/api/v1/devices/{id}` | 删除设备（M16）：软删（`revoked_at`）+ 吊销其全部 device tokens（下一请求即 401）；历史保留（device_sessions/device_events 供审计），客户端表面一律读为不存在（GET/list/重复 DELETE 均 404，他人 403）；删除后 SSE 立即广播 `device.status {online:false}` |
| GET | `/api/v1/devices/{id}/sessions` | 会话索引（meta 原样返回，M5 起为密文由客户端解密）；按 `last_activity_at DESC NULLS LAST, session_id` 稳定排序。`updated_at` 仅表示云镜像刷新时间，不可作为会话活动时间。 |
| GET | `/api/v1/devices/{id}/sessions/{sid}/events?after_seq=N&limit=M` | 历史事件按 seq 升序回放 |
| GET | `/api/v1/devices/{id}/stream` | SSE 设备级实时流（支持 `?access_token=`）：`device.status`（online/offline 变更，连接时先发当前态）、`session.event`（durable 落库通知 `{session_id,seq,kind,payload}`）、`session.delta`（ephemeral 转发 `{session_id,kind,payload}`）；15s heartbeat 注释 |
| POST | `/api/v1/devices/{id}/sessions/{sid}/messages` | 发消息/新会话：body 二选一 `{text, mode?}`（明文，服务端组 payload 加 `channel:"console"`）或 `{envelope:{enc,key_gen,nonce,ct}}`（E2EE 密文原样存入指令 payload，服务端不解析，§6.2）；sid 可为 `new`（指令 session_id=null）；入队 `chat.send`，202 `{command_id, session_id}`。新会话的 connector ACK 必须带 `result.session_id`（明文或 E2EE result 信封中的真实本地 session id）；客户端按该 command_id 轮询 ACK 后才可导航，缺失/超时必须报错而非猜测列表项。设备 offline → 409 `device_offline` |
| POST | `/api/v1/devices/{id}/sessions/{sid}/stop` | 入队 `chat.stop`（payload `{}` 或 `{envelope}`，目标在指令 session_id 字段）；offline 同样 409 |
| POST | `/api/v1/devices/{id}/sessions/{sid}/approval` | `{approval_id, decision}` 或 `{envelope}` → 入队 `approval.respond`；offline 同样 409 |
| POST | `/api/v1/devices/{id}/workspace/browse` | `{path?}` 或 `{envelope}` → 入队只读 `workspace.browse`；空 path 从设备用户 home 开始 |
| GET | `/api/v1/devices/{id}/commands/{cid}` | 查询命令 `pending/delivered/acked/failed` 与 opaque result；工作区选择器据此取得设备返回的 `{current,folders}` |
| POST | `/api/v1/devices/{id}/pairings` | 发起配对请求 `{label, kty:"P-256", pubkey}`（§6.3）：建行 pending + 入队 `pairing.request` 指令（设备离线也入队，下次 poll 拾取）→ 201 `{pairing_id, status}` |
| GET | `/api/v1/devices/{id}/pairings/{pid}` | 配对状态轮询 `{status, key_gen, wrap?}`：approved 时带当前代 wrap（解包在客户端）；revoked 时客户端必须清除本地 CEK 并重新配对；pending 超 10 分钟读为 expired |
| GET | `/api/v1/devices/{id}/pairings?approver_id={approved_pid}&status=pending` | 已批准客户端列出待审批客户端；`approver_id` 必须仍为 approved，否则 403 `pairing_approval_required` |
| POST | `/api/v1/devices/{id}/pairings/{pid}/respond` | 已批准客户端审批其他客户端：`{approver_id, approve, key_gen?, wrap?}`；approve 必须提交当前 `key_gen`，wrap 由审批端用当前 CEK 为 requester pubkey 生成，服务端只保存密文；旧代次返回 409，客户端刷新后重试 |

他人设备一律 403（不存在的 404，与项目 authorize 语义一致）；`session.list.req` / `session.patch` 指令 kind 仍为协议预留。

### 4.4 事件分类

- **durable**：`user`、`assistant`（完整消息）、`tool_call`、`tool_result`、`approval_request`、`session_state`（busy/idle/done）、`error`。落 `device_events`，离线回放靠它。
- **ephemeral**：`assistant_delta`（token 流）、`progress`。只过 SSE hub，不落库。客户端断线重连用 `after_seq` 补 durable gap，delta 丢失由最终完整消息兜底（与 jcode web WS 重连语义一致）。

> 实现注记（M3，jcode `internal/cloud/events.go`）：wire kind 直接沿用 jcode WS 事件名——durable 有 `user_message` / `agent_message` / `tool_call` / `tool_result` / `approval_request` / `ask_user_request` / `agent_start` / `agent_done` / `task_status` / `todo_update` / `goal_update` / `session_reset` / `mode_changed` / `model_changed` / `subagent_event`；ephemeral 有 `agent_text` / `token_update` / `subagent_progress`。其中 **`agent_message` 不是 jcode 原生事件**：jcode 只发 token 级 `agent_text`，connector 在事件泵里按 session 累积 delta，收到同 session 的 `agent_done` 时合成一条 `{kind:"agent_message", payload:{data:{text: 全文, error, stopped}}}` 紧跟 `agent_done` 上传（buffer 上限 256KB 截断；`session_reset` 清空；connector 重启丢失未定稿 buffer）。

## 5. 数据模型（migration 草案）

> 实现注记（0030_device.sql）：主键/外键实际采用 TEXT（`domain.NewID()` hex），对齐现有 `users(id)` 类型；FK 均 `ON DELETE CASCADE`；`devices.pubkey` 允许空串占位（token 颁发时建行，pubkey 待 register 才到）。M3 relay 阶段 wire 上的 `meta`/`payload` 为明文 JSON，原样字节存入 `device_sessions.meta` / `device_events.envelope` / `device_commands.envelope,result`（bytea），服务端不解析；M5 E2EE 上线后这些列直接改存密文，表结构不变。下方 SQL 为逻辑模型。

```sql
CREATE TABLE devices (
  id            uuid PRIMARY KEY,
  user_id       uuid NOT NULL REFERENCES users(id),
  name          text NOT NULL,           -- 用户可改，默认 hostname
  hostname      text,
  jcode_version text,
  pubkey        text NOT NULL,           -- P-256 公钥 base64（SPKI）
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
  last_activity_at timestamptz, -- connector 本地活动时间；legacy 为 NULL
  updated_at timestamptz NOT NULL DEFAULT now(), -- 云镜像刷新时间
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
  status text NOT NULL DEFAULT 'pending',     -- pending/approved/denied/expired/revoked
  wrapped_cek bytea,                          -- 批准后由在线设备写入
  created_at timestamptz NOT NULL DEFAULT now(), resolved_at timestamptz
);
```

## 6. E2E 加密

> 算法定稿（M5）：信封 AES-256-GCM（弃用草案的 XChaCha20-Poly1305）；配对包裹 P-256 ECIES + HKDF-SHA256（弃用草案的 X25519），HKDF info 固定为 `"jcode-device-cek"`。选定 WebCrypto 原生算法栈，console/手机/jcode 三端零第三方依赖互通。跨端互验向量：`jcode-cloud-relay/shared/test-vectors.json`（`{cek_b64, plaintext, envelope}`）。

### 6.1 密钥结构

- **CEK**（账号级一把，AES-256-GCM，32B）：由第一台登录设备生成，永不明文出设备。
- **客户端配对密钥对**（P-256 ECDH）：每个客户端（console 浏览器/手机）一次配对生成一对，公钥（SPKI base64）随配对请求上云；设备的包裹用临时（ephemeral）P-256 密钥对完成，不依赖设备身份密钥。
- **generation**：换钥时 key_gen+1，新事件用新钥；旧代密钥保留用于解密历史；信封带 `key_gen`。

### 6.2 信封格式

```json
{ "enc": "aes-256-gcm", "key_gen": 1, "nonce": "<base64 12B>", "ct": "<base64>" }
```

**判定规则（三端统一）**：payload 是 object 且含字符串 `enc` 字段 → 信封；否则明文（灰度兼容，§6.6）。控制指令下行同理：`{text, mode?}` 明文或 `{envelope}` 密文二选一，envelope 原样存入 command payload，服务端不解析。

明文侧仅保留：device_id、session_id、seq、kind、时间戳、online 状态。

### 6.3 配对流程（新客户端拿 CEK）

1. 客户端（手机/console 浏览器）WebCrypto 生成 P-256 密钥对（`ECDH/P-256`，SPKI 导出 base64 为 pubkey），调 `POST /api/v1/devices/{id}/pairings`（body `{label, kty:"P-256", pubkey}`）。
2. 云端建 pairing 行（pending，10 分钟过期）并入队 `pairing.request` 指令（payload `{pairing_id, label, kty, pubkey}`）推给目标设备（若离线则 pending 留在队列，客户端轮询状态）。
3. 本地 jcode 在 UI（desktop/TUI/web 均可见，复用 approval 交互）弹出审批：显示 requester_label；CLI 侧 `jcode cloud approve <pairing_id>` 批准。任一仍为 approved 且持有当前 CEK 的 console/mobile 客户端也可以审批新请求；它先用自己的 pairing id 取得 pending 记录，再在本地完成相同的 CEK wrap。
4. 批准 → 设备生成临时 P-256 密钥对：ECDH(ephemeral 私钥, requester_pubkey) → HKDF-SHA256(salt=空, info="jcode-device-cek") 派生包裹密钥 → AES-256-GCM 加密 `{"cek":"<b64>","key_gen":N}` → `POST /internal/v1/device/pairings/{pid}/respond` body `{approve:true, key_gen:N, wrap:{"ephemeral_pubkey":"<b64 SPKI>","nonce":"<b64 12B>","ct":"<b64>"}}` 上行写入 `wrapped_cek`。服务端在同一事务锁定 device generation；若 revoke 已推进到 N+1，旧包裹以 409 拒绝，避免刚批准的客户端被留在旧钥代。
5. 客户端轮询 `GET /api/v1/devices/{id}/pairings/{pid}` 到 approved → 取回 wrap：ECDH(客户端私钥, ephemeral_pubkey) → 同一 HKDF 派生 → AES-256-GCM 解出 CEK → 存系统 keychain（手机）/ IndexedDB（console，per device_id 存 cek raw + key_gen）。
6. 配对记录 10 分钟过期（读取方惰性结算为 expired）。

**扫码配对（M11）**：设备侧 mint 一次性 offer（`POST /internal/v1/device/pairing-offers`，device token）→ `{offer_id, secret, expires_at}`（10min；服务端只存 secret 的 SHA-256），渲染 QR `jcode://pair?cloud=..&device=..&offer=..&secret=..`。扫码客户端调 `POST /api/v1/pairing-offers/{offer_id}/claim`（session，body `{secret, label, kty:"P-256", pubkey}`）→ 建 §6.3 标准 pairing 行（pending）+ `pairing.request` 指令（payload 额外带 `offer_id`，供设备识别自己的 offer 自动批准）→ offer 标已用（claimed_by/claimed_at，条件更新防并发双领）→ 201 `{pairing_id, device_id}`。负路径：secret 错 403、过期 410、已用 409。存储：`device_pairing_offers` 表（migration 0033）。claim 之后客户端走 §6.3 第 5 步原路径（轮询 → 解 wrap 存 CEK），移动端复用同一 PairingSession 恢复逻辑。

**移动端 OAuth 登录（M11）**：`GET /auth/login/{provider}?client=mobile` 在签名 state 里携带 client 标记；callback 完成 startSession 后 302 到固定 `jcode://auth#token=<session-token>`（token 放 fragment，不进日志/历史）。只接受 `client=mobile` 一个值（link/integration 模式拒绝），不接受任意 redirect 参数，防开放跳转。app 侧 tauri-plugin-deep-link 回收（Android intent-filter / iOS URL types，scheme jcode host auth），token 验证后存为 Bearer 登录态；手动粘贴 token 保留为降级。

### 6.4 吊销与换代

desktop 持久展示全部配对审计记录（pending/approved/denied/expired/revoked），而不是只展示进程内 pending inbox。只有 desktop/device-token 路径可以 revoke。

revoke 必须原子提交目标状态和换代结果：desktop 生成 gen N+1 CEK，为除目标外的每个 approved 客户端按其原始 P-256 pubkey 重包裹新 CEK，调用 device-token rekey 端点；服务端在同一事务中把目标置为 revoked、更新其余 wrap、更新 `devices.key_gen`。官方客户端在打开设备时轮询自己的 pairing 状态：仍 approved 且 `key_gen` 增加时用持久保存的 pairing 私钥解新 wrap；revoked 时清除 CEK，回到配对门。目标拿不到 N+1 CEK，因此不能解密后续内容。

任何 rekey/revoke 部分失败都必须显式报错；不得只改状态而不换钥，也不得只换本地钥而不提交服务端记录。

### 6.5 灾难恢复

CEK 生成时同时可导出 24 词 recovery phrase（BIP39 编码 256bit，`jcode cloud key show-phrase`）。全部设备丢失后，`jcode cloud key recover` 输入短语重建 CEK。

### 6.6 灰度策略

信封判定即 `enc` 标记规则（§6.2）：无 `enc` 字段 = 明文（M3 阶段），原样透传/渲染；E2EE 上线后新事件必须带 `enc`。服务端不校验内容，只透传——明文/密文天然可共存。客户端无 CEK 时按原样渲染（配对引导卡片是可见状态，§7.1），有 CEK 但解密失败则按错误处理（fail visibly）。

设备侧灰度开关：jcode 配置 `cloud.e2ee`（`~/.jcode/config.json`，bool，默认 true）。置 `false` 时 connector 跳过 CEK 初始化（`EnsureCEK`），上行保持明文路径——用于灰度回滚与排查，等价于测试注入的 `CipherDisabled`。缺省/`true` 即 M5 行为：connector 启动时惰性生成 CEK 并全量加密上行。

### 6.7 配对门（M13）

灰度期（§6.6）服务端对明文/密文一律透传，意味着任何持有用户 session 的调用方都能向已开启 E2EE 的设备直接注入明文指令——设备收到无法解密的 payload，E2EE 保证名存实亡。配对门在协议层关掉这条路：

- **状态上报**：connector register（`POST /internal/v1/device/register`）新增可选字段 `e2ee: bool`，取**实际加密状态**（CEK 已激活且 `cloud.e2ee` 未置 false），不是配置项原样。服务端存 `devices.e2ee`（migration 0035，默认 false），并在 `GET /api/v1/devices/{id}` 的 deviceView 里回显（非 omitempty，老 connector 缺省即 false）。
- **门**：所有客户端下行指令端点（messages、stop、approval、workspace browse）在 `e2ee=true` 时只接受 `{envelope:{enc,...}}` 密文形式；明文 body 返回 **409 `{error:{code:"pairing_required"}}`**，不入队。客户端见此错误应引导配对（§6.3）拿 CEK 后改发密文。
- **兼容**：`e2ee=false`（老 connector 或 `cloud.e2ee:false`）行为完全不变，明文路径照常放行——J8（e2ee:false 设备）明文发送不受影响即回归证据。

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
