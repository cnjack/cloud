# 账号级配置同步（E2EE）

状态：Implementation contract

## 目标与边界

同一账号的 desktop、cloud console 和 mobile 可以同步少量可移植偏好。云端只保存账号级不透明 E2EE 信封，不解析配置值。

首期白名单：

- `model`：默认 `provider/model` 引用；接收端没有该 provider/model 时保留本地值并显示不可应用状态。
- `small_model`：可选轻量模型引用，同样只在本地可解析时应用。
- `language`：`en`、`zh-Hans`、`zh-Hant`、`ja`、`ko`。
- `theme`：内置主题 id；未知主题不应用。
- `default_mode`：`approval`、`plan`、`full_access`。

明确不同步：provider/API key、自定义 headers、MCP/OAuth secret、SSH/Docker alias、本地路径、channel 登录态、设备 token、审批/权限策略、调试与遥测凭据。原因是这些字段不是跨设备可移植偏好，或会扩大凭据泄露与权限提升面。

## Wire 与存储

逻辑明文（只在持有 CEK 的客户端出现）：

```json
{
  "schema_version": 1,
  "model": "openai/gpt-5",
  "small_model": "openai/gpt-5-mini",
  "language": "zh-Hans",
  "theme": "jcode-dark",
  "default_mode": "approval",
  "updated_at": "2026-07-22T12:00:00Z"
}
```

客户端用账号 CEK 的 AES-256-GCM 信封格式（docs/17 §6.2）加密。Postgres 只保存：`user_id`、单调 `version`、`envelope bytea`、`updated_at`。账号删除级联删除设置。

API 同时提供 session-auth `/api/v1/account/settings` 与本账号 device-token `/internal/v1/device/account-settings`：

- `GET`：不存在返回 `200 {version:0,envelope:null}`。
- `PUT {base_version,envelope}`：乐观并发；版本匹配后写 `version+1`，否则 `409 settings_conflict` 并返回当前版本。信封大小上限 64 KiB。

## 合并策略与测试计划

写入是 whole-document compare-and-swap，避免字段级最后写入静默覆盖。冲突端必须先读取、解密、以白名单字段合并后再提交。

验证：

1. session 与同账号 device token 可读写，其他账号隔离。
2. 数据库与日志零明文模型/语言值，只出现 envelope。
3. stale `base_version` 得到 409，当前值不被覆盖。
4. 超限、非信封、未知字段被拒绝。
5. desktop 只应用本地可解析的模型/主题/模式，非法语言不落盘。
