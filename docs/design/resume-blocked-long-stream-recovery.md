# resume_blocked 长流中断修复方案设计

> **状态**: 设计稿（待评审）
> **作者**: 2026-09-01 会话
> **背景**: `.handoff/gateway-error-fix-phase2-20260901.md` §三/§五.1 —
> holdback window（L1，5s/20 chunks）对 48h 内全部 12 个
> `survival_resume_blocked` 事件覆盖率 **0%**，长流中段中断需要新方案。

---

## 一、问题定义

`survival_resume_blocked`（`TaskActionResumeBlocked`，reason=`committed_output`）
是 survival 协调器在**已有语义内容写给客户端**后遇到可恢复失败时的终态：

```
domains/streaming/attempt_outcome.go:208/337/340
  if committed && (hasRetry || hasWait) {
      return TaskDecision{Action: TaskActionResumeBlocked, Reason: "committed_output"}
  }
```

设计原则是"绝不透明重试已提交内容"（doc 18 §10.3）：任何透明重发都会让客户端
看到重复内容。于是该人群只能拿到错误信封，**几十秒到几百秒的输出全部作废**。

实测人群画像（48h，245，12 个请求，全部 attempt 1）：

| 维度 | 范围 |
|---|---|
| latency_ms | 35,083 – 177,706 |
| stream_chunks | 27 – 5,568 |
| 模型 | glm-5.2 / minimax-m3 / deepseek-v4-flash |
| 中断形态 | 长流中段 network_error（othersapi.com 中继） |

关键结论：**最小样本 27 chunks / 36 秒**，全部远超 L1 窗口（20 chunks / 5s）。
扩大 L1 窗口不可行——holdback 本质是牺牲首 token 延迟换可撤销性，窗口开到
分钟级等于对所有请求加同等延迟。

## 二、现有资产盘点（代码事实）

恢复阶梯 L0–L4 已在 `domains/streaming/stream_recovery.go` 实现，但**只有
L0/L1 通过 AttemptCommitGate + SurvivalCoordinator 实际接线，L2/L3 从未被
主路径调用**：

| 组件 | 状态 | 位置 |
|---|---|---|
| L0 同节点重发 | ✅ 已接线（survival 重试循环） | survival_coordinator.go |
| L1 holdback 窗口 | ✅ 已接线（OPT-IN env，154/245 已启用） | attempt_commit_gate.go + `RecoveryHoldbackFromEnv` |
| `NextRecoveryAction`（L0-L4 决策函数） | ⚠️ 已实现，**无调用方** | stream_recovery.go:257 |
| `CommittedPrefixCache`（逐请求 FNV64 哈希 + 64KiB head 窗口，容量 1024） | ⚠️ 已实现，**无调用方** | stream_recovery.go:573 |
| `PrefixAligner`（重放流 vs 已提交前缀评分，阈值 9000bp） | ⚠️ 已实现，**无调用方** | stream_recovery.go:655 |
| L3 continuation（白名单开关，默认关） | ⚠️ 配置骨架就绪，构造逻辑未写 | stream_recovery_config.go:56 |
| L4 visible restart / stream-undo | ⚠️ `l4Decision` 已实现，`AllowVisibleRestart`/`ClientStreamUndo` 无注入方 | stream_recovery.go:414 |
| 跨凭据解锁门（ADR-Disp-007 四条件） | ⚠️ `CrossCredentialUnlockAllowed` 已实现，无调用方 | stream_recovery.go:440 |

**这是"方案已设计、实现已完成 70%、缺最后一公里接线"的局面**，不是从零设计。

## 三、方案：把 L2 前缀对齐接进 survival 重试路径（推荐主路径）

### 3.1 核心思路

resume_blocked 的本质矛盾：内容已提交 → 不能重发；但重放流与已提交内容
**高度重叠**（同一个 prompt、温度趋零、多数 provider 有 KV/prompt cache）。
L2 的解法是：重放时**逐字节比对已提交前缀，重叠部分抑制不发，只转发
suffix** —— 客户端视角等价于"流继续"，无重复内容。

ADR-Disp-007 四条件之二（"L2 前缀对齐成功"）正是为这个场景预留的跨凭据
解锁条件——对齐成功本身就证明没有重复提交。

### 3.2 数据流

```
┌─ 正常流 ─────────────────────────────────────────────┐
│ gate.Flush(chunk) ──┬─→ 客户端                       │
│                     └─→ prefixCache.Observe(reqID,    │
│                            semanticBytes)   [新增接线] │
└──────────────────────────────────────────────────────┘
┌─ 中断恢复（committed>0 且 failure 可恢复）──────────────┐
│ 1. NextRecoveryAction(state, cfg, err)                 │
│    → RecoveryActionAlignedContinuation                 │
│ 2. 重放（同凭据优先；对齐成功可解锁跨凭据/跨模型）          │
│ 3. 重放流逐帧过 PrefixAligner：                          │
│    head 64KiB 字节级比对 + 尾部全量哈希校验               │
│    score ≥ 9000bp → 抑制前缀，仅转发 suffix             │
│    score < 9000bp → alignment miss → L3/L4             │
└──────────────────────────────────────────────────────┘
```

### 3.3 改动清单（按依赖顺序）

1. **AttemptCommitGate 接 Observe**（attempt_commit_gate.go）：每次
   semantic flush 同步喂 `CommittedPrefixCache.Observe`；请求终态
   （succeed/error/envelope）调 `Remove`。缓存实例放
   SurvivalCoordinator（或包级单例 + 容量 env）。
   - 注意：Observe 的输入必须是**发给客户端的语义字节**（SSE data 载荷或
     等价规范化形式），不含 transport 心跳/注释帧。
2. **survival 重试循环接 NextRecoveryAction**（survival_coordinator.go
   default 分支之前）：`committed_output` 且缓存命中时不再直接
   resume_blocked，先问 L2。
3. **重放 attempt 的 gate 挂 aligner**：重放流的前 N 字节先缓冲进
   PrefixAligner（复用 holdback 缓冲机制，窗口 = min(64KiB,
   CommittedPrefix.TotalBytes)），判定后：Aligned → 丢弃前缀转发 suffix；
   miss → 该 attempt 作废走 L3/L4。
4. **决策日志**：`survival_recovery_action`（已实现）+ 新增
   `survival_l2_alignment`（score_bp、common_bytes、hash_only、aligned），
   与 §五 观察指标对齐。
5. **配置**（全部走 hotconfig/env，默认关，灰度开）：
   - `LLM_GATEWAY_RECOVERY_L2_ENABLED=false`（总开关）
   - 复用 `AlignmentThresholdBP=9000` / `CommittedPrefixWindowBytes=64KiB`
     / `CommittedPrefixCacheCapacity=1024` 既有默认。

### 3.4 关键风险与对策

| 风险 | 对策 |
|---|---|
| 重放流与已提交内容**前缀发散**（模型非确定、provider 无 cache）→ 对齐失败率过高 | ① 阈值 9000bp 相对宽松；② miss 无害降级（现状=resume_blocked）；③ 上线前用 48h journal 回放离线评估命中率（见 §六 P0） |
| 64KiB head 窗口外只哈希校验，模型复读但字节偏移 → hash miss | 现状等价（不转发任何内容）；可选 P2 扩展滑窗哈希 |
| 重放期间客户端静默（缓冲对齐窗口）→ 客户端超时 | 对齐窗口 ≤ 已提交字节数，通常 <1s 即判定；期间发 transport 心跳（Keepalive 已有） |
| 双倍 token 计费（重放完整 prompt） | 仅在原本就要作废全部输出的场景触发，净收益仍为正；A/B 后再考虑 prompt-cache 亲和节点优先 |
| 内存：1024 请求 × 64KiB = 64MiB 上限 | 容量可调；Observe 热路径只做 FNV64 追加，无锁竞争（per-request entry + 全局 mutex，低频） |
| SSE 事件边界 vs 字节边界：suppress 前缀可能截断在事件中间 | aligner 输出 SuffixOffset 必须对齐到帧边界（缓冲到下一 `\n\n` 再转发）；实现时加不变量测试 |

### 3.5 为什么不是其它方向（phase2 §五.1 的 A–D 对照）

- **A = 本方案**（L2 前缀对齐）。骨架已在，接线路径最短。
- **B 客户端可见重启（stream-undo）**：需要客户端协议升级配合，周期长；
  保留为 L4 降级形态（`ClientStreamUndo` 已有字段），不作为主路径。
- **C 收紧 stream timeout 让失败提前进 L1 窗口**：人群 latency 35–178s、
  chunk 间隔 2–6s，把 timeout 压到 5s 会误杀正常长流；仅对特定
  credential/model 精细设定才有意义，作为辅助手段（配 L2 灰度观察）。
- **D 中继网络治理**：othersapi.com 质量是外部因素，值得并行推进但不受
  我们控制，不能作为唯一解。

## 四、L4 兜底增强（第二阶段，可选）

L2 miss 时的现状是错误信封。可分两步改善：

1. **AllowVisibleRestart（运营开关）**：miss 时发 restart 控制事件 +
   完整重生成，客户端把重复内容可视化去重。改动小（l4Decision 已实现），
   但需要前端/客户端约定，先在内部客户端灰度。
2. **error_kind 补齐**（phase2 §五.2，独立小修）：gpt-5.6-terra 37 次/48h
   空 error_kind，audit pipeline 分类没落库，影响所有人群的可观测性。

## 五、观察指标（复用 §六 成功标准框架）

| 指标 | 采集 | 目标 |
|---|---|---|
| `survival_resume_blocked` 计数 | journalctl grep（现成） | 灰度后下降 ≥80%（L2 命中部分） |
| `survival_l2_alignment{aligned}` | 新日志/metric | 命中率（对齐成功/触发次数）≥60% 起步 |
| `survival_recovery_action{mode=aligned}` | 已实现日志 | 触发即走通 |
| 对齐请求的最终 succeed 率 | `request_survival_finished` | ≥90% |
| 首 token 延迟 p95 | 现有 metrics | 不变（L2 不动 holdback） |
| 重复内容投诉/自动检测 | 客户端 checksum 对比 | 0（不变量：绝不拼接重复字节） |

## 六、实施计划

| 阶段 | 内容 | 验收 |
|---|---|---|
| P0 离线评估（0.5 天） | 用 245 48h journal 中 12 个 resume_blocked 样本的 request_delta/response_delta，离线模拟"重放=重发原 prompt"的对齐命中率（假设重放流=原流前缀+随机尾，统计 64KiB 窗口覆盖率） | 命中率估计 ≥50% 才进 P1，否则直接转 L4 路线 |
| P1 接线 + 单测（1–1.5 天） | §3.3 改动 1–4，不变量测试（帧边界、容量上限、hash-only 路径） | `go test ./domains/streaming/` 全绿；新增 UT-SR-L2-* 用例 |
| P2 影子模式（245，3 天） | `L2_ENABLED=true` 但 miss 不影响行为（只记日志） | §五 指标基线；对齐命中率实测 |
| P3 生效灰度（154 单模型 minimax-m3 → glm-5.2） | 对齐成功才真抑制前缀 | resume_blocked 下降、零重复内容 |
| P4 全量 + 文档 | hotconfig 默认开 | 24h 观察报告 |

## 七、与当前部署的关系

- 本设计**不含任何已部署迁移的依赖**；P1 代码可随任意下次 deploy-seamless
  发布（迁移侧无新增）。
- 245 现已运行 1874（含新日志三件套），P0 的 journal 数据已经可采。

---

**附：关键代码坐标**

- 决策入口（现状 resume_blocked 收口）：`domains/streaming/survival_coordinator.go:571-592`（default 分支）
- committed 判定：`domains/streaming/attempt_outcome.go:208,337,340`
- L2 组件（待接线）：`domains/streaming/stream_recovery.go:257,554-700`
- L1 holdback（已接线，对照参考）：`domains/streaming/stream_recovery_config.go:100+`
- ADR-Disp-007 门：`domains/streaming/stream_recovery.go:440-465`
