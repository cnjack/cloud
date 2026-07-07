# 13 · 多租户 + Service 层 + OAuth + PR Review — 施工蓝图

> 状态:**已全部落地**(2026-07-07,M1-M6 完成;实施裁决见各里程碑 commit)。本文是本轮迭代所有改动的**唯一契约**。
> 设计讨论结论见会话记录;每个里程碑落地后保持 main 可构建、测试全绿。

## 0 · 总原则

- **傻瓜 UX**:表面极简(一个仓库=一个项目,不暴露 service 术语);service 是渐进式高级能力。
- **真 token 永不进 runner pod**(对齐 04-byok):push/开 PR/发 review 全部由 orchestrator 控制面完成。
- **向后兼容**:`CONSOLE_TOKEN` 保留为"集群管理服务凭证"(等价 cluster-admin),现有 e2e/smoke 脚本不破坏;旧 API 形状保留 shim。
- 里程碑:M1 Service 层 → M2 Auth/RBAC → M3 Runner 契约反转 → M4 Console UI → M5 PR Review → M6 严格测试/体验 → 每步后 push main。

## 1 · 数据模型(Postgres,orchestrator/internal/store/migrations)

新表:

```sql
users(
  id uuid pk default gen_random_uuid(),
  display_name text not null,
  avatar_url text not null default '',
  is_cluster_admin boolean not null default false,
  created_at timestamptz not null default now()
)

user_identities(
  id uuid pk,
  user_id uuid not null references users(id) on delete cascade,
  provider text not null check (provider in ('gitea','github','gitlab')),
  provider_uid text not null,
  username text not null,
  access_token_enc bytea not null,      -- AES-256-GCM,密钥 env AUTH_TOKEN_KEY(32B base64)
  refresh_token_enc bytea,
  token_expires_at timestamptz,
  created_at timestamptz not null default now(),
  unique(provider, provider_uid)
)

sessions(
  id uuid pk,
  user_id uuid not null references users(id) on delete cascade,
  token_hash text not null unique,      -- sha256(opaque token),同 run token 惯例
  created_at timestamptz not null default now(),
  expires_at timestamptz not null,      -- 默认 30 天
  revoked_at timestamptz
)

project_members(
  project_id uuid references projects(id) on delete cascade,
  user_id uuid references users(id) on delete cascade,
  role text not null check (role in ('owner','member','viewer')),
  primary key(project_id, user_id)
)

services(
  id uuid pk,
  project_id uuid not null references projects(id) on delete cascade,
  name text not null default 'default',
  repo_kind text not null check (repo_kind in ('provider','raw')),
  provider text check (provider in ('gitea','github','gitlab')),  -- repo_kind=provider 时必填
  repo_owner_name text,                 -- "owner/name",repo_kind=provider 时必填
  raw_repo_url text,                    -- repo_kind=raw 时必填(git:// seed 等,仅 readonly)
  default_branch text not null default 'main',
  git_mode text not null default 'readonly' check (git_mode in ('readonly','draft_pr')),
  created_at timestamptz not null default now(),
  unique(project_id, name)
)
-- 约束:git_mode=draft_pr ⇒ repo_kind=provider(raw 仓库只能 readonly)
```

改表:

```sql
projects: + owner_user_id uuid null references users(id),
          + max_concurrent_runs int null,      -- null=继承全局
          + run_timeout_secs bigint null,
          + provider_allowlist text[] null,
          + injected_env jsonb not null default '{}'
          - 迁移后 DROP: repo_url, default_branch, git_mode, provider, provider_url, provider_repo
            (数据迁移:每个现有 project 先创建 name='default' 的 service 承接这些字段;
             repo_url 是 git://或非 provider 形态 → repo_kind='raw')

runs:     + service_id uuid not null references services(id),  -- backfill 到 default service
          + triggered_by_user_id uuid null references users(id),
          + kind text not null default 'agent' check (kind in ('agent','review')),
          + review_output text not null default '',   -- review 运行的产出(markdown)
          (project_id 保留,冗余便于查询/兼容)
```

产物存储:复用现有 artifact 机制,新增 kind:`bundle`(git bundle,bytea,上限 16MiB)、`source`(orchestrator 预克隆源码 bundle)。

## 2 · Auth(M2)

### 端点

```
GET  /auth/providers            → {providers:[{id,name,login_url}]}(无鉴权)
GET  /auth/login/{provider}     → 302 provider authorize(state 防 CSRF,cookie)
GET  /auth/callback/{provider}  → code 换 token(用 internal URL)→ upsert user+identity
                                   → 首个用户 is_cluster_admin=true
                                   → 建 session,Set-Cookie: jcloud_session(httpOnly,SameSite=Lax,30d)
                                   → 302 CONSOLE_URL(env,默认 http://localhost:5173)
GET  /auth/link/{provider}      → 已登录用户绑定额外身份(state 携带 session)
POST /auth/logout               → revoke session + 清 cookie
GET  /api/v1/me                 → {user:{id,display_name,avatar_url,is_cluster_admin},
                                    identities:[{provider,username}]}
```

### Provider 配置(env,dual-URL)

```
AUTH_TOKEN_KEY                  32B base64,AES-GCM 加密 identity token(bootstrap 生成进 Secret)
AUTH_{GITEA|GITHUB|GITLAB}_CLIENT_ID / _CLIENT_SECRET
AUTH_{P}_EXTERNAL_URL           浏览器可达(gitea 本地= http://localhost:3000;github 固定 https://github.com)
AUTH_{P}_INTERNAL_URL           服务端到服务端(gitea= http://gitea.jcloud.svc.cluster.local:3000)
CONSOLE_URL                     登录后跳回地址
```
未配置 client_id 的 provider 不出现在 /auth/providers。**本地 e2e 只有 gitea 真跑通**;github/gitlab 实现 + 单测(httptest 模拟),不做真实联调。

### 鉴权中间件(替换现 console middleware)

顺序:`jcloud_session` cookie → `Authorization: Bearer <session token>` → `Bearer CONSOLE_TOKEN`(→ 虚拟 cluster-admin 服务主体,user_id=null)。
SSE/下载的 `?access_token=` 同样接受 session token 或 CONSOLE_TOKEN。

### RBAC(服务端强制)

| 动作 | cluster-admin | owner | member | viewer |
|---|---|---|---|---|
| 建 project(成为 owner) | ✓ | ✓(任何登录用户) | — | — |
| project 设置/成员/删除/建改 service | ✓ | ✓ | ✗ | ✗ |
| 发 run / retry / cancel / 请求 review | ✓ | ✓ | ✓ | ✗ |
| 查看 project/run/diff/PR | ✓ | ✓ | ✓ | ✓ |
| GET /api/v1/system、列出所有 project | ✓ | 仅自己是成员的 | 同 | 同 |
| 成员管理 API | `GET/POST/DELETE /api/v1/projects/{id}/members` | | | |

用户搜索:`GET /api/v1/users?q=`(登录用户可用,供加成员)。

### Gitea 本地闭环(deploy)

- gitea-bootstrap Job 追加:用 admin 账号建 OAuth2 app(redirect: `http://localhost:8080/auth/callback/gitea`),把 client_id/secret + AUTH_TOKEN_KEY 写进 `gitea-orchestrator` Secret。
- Gitea `ROOT_URL=http://localhost:3000`(浏览器侧);集群内 API/git 仍走 svc DNS。
- deploy Makefile:`make port-forward-gitea`(3000)+ `make port-forward` 说明同时开两个;README 更新。

## 3 · Runner 契约反转(M3)

- **移除** jobEnv 中一切 provider token(GIT_TOKEN 等)。
- 新 env:`GIT_MODE`, `BRANCH_NAME=jcode/run-<shortid>`, `BASE_BRANCH`, `SOURCE_MODE=clone|fetch`。
- 克隆:public/raw → runner 直接 clone(现状);私有 provider repo → orchestrator 预克隆打 source bundle,runner `GET /internal/v1/runs/{id}/source`(RUN_TOKEN)下载解包。
- draft_pr 且有改动:runner 本地 commit 到 BRANCH_NAME,`git bundle create`(BASE..BRANCH),`POST /internal/v1/runs/{id}/bundle`(RUN_TOKEN,≤16MiB)。**不再自己 push**。
- orchestrator 收到 bundle + run 成功 → reconcile pass:取触发用户对应 provider 的 token(过期先 refresh;user_id=null 的遗留触发 → 回退全局 GITEA_TOKEN)→ 临时目录 clone(internal URL)→ fetch bundle → push 分支 → 开 draft PR(标题沿用 prTitle)→ 落 git_branch/pr_url。幂等:pr_url 已有则跳过。
- orchestrator 镜像加 `git` 二进制;临时目录严格清理;bundle 大小/超时上限。
- review run(kind=review):env `PR_HEAD`/`PR_BASE`;runner 产出 `/out/review.md` → `POST /internal/v1/runs/{id}/review`(RUN_TOKEN)→ orchestrator 存 review_output 并用**用户 token** 调 provider 发 PR review comment。

## 4 · API 面(M1/M2/M5 合计)

```
POST /api/v1/projects                {name}                      → 建 project(owner=当前用户)
POST /api/v1/projects/{id}/services  {name?,repo_url|{provider,owner_name},git_mode,default_branch?}
GET  /api/v1/projects/{id}/services
PATCH/DELETE /api/v1/services/{id}
POST /api/v1/services/{id}/runs      {prompt}                    → 发 run
GET  /api/v1/services/{id}/runs
POST /api/v1/runs/{id}/review                                    → 建 review run(kind=review,同 service)
GET  /api/v1/runs/{id}/pr            → {url,state,review_runs:[...]}(state 实时查 provider,查不到给 unknown)

—— 兼容 shim(现有脚本/console 不炸):——
POST /api/v1/projects 带 repo_url/git_mode/provider_repo → 自动建 default service
POST /api/v1/projects/{id}/runs → 路由到 default service
GET  /api/v1/projects/{id} → 附带 services 数组 + default service 字段平铺(过渡)
```

repo_url 智能解析(server 端统一):`git://` 或未知 host → raw;`http(s)://<known-provider-host>/owner/name(.git)` → provider 形态。known hosts 来自已配置的 AUTH_*_URL + github.com/gitlab.com。

## 5 · Console UX(M4/M5,傻瓜原则)

- **登录页**:provider 大按钮(来自 /auth/providers);"Advanced" 折叠里保留 console token 输入(管理员/脚本场景)。已有 OnboardingGate 三态保留,校验探测从 /api/v1/system 改为 /api/v1/me(401→登录)。
- **首次登录落地卡**:提示"你是第一位用户,已成为 cluster admin"。
- **身份 chip**:头像+用户名+角色;菜单:绑定其他 provider、登出。
- **新建项目**:两个字段——名字 + 仓库地址(一个输入框,智能解析),git 模式开关(默认 readonly)。service 概念不出现;背后建 default service。draft_pr 且用户未绑对应 provider → 内联提示"先绑定 GitLab"+按钮直达绑定。
- **项目页**:composer 置顶("让 agent 做什么?"),run 列表;只有 >1 个 service 或点了"Add repository"才出现 service 维度(composer 加 service 选择器)。设置弹窗加 Members tab(搜索用户,设 owner/member/viewer)。
- **Run 页**:现有 Timeline/Diff 之外,PR 存在时加 **PR tab**:PR 链接+状态徽章 + "Request AI review" 一个按钮 + review 结果(markdown 渲染)+ 历史 review runs。viewer 看不到操作按钮。
- UI 全部走既有 token 体系(lint:tokens),两主题都要好看。

## 6 · 测试与验收(M6,"严格")

- Go 单测:decision/rbac/oauth(httptest 三 provider)/token 加解密/bundle 收发/repo_url 解析/成员权限矩阵。`go test ./...` 全绿。
- store 迁移测试:旧数据(带 repo_url 的 project + runs)迁移后 default service 正确、run backfill 正确。
- Console vitest:登录页 provider 按钮、绑定提示、members、PR tab、composer;lint:tokens、typecheck 全绿。
- 脚本 e2e(make smoke 扩展或新增 e2e/j5-auth.sh):readonly seed run 照跑(CONSOLE_TOKEN 路径);draft_pr:OAuth 用户触发 → bundle → orchestrator 代 push → PR 属于该用户 → AI review 评论出现在 Gitea PR。
- 亲自体验:preview 浏览器完整走一遍(首用户 OAuth 注册成 admin → 建项目 → 跑 run → 看 PR → 点 AI review → Gitea 里看到评论;第二用户注册 → 无权限看到别人项目 → 被加为 viewer 后只读)。截图留档。

## 7 · 里程碑与委托

| # | 内容 | 模型 | 主要目录 |
|---|---|---|---|
| M1 | schema+store+service API+shim+reconciler 适配 | opus | orchestrator/ |
| M1b | deploy:gitea OAuth app bootstrap + ROOT_URL + port-forward-gitea | sonnet | deploy/ |
| M2 | users/sessions/OAuth/RBAC/middleware/me | opus | orchestrator/ |
| M3 | runner 契约反转 + orchestrator 代 push + review 通道 | opus | runner/ + orchestrator/ |
| M4 | console:登录/身份/项目/composer/members | opus | console/ |
| M5 | PR tab + AI review + /pr API | sonnet | console/ + orchestrator/(小) |
| M6 | e2e 脚本 + 全量验证 + 体验走查 | sonnet+本人 | e2e/ deploy/ |

每个 M 完成:构建+单测全绿 → 我验收 → commit(可 push)→ 下一个。

## 8 · @mention webhook(第二轮追加,2026-07-07)

> 状态:实施中。Gitea PR 评论 `@jcode …` 触发云端 run;GitHub/GitLab 留接口。

### 触发面与语义
- Gitea `issue_comment`(action=created)且 issue 是 PR、评论以 `@jcode`(不区分大小写,允许前导空白)开头:
  - `@jcode review` → 对该 PR 创建 kind=review run(pr_head/pr_base 取自 webhook payload;pr_url 预填现有 PR)。
  - `@jcode <任务文本>` → agent run,**基线 = PR head 分支**,产出**推回同一分支**(PR 自动更新):
    run 预填 pr_url/pr_number(现有 PR)+ pr_head_branch=head;jobEnv 规则:pr_head_branch 非空的 agent run → BASE_BRANCH=BRANCH_NAME=该分支;entrypoint 在 BRANCH_NAME==BASE_BRANCH 时 bundle = 克隆时 SHA..HEAD;push pass 见 run.pr_url 已存在 → **update 模式**:ff-only push 同名分支、跳过开 PR。
- 其他评论/编辑/删除一律 200 no-op。

### 端点与安全
- `POST /webhooks/gitea`(公开路径):校验 `X-Gitea-Signature`(HMAC-SHA256(raw body, WEBHOOK_SECRET)),不匹配 401;`X-Gitea-Event != issue_comment` → 200 忽略。
- **身份映射是硬门槛**:评论者 gitea uid → user_identities → jcloud 用户,且须为目标项目 member+;triggered_by=该用户(push 用其 OAuth token,与 §3 一致)。映射失败/无权限 → 用 GITEA_TOKEN PAT 回帖说明,不创建 run。webhook 路径**不允许**服务主体回退。
- 去重:runs 新增 origin('api'|'webhook',默认 api)、origin_comment_id、origin_comment_url;origin_comment_id 唯一部分索引;重投递 → 200 no-op。
- service 解析:payload repo full_name ↔ service(provider=gitea, repo_owner_name);多项目命中取"评论者是成员"的第一个;无命中 → PAT 回帖说明。

### 回执
- 受理成功:PAT 回帖 `🚀 jcode run started — <CONSOLE_URL>/runs/<id>`;失败场景回帖一句原因。v1 不发完成回执(update 模式 PR 自身会更新;review 会出现 review comment)。

### 部署
- bootstrap 追加:幂等生成 WEBHOOK_SECRET 入 gitea-orchestrator Secret;对 jcloud org 建 org 级 webhook(target `http://orchestrator.jcloud.svc.cluster.local:8080/webhooks/gitea`,secret 同步,events=issue_comment **加 pull_request_comment**——Gitea 对 PR 上的评论触发后者,只挂 issue_comment 会静默不投递,M7 live find)。orchestrator env 接 WEBHOOK_SECRET(未配置 → webhook 路由 404,系统照常)。

### UI(极简)
- run 详情状态头:origin=webhook 时显示小 chip「from PR comment ↗」链接 origin_comment_url。不加新页面。

### 验收
- Go 单测:验签/事件过滤/解析(review vs 任务)/映射与 RBAC 拒绝/去重/update push 模式。
- e2e j6-webhook.sh:API 以 jcloud-admin 发评论 `@jcode review` → review run + PR 出现 review;发 `@jcode Add CONTRIBUTING.md ...` → agent run + **同分支新 commit**(PR 更新)+ 回执评论。
