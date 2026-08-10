# Gateway 事件字段映射表

> 状态：WP2 实施中
> 目的：核验 Gateway producer 与 ASM consumer 的 v1 事件契约一致性
> 依据：`02-CROSS-REPO-EVENT-CONTRACT.md`、`domains/events/canonical.go`

## 1. 当前 Gateway 事件发布状态

### 1.1 已实现的内部事件

Gateway 当前通过 `domain/analysis` 包发布内部异步事件：

| 事件类型 | 发布位置 | Payload 类型 | ASM 契约对应 |
|---|---|---|---|
| `request.completed` | `cmd/gateway/main_pipeline.go:838` | `map[string]any` | `request.completed.v1` (待映射) |
| `session.closed` | (未找到) | - | `session.deleted.v1` (待映射) |
| `tool.completed` | (未找到) | - | - |
| `approval.decided` | (未找到) | - | - |
| `failure.detected` | (未找到) | - | - |

**发现**：当前 Gateway 只发布到内部 `analysis_events` 表，**尚未实现跨仓 outbox 和 ASM 投递**。

### 1.2 Canonical 值对象

`domains/events/canonical.go` 定义了三个 canonical 类型：

- `ProviderRef` - provider 引用
- `RoutingDecision` - 路由决策
- `CompressionEvent` - 压缩结果

这些类型设计用于对外事件，但**当前未被 `analysis.AnalysisEvent` 使用**。

## 2. ASM v1 事件契约要求

根据 `02-CROSS-REPO-EVENT-CONTRACT.md` §1，ASM 期望接收 5 个 v1 事件：

### 2.1 session.opened.v1

**用途**：会话创建时发布

**必填字段**：
```json
{
  "session_id": "uuid",
  "tenant_id": "string",
  "created_at": "timestamp",
  "client_metadata": {
    "client_kind": "string",
    "agent_kind": "string",
    "task_id": "string"
  }
}
```

**Gateway 当前状态**：❌ 未实现

**缺失字段**：
- session_id 生成逻辑存在但未发布事件
- client_metadata 未系统收集

### 2.2 request.completed.v1

**用途**：单次请求完成时发布（含成功和失败）

**v1 白名单字段** (02-CROSS-REPO-EVENT-CONTRACT.md §3)：
```json
{
  "session_id": "uuid",
  "turn_no": 3,
  "request_id": "string",
  "correlation_id": "string",
  "idempotency_key": "string",
  "provider": "provider-family",
  "model": "model-name",
  "status": "succeeded|failed|timeout",
  "token_usage": {
    "prompt_tokens": 10,
    "completion_tokens": 20,
    "total_tokens": 30
  },
  "latency_ms": 420,
  "body_refs": {
    "prompt_ref": "internal://body/req-1/prompt",
    "response_ref": "internal://body/req-1/response"
  }
}
```

**Gateway 当前发布字段** (main_pipeline.go:845)：
```json
{
  "user_content": "string",      // ❌ 违反契约：不应包含 prompt 正文
  "status_code": 200,             // ⚠️ 字段名不匹配：应为 status
  "model": "string",              // ✅
  "path": "/v1/chat/completions"  // ⚠️ 未在契约中
}
```

**字段映射 diff**：

| 契约字段 | Gateway 字段 | 状态 | 修复动作 |
|---|---|---|---|
| `session_id` | `env.SessionID` | ✅ 已有 | 已在 Event envelope 中 |
| `turn_no` | - | ❌ 缺失 | 需从 session_turns 读取 |
| `request_id` | `requestID` | ✅ 已有 | 已在 Event envelope 中 |
| `correlation_id` | - | ❌ 缺失 | 需设计生成规则 |
| `idempotency_key` | - | ❌ 缺失 | 需设计生成规则 |
| `provider` | - | ❌ 缺失 | 需从 routing result 提取 |
| `model` | `env.Metadata["model"]` | ⚠️ 类型不确定 | 需类型断言 |
| `status` | `env.StatusCode` | ⚠️ 语义不匹配 | HTTP code → status enum |
| `token_usage` | - | ❌ 缺失 | 需从 response 提取 |
| `latency_ms` | - | ❌ 缺失 | 需记录 start time |
| `body_refs` | - | ❌ 缺失 | 需设计 body storage ref |
| (不应有) | `user_content` | ❌ 违反契约 | **必须删除** |
| (不应有) | `path` | ⚠️ 未在契约 | 可作为未来扩展 |

### 2.3 verdict.recorded.v1

**用途**：质量评估或人工审核结果

**必填字段**：
```json
{
  "session_id": "uuid",
  "turn_no": 3,
  "verdict_type": "quality|safety|approval",
  "result": "pass|fail|pending",
  "score": 0.85,
  "recorded_at": "timestamp"
}
```

**Gateway 当前状态**：❌ 未实现

**备注**：归属 ai-session-manager Semantic Worker，Gateway 不应发布此事件。

### 2.4 session.deleted.v1

**用途**：会话删除时发布

**必填字段**：
```json
{
  "session_id": "uuid",
  "tenant_id": "string",
  "deleted_at": "timestamp",
  "reason": "user_request|retention_policy|compliance"
}
```

**Gateway 当前状态**：❌ 未实现

**可能对应**：`analysis.EventSessionClosed`，但未找到发布位置。

### 2.5 task.execution_host_changed.v1

**用途**：会话执行宿主变更（如从本地切换到云端）

**必填字段**：
```json
{
  "session_id": "uuid",
  "task_id": "string",
  "old_host": "local",
  "new_host": "cloud",
  "changed_at": "timestamp"
}
```

**Gateway 当前状态**：❌ 未实现

**备注**：此事件可能归属客户端或编排层，Gateway 不一定是 producer。

## 3. Envelope 和传输层缺失

根据 `02-CROSS-REPO-EVENT-CONTRACT.md` §2，Gateway 必须实现：

### 3.1 Durable Outbox

**要求**：
- 与业务事务同一事务提交
- `event_id` 固定（重试不变）
- `aggregate_version` 单调递增
- 原始 JSON bytes 用于签名

**Gateway 当前状态**：❌ 未实现

**缺失组件**：
- `outbox_events` 表
- `OutboxWriter` 服务
- `OutboxDispatcher` 后台进程

### 3.2 签名和传输

**要求**：
- body-bound HMAC (sha256)
- Tenant header 与 envelope 一致性校验
- Retry-After 支持
- DLQ 和告警

**Gateway 当前状态**：❌ 未实现

**缺失组件**：
- HMAC 签名器
- ASM HTTP client
- 重试策略
- DLQ 表

## 4. 实施优先级

### Phase 1: 测试框架（本 WP2）

1. ✅ 创建字段映射表（本文档）
2. ⬜ 创建 v1 事件 fixture（正例 + 5 类负例）
3. ⬜ 创建 Gateway mock producer 测试
4. ⬜ 创建 ASM mock consumer 测试（基于文档）
5. ⬜ 验证 5 类负例稳定复现

**交付标准**：所有测试可运行且失败（因实现缺失），为后续实现提供可验收标准。

### Phase 2: 最小 Outbox 实现

仅在 Phase 1 测试全部就绪后开始。

1. 创建 `outbox_events` 表
2. 实现 `OutboxWriter`（与 request 事务同提交）
3. 实现 `OutboxDispatcher`（后台轮询 + 重试）
4. 补全 `request.completed.v1` 的 9 个缺失字段
5. 删除 `user_content` 违规字段

**交付标准**：Phase 1 的 `request.completed.v1` 正例测试通过。

### Phase 3: 完整 v1 契约

1. 实现 `session.opened.v1`
2. 实现 `session.deleted.v1`
3. 签名和 tenant 校验
4. DLQ 和监控

**交付标准**：5 类负例测试全部通过（duplicate/stale/tamper/mismatch/unknown_field）。

## 5. 禁止项

在测试框架和 Phase 2 outbox 实现完成前：

- ❌ 不添加 routing/compression metadata 到 `request.completed.v1`
- ❌ 不修改 SessionCacheV2、RecoveryCoordinator、compressor
- ❌ 不启用 ASM handshake `event_mode: "enabled"`
- ❌ 不在生产环境投递事件（仅 shadow 或测试目标）

## 6. 下一步行动

1. 创建 `test/events/fixtures/request_completed_v1.json`（正例）
2. 创建 5 个负例 fixture（duplicate/stale/tamper/mismatch/unknown_field）
3. 创建 `test/events/contract/request_completed_test.go`
4. 运行测试，验证全部失败（预期）
5. 提交：`test(events): add gateway-asm v1 contract fixtures for request.completed`
