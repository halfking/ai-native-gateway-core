# 108-R89-u：`domains/session/preprocess` 查到底 —— **是明写的分阶段交付，不是遗漏**；并修正 A/B 二分的分类框架

- 轮次：R89-u（主代理自查）
- HEAD 基线：`312da9799`
- 触发：把 105 号采信的「31 个零导入包」里最大的一���（`domains/session/preprocess`，
  **4,930 行 + 11 个测试文件**）查到底
- 结论：**它有 README，作者把「不接生产管线」写在第一段** ⇒ 属**第三类：
  刻意分阶段交付（C 类）**，不是 A 类（删）也不是 B 类（接）。
  **并据此修正 105 号那套 A/B 二分框架——它少了一类。**
- 取证：主代理逐环回原代码复核
- 改动：**零生产代码、零配置、零门**

---

## 0. 一句话

105 号的子代理把零导入包分成 A（死，删）/ B（未接线，接）两类。
**这个二分少了第三类：这个包是「lite 版，有 kill-switch，README 第一段就写着
『不接生产管线、等集成者注入』」——它是作者主动排期的下一阶段，不是被遗忘的代码。**

**判错的后果很具体**：按 A 类删掉，等于删掉会话优化 v4 的 T11 产物；
按 B 类接上，等于在三条 P0 前置只完成一条的情况下强行接线。

---

## 1. 作者自己怎么说（README 第 1 段与「集成接线点」节）

`domains/session/preprocess/README.md:6-7`：

> 本包为 T11 的 **lite 版**：**不接生产管线**、不 import sanitizer/compressor 实现本体，
> 生成逻辑通过 `ArtifactBuilder` 接口**由集成者注入**。

`README.md:45-56`「集成接线点（由集成者完成）」列出四条：

| 接线点 | 内容 |
|---|---|
| `ArtifactBuilder` | Raw 侧接 canonicalizer、Sanitized 侧复用 `security/sanitize`、Compressed 侧复用 `domains/hooks/compression` |
| `EventSink` | 桥接 `internal/liveactions` 与 journey 事件（本包不 import，防环） |
| **挂点（3 个）** | `Prepare` 在会话装载后、压缩 seam 前；`CommitFirstForward` 在 `beginUpstreamAttempt(AttemptNo==1)`；`AppendTerminal` 在终态持久化成功后 |
| **kill-switch** | **「现有 direct path 保留——不装配本 Hook 即回到旧行为」** |

**⇒ 「零导入」是这个包的设计约束，不是事故。**

---

## 2. 它与现役三层链路的关系（与 107 号同型，但结论相反）

### 2.1 契约类型在生产完全不存在

| 类型 | 外部出现次数（排除本包与测试） |
|---|---|
| `ArtifactKind` | **0** |
| `SessionRevision` | **0** |
| `ArtifactMeta` | **0** |
| `TransformReceipt` | **0** |
| `TurnArtifactBlock` | 1 |

**⇒ 生产代码完全不知道这套契约的存在。**

### 2.2 objective 要的三层链路，**现役实现在别处**

`SanitizedMessageRefs` / `AlignmentMap` 的活跃实现（排除测试）：

- `domains/hooks/compression/alignment.go`、`alignment_reverse.go`、`session_compressor.go`
- `domains/streaming/executors/executor_responses_provenance.go`
- `domains/streaming/executors/executor_chat.go`
- `domains/streaming/request_log_pipeline.go`
- `domains/streaming/handler.go`
- `domains/transformation/responses_compress.go`

**⇒ 与 107 号（`nodestatecache` vs URSM v2）同型：**
目标能力**已由另一套实现在线上跑**。

**但结论相反**：107 号那个包的动机**已被证伪**（加速已由 URSM 自带镜像承担）；
本包**动机未被证伪**——它是「会话优化 v4 / FR-11 的 T11 lite 版」，
即对现役三层链路的**下一阶段升级**，README 明确把集成责任留给集成者。

---

## 3. README 自列的三条 P0 前置 —— 本轮逐条核实现状

README `:55-56` 自陈「**已知前置修复（不在本包内）**」，这是能否接线的判据：

| # | 前置 | 本轮核实结果 | 证据 |
|---|---|---|---|
| ① | sanitize Redis key tenant scope v2 | ✅ **已完成** | `security/sanitize/tenant_scope_test.go:72` `TestSanitizeRedisKey_TenantScoped` 断言「不同 tenantHash 必须产生不同 key」；`input_protocols.go:21-25` 有 tenant 上下文 |
| ② | placeholder offset HINCRBY 原子化 | ⚠️ **部分实现** | `security/sanitize/sanitizer.go:117` 注释仍写「**由调用方从 Redis 查询现有计数**」＝读-改-写；`smart_sani_guard.go:229/317` 有 Redis 租约与 `releaseOffsetsLockScript` 锁，但**不是单次原子预占**。全仓 HINCRBY 只出现在 `domains/dispatch/` 的 Lua 脚本，`security/sanitize` 下无 |
| ③ | `session_summaries` tenant scope（R11.8 P0） | ⚠️ **列已存在**，写入侧未核 | `sql/schema/01-schema.sql:15974` `tenant_id character varying(255) NOT NULL` |

**⇒ 三条前置：1 条完成、1 条部分、1 条只确认了列存在。**
**这正是「不宜贸然接线」的客观依据**——前置②的原子预占没做完时接线，
跨进程并发下的 offset 分配仍可能重复占位（这正是 objective 里
「sanitizer 跨进程 offset 原子预占」那一条本身的要求）。

---

## 4. 结论与建议

### 4.1 分类修正（这是本报告的首要产出）

**105 号的 A/B 二分需要补第三类**：

| 类别 | 含义 | 处置 |
|---|---|---|
| **A** 真死代码 | 零调用且无实现价值 | 删 |
| **B** 能力已在但漏接线 | 有实现、有测试、只差调用 | 接（优先） |
| **C** **刻意分阶段**（新） | **有设计文档明写「等集成者」、有 kill-switch、前置未完成** | **既不删也不接；先确认阶段是否仍要做** |

`domains/session/preprocess` 属 **C 类**。

### 4.2 给决策的一句话

> **会话优化 v4 的 T11 全量还要不要做？**
>
> - **要** ⇒ 先补完前置②（offset 原子预占）与③（写入侧 tenant scope），
>   再由集成者注入 `ArtifactBuilder`/`EventSink` 并挂 3 个点；
>   **注意本包自带 kill-switch，可灰度、可随时退回 direct path。**
> - **不要**（现役 `domains/hooks/compression` + provenance executors 已够用）
>   ⇒ 整包连同 11 个测试文件一并删除，**但先把 README 归档**，
>   否则下一个审计者会重复走一遍「零导入 ⇒ 死代码」的错误推理。

**两种情况下都不要按 105 号子代理的 A 类建议直接删除。**

---

## 5. 诚实的未证实项

- **我没有验证现役三层链路（`domains/hooks/compression` + provenance executors）
  在功能上是否已覆盖 T11 的 R11.1–R11.9 全部 9 条**——
  本轮只证明了「preprocess 的契约类型在生产完全不存在」，
  没有逐条比对两套实现的能力覆盖。**这是判断「T11 要不要做」的关键，尚未完成。**
- 前置③我只确认了 `session_summaries` 表**有** `tenant_id` 列，
  **没有核写入侧是否每处都带该列**。
- 前置②我确认了「非单次原子预占」，**但没有核现有租约+锁在并发下的实际正确性**
  ——它可能已经足够安全，只是没有做到 README 设想的那种原子性。
