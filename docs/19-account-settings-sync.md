# Desktop 配置网格与 Cloud Provider Proxy

状态：Implementation contract

## 1. 产品目标

同一账号下的多台 jcode Desktop 共享**可移植配置**，但每台 Desktop
仍然是完整、可离线工作的本地产品。Cloud 是可选同步面，不是本地模型调用
的强制中转。

模型来源分为两条互不混淆的通路：

| 来源 | 凭据位置 | 推理路径 | Cloud 不可用时 |
|---|---|---|---|
| Desktop Provider | 每台获批 Desktop 的本地安全存储；经账号同步密钥 E2EE 同步 | Desktop 直连上游 | 继续可用 |
| Cloud Provider | orchestrator 的 `api_key_enc` / `headers_enc` | Desktop → Cloud proxy → 上游 | 显式不可用，不静默回退 |

Desktop 的模型选择器聚合两条通路，但运行时必须保留来源，不能因为同名
provider/model 而互相覆盖。

## 2. 非目标与安全边界

- 不把整个 `~/.jcode/config.json` 上传。
- 不同步本地路径、SSH/Docker alias、MCP/OAuth secret、channel 登录态、
  device token、审批策略、调试或遥测凭据。
- Desktop Provider 的明文 API key/custom headers 不得出现在 Cloud 日志、
  数据库或普通账号 session API。
- Cloud Provider 的明文凭据永不下发到 Desktop；只在 orchestrator 代理进程
  内解密并注入上游请求。
- Mobile/console 的设备会话 CEK 不等于 Desktop 配置同步密钥；获批查看会话
  不自动获得 Provider 密钥。

## 3. 密钥模型：Device CEK 与 Account Sync Key 分离

现有 `Device CEK` 生命周期实际属于一台 Desktop：首次登录时本机生成，
用于该设备的会话、命令和已配对 console/mobile 客户端。它不能直接作为
多 Desktop 配置密钥，否则任一设备的客户端撤销换钥会破坏其他 Desktop。

新增 `Account Sync Key`（ASK）：

- 32-byte AES-256 key，按账号维护 generation。
- 只发给获批 Desktop，保存在操作系统 keyring。
- 只加密账号偏好和 Desktop Provider 配置。
- Cloud 仅保存 ASK 针对 Desktop X25519 identity public key 的 opaque wrap。
- 新 Desktop 登录账号后先处于 `waiting_for_approval`；已有获批 Desktop
  本地完成 X25519 ECDH + HKDF-SHA256（ASK 独立域）+ AES-GCM wrap，Cloud 不接触 ASK 明文。
- 第一台 Desktop 在服务端尚无 ASK generation 时原子初始化 generation 1。
- 当前版本没有恢复短语：没有旧 Desktop 可审批时，不允许新设备自行生成
  另一把 ASK 并覆盖已有密文。账号恢复需要后续单独设计，避免把便捷恢复
  变成绕过设备审批的后门。

撤销 Desktop 的配置同步授权后，它不能读取新的 provider envelopes；这与
撤销整个 Device token 分开，避免影响该 Desktop 的会话中继。已经同步到
被撤销 Desktop 的上游 API key 无法被“远程遗忘”；真正撤销仍需在上游
轮换 key，UI 必须明确说明这一现实边界。

## 4. Desktop Provider 同步资源

Provider 是独立同步单元，不再放进单个 64 KiB whole-document：

```json
{
  "schema_version": 1,
  "provider_id": "openai",
  "kind": "openai",
  "config": {
    "name": "OpenAI",
    "base_url": "https://api.openai.com/v1",
    "api_key": "sk-...",
    "headers": {"X-Org-Id": "..."},
    "vision": null,
    "thinking": null,
    "reasoning_effort": "",
    "models": []
  },
  "model_state": {
    "favorite": ["gpt-5"],
    "enabled_models": ["gpt-5"],
    "effort_overrides": {"gpt-5": "high"}
  },
  "deleted": false,
  "updated_at": "2026-07-23T12:00:00Z"
}
```

明文只在获批 Desktop 出现。每个 provider row 保存：

- `user_id`
- `provider_id`
- 单调 `version`
- ASK AES-256-GCM `envelope`
- `deleted` tombstone
- `updated_at`

`provider_id` 是账号内稳定 identity；名称、图标和运行 adapter 来自 `kind`
或内置 provider registry。删除写 tombstone，防止离线 Desktop 把旧配置
复活。PUT 使用 `base_version` CAS；冲突必须拉取后显式合并或报告，不能
静默覆盖。

应用顺序固定为：

1. 拉取/合并 provider；
2. 合并 custom models 与 provider overrides；
3. 最后应用 `model` / `small_model`。

本机第一次开启“同步模型与密钥”：

- 远端为空：上传所选本地 provider；
- 远端非空：先拉取，再展示同 ID 冲突；不得无提示覆盖；
- 开关关闭后停止上传/拉取，但已落地本机的 provider 继续直接工作。

## 5. 账号偏好

账号偏好继续是小型 whole-document CAS，但改用 ASK 而不是 Device CEK：

```json
{
  "schema_version": 2,
  "model": "desktop:openai/gpt-5",
  "small_model": "desktop:openai/gpt-5-mini",
  "language": "zh-Hans",
  "theme": "jcode-dark",
  "default_mode": "approval",
  "updated_at": "2026-07-23T12:00:00Z"
}
```

模型引用必须带来源：

- `desktop:{provider_id}/{model_id}`
- `cloud:{provider_uuid}/{model_id}`

旧 v1 `provider/model` 在迁移时解释为 `desktop:`，写回即升级为 v2。

## 6. Cloud Provider 目录与图标契约

Cluster/Project Provider 保持 Cloud 托管凭据。暴露给 Desktop 的 DTO 至少
包含：

```json
{
  "model_id": "uuid",
  "provider_id": "uuid",
  "kind": "zhipuai",
  "provider_name": "ZhipuAI Coding Plan",
  "model_name": "GLM-5.2",
  "upstream_model_id": "glm-5.2",
  "scope": "project",
  "scope_id": "project-uuid",
  "capabilities": {"reasoning": true, "tools": true, "image": true},
  "context_window": 1000000
}
```

- `provider_id` 是 Cloud 资源 identity。
- `kind` 是品牌、图标和 Desktop adapter identity。
- Desktop 用 `kind` 查本地 canonical provider registry/icon；Cloud 不存
  图标二进制。
- 未知 `kind` 使用统一自定义 Provider 图标或名称首字母，不能伪装成已知品牌。
- 同一 `kind` 可以有多个不同 key/base URL/scope 的 provider 实例。

Desktop 可见集合为以下两部分的并集：

- 直接授予 Account 的 Cluster-global models；
- 用户有权访问的 Project enabled models 与该 Project 的 Cluster grants。

直接 Account grant 对该账号当前及未来新增的所有已认证 Desktop 生效；它不授予
Project membership，也不允许把 Project-owned provider/model 暴露给其他账号。
同一 model 同时从 Account 与 Project 可见时只返回一次，并优先标记
`scope: "account"`。Provider 的 `kind`、名称与图标协议保持不变。

## 7. Device Cloud Proxy

新增 device-token 入口：

```text
GET /internal/v1/device/cloud-models
ANY /internal/v1/device/cloud-models/{model_id}/llm/{rest...}
```

列表只返回脱敏目录。代理：

1. `requireDevice`，取得 `deviceUserID`；
2. 按 `model_id` 解析数据库记录；
3. 证明该用户拥有直接 Account grant，或对 model 所属
   project/cluster grant 有访问权；
4. 用现有 `modelcfg.Resolver` 解密 Cloud Provider key/headers；
5. 删除 Desktop 传入的 `Authorization`；
6. 先设置允许的 custom headers，最后设置受管 Bearer key；
7. 流式透传 SSE，绝不记录 body/key/header value。

现有 run-token LLM proxy 与 device proxy 必须共用一个
`proxyResolvedModel` 实现，避免安全规则漂移。Desktop 不能提交 base URL、
API key 或 arbitrary model config 来影响代理目标。

Cloud model 离线/未授权/删除分别返回 typed error：

- `503 cloud_model_unavailable`
- `404 cloud_model_not_found`

Desktop 显式标记 Cloud model 不可用，绝不自动换成本地同名模型。

## 8. Desktop UI

Settings → Cloud 新增“配置加密同步”：

- ASK 状态：未初始化 / 等待审批 / 已保护 / 错误；
- “同步模型与密钥”开关；
- 最后同步时间、立即同步；
- 待审批 Desktop，显示设备名与指纹；
- 批准、拒绝、撤销未来同步访问；
- 初次本地 provider 上传确认和同 ID 冲突处理；
- 安全说明：撤销设备不会擦除它已经拿到的上游 key。

Provider/模型 UI 按来源分组并显示 badge：

- `本地`：直接调用，可离线；
- `Cloud · <Project>` / `Cloud · Cluster`：proxy 调用，需要在线；
- 图标统一按 `kind` 解析。

## 9. API 与存储

新增 append-only migrations：

- `account_sync_keys`
- `account_sync_key_wraps`
- `account_provider_configs`

所有 FK 必须级联到 users/devices；PG 测试必须覆盖用户删除清理和列扫描。
provider vault API 只接受 device token，不提供 session-auth 等价入口，防止
普通浏览器登录态下载 provider 密文。

## 10. 测试与对抗审核

实现前固定以下用例：

### 多 Desktop

1. Desktop A 初始化 ASK；B 不能自行覆盖。
2. A 审批 B 后，两者 ASK 一致，Cloud/日志无明文。
3. B 拉取 provider 后关闭 Cloud，仍可直接调用本地 provider。
4. API key/header/custom model/默认模型增删改双向同步。
5. provider tombstone 不被离线旧副本复活。
6. CAS 冲突可见，不静默丢配置。
7. 撤销某 Desktop 的配置同步授权后不能读取后续 envelope，但不影响它的
   普通会话中继。

### Cloud Provider Proxy

1. Desktop 只看见直接授予其 Account，或其 Project 有权访问且
   enabled/granted 的模型。
2. 同账号两台现有 Desktop 与后来新增 Desktop 自动继承 Account grant；其他账号
   device token 看不到也不能代理该模型。
3. Account grant 撤销后目录和 proxy 下一次请求立即拒绝，不依赖缓存。
4. Project-owned model 不能被 Account grant；尝试返回
   `409 model_not_grantable`。
5. DTO `kind` 驱动与 Desktop 相同的 icon。
6. device token 永不转发上游；真实 key 最后注入。
7. custom headers 过滤 Host/hop-by-hop，受管 Authorization 胜出。
8. SSE、普通响应、GET `/models` 都可透传。
9. 跨账号/项目 model UUID 与未知 UUID 都返回相同的 404，不泄露配置存在性。
10. Cloud 不可用时 Desktop 本地 provider 仍工作。

### 对抗审核

- 数据库、API response、日志 grep provider secret 零命中。
- 尝试通过 rest path、Host、Authorization、自定义 headers 做 SSRF/凭据外带。
- session cookie、mobile pairing id、其他账号 device token 不能取 vault。
- 64 KiB account preferences 限制不影响独立 provider rows。
- 真 PG migration/scan/delete-cascade 测试，不只跑 memory store。

## 11. 发布顺序

1. Cloud migrations + ASK/vault/device proxy API；
2. Cloud 部署并验证旧 Desktop 兼容；
3. jcode connector/provider aggregator/UI；
4. 跨两台 Desktop e2e；
5. 官网 Desktop ↔ Cloud 专页、公开文档和 Remotion 动画；
6. Cloud 提交 `main`，jcode 通过 PR 合入。
