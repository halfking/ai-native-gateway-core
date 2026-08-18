# Gateway → ASM 事件契约测试

> 状态：WP2 实施中 - 测试框架已就绪
> 目的：建立 Gateway producer 与 ASM consumer 的 v1 事件契约验收标准
> 依据：`docs/omni-ref2/02-CROSS-REPO-EVENT-CONTRACT.md`

## 目录结构

```
test/events/
├── README.md                          # 本文件
├── fixtures/                          # 测试数据
│   ├── request_completed_v1_valid.json              # 正例
│   ├── request_completed_v1_duplicate.json          # 负例：重复 event_id
│   ├── request_completed_v1_stale.json              # 负例：旧版本
│   ├── request_completed_v1_tamper.json             # 负例：签名篡改
│   ├── request_completed_v1_tenant_mismatch.json    # 负例：tenant 不一致
│   ├── request_completed_v1_forbidden_fields.json   # 负例：禁止字段
│   ├── session_identity_v1_valid.json               # T0 五元身份与 attempt 语义
│   ├── restart_semantics_v1_valid.json              # T0 四类重启语义
│   └── vocabulary_v1_valid.json                     # T0 lifecycle/event/action/error/resource 词表
└── contract/                          # 契约测试
    ├── request_completed_test.go      # request.completed.v1 测试套件
    ├── session_identity_test.go       # T0 identity fixture 校验
    ├── restart_semantics_test.go      # T0 restart fixture 校验
    └── vocabulary_test.go             # T0 vocabulary fixture 校验
```

## 当前状态

### ✅ 已完成

1. **字段映射表** (`docs/omni-ref2/03-GATEWAY-EVENT-FIELD-MAPPING.md`)
   - 定义 5 个 v1 事件的必填字段
   - 对比 Gateway 当前发布字段与 ASM 契约要求
   - 标识 10 个缺失字段和 1 个违规字段

2. **Fixtures**
   - 1 个正例：符合契约的完整事件
   - 5 个负例：duplicate/stale/tamper/tenant_mismatch/forbidden_fields

3. **测试套件** (`test/events/contract/request_completed_test.go`)
   - 6 个契约验证测试（当前 SKIP）
   - 1 个当前实现分析测试（**FAIL - 预期**）

### 测试运行结果

```bash
$ go test ./test/events/contract/... -v

=== RUN   TestRequestCompletedV1_ValidEvent
--- SKIP: TestRequestCompletedV1_ValidEvent (0.00s)
    # Gateway outbox not implemented yet

=== RUN   TestRequestCompletedV1_Duplicate
--- SKIP: TestRequestCompletedV1_Duplicate (0.00s)

=== RUN   TestRequestCompletedV1_Stale
--- SKIP: TestRequestCompletedV1_Stale (0.00s)

=== RUN   TestRequestCompletedV1_Tamper
--- SKIP: TestRequestCompletedV1_Tamper (0.00s)

=== RUN   TestRequestCompletedV1_TenantMismatch
--- SKIP: TestRequestCompletedV1_TenantMismatch (0.00s)

=== RUN   TestRequestCompletedV1_ForbiddenFields
--- SKIP: TestRequestCompletedV1_ForbiddenFields (0.00s)

=== RUN   TestCurrentGatewayPublisher
--- FAIL: TestCurrentGatewayPublisher (0.00s)
    request_completed_test.go:331: Current Gateway payload has 4 fields
    request_completed_test.go:332: Contract requires 11 fields
    request_completed_test.go:333: Missing fields (10): [session_id turn_no request_id correlation_id idempotency_key provider status token_usage latency_ms body_refs]
    request_completed_test.go:337: ❌ VIOLATION: user_content (prompt text) should not be in payload
    request_completed_test.go:341: ⚠️  MISMATCH: status_code should be 'status' (string enum, not int)
```

**关键发现**：
- 当前 Gateway 只发布 4 个字段，契约要求 11 个
- 包含 1 个违规字段（`user_content` - prompt 正文不应在事件中）
- 缺失 10 个必填字段

## 下一步实施（Phase 2）

### 前提条件

✅ **已满足**：
- 字段映射表已创建
- 测试框架已就绪
- 负例测试可稳定复现（当前 SKIP）

### 实施顺序

#### Step 1: 创建 Outbox 表

```sql
CREATE TABLE outbox_events (
    id BIGSERIAL PRIMARY KEY,
    event_id TEXT NOT NULL UNIQUE,
    event_type TEXT NOT NULL,
    schema_version INT NOT NULL DEFAULT 1,
    tenant_id TEXT NOT NULL,
    aggregate_id TEXT NOT NULL,
    aggregate_version INT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',  -- pending|sent|failed
    attempts INT NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMPTZ,
    next_retry_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_outbox_status_retry ON outbox_events(status, next_retry_at)
    WHERE status IN ('pending', 'failed');
CREATE INDEX idx_outbox_tenant_aggregate ON outbox_events(tenant_id, aggregate_id, aggregate_version);
```

#### Step 2: 实现 OutboxWriter

位置：`internal/outbox/writer.go`

职责：
- 与业务事务同一事务提交
- 生成固定的 `event_id`（重试不变）
- 序列化 payload 为 JSONB

#### Step 3: 补全 request.completed.v1 字段

修改位置：`cmd/gateway/main_pipeline.go:845`

必须补全的 10 个字段：
1. `session_id` - ✅ 已有 `env.SessionID`
2. `turn_no` - ❌ 需从 `session_turns` 读取或计数
3. `request_id` - ✅ 已有 `requestID`
4. `correlation_id` - ❌ 需设计生成规则
5. `idempotency_key` - ❌ 需设计生成规则
6. `provider` - ❌ 需从 routing result 提取
7. `status` - ⚠️ 需将 `status_code` (int) 转换为 enum (string)
8. `token_usage` - ❌ 需从 response 提取
9. `latency_ms` - ❌ 需记录 start time
10. `body_refs` - ❌ 需设计 body storage ref 格式

**必须删除的 1 个字段**：
- `user_content` - ❌ 违反契约（不应包含 prompt 正文）

#### Step 4: 实现 OutboxDispatcher

位置：`internal/outbox/dispatcher.go`

职责：
- 后台轮询 `outbox_events` 表
- HMAC 签名
- HTTP POST 到 ASM `/internal/v1/events`
- 指数退避重试
- DLQ 处理

#### Step 5: 启用测试

取消 `t.Skip()` 并实现：
- Mock ASM HTTP endpoint
- 验证签名
- 验证 tenant header
- 验证 payload 字段
- 返回 duplicate/stale/401/403

### 验收标准

Phase 2 完成的定义：

- [ ] `TestRequestCompletedV1_ValidEvent` ✅ PASS
- [ ] `TestRequestCompletedV1_Duplicate` ✅ PASS（返回 duplicate）
- [ ] `TestRequestCompletedV1_Stale` ✅ PASS（返回 stale）
- [ ] `TestRequestCompletedV1_Tamper` ✅ PASS（返回 401）
- [ ] `TestRequestCompletedV1_TenantMismatch` ✅ PASS（返回 403）
- [ ] `TestRequestCompletedV1_ForbiddenFields` ✅ PASS（拒绝）
- [ ] `TestCurrentGatewayPublisher` ✅ PASS（无缺失字段）

## 禁止项（WP2 阶段）

在上述验收标准全部通过前：

- ❌ **不**添加 `routing` / `compression` metadata 到 `request.completed.v1`
- ❌ **不**修改 SessionCacheV2、RecoveryCoordinator、compressor
- ❌ **不**启用 ASM handshake `event_mode: "enabled"`
- ❌ **不**在生产环境投递事件（仅 shadow 或测试目标）
- ❌ **不**同时实施其他 4 个 v1 事件（`session.opened`/`session.deleted`/`verdict.recorded`/`task.execution_host_changed`）

## 相关文档

- `docs/omni-ref2/02-CROSS-REPO-EVENT-CONTRACT.md` - 跨仓事件契约定义
- `docs/omni-ref2/03-GATEWAY-EVENT-FIELD-MAPPING.md` - 字段映射表
- `docs/修订0811/06-下一阶段实施计划.md` - WP2 实施计划
- `docs/修订0811/05-方案审计修订说明.md` - Ownership 边界

## 运行测试

```bash
# 运行全部契约测试
go test ./test/events/contract/... -v

# 只运行当前实现分析
go test ./test/events/contract/... -v -run TestCurrentGatewayPublisher

# 检查 fixtures 是否有效
go test ./test/events/contract/... -v -run TestRequestCompletedV1_ValidEvent
```

## 提交历史

- `test(events): add gateway-asm v1 contract fixtures for request.completed` - WP2 baseline
