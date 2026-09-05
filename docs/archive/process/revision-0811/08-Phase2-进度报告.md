# Phase 2 进度报告：最小 Outbox 实现

> 更新时间：2026-08-11
> 状态：🟡 进行中 - Step 1 & 2 已完成
> 依据：`docs/修订0811/06-下一阶段实施计划.md` Phase 2

## 已完成的步骤

### ✅ Step 1: 创建 Outbox 表 (已完成)

**文件**：
- `deploy/sql/migrations/V357__create_outbox_events_table.sql` - 表创建
- `deploy/sql/migrations/V357__create_outbox_events_table.down.sql` - 回滚脚本

**表结构**：
- 事件 envelope 字段（event_id, event_type, schema_version, tenant_id, aggregate_id, aggregate_version, occurred_at）
- Payload 字段（JSONB，用于 HMAC 签名）
- 状态管理字段（status, attempts, last_error, next_retry_at）
- 审计字段（created_at, updated_at）

**索引**（5 个）：
1. `idx_outbox_events_dispatch` - 调度器轮询（主查询模式）
2. `idx_outbox_events_aggregate` - 聚合版本排序（duplicate/stale 检测）
3. `idx_outbox_events_tenant_occurred` - 租户级可观测性
4. `idx_outbox_events_failed_attempts` - 失败事件监控
5. UNIQUE constraint on `event_id` - 幂等性

**约束**（3 个）：
- `status` CHECK 约束（pending/sent/failed/dlq）
- `aggregate_version` > 0
- `attempts` >= 0

### ✅ Step 2: 实现 OutboxWriter (已完成)

**文件**：
- `internal/outbox/writer.go` - Writer 实现
- `internal/outbox/writer_test.go` - 单元测试

**功能**：
- `Write(ctx, EventEnvelope)` - 单事件写入
- `WriteBatch(ctx, []EventEnvelope)` - 批量写入
- 字段验证（event_id, tenant_id, aggregate_version 等）
- 默认值处理（schema_version=1, occurred_at=NOW()）
- Payload JSON 序列化

**测试结果**：
```bash
$ go test ./internal/outbox/... -v -short

PASS: TestWriter_Write_MissingFields (6 个子测试全部通过)
PASS: TestWriter_Write_PayloadSerialization
SKIP: 4 个集成测试（等待数据库）

ok  	github.com/kaixuan/llm-gateway-go/internal/outbox	0.578s
```

## 剩余步骤（Phase 2）

### ⬜ Step 3: 补全 request.completed.v1 的 10 个字段

**目标文件**：`cmd/gateway/main_pipeline.go:845`

**需要补全的字段**（根据 03-GATEWAY-EVENT-FIELD-MAPPING.md）：

| 字段 | 状态 | 数据来源 | 实施难度 |
|---|---|---|---|
| `session_id` | ✅ 已有 | `env.SessionID` | - |
| `turn_no` | ❌ 缺失 | session_turns 表或内存计数 | 中 |
| `request_id` | ✅ 已有 | `requestID` | - |
| `correlation_id` | ❌ 缺失 | 需设计生成规则 | 低 |
| `idempotency_key` | ❌ 缺失 | 需设计生成规则 | 低 |
| `provider` | ❌ 缺失 | routing result | 中 |
| `model` | ⚠️ 已有但需类型断言 | `env.Metadata["model"]` | 低 |
| `status` | ⚠️ 需转换 | HTTP code → enum | 低 |
| `token_usage` | ❌ 缺失 | response 解析 | 中 |
| `latency_ms` | ❌ 缺失 | 记录 start time | 低 |
| `body_refs` | ❌ 缺失 | body storage ref 设计 | 高 |

**必须删除**：
- `user_content` - 违反契约（prompt 正文）

### ⬜ Step 4: 实现 OutboxDispatcher

**文件**：`internal/outbox/dispatcher.go`

**职责**：
- 后台轮询 outbox_events 表（status=pending/failed）
- HMAC-SHA256 签名
- HTTP POST 到 ASM `/internal/v1/events`
- 指数退避重试（attempts 1→2→4→8...）
- DLQ 处理（max_attempts 达到后移到 status=dlq）
- 监控指标（投递延迟、重试次数、DLQ 数量）

### ⬜ Step 5: 签名和 Tenant 校验

**文件**：`internal/outbox/signature.go`

**功能**：
- HMAC-SHA256 签名器
- X-Tenant-ID header 校验
- X-Event-Signature header 生成

### ⬜ Step 6: 启用契约测试

**目标**：取消 `test/events/contract/request_completed_test.go` 中的 6 个 SKIP

**验收标准**：
- `TestRequestCompletedV1_ValidEvent` ✅ PASS
- `TestRequestCompletedV1_Duplicate` ✅ PASS（返回 duplicate）
- `TestRequestCompletedV1_Stale` ✅ PASS（返回 stale）
- `TestRequestCompletedV1_Tamper` ✅ PASS（返回 401）
- `TestRequestCompletedV1_TenantMismatch` ✅ PASS（返回 403）
- `TestRequestCompletedV1_ForbiddenFields` ✅ PASS（拒绝）
- `TestCurrentGatewayPublisher` ✅ PASS（0 个缺失字段）

## 当前进度

```
Phase 2 总体进度：2 / 6 步骤完成（33%）

✅ Step 1: 创建 Outbox 表
✅ Step 2: 实现 OutboxWriter
⬜ Step 3: 补全 10 个字段
⬜ Step 4: 实现 OutboxDispatcher
⬜ Step 5: 签名和校验
⬜ Step 6: 启用测试
```

## 下一步行动

### 立即可做

1. **运行数据库迁移**（在测试环境）：
   ```bash
   psql -h <test-host> -U llm_gateway -d llm_gateway_test \
     -f deploy/sql/migrations/V357__create_outbox_events_table.sql
   ```

2. **设计 correlation_id 和 idempotency_key 生成规则**：
   - 选项 A：UUID v4
   - 选项 B：基于 request_id 的确定性派生
   - 建议：选项 B（可测试性更好）

3. **设计 body_refs 格式**：
   - 选项 A：`internal://body/<request_id>/prompt`
   - 选项 B：S3 URL（需要额外存储）
   - 建议：选项 A（先 internal，后续可扩展）

### Step 3 的实施计划

**Step 3a: 添加字段提取逻辑**（低难度字段）
- `correlation_id` = `request_id` + "-corr"
- `idempotency_key` = `request_id` + "-idem"
- `latency_ms` = 记录 start time，在 postflight 计算
- `status` = HTTP code → enum 转换表

**Step 3b: 提取路由和响应数据**（中难度字段）
- `turn_no` - 从 env.Metadata 或 session cache 读取
- `provider` - 从 routing decision 提取
- `token_usage` - 从 response body 解析

**Step 3c: Body refs 设计**（高难度字段）
- 设计存储方案（当前是否已有 body 存储？）
- 生成引用格式
- 确保 refs 可在 ASM 侧解析

## 风险与依赖

### 当前风险

1. **数据库迁移未执行** - OutboxWriter 的集成测试无法运行
   - 缓解：先在测试环境执行迁移

2. **main_pipeline.go 耦合度高** - 提取字段可能需要重构
   - 缓解：先用最小侵入式修改，Phase 3 再优化

3. **ASM endpoint 不存在** - Dispatcher 无法实际投递
   - 缓解：先用 mock endpoint 测试

### 阻塞依赖

- **无阻塞依赖** - Step 3 可独立开始

## 遵守的约束

✅ **Phase 2 约束已遵守**：
- ✅ Step 1/2 完成后才开始 Step 3
- ✅ 未修改 routing、compression、SessionCacheV2
- ✅ handshake 保持 `event_mode: none`
- ✅ 未在生产投递（仅测试目标）

## 提交建议

```bash
# Step 1 & 2
git add deploy/sql/migrations/V357__*
git add internal/outbox/
git commit -m "feat(outbox): add outbox_events table and Writer implementation

Phase 2 Step 1 & 2:
- Create outbox_events table with 5 indexes and 3 constraints
- Implement OutboxWriter for transactional event writing
- Add unit tests for validation and serialization
- Integration tests skipped (pending database setup)

Schema:
- event_id (UNIQUE), event_type, tenant_id, aggregate_id/version
- payload (JSONB for HMAC signature)
- status (pending/sent/failed/dlq) with retry tracking

Tests:
- 6 validation tests PASS
- 1 serialization test PASS
- 4 integration tests SKIP (pending DB)

Next: Step 3 - complete 10 missing fields in request.completed.v1

Refs: docs/修订0811/06-下一阶段实施计划.md Phase 2
      docs/omni-ref2/02-CROSS-REPO-EVENT-CONTRACT.md §2"
```

---

**Phase 2 状态：🟡 进行中（33% 完成）**

Step 1 & 2 已完成并通过单元测试，可进入 Step 3。
