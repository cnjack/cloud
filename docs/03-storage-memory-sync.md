# 03 · jcode `Store` 改造 RFC —— 会话回传 / 跨项目记忆 / local↔cloud 同步

> 一次改造解三件事:把 jcode 写死在 `~/.jcode/` 的 **session Recorder** 与 **memory** 抽成可插拔 `Store`(`LocalStore` / `RemoteStore` 两后端),云端后端 = orchestrator 自有 store。

## 为什么是 storage,不是 hook

外挂 hook(agent 边跑边把 conversation POST 到控制面)会造成**两条写路径、两个真相源**,还得自己处理顺序/重试/去重。jcode 的 `Recorder` 已经是所有 session 事件的**唯一咽喉**——把它背后的落盘抽成接口,"写到哪"就变成配置。三个理由:

1. **单一写路径**,不会 race。
2. conversation 回传、跨项目 memory 归属、local↔cloud 同步是**同一个改造**顺带全解。
3. **向后兼容**:`LocalStore` 就是今天的行为,本地用户零感知。

**hook 与 storage 是两层,不是二选一:**
- **实时那一路**:`handler.AgentEventHandler` → WS/SSE 给控制面 UI 看 agent 在干嘛(临时、可丢)。控制面亦可直接转播 Store 收到的流。
- **持久那一路**:`Recorder`(改可插拔)/ memory → 权威落控制面。

## 现状(代码事实)

- **Session**:`internal/session/session.go` 的 `Recorder` 用裸 `os.OpenFile` append 到 `~/.jcode/sessions/{uuid}.json`(teammate 到 `.../subagents/agent-*.jsonl`),并维护 `session.json` 索引。**写死本地盘、不是接口。**
- **Memory**:`internal/memory/` 已有两 scope —— `memory/projects/{slug}/`(`ProjectRoot`,`ProjectSlug` 对路径哈希去重)与 `memory/global/`(`GlobalRoot`,**跨项目/用户级**)。文件:`SummaryFile`、`IndexFile`、`notes/`、`session_summaries/`。`inject.go` 的 `BuildInjection(projectDir)` 读 project + global 的 summary/notes 注入每次模型调用。`memory sync` 子命令跑蒸馏 pipeline(`internal/memory/pipeline`)从会话提炼 memory。`Note` 带 `Scope`/`Kind`/`Source`。

## 接口草案

```go
// internal/session
type Store interface {
    Append(ctx context.Context, sessionID string, e Entry) error   // 唯一写路径
    Load(ctx context.Context, sessionID string) ([]Entry, error)
    List(ctx context.Context) ([]SessionMeta, error)
    // index/meta 操作…
}

// internal/memory
type Scope struct {                 // Project(tenant,proj) | Global(tenant)
    Kind    string                  // "project" | "global"
    Tenant  string
    Project string                  // 仅 project scope
}
type MemoryStore interface {
    Get(ctx context.Context, s Scope) (Snapshot, error)     // summary + recent notes + index
    PutNote(ctx context.Context, s Scope, n Note) error
    PutSummary(ctx context.Context, s Scope, summary string) error
}
```

**两个实现:**
- `LocalStore` / `LocalMemoryStore` —— 今天的 `~/.jcode/` 行为(默认,本地/桌面用)。
- `RemoteStore` / `RemoteMemoryStore` —— HTTP 客户端打控制面 API,用 **run-scoped token** 授权(云端 runner 用)。

后端由 config 选择:本地模式 = Local;云端模式 = Remote → orchestrator store。

## 数据流(PVC 只是工作副本)

```
SETUP:
  RemoteMemoryStore.Get(project) + Get(global)  ──▶  写入 PVC 工作副本
  jcode inject.go 照常注入

AGENT ×N:
  Recorder.Append(entry) ──▶ RemoteStore 流式 POST 到控制面 (append-only, 便宜)
                          └─▶ PVC 同时留工作副本 (本 run 崩溃续跑)
  实时 UI ◀── handler.AgentEventHandler (WS/SSE)
  LLM 调用 ──▶ 控制面 LLM 代理 (temp token, 无真 key)

FINALIZE:
  控制面(可信)蒸馏 pipeline 读本 run 会话 ──▶ 更新 memory(project + 视情况 global)──▶ 写回 store
  (sandbox 从不直接写共享 memory)
```

**结论**:conversation 回传 = `RemoteStore` 这条 sink;**权威副本在控制面,PVC 只是缓存**。sandbox 半路死了,控制面已有到最后一条 flush 的全部,reconciler 重排即可续。

## 三类数据 → 三个归属

| 数据 | scope | 权威副本 | PVC 角色 |
|---|---|---|---|
| conversation / session | per issue | 控制面:Postgres 索引 + 对象存储 blob | 仅运行期工作副本 |
| project memory | per (tenant, project) | orchestrator store,绑 project 实体 | 不放 |
| **global / 跨项目 memory** | per tenant / user | orchestrator 租户级共享 store | **绝不放** |

> **为什么 memory 不能放 PVC**:PVC 是 per-issue、用完删的;memory(尤其 global)要跨 issue / 跨 run / 跨项目存活。放 PVC 等于 issue 一结束记忆就蒸发。

## 完整性护栏(sandbox 不可信)

sandbox 跑模型生成的代码,**只准写自己这个 run 的 session**;**跨项目 memory 由控制面侧(可信)的蒸馏 pipeline 从会话生成/校验后写回**——否则一个被攻陷的 sandbox 能污染整个租户的记忆。jcode 的 `memory sync` 蒸馏 pipeline 正好挪到控制面侧跑。

## local↔cloud 同步(同一个 Store,两种姿势)

- **在线薄同步**:本地 jcode 的 Store 直接 = `RemoteStore`,永远读写云端(需联网)。
- **local-first + 同步(推荐)**:本地保留 `LocalStore`,一个 sync 循环按游标调和 ——
  - **session = append-only 日志** → 同步 = 按 `(session_id, seq)` 取并集,近乎无冲突,最好做;
  - **memory = 小结构化 notes** → 按 note key LWW + 让控制面蒸馏 pipeline 当合并权威。

这正是 jtype 已经趟平的 local-first 模式(lamport/cursor),但按 D13 **在 orchestrator 内自建、不依赖 jtype**。桌面/CLI 能离线干活、连上即同步。

## 净改动

jcode 加一层 `Store`(session + memory 各一个接口、各两个实现),控制面加对应的 run-token 授权 API + 蒸馏 pipeline。一次重构同时解 conversation 回传、跨项目记忆归属、local↔cloud 同步,且默认 `LocalStore` = 今天的行为。
