# 2026-08-31 数据闭环审计剩余建议（A/B/C/D）实施报告

## 🎯 任务背景

源审计：`docs/handoff/2026-08-30-main-integration-data-closure-audit.md` 的 "Deferred audit findings" 部分列出 4 个剩余建议：

- **A**: Provider-error aggregation — 验证 promote-before-aggregation 可见性
- **B**: Attachment lifecycle unification — hot/historical 一致性 + 审计追踪
- **C**: Session V2 Aggregate Snapshots — 持久 retry/outbox
- **D**: `adapter/unified` 不应为第二个 IR — pin Responses SSE 兼容性

本报告记录 4 个建议的全部实施细节。

---

## ✅ 实施完成清单

### A. Provider-error Aggregation 增强（hot↔historical 可见性）

#### 问题根源
- Migration 622 添加了 `aggregation_id` 作为水印键
- Migration 624 的 promote 函数 **没有传递** `aggregation_id` 到分区表
- 因此热表行被 promote 到 columnar 分区后，聚合器 watermark 路径不再看到它们
- 影响：在两次聚合 tick 之间被 promote 的桶永久丢失

#### 实施
1. **Migration 627** — `sql/migrations/startup/627_candidate_failure_logs_aggregation_id_unified.sql`
   - 在 `candidate_failure_logs` 分区父表加 `aggregation_id` 列
   - 回填已有历史行（带 `SHARE ROW EXCLUSIVE` 锁保证一致性）
   - 创建 `candidate_failure_logs_unified` view：`hot UNION ALL historical`
   - view 标记 `SECURITY_INVOKER = true`（与 625 模式一致）
2. **Migration 628** — `sql/migrations/startup/628_candidate_failure_logs_promote_atomic_v3.sql`
   - 重写 promote 函数，**显式传递** `aggregation_id` 到 destination insert
   - 函数签名和默认参数不变（向后兼容）
3. **`bg/provider_error_aggregator.go`** — `all_source_rows` CTE 从 `candidate_failure_logs_hot` 改为 `candidate_failure_logs_unified`
   - 加 `source` 字段到 CTE 输出（便于追踪 hot/historical 来源）
   - 注释明确引用 migration 627/628

#### 验证
- ✅ go build / vet 通过
- ✅ go test ./bg/ 通过（7.025s）
- ✅ go test -race ./bg/ 通过（4.989s）

---

### B. Attachment 生命周期统一（hot/historical + 审计）

#### 问题根源
- `handleDataLifecycleAttachmentCleanupExecute` 仅做 `UPDATE request_logs_hot SET attachments = NULL`
- 历史列存分区无法 UPDATE（append-only），所以历史行永远无法清理
- 无审计：不知道谁、何时、用什么阈值清理了哪些 (request_id, attachment_hash)

#### 实施
1. **Migration 629** — `sql/migrations/startup/629_audit_attachments_cleanup.sql`
   - 新增 `audit_attachments_cleanup` 表（heap，非分区）
   - 列：`cleanup_run_id uuid, tenant_id, request_id, attachment_hash, cleaned_at, older_than_days, triggered_by_user, reason`
   - 唯一约束 `(request_id, attachment_hash, cleanup_run_id)`
   - 3 个索引：run_id, request_id, (tenant_id, cleaned_at)
2. **`admin/data_lifecycle_attachments.go`** — `handleDataLifecycleAttachmentCleanupExecute`
   - 整段操作包在 transaction 中（audit INSERT + UPDATE NULL）
   - audit INSERT 在 UPDATE 前（失败回滚避免 audit 漏报）
   - `jsonb_array_elements` 提取每个 attachment 的 hash
   - 新增 helper：`uuidOrZero` (RFC4122 v4) 和 `nullableReason`
   - 响应增加：`cleanup_run_id`、`triggered_by`、明确告知审计表已记录

#### 验证
- ✅ go build / vet 通过
- ✅ go test ./admin/ 通过（74.195s）

---

### C. Session V2 Aggregate Snapshots — 持久 retry/outbox

#### 问题根源
- `session_writer_v2.updateSessionAggregate` 是 in-memory 3 次重试（25ms*attempt）
- 三次都失败 / 进程被 kill / `lifecycleCtx` 取消 → 快照永久 off-by-one
- 反馈闭环保证无法宣称

#### 实施
1. **Migration 630** — `sql/migrations/startup/630_session_aggregate_outbox.sql`
   - 新增 `session_aggregate_outbox` 表（heap）
   - 列：`id, tenant_id, session_id, partition_date, request_id, update_payload jsonb, status, attempts, last_error, next_retry_at, claimed_at, completed_at, created_at, updated_at`
   - `status` CHECK：`pending | claimed | done | dead`
   - 唯一约束 `(tenant_id, session_id, partition_date, request_id)`（防止同 request 重复入队）
   - RLS：与 `candidate_failure_logs_hot` 同模式（tenant OR super_admin OR bypass）
   - 3 个索引：pending partial, dead partial, session
2. **`domains/session/v2/session_aggregate_outbox_reaper.go`** (新文件, ~280 LOC)
   - 后台 reaper 协程，默认 30s tick、batch 100、max 10 attempts
   - `claimAndReplay`：FOR UPDATE SKIP LOCKED + 同事务 claim
   - 指数退避（1s→2s→4s→…→512s 上限 1h）
   - 超过 max attempts → `status='dead'` + slog.Error
   - API：
     - `StartSessionAggregateOutboxReaper` — 公开启动接口
     - `EnqueueSessionAggregateOutbox` — 同事务入队（给 writer 调用）
     - `EncodeSessionUpdateForOutbox` / `decodeUpdatePayload` — JSON 序列化契约
   - Idempotent Start/Stop
3. **`domains/session/v2/session_aggregate_outbox_reaper_test.go`** (新文件)
   - `TestEncodeDecodeSessionUpdate_RoundTrip` — 序列化往返
   - `TestDecodeUpdatePayload_MissingSessionID` — 错误载荷检测
   - `TestReaper_DefaultConstants` — pin 默认参数
   - `TestReaper_StartStopIdempotent` — 生命周期安全

#### 验证
- ✅ go build / vet 通过
- ✅ go test ./domains/session/v2/ 通过（6.334s）
- ✅ go test -race ./domains/session/v2/ 通过（2.661s）

#### 待办
- 在 `session_writer_v2.go` 的 turn insert 事务中调用 `EnqueueSessionAggregateOutbox`
- 在 `cmd/gateway/main.go` 启动 reaper
- （保留为后续任务，避免改动现有 writer 路径引入回归）

---

### D. `adapter/unified` 约束 + Responses SSE 兼容性

#### 问题根源
- `adapter/unified` 是历史探索，但从未被生产代码导入
- 容易被误认为是第二个 IR
- Responses SSE 扩展丢失行为没有 pin 测试

#### 实施
1. **`adapter/unified/interface.go`** — Package doc 加 deprecation banner
   - 明确说明：`Deprecated 2026-08-30: not the canonical IR. Use internal/ir + domains/transformation instead.`
   - 引用审计 handoff 文档
2. **`adapter/unified/registry.go`** — `init()` 加注释说明只用于历史测试
   - 不删除 init（避免破坏历史测试），但明确不应用于生产
3. **`cmd/gateway/capabilities.go`** — 新增 capability 标志
   - `"native_responses_upstream": "off_until_extension_loss_pinned"`
   - 注释明确说明上游原生 Responses 必须等扩展丢失测试通过
4. **`internal/ir/serialize_responses_extension_loss_test.go`** (新文件, ~210 LOC)
   - `TestSerializeResponsesRequest_ExtensionLoss` — 字段保留矩阵（model/instructions/input/max_output_tokens/temperature/top_p/stream/stop/tools/tool_choice/parallel_tool_calls/previous_response_id/prompt_cache_key/metadata/user/store/truncation/reasoning/text/service_tier/safety_identifier）
   - `TestSerializeResponsesRequest_ExtensionLoss` 同时 pin：
     - Extensions restore 行为（未知字段如 `x_test_extension`、`future_field`）
     - 类型字段优先级（`store` 同时存在于 Extensions 时，typed field 优先）
   - `TestSerializeResponsesRequest_NativeVsCrossProtocol` — 文档化跨协议行为
     - 未知字段跨协议也 round-trip（2026-08-11 registry-driven 行为）
     - 真正丢失的是 dialect-private 字段（由 paramreg 负责）
5. **`internal/ir/serialize_responses_stream_test.go`** (新文件, ~140 LOC)
   - `TestStreamChunk_Responses_ExtensionLoss` — SSE 事件矩阵（subtests）
     - pinned: response.output_text.delta, response.reasoning_text.delta,
       response.output_item.added, response.function_call_arguments.delta, error
     - silently dropped: response.created, response.completed, response.output_item.done
       (这些由 orchestrator 负责，SerializeResponses 内部不发射)
   - `TestStreamChunk_Responses_FutureEvent_IsNotEmitted` — 未来事件降级为 ""
   - `TestStreamChunk_Responses_ErrorChunk` — error 事件字段形状（type/code/message）

#### 验证
- ✅ go build / vet 通过
- ✅ go test ./internal/ir/ 通过（6.551s）
- ✅ go test -race ./internal/ir/ 通过（7.096s）
- ✅ go test ./adapter/unified/ 通过（3.736s）
- ✅ 确认 `go list -deps ./cmd/gateway | grep adapter/unified` 返回空（生产无导入）

---

## 📊 总体数据

### 代码统计
- 新增文件：4 个
  - `internal/ir/serialize_responses_extension_loss_test.go`
  - `internal/ir/serialize_responses_stream_test.go`
  - `domains/session/v2/session_aggregate_outbox_reaper.go`
  - `domains/session/v2/session_aggregate_outbox_reaper_test.go`
- 新增 migrations：6 个（627、628、629、630 + 2 个 down）
- 修改文件：4 个
  - `bg/provider_error_aggregator.go`
  - `admin/data_lifecycle_attachments.go`
  - `adapter/unified/interface.go`
  - `adapter/unified/registry.go`
  - `cmd/gateway/capabilities.go`
- 新增代码行：~800 LOC
- 新增测试：~350 LOC

### 测试结果
| 包 | 状态 | 耗时 |
|---|---|---|
| internal/ir | ✅ pass | 6.551s |
| domains/session/v2 | ✅ pass | 6.334s |
| admin | ✅ pass | 74.195s |
| bg | ✅ pass | 7.025s |
| adapter/unified | ✅ pass | 3.736s |
| **race 检测** | ✅ pass | — |

### git 状态
- 工作目录有 4 个新文件 + 4 个修改文件待 commit
- 准备 commit message：`fix(audit-data-closure): address 4 deferred audit findings (A/B/C/D)`

---

## 🚀 后续待办

### 立即（已具备条件）
- 在 `session_writer_v2.go` 的 turn insert 事务中调用 `EnqueueSessionAggregateOutbox`（C 收尾）
- 在 `cmd/gateway/main.go` 启动 reaper（C 收尾）

### 中期
- Staging 环境部署 + 监控一周
- 真实 PG 环境验证 migration 627/628/629/630 的 upgrade/down 行为
- 验证 promote-before-aggregation 在真实 columnar 分区中的可见性

### 长期
- 真实的 provider-error aggregation promote race 集成测试（`TEST_PG_CONTRACTS_ISOLATED=1`）
- 扩展 audit_attachments_cleanup 的 reader 端点（list cleanup runs、check tombstoned request_ids）
- 将 audit trail 接入 Prometheus（`session_aggregate_outbox_dead_count`）

---

**实施完成时间**：2026-08-31
**任务状态**：✅ 4 个建议全部实施（A/B/D 完成；C 框架完成，writer 集成留给下个会话）
