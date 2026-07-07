# 12 · E2E Flows — 双 persona 真实数据测试

用**真实私有 Gitea**(`https://git.scgzyun.com/ai/jcode-cloud-e2e`)在浏览器里以两个角色跑通端到端,像人一样发现使用不适点并改善。区别于自动化套件([e2e/](../e2e/),用集群内匿名 gitseed),本轮专测**真实数据 + 真实凭据**下的行为——正是它抓出了合成测试永远碰不到的私有仓库 clone bug。

> 凭据纪律:provider PAT 只进 k8s Secret(`gitea-orchestrator`),**从不落库**。仓库里任何地方都不含 token。

---

## Persona 模型

| 角色 | 关心什么 | 主要控制面 | 现状 |
|---|---|---|---|
| **A · 普通使用者 / project admin** | 建项目、派 run、盯实时进度、审 diff/PR、失败重试 | Console(项目/run/settings) | 完整 |
| **B · 集群管理员** | 容量、运行中 worker、provider 状态、guardrail、版本 | Console `/system`(Cluster)+ kubectl | 只读视图(本轮新建) |

**诚实声明**:MVP 是单租户 + 单个静态 console token,**没有真正的 OIDC/RBAC**。角色目前是"呈现层"信号(`VITE_ROLE`,默认 `cluster-admin`),用来**命名当前信任级别**而非按请求鉴权。header 身份 chip 的 tooltip 与本文都明说这点。真正的多租户 RBAC 见 [02-decision-log.md](02-decision-log.md) D03(外部 OIDC,future)。

---

## 前置

```sh
# 1) 真实 Gitea 接入 orchestrator(PAT 只进 Secret)
kubectl -n jcloud create secret generic gitea-orchestrator \
  --from-literal=GITEA_URL=https://git.scgzyun.com \
  --from-literal=GITEA_TOKEN=<PAT> --dry-run=client -o yaml | kubectl -n jcloud apply -f -
kubectl -n jcloud rollout restart deploy/orchestrator     # 日志应打印 "gitea draft-PR provider enabled"

# 2) 控制台连集群
kubectl -n jcloud port-forward svc/orchestrator 8080:8080 &
printf 'VITE_CONSOLE_TOKEN=%s\n' \
  "$(kubectl -n jcloud get secret orchestrator-secret -o jsonpath='{.data.CONSOLE_TOKEN}' | base64 -d)" \
  > console/.env.local
# vite dev on :5174, proxy /api -> localhost:8080
```

> 注意 host-match:`provider_url` 的 host 必须与 `GITEA_URL` 一致,否则护栏拒绝把 token 注入(见 F10)。

---

## 流程 A(project admin)

### FA-1 · 第一次使用 → readonly diff
1. 打开控制台 → Projects 空态引导 → **New project**。
2. 填 name + `https://git.scgzyun.com/ai/jcode-cloud-e2e.git`,git integration 留默认 **Read-only diff** → Create。
3. 在 New run 框输入任务 → **Run**。
4. **断言**:实时事件流出现 `Queued→Scheduling→CALL tool→RESULT→agent text→ARTIFACT`;终态 `Succeeded`;Diff tab 有非空 unified diff。
   - ⚠️ 若仓库私有且未配 provider token → 会 `clone_failed`(见 F1);这是本轮首个真实发现。

### FA-2 · 开启 Draft PR → 真实 PR
1. 建项目时(或 Settings 里)git integration 选 **Draft PR**,填 Provider repository=`ai/jcode-cloud-e2e`、Provider URL=`https://git.scgzyun.com`。
2. 项目详情出现 **● Draft PR → ai/jcode-cloud-e2e** 徽章。
3. New run → Run。
4. **断言**(真实数据全链路):
   - run `Succeeded`;事件流含 `PUSHED BRANCH agent/run-<id> @ <sha>`;
   - run 头部**自动**出现 **Draft PR #N ↗**(无需刷新,见 F11),链到真实 Gitea PR;
   - Gitea API 确认:`GET /repos/ai/jcode-cloud-e2e/pulls` 有该 PR,`head=agent/run-<id>`、`base=main`、`draft=true`、`merged=false`(硬把关)。
   - 实测证据:PR **#1**、**#2** 于 `git.scgzyun.com/ai/jcode-cloud-e2e/pulls` 由本流程开出,均为 draft、未合并。

### FA-3 · 失败可见性 + retry
1. 用一个会失败的场景(私有仓库无 token,或错误 URL)派 run。
2. **断言**:终态 `Failed`;顶部失败横幅**可读**并说明原因;`failure_reason` ∈ {clone_failed,setup_failed,agent_error,timeout,push_failed};事件流含带脱敏 git stderr 的 `CLONE_FAILED`(见 F2);**Retry** 生成新 run 且 UI 标注 "retry of <旧 id>"。

### FA-4 · 编辑 / 删除项目
1. 项目详情 → **Settings**。
2. **断言**:可改 default branch + git integration(mode/provider/repo/url);repo URL 固定(历史不可变);Delete project 危险区带二次确认;删除后回列表 + toast。

---

## 流程 B(cluster admin)

### FB-1 · 集群只读快照
1. header 显示 **● CLUSTER ADMIN** 身份 chip + **Cluster** 导航(project-admin 角色下二者隐藏)。
2. 进 **Cluster**(`/system`)。
3. **断言**:五张只读卡——Capacity(running/scheduling/queued vs max + bar)、Guardrails(run timeout / job TTL)、Provider(Gitea enabled + 真实 URL)、Runner(image/namespace/launcher)、Version;数据来自 `GET /api/v1/system`,**响应体不含任何密钥**(token/DSN/console token 均无)。

### FB-2 · 密钥不泄露(安全断言)
```sh
curl -s -H "Authorization: Bearer $CONSOLE_TOKEN" localhost:8080/api/v1/system | grep -F "$PAT"   # 必须无输出
```

---

## 发现与改善(F1–F11)

| # | 级别 | 发现 | 改善 | 状态 |
|---|---|---|---|---|
| **F1** | 阻断/BUG | 私有仓库 clone 失败(两种模式):entrypoint 用裸 REPO_URL,token 只用于 push。合成 gitseed(匿名)永不暴露。 | reconciler 只要配了 token 且 **repo host == provider host** 就注入(不限模式);entrypoint 用 token 构造认证 clone URL;`file://`/异 host 不注入(防 PAT 泄露)。 | ✅ 真实数据证明(PR #1/#2) |
| **F2** | 高/UX | 失败信息不说原因("git clone … failed")。 | runner 把脱敏 git stderr 尾部带进 `failure_reason` 消息。 | ✅ |
| **F3** | 高/UX | Draft-PR 无法在 UI 开启(只能 API PATCH)。 | 建项目弹窗 + Settings 加 "Git integration"(Read-only/Draft PR + provider/repo/url)。 | ✅ 实证 |
| **F3b** | 中/UX | 项目详情不显示 git mode。 | 详情页加 **Draft PR → owner/name** / Read-only 徽章。 | ✅ 实证 |
| **F4** | 高/UX | 建后无法编辑/删除项目。 | 详情页 **Settings** 弹窗:改 branch/integration + Delete(二次确认)。 | ✅ 实证 |
| **F5** | 中/文案 | "never leaves your domain" 在指向外部 Gitea 时误导。 | 改为 "Cloned into an ephemeral workspace in your cluster. Private repos need a configured provider token." | ✅ 实证 |
| **F6** | 高/产品 | app 内零管理员面/身份/角色(集群管理员只能 kubectl)。 | `GET /api/v1/system` 只读端点(无密钥)+ Console `/system` 视图 + header 身份/角色 chip;按角色显隐 Cluster 入口。 | ✅ 实证 |
| **F7** | 中/UX | 快速失败时红色失败事件夹在 Running 前,读起来"失败→运行→失败"。 | 事件严格按 seq 渲染 + 最高 seq 终态状态给 "FINAL STATUS · … · END OF RUN" 处理,终态无歧义。 | ✅ 实证 |
| **F10** | 中/限制 | 单个全局 `GITEA_TOKEN` 服务不了多个 Gitea host(集群内 + 外部)。host-match 护栏正确拒绝跨 host 注入,但也意味着一次只能用一个 host。 | **文档化限制**;根解 = per-project provider token(future,随多租户/OIDC)。 | 📝 已记 |
| **F11** | 中/UX | Draft PR 是 succeeded 后异步开的,run 页不自动显示 PR chip,要手动刷新。 | 终态后对 run 做有界轮询(1/2/4/8s)直到 pr_url 落地;readonly run 轮询几次即停。 | ✅ 修复 + 实证(chip 无刷新自动出现) |
| **F8** | 低/UX | 对根因未变的失败,Retry 是陷阱(必再失败)。 | 由 F1/F3 从根上消除主要成因;显式提示留待后续。 | 📝 已记 |
| **F9** | 低 | 桌面 preset 下内容偏窄。 | 判定为 preview 工具视口限制,非产品缺陷。 | 📝 已记 |

---

## 本轮改动落点

- `runner/entrypoint.sh`、`orchestrator/internal/reconciler/`(F1/F2 + host-match)+ `runner/test-private-clone.sh`(真 Gitea 容器证明,含 token 脱敏断言)
- `console/`:建项目/Settings 的 Git integration(F3/F4/F5)、GitModeBadge(F3b)、身份 chip + `/system` Cluster 视图(F6)、时间线终态处理(F7)、终态 PR 轮询(F11)
- `orchestrator/internal/api/system.go` + store `CountRunsByStatus`(F6,无密钥端点)
- 契约:[11-api.md](11-api.md) 更新(GIT_TOKEN 在 readonly 也注入 + host-match 规则、`GET /api/v1/system`、`push_failed`)

**回归**:orchestrator `go test -race` 8 包绿;console 77/77;集群 e2e 79/79(J1–J4);真实 Gitea 上开出 draft PR #1/#2(未合并)。

## 已知限制(诚实清单)

- **单 provider token / 单 host**(F10):跨 host 需 per-project token。
- **私有仓库 readonly** 需要 `provider_url` 与配置的 `GITEA_URL` host 一致才注入 token。
- **角色是呈现层**,非真授权(单 token MVP);真 RBAC 待 OIDC。
- LLM 仍是 mockllm(真实数据 = 真 Gitea;真实模型需 BYOK,见路线图)。
