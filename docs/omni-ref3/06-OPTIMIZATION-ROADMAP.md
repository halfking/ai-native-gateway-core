# 06 — 优化路线图（P0–P3）

> 把 01–05 的建议按**依赖关系**和**风险**排成阶段。每个阶段有：目标、条目、前置、验收门禁、回滚。所有新能力默认**按租户灰度 + flag 默认关**。

## 阶段总览

```
P0 去债 + 安全 + 前置统一（独立、低风险）
   ├─ 删旧压缩副本（C5/01-M6）
   ├─ 修 tool_call_id 生成器（E2）
   ├─ LLM 摘要前 secret mask（C2）
   └─ 统一消息指纹（A2）+ 摘要读源抽象（A6）   ← V2 读放开的前置
        ↓
P1 V2 读灰度放开（总开关）+ 接线归纳
   ├─ shouldUseV2 真读 flag + 灰度（A1）
   ├─ 统一 token 估算（C4）← 多处依赖
   ├─ 会话元数据聚合器接线归纳（M1）+ 结构化 tag（M3）
   ├─ 引擎/摘要熔断（C1）+ 缓存感知压缩/cache-safe 注入（C8/01-M4）
   ├─ prompt-cache 前缀分析器（D7）+ 统一 cache 指标（D2）
   ├─ 旧内联图片裁剪（A3）+ 上下文窗口自校正（A4）
   ├─ 声明式 provider 变换 DSL（E1）+ role/Responses 净化补齐（E3/E4/E5）
   └─ V2 Message 强类型化（A5/E6）
        ↓
P2 收敛 + 工程化
   ├─ 统一元数据事实源（M2，依赖 A1 全量）
   ├─ 摘要衰减/归档（M5）+ 首轮即时 task_type（M7）
   ├─ 压缩结果 memo（C3）+ 压缩预览 endpoint（C7）
   ├─ L1 byte 限制（D3）+ 收敛 sticky 实现（D4/D5）
   └─ 删 Estimator.NeedsCompression 死代码（C6）
        ↓
P3 退役 V1
   └─ request_logs bodies 退役（摘要/压缩全切 session_bodies）
```

---

## P0 — 去债 + 安全 + 前置统一

**目标**：消除已知 bug 回退风险与安全泄漏；为 V2 读放开做前置。全部独立、可并行、低风险。

| 条目 | 来源 | 工作量 | 风险 | 验收 |
|---|---|---|---|---|
| ✅ 删 `_to-be-deprecated/compressor/`（审计确认无外部引用） | C5/M6 | S | 低 | 编译通过；`grep` 无残留引用 — **已实现** |
| ✅ 修 `generateToolCallID`（>10 不碰撞） | E2 | S | 低 | 单测：>10 个 tool_calls id 唯一 — **已实现** |
| ✅ 统一消息指纹（V2 用 SHA256(512B)） | A2 | S | 低 | V1/V2 同会话 delta 边界一致 — **已实现** |
| ✅ 摘要读源抽象（`MessageSource` 接口 + V1 默认实现 + V2 `session_bodies` 实现） | A6 | M | 中 | 接口层 + V1/V2 两源可切换；A1 只需翻 flag + 接线一行 — **已实现** |
| ✅ **D8 命名空间调查**（`domain/analysis` vs `domains/analysis` 均活跃有意分层；旧 `cache/{semantic,delta,kv}` 孤立可删，`cache/prefix` 保留） | D8/审计§2.5 | S | 低 | 结论见 07 §2B — **已结案** |
| ✅ 接线 `Engines.TitleGenerator`（首请求标题不再静默跳过） | M7 | S | 低 | 类型断言编译期保证；配置开后 title 非空 — **已实现** |
| ⬜ LLM 摘要前 secret mask | C2 | M | 中（误 mask 破坏内容） | 含假 key 的 body 摘要不含原 key；正例/负例回归 |

**回滚**：均为代码级，git revert。secret mask 可加 flag `compression.summary_secret_mask` 默认开。

**门禁**：P0 全部合并且测试绿后，才进入 P1 的 V2 读放开。

---

## P1 — V2 读灰度放开 + 接线归纳（主阶段）

**目标**：打通“V2 读 → 增量重建 → 摘要 → 归纳写回 live 元数据”的现代化主链路；吸收 omniroute 的门禁/熔断/缓存感知/前缀分析。

### P1.1 V2 读放开（A1）— 关键路径 ✅ 已执行（2026-08-07）
- **决策**：压缩读 + 摘要读一起切，默认开，平台级 kill-switch，不灰度（用户量小）。
- 改 `shouldUseV2` 删硬编码 `return false` → `GetPlatformBool("sessions_v2_compression_read", true)`；摘要 `SetMessageSource(NewV2SessionBodiesSource)` 同 flag 接线；spec 改 `ScopePlatform/Default true`。
- 前置 A2/A6/M7 均已满足。
- 回滚：platform 设 `sessions_v2_compression_read=false`（热加载，秒级）。
- 验收：`should_use_v2_test.go` 5 例 + 全套测试通过。详见 `08-A1-V2-READ-CANARY-RFC.md` §0。
- **遗留**：V1/LCS 与 V2/delta 在边缘（attachment-only、orphan tool）算法不同，default-on 后理论上个别会话的 outbound 内容会变。fail-open 兜底已在；若线上观察到异常，kill-switch 即回 V1。

### P1.2 基础设施
| 条目 | 来源 | 前置 | 状态 |
|---|---|---|---|
| ✅ 统一 token 估算（`tokenest` 包，3.5 统一；修 V2 builder /4 离群） | C4 | 无 | **已实现** |
| ✅ prompt-cache 前缀分析器（`cache/prefix` 新增 `PrefixHash`/`computePrefixHash`） | D7 | C4 | **已实现** |
| 统一 cache 指标表 | D2 | 无 | 待 |

### P1.3 压缩增强
| 条目 | 来源 | 前置 | 状态 |
|---|---|---|---|
| ✅ 摘要熔断（`summaryBreaker`：3 次失败/30s cooldown/half-open 探测/可 disable） | C1 | 无 | **已实现** |
| ✅ 摘要前 secret mask（`secretmask` 包，两处 LLM 入口） | C2 | 无 | **已实现** |
| ✅ 旧内联图片按预算裁剪（`PruneOldMediaBlocks`：最旧 N 个 image/audio 块→占位符） | A3 | C4 | **已实现** |
| 缓存感知压缩 + cache-safe marker 注入 | C8/01-M4 | D7 | 待 |

> C6 调整结论（2026-08-07）：`Estimator.NeedsCompression` **非死代码**——`Compressor.ShouldCompressPreRequest`（compressor.go:261）经 compressor.go:318 在 pre-request 路径实时调用它。原 omni-ref3 标"删 dead code"作废；仅修正了 estimator.go:43 的误导性注释（原称"无 live caller"）。
| 上下文窗口自校正 | A4 | 无 |

### P1.4 会话元数据接线
| 条目 | 来源 | 前置 |
|---|---|---|
| 元数据聚合器（摘要/intent/cluster → `SetSessionMetadata`） | M1 | A1 |
| 结构化 tag + 自动打标 | M3 | M1 |

### P1.5 IR / 变换
| 条目 | 来源 | 前置 |
|---|---|---|
| 声明式 provider 变换 DSL | E1 | 无 |
| enforceRoleAlternation 可选修复 | E3 | 无 |
| GLM 版本感知 + Responses 净化补齐 | E4/E5 | 无 |
| V2 Message 强类型化（复用 ir.Message） | A5/E6 | 迁移兼容读 |

**P1 整体验收门禁**：
- 灰度租户：`sessions.task_type/topic/intent` 非空且与摘要一致；压缩 token 节省不退化；provider 400 率不升。
- flag 全部默认关；新能力按租户开启。
- 全套指标（D2）可见。

---

## P2 — 收敛 + 工程化

**目标**：V2 读全量后，收敛双轨、补工程化能力。依赖 P1 的 A1 全量。

| 条目 | 来源 | 前置 |
|---|---|---|
| 统一元数据事实源（V2 为准，Redis 降缓存） | M2 | A1 全量 |
| 摘要衰减/归档 | M5 | M2 |
| 首轮即时 task_type | M7 | M1 |
| 压缩结果 memo | C3 | C4 |
| 压缩预览 endpoint | C7 | 无 |
| L1 byte 限制 | D3 | 无 |
| 收敛 sticky 实现（删旧版） | D4/D5 | 无 |
| 旧缓存包退役（`cache/semantic\|prefix\|delta\|kv`，随 V1） | D8 | 无生产引用确认 |
| 删 `Estimator.NeedsCompression` 死代码 | C6 | C4 |

---

## P3 — 退役 V1

**目标**：V2 全量稳定后退役 `request_logs_bodies` 的全量存储。

- 条件：摘要、压缩、重建全部走 `session_bodies`（A6 + A1 全量）且稳定 ≥1 个计费周期。
- 动作：停止写 `request_logs_bodies`（保留 `request_logs` 元数据）；归档/分区清理。
- 风险：高（不可逆存储变更）→ 需独立评审 + 双写期 + 可回滚（恢复写）。

---

## 跨阶段不变量清单（每个 PR 自检）

1. ☐ fail-open：压缩/摘要/净化出错回退原始 body。
2. ☐ NeverWorse：上送 body 不劣于原始（`handler.go:2561` 守卫不被绕过）。
3. ☐ toolChainIntact：裁剪后工具链完整。
4. ☐ RLS / 认证上下文：无跨租户泄漏；缓存 key 带 tenant 维度。
5. ☐ 迁移号：新增前 `ls sql/migrations/startup/ | sort -n | tail` 重检。
6. ☐ flag 默认关：新能力按租户灰度。
7. ☐ 不破坏 `executor` 依赖边界（不依赖 mcp/a2a/fusion）。

## 优先级速查（按“性价比”）

| 最高性价比（低风险高收益，先做） | 说明 |
|---|---|
| 删旧压缩副本（C5） | 防 bug 回退 |
| 修 tool_call_id（E2） | 防 provider 混乱 |
| secret mask（C2） | 安全 |
| 统一 token 估算（C4） | 多处依赖、消除误触发 |
| 删 Estimator 死代码（C6） | 降认知负担 |

| 高价值但高风险（需评审） | 说明 |
|---|---|
| V2 读放开（A1） | 总开关，依赖多 |
| 统一元数据事实源（M2） | 不可逆语义变更 |
| V1 退役（P3） | 存储不可逆 |
