# LLM Gateway Go 会话压缩 / 多轮拼接 / 三级缓存审计与优化方案（2026-08-09）

> **配套文档**：本文为审计结论 + 优化方案。审计为只读（无代码改动），基线已验证（`go build ./...` exit 0；`domains/hooks/compression`、`domains/session/...`、`security/sanitize` 全部单测通过）。
> **范围**：仅写方案，不直接改代码；每个优化项附"前置 / 改动 / 验证 / 回滚"四段式，对齐 `docs/design/2026-07-28-llm-gateway-flow-improvements.md` 的既有格式。
> **关联参考蓝本**：`tests/session_cache/`（L1 原始 / L2 压缩 / L3 安全审计 三层设计原型，**仅测试用，未接线生产**）。

---

## 目录

- [1. 背景与目标](#1-背景与目标)
- [2. 生产现状审计（逐段验证，带 file:line）](#2-生产现状审计逐段验证带-fileline)
- [3. 设计意图 vs 生产现状 对比](#3-设计意图-vs-生产现状-对比)
- [4. 差距结论](#4-差距结论)
- [5. 优化方案（按优先级）](#5-优化方案按优先级)
- [6. 验证标准](#6-验证标准)
- [7. 回滚预案](#7-回滚预案)
- [8. 决策请求](#8-决策请求)

---

## 1. 背景与目标

审计并优化 `llm-gateway-go` 的会话压缩 / 多轮自动拼接 / 三级缓存流程，以及敏感信息脱敏与还原、输出安全检查的时序合理性。目标定义：

| 目标 | 验收标准 |
|---|---|
| L1 原始多轮会话 | 单轮增量存储、拼接还原（现状走 session_turns/session_bodies） |
| L2 压缩后多轮会话 | 可跨轮次，增量 diff-append + 滑动窗口触发摘要/裁剪 |
| L3 脱敏+安全审核后发送会话 | 收到的信息先入 L3；输出做安全检查；占位符还原后再进入压缩会话回写 |
| 敏感信息 | 网关侧替换为占位符 → Redis 缓存映射 → 响应时还原，中间全程不见明文 |
| 会话后分析 | 标题抽取 / 会话摘要 / 成本 / 项目任务关联 / 聚类 |

## 2. 生产现状审计（逐段验证，带 file:line）

### 2.1 请求侧真实顺序

```
HTTP 入口脱敏 (SanitizeInputMiddleware, handler.go:1326-1328)
  → serveHTTPInner (handler.go:1333)
  → 会话解析/候选路由
  → SessionCompressor.Prepare (handler.go:2818)   ← 压缩唯一入口
       · V2 优先 (session_compressor.go:216-240)
       · V1 回退 (session_compressor.go:243-250)
  → NeverWorse 守卫 + tools 还原 (handler.go:2831-2868)
  → 上游执行
```

关键事实：

- 脱敏中间件在最外层包裹整个 handler（handler.go:1326），**先于一切业务逻辑**。
- 压缩执行时 body 已经是脱敏后的占位符版本 → **session 缓存存储的就是脱敏内容**。
- `Prepare` 内部顺序（session_compressor.go:179-308）：
  - Phase 0 session_id 校验 → Phase 1 读 V2/V1 状态 → Phase 2 delta-append → Phase 3 tools 缓存 → Phase 4 滑动窗口评估 → 摘要/裁剪 → `updateCache`。
- `updateCache`（session_compressor.go:650-698）：V2 读路径不写回 V1（防状态污染，:667-669）；写入 `SessionState{SchemaVersion, LastOutboundHash, MsgCount, TokenEstimate, SummaryMarker, ToolsHash, SystemPrompt, LastCompressedAt}`。

### 2.2 响应侧真实顺序（关键）

```
上游响应
  → responseInterceptor chain (handler.go:3209-3217)
       · output_compliance 先执行   (goal_control.go:466-467 注释明确)
       · sanitize_restore 最后执行  (goal_control.go:467 追加到 chain 末尾)
  → request_logs 落库 (存脱敏 body)
  → 异步 auto-title (handler.go:4767)
  → 异步 auto-summary (handler.go:4790)
```

设计意图"先安全检查，再还原占位符" **已在生产实现中成立**：检查只见占位符，还原在链末。此为正确且安全的设计。

### 2.3 缓存实现现状

| 缓存 | 文件 | 层级 | 现状 |
|---|---|---|---|
| `SessionCache` | `domains/hooks/compression/session_cache.go` | L1 in-process sync.Map → L2 Redis Hash `session:sc:{tenant}:{gwSession}:v1` → L3 PG `request_logs` 回源 | ✅ 生产在用（压缩状态缓存） |
| `SessionCacheV2` | `domains/session/v2/cache_v2.go` + `cache_v2_redis.go` | Redis `session:v2`（TTL 30min）+ DB `session_bodies` | ✅ 生产在用（V2 优先） |
| `RawCacheV2` | `domains/session/v2/raw_cache_v2.go` | L0 原始 LRU（1024，存轮次增量） | ⚠️ **未接线生产**，仅自测 |
| `TurnWriter` / `TurnReader` | `domains/session/v2/turn_writer.go` / `turn_reader.go` | 单轮增量 → session_turns/session_bodies（24h TTL） | ✅ 生产在用 |
| 参考蓝本三层 | `tests/session_cache/*` | Raw → Compressed → Audited + AlignmentMap | ⚠️ 仅测试，未接线 |

### 2.4 脱敏/安全实现现状

- `SmartSaniGuard`：输入脱敏中间件（只脱 user/system）+ 响应还原拦截器。
- 装配：`cmd/gateway/goal_control.go:388-467`；`main.go:3225` 调用 `installSmartSaniGuard`。
- 占位符：`{SENSITIVE:type:index}`（placeholder.go:32）；检测类型 phone/id_card/email/credit_card/secret/internal_ip/name/custom（detector.go:39-46）。
- 映射：Redis Hash `session:{sid}:sanitize`（TTL 30min）+ offsets `session:{sid}:sanitize:offsets`（防跨轮索引碰撞）。

### 2.5 会话后分析现状

- 自动标题：`admin/auto_title_generator.go`，首轮成功后异步（handler.go:4767-4781）。
- 自动摘要：`admin/auto_summary_generator.go`，滚动门限 ≤3 新轮跳过、≤12k 单次、>12k map-reduce、每租户限流 6/min（handler.go:4790-4800）。
- `session_summary_worker`（`domains/analysis/workers/session_summary_worker.go:24-88`）：订阅 EventSessionClosed，**当前休眠**。
- 两者均基于 request_logs 触发，未消费 SessionCacheV2 状态。

## 3. 设计意图 vs 生产现状 对比

| 设计项 | 设计意图 | 生产现状 | 差距级别 |
|---|---|---|---|
| 三层缓存 L1 原始 | 完整消息历史 | session_turns/session_bodies（单轮增量 + 拼接） | ✅ 实现（语义为 DB 落库而非缓存） |
| 三层缓存 L2 压缩 | 可跨轮次压缩状态 | SessionCache + SessionCacheV2 | ✅ 实现 |
| 三层缓存 L3 脱敏+审计 | 独立"已审核待发送"缓存 | **无独立对象**，脱敏前置使缓存天然为脱敏内容 | ⚠️ 语义隐式 |
| "收到先入 L3" 时序 | 先入 L3 再后续处理 | 脱敏在最外层 HTTP 边界，早于压缩 | ✅ 等价（且更靠前） |
| "检查先、还原后" 时序 | 检查只见占位符 | output_compliance → sanitize_restore | ✅ 完全一致 |
| AlignmentMap 对齐信息 | 原始↔压缩↔审计位置映射 | 生产无（仅测试蓝本有） | ⚠️ 缺失 |
| RawCacheV2 | L0 原始缓存加速 | 未接线 | ⚠️ 未接线 |
| 会话后分析 | 标题/摘要/成本/关联/聚类 | 标题+摘要已接线；成本/关联/聚类部分；worker 休眠 | ⚠️ 部分 |

## 4. 差距结论

1. **L3 无独立缓存对象**：脱敏在压缩之前执行，缓存存储的就是脱敏内容 —— 这本身就是安全的，但"已审核待发送"的语义是隐式的，运维/审计无法显式确认"这条发出去的内容经过了安全检查"。
2. **无 AlignmentMap**：三层间位置对齐信息缺失，导致无法回溯"第 N 条原始消息被压缩进了哪条摘要 / 审计后位置在哪"。参考蓝本已有完整设计但未落地。
3. **RawCacheV2 悬空**：L0 原始缓存已实现（LRU/增量/并发安全，测试齐），未接入生产路径。
4. **会话后分析与压缩状态解耦**：auto-title/auto-summary 基于 request_logs，未利用 SessionCacheV2 状态，存在重复摘要 / 与压缩会话不一致风险。

## 5. 优化方案（按优先级）

### O-1（P1）：L3 审核语义显式化 —— 给压缩结果打 `audited` 标记

**目标**：让"已脱敏+已安全审核"成为缓存状态的显式字段，可被运维与审计查询。

#### 前置

- 确认现有 `SessionState` 的 JSONB `compression_meta` 可扩展（是，`updateCache` 已写 `window_triggered` 等）。

#### 改动

1. `domains/hooks/compression/session_cache.go` 的 `SessionState` 增加字段 `Audited bool` + `AuditedAt int64`（可选）。
2. `updateCache`（session_compressor.go:695）写入时，因脱敏在压缩前已生效，默认置 `Audited=true` 并记录时间。
3. `cmd/gateway/goal_control.go` 还原拦截器成功执行时，回写 `audited=true`（证明"还原环节也跑过"）。

#### 验证

- `go test ./domains/hooks/compression/...` 通过。
- 发一次请求后查 Redis `session:sc:{tenant}:{session}:v1`，`audited=true`。

#### 回滚

- 回滚 commit；字段为新增可空字段，无破坏性。

### O-2（P1）：落地 AlignmentMap（原始↔压缩↔审计 位置映射）

**目标**：三层间位置可追溯，支持"某条原始消息去哪儿了"的审计需求。

#### 前置

- 参考 `tests/session_cache/three_tier_cache_test.go`（AlignmentInfo 结构、1:1 / 压缩映射构建）。

#### 改动

1. `domains/hooks/compression/types.go` 增加 `AlignmentInfo`（OriginalIndex / CompressedIndex / IsCompressed / CompressedInto / Hash）。
2. `SessionState` 增加 `AlignmentMap []AlignmentInfo`，随 `compression_meta` JSONB 序列化。
3. 压缩触发时构建对齐映射（摘要消息 → 被压缩消息；保留消息 → 1:1）。

#### 验证

- 新增单测：构造 15 条消息触发摘要，断言 AlignmentMap 长度=原始消息数、摘要映射正确。
- 跨层一致性：压缩摘要后再审计，审计层对齐映射正确。

#### 回滚

- 回滚 commit；新字段可空，旧数据无损。

### O-3（P2）：接线 RawCacheV2（或标注 KEEP 归档）

**目标**：消除"已实现但悬空"的代码；要么接线加速跨轮次拼接，要么明确归档防误删。

#### 前置

- 评估：RawCacheV2 存轮次增量（RawEntry），与 TurnWriter 写入同步。接线成本 = 在 `initSessionV2Writer`（session_v2_init.go:38）创建 + 在写入路径调用 `Put`。

#### 改动

1. `cmd/gateway/session_v2_init.go` 创建 `RawCacheV2` 并注入 SessionWriterV2。
2. `domains/session/v2/session_writer_v2.go` 在 AppendTurn 成功后 `rawCache.Put(...)`。
3. `session_compressor.go` V2 读路径优先查 L0 原始缓存（命中则跳过 LoadChain 拼接）。

#### 验证

- `go test ./domains/session/v2/...` 通过。
- 二次请求热路径延迟下降（L0 命中率指标）。

#### 回滚

- 回滚 commit；RawCacheV2 为纯增量缓存，不持久，无数据风险。

> **替代方案**：若接线收益不明确（DB 拼接已有 24h TTL + LoadChain），则改为标注 `// KEEP: L0 原始缓存预留 @session 2026-08-09`，归档到季度审计清单，不接线。

### O-4（P3）：会话后分析与 SessionCacheV2 协同

**目标**：auto-title/auto-summary 读取压缩会话状态，避免与压缩会话内容不一致 / 重复摘要。

#### 改动

1. `admin/auto_summary_generator.go` 在生成摘要前读取该 session 最近摘要状态（SessionState.SummaryMarker），做增量而非全量。
2. 若 `session_summary_worker`（EventSessionClosed）启用，与 request_logs 触发路径合并去重（同一 session + 同一 turn 只摘要一次）。

#### 验证

- 同一 session 连续 N 轮，摘要调用次数 ≤ 现有实现（不重复）。
- `session_summary_worker` 启用后与 auto_summary 不产生重复行。

#### 回滚

- 回滚 commit；改的是触发逻辑，不影响缓存结构。

## 6. 验证标准

- 结构验证（每文件）：`go build ./...` exit 0；相关包 `go test` 全过。
- 单测：新增 O-2 AlignmentMap / O-3 RawCacheV2 接线测试，覆盖 happy + boundary + error。
- 行为验证（部署后）：一次真实请求 → Redis `session:sc:*` 出现 `audited=true`；连续多轮请求 → AlignmentMap 随压缩正确更新；二次请求 L0 命中。
- 安全回归：脱敏-压缩-上游-检查-还原 全链路时序不变（O-1/O-2 只加字段不改时序）。

## 7. 回滚预案

- 每项独立 commit + 独立回滚命令（见 §5 各项"回滚"）。
- 所有新增字段可空、非破坏性，回滚仅需 revert commit，无 DB 迁移。

## 8. 决策请求

| 决策点 | 选项 | 建议 |
|---|---|---|
| O-3 接线 vs 归档 | A) 接线 RawCacheV2 到生产；B) 标注 KEEP 归档 | 若热路径命中是刚需选 A；否则选 B |
| O-1/O-2 是否同批实施 | A) 同批；B) 分两批 | 建议同批（同属 SessionState 扩展，diff 小） |
| 是否需要我直接实施 | A) 逐项按方案实施；B) 仅保留方案文档 | 等待老板指令 |

---

_本文为审计+方案文档，仅写方案不直接改代码；实施需按 §5 各项独立 commit + 验证 + 回滚。_
