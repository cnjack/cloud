# 04 · BYOK 与隔离

## 核心洞察:让密钥待在"不可信代码碰不到"的地方

BYOK 最大的风险源于把 key 放进"跑模型生成代码"的那个 pod。修法是把两件事分开:

- **调 LLM 的 agent loop**(需要 key)——不必和不可信代码在一起。
- **工具执行**(shell/文件/git/build)——只有这部分需要在 sandbox 里跑。

## 选定模型:控制面 LLM 代理 + 短期 temp token(D10)

```
┌────────────────────── 控制面 (可信) ──────────────────────┐
│  BYOK 保管库: 真 key + endpoint (每租户加密)                │
│  LLM 代理: 接收 run 的 temp token → 注入真 key → 转发上游    │
│  按 run 签发/撤销 temp token (短 TTL, 限流, scope 到该 run) │
└───────────────▲───────────────────────────────────────────┘
                │ temp token (无真 key)
┌───────────────┴──────── sandbox pod (不可信) ──────────────┐
│  jcode headless full_access · LocalExecutor                │
│  跑 repo / 工具链 / 模型生成的代码                          │
│  LLM 调用 ──▶ 控制面 LLM 代理 (用 temp token)              │
│  持有: 仅 temp token + run-scoped store token              │
│  绝不持有: 真 BYOK key / endpoint                          │
└────────────────────────────────────────────────────────────┘
```

- **真 key 与 endpoint 永不出控制面。** 连上游 endpoint 都藏在代理后。
- 即使 sandbox 被彻底攻陷,也只有一个**限流、可撤销、短 TTL、scope 到该 run** 的代理 token——不是真 key。
- 因此**不需要 engine/sandbox 双 pod 拆分**:单 sandbox pod 里 agent 用 temp token 即可。
- 附带好处:**集中限流 / 计量 / 密钥轮换**都在代理这一处。

这也复用了 jcode 现成的能力:`internal/config/ProviderConfig` 的 `base_url` 只要指向控制面代理,`api_key` 换成 temp token,jcode 本身几乎不用改。

## 三层隔离(各司其职,别混为一谈)

| 隔离层 | 由谁负责 | 防什么 |
|---|---|---|
| **密钥隔离** | 控制面 LLM 代理 + temp token | sandbox 拿不到真 key/endpoint |
| **数据/workspace 隔离** | 专属 per-issue PVC | issue A 挂不到 issue B 的文件 |
| **算力/爆炸半径隔离** | pod 边界 + NetworkPolicy(+可选 gVisor/Firecracker)+ 墙钟/max_turns | 被攻陷的 sandbox 打节点/网络/别的 pod/无限跑 |

## PVC 隔离:天生的(只要不池化)

**专属 per-issue PVC = workspace/数据隔离是天生的**:每 issue 一个独立卷,不需要额外"擦除"仪式——只要**每 issue 新建、用完删除、不池化复用**。跨租户串数据的风险**只在**"为省冷启动而池化/复用 PVC"时才出现。

两点精度:
1. PVC 管的是**数据静态隔离**;算力/爆炸半径隔离是另一层(pod + NetworkPolicy)。
2. **把 key 挪出 sandbox 之后**,就算 sandbox 被彻底攻陷,它也没有真密钥可偷,只能碰它本就拿到的源码——最吓人的东西没了,隔离故事整体大幅简化。

## egress 出口管控

- **SETUP 阶段**:网络开(clone、装依赖、跑项目 setup 脚本)。
- **AGENT 阶段**:egress 收敛到 **NetworkPolicy 白名单**(控制面 LLM 代理、git remote、必要的包仓库)。防的是"把它有权读的源码外传"——这是任何代码沙箱的固有项。

## 硬把关(与 Copilot / Codex / Symphony 三家一致)

- agent **不能** merge、**不能** 触发 CI;draft PR + 人工 review 是唯一验收关口。
- 每 run 有**墙钟上限 + `max_turns`**(Symphony 的 stall / 超时语义)。
- 密钥只在需要处出现,agent 阶段 egress 白名单。

## 记忆投毒(与存储相关的隔离)

sandbox 不可信 → **只准写自己 run 的 session**;**跨项目 memory 由控制面侧蒸馏 pipeline 生成/校验**,防止被攻陷的 sandbox 污染全租户记忆。详见 [03-storage-memory-sync.md](03-storage-memory-sync.md)。
