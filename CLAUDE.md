# jcode Cloud — 项目准则

## 产品红线（必须遵守）

### 1. Fail-visible：禁止静默 mock / 静默降级

**任何未配置的依赖（LLM、git provider、webhook、对象存储…）必须以"一等公民状态"呈现，绝不允许静默替换成 mock 或假实现。**

- API 层：返回带类型的错误（如 `409 {"error":"model_not_configured"}`），而不是用假数据装作成功。
- UI 层：对应操作**禁用**并给出明确提示（"LLM 未配置 — 去 Cluster 页配置" / "联系管理员"），而不是让用户点了发送才发现结果是假的。
- Webhook/自动化路径：以可见方式回帖/记录原因，而不是跑出一个假结果。
- mock 实现（mockllm、FakeProvider 等）**只允许出现在测试与显式的 e2e rig 中**（由测试脚手架显式接线、显式标注），**永远不允许作为产品 manifest / 默认配置的兜底值**。
- 反面教材（勿重蹈）：base configmap 曾默认 `MODEL_BASE_URL=mockllm`，导致生产部署静默跑出 "jcode ran headless…" 的假输出，用户误以为 AI 已生效。

### 2. 凭据纪律

- 真实凭据只进 gitignored 的 `deploy/secrets/*.local.yaml`，绝不进 git。
- Gitea OAuth app 的 redirect_uris **不要用 API PATCH 修改**（每次 PATCH 轮换 client_secret）；要改去网页端。
- `kubectl apply -k` 会把占位符 secret 盖掉真实值：apply 之后必须重放对应的 `deploy/secrets/*.local.yaml` 并重启 orchestrator。

## 工作流程（每个 feature 必须走完）

1. **测试设计**先行：先列测试用例（可 mock 依赖，或尽可能本地启动真依赖），再写实现。
2. **实现**：小步提交粒度；遵循周边代码的注释密度与命名习惯。
3. **多人审计**：实现完成后由独立 reviewer（多代理）对 diff 做对抗式审查，确认无 CONFIRMED 级缺陷才继续。
4. **测试**：`cd orchestrator && go test ./...`；`cd console && pnpm test && pnpm typecheck`；manifest 动过则 `kubectl kustomize` 三个目标（根、overlays/company）都要能渲染。
5. **提交**：conventional commit；feature 级一个 commit。

## 仓库地图

- `orchestrator/` — Go 控制面（API、reconciler、store、k8s Job 调度、provider 客户端、OAuth、webhook）。
- `console/` — React SPA（vite + vitest），生产态 nginx 同源代理 `/api` `/auth` 到 orchestrator。
- `runner/` — runner 镜像（真 jcode 二进制 + entrypoint.sh 驱动 `jcode acp`）；`mockllm/` 仅供测试 rig。
- `deploy/` — kustomize：根 = 本地 OrbStack（e2e rig，显式接 mockllm）；`overlays/company` = 公司集群（jcode ns，ghcr 镜像，Kong ingress `cloud.j-code.net`）。
- `e2e/` — 端到端脚本（j1–j4，跑在 OrbStack rig 上）。
- `docs/` — 架构与决策日志（`02-decision-log.md`，D 编号）；重大取舍必须落 D 条目。

## 部署（公司集群）

- push main → GitHub Actions 构建 ghcr `:latest` → `kubectl --context wangwenhui@local apply -k deploy/overlays/company` → **重放 secrets** → `rollout restart deploy/orchestrator deploy/console -n jcode`。
- 集群访问需 `NO_PROXY=192.168.10.221`（本机代理会劫持内网段）。
