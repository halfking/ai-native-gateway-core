# magpie 对比 · post-P0 状态与下一步（2026-10-14 晚）

> 本文件是 goal 交付物：在 v2 研究
> [`2026-10-14-magpie对比与优化方案.md`](./2026-10-14-magpie对比与优化方案.md) +
> [`2026-10-14-magpie优化执行规划.md`](./2026-10-14-magpie优化执行规划.md) 之上，
> 用 **P0 四项落地后的仓库实态** 重判缺口，给出稳定性/性能下一刀。
>
> magpie 源：`~/workspace/ai/magpie` HEAD `591ed58b`。
> 本仓 P0 证据：`docs/audit/2026-10-14-magpie-p0四项落地与本地E2E验证.md`（2608 E2E）。

---

## 1. magpie 三大职能（流程速写）

### 1.1 多模型转换

```
Agent 请求 (Chat|Responses|Anthropic|Gemini)
  → parse(proto) → IR
  → usable / unfit 决定线格式（可换端）
  → build(upstreamProto) → 上游体
  → 流式 decode → collector → encode(clientProto)
```

要点：翻译路径强制上游流式；拒绝可选字段 → 同候选剥参重试 → `unfit` 记忆；
散文工具 / 工具历史配对 / 密封推理 / Gemini thoughtSignature 编进 call id。

### 1.2 请求转发

```
serve → plan(候选) → affine(粘性)
  → holdWriter 包裹 attempt 循环
      失败且未发内容 → rest(分级) → 下一候选
      全灭 → probeOne（最早到期半开）或 quotaHeld（直接 429）
      忙组 → Patience(≤240s) replan
  → 已发内容后失败 → 不换人，终态化
```

要点：只有 rest 罚账号；内容/形状/认证不罚；预计等待 >8s 换人。

### 1.3 会话管理

- 身份：**只认请求头**（无头不录会话文本）。
- usage：cache 恒写（含 0）；tier asked/sent/served；重试写 `handed_on` 子行。
- trace：决策点当场写 Order / Left / Tries / Swapped。
- 脱敏：可逆 Mask（发厂商）+ 不可逆 Scrub（落盘）。

---

## 2. 本仓对照（P0 落地后判定）

| 域 | # | 能力 | magpie | 本仓现状（2026-10-14 晚） | 判定 | 下一步 |
|---|---|---|---|---|---|---|
| A | A1 | 统一 IR | hub-and-spoke | internal/ir + transformation | 持平 | — |
| A | A3 | 剔参记忆 | 拒绝即剥、单记忆 | **P0-B**：reqprobe 学习 + 线格式骨架 + miniredis 跨实例；Anthropic 仅学后剔除（无剥参重试） | **代码落地** | 观测 E1（未部署） |
| A | A5 | 流/非流单路径 | 翻译强制流 | 双路径 | 落后 | P2-D（先黄金门） |
| A | A6 | 散文工具 | 5 格式 | 2 格式 + 流式 coercer | 部分落后 | P1-C |
| A | A8 | 断流续写 | 白名单；tool 禁续 | **默认 true**；能力集偏宽 | 部分落后 | **P1-A** |
| A | A9 | 回复循环守卫 | 256/24 不罚账号 | 无 | 落后 | P1-B |
| B | B2 | 健康存放 | 进程内 | URSM+PG+breaker | **本仓强** | 保留 |
| B | B3 | rest 分级 | 五档 + resetsAt | P0-4 + **P0-C**：writer 薄包装 + breaker `RecordFailureWithReset` wiring 测 | **代码落地** | 观测 E2（未部署） |
| B | B4 | 失败归责矩阵 | 内容/形状不罚 | **P0-C**：`PenaltyOf` + DecideFailover + Writer + **breaker 短路** | **代码落地** | 观测级联窗（未部署） |
| B | B5 | 全灭行为 | probeOne + quotaHeld | **P0-D**：CoolUntil 排序 + Redis SET NX 单飞 + Retry-After←resetAt | **代码落地** | 观测 W2（未部署） |
| B | B6 | 等待预算 | Patience ≤240s | 默认 240s + **P0-A+** `retry_after_seconds` / Retry-After | **已落地** | — |
| B | B15 | 2xx 非 API | HTML→502 切换 | 未见 | 落后 | P1-E |
| C | C1 | 会话存储 | 文件+usage 投影 | PG session 族 S4 | 产品差异 | 不照搬 |
| C | C4 | 决策 trace | Left/Tries/Swapped | request_flow 散点 | 部分落后 | P2-F |
| C | C6/C7 | 外显归一 / Should-Retry | 强 | P0-A+ 终态字段齐；仍缺 X-Should-Retry | 部分落后 | **P1-F** |

---

## 3. 与稳定性/性能痛点的映射

| 本仓痛点（AGENTS 史） | magpie 解法 | 本仓进度 | 下一动作 |
|---|---|---|---|
| 严格中转字段 400 连环修 | unfit 拒绝即学习 | **P0-B 代码落地**（Anthropic 剥参重试写死不做） | 观测 E1（未部署） |
| 5h 挂起 / p99 极大 | Patience ≤240s | **P0-2 + P0-A+ 已落地** | 部署后 curl 验头/字段 |
| 全灭后放大打上游 | probeOne / quotaHeld | **P0-D 代码落地**（CoolUntil+单飞） | 观测 ProbeSync / W2（未部署） |
| 客户端错误级联冷却 | 归责：不罚账号 | **P0-C breaker×PenaltyOf 代码落地** | 观测级联窗（未部署） |
| 中途断流截断 | 能力白名单续写 | P0-3 默认开、集偏宽 | **P1-A** |
| 静默吞流 | 永不静默 + 可读终态 | r1028 已修拦截可见性 | 保持 + 终态契约 |
| 冷启动 / 键 TTL | affinity + 不砍 probe TTL | r1026 TTL extend-only | 观测 reconciler |

---

## 4. 下一步方案（可执行顺序）

### M2 剩余 P0（续轮代码债已收；**非规划卡全绿 / 未部署观测**）

批判审计全文：`docs/audit/2026-10-14-magpie-p0批判审计与收口.md`。

1. **P0-B** — 谓词收敛 + 拒绝即学习 + 线格式骨架 + chat 剥参重试 + Anthropic 学后剔除 + pydantic loc + **miniredis 跨实例收敛测**。Anthropic **同候选剥参重试写死不做**（`anthropicSameCandidateStripRetry=false`）。
2. **P0-C** — `PenaltyOf` + DecideFailover + Writer + **breaker `RecordFailure` 短路** + rest 分级 wiring（json-word 不冷却 + Allow 仍 true；`resets in 0h 19min` 经 ApplyNodeHealthDecision≈19min）。
3. **P0-D** — CoolUntil 排序 + Redis/memory SET NX 单飞 + `quotaHeld` Retry-After←earliest resetAt；Execute 同层测上游=1 / 并发=1（memory + miniredis Redis）。
4. **P0-A+（契约层）** — 终态字段齐。**未做**：全候选红夹具计时门 + deadline=18000 前提配对。
5. **部署观测** — **154=`2637-6db2d18e`@8781** + **本地=`2.5.8.2638`@8782**（脏树；四指纹 OK；`magpie-p0-154-postdeploy-20261014.json`）。154 窗内 ProbeSync/quotaHeld/client_bug=0（安静，**勿宣称 W2/E2 全绿**）。本地用 `127.0.0.1:5432` + Redis `16379`（非 54318）。WIP 未 commit。

**包测（续轮后）**：`reqprobe`/`errorsx`/`credential`/`streaming`(survival|quotaHeld)/`executors`(P0 子集) 绿；P0-D/C 变异 M1–M4 红在正确断言后已恢复。

### 随后（M3 P1，约 10 人日）

P1-A 续写白名单默认关 → P1-B loop guard → P1-C 散文格式 → P1-E 非 API 2xx → P1-F 外显 → P1-G 上游字节黄金门（挡 r1026 类双路径回归）。

### 按事故驱动（M4 P2）

cache 粘性 / Gemini signature / 字节保真 / 翻译统一走流 / 决策 trace / smartretry 死代码裁决。

### 不照搬

订阅轮换、GUI、文件态健康、会话文件制、System One、无拨号超时的 Transport。

---

## 5. 验收证据（本 goal）

| 交付物 | 路径 |
|---|---|
| 流程+对比+方案详文（v2） | `docs/research/2026-10-14-magpie对比与优化方案.md` |
| 执行规划（任务卡） | `docs/research/2026-10-14-magpie优化执行规划.md` |
| P0 落地实证 | `docs/audit/2026-10-14-magpie-p0四项落地与本地E2E验证.md` |
| post-P0 状态重判（本文件） | `docs/research/2026-10-14-magpie对比-postP0状态与下一步.md` |
| 可视化对照表 | IDE canvas `magpie-gateway-comparison.canvas.tsx` |

源码核对：magpie `docs/subsystems/gateway-routing.md` / `gateway-sessions.md`；
本仓 `settings/spec_stream_continuation.go`（Default=true）、`config` 240s 默认、
`internal/paramrefusal` 谓词转调 reqprobe、`ProbeSync` 经 `selectProbeOne`(CoolUntil)+`ProbeOneFlight` 收窄。
