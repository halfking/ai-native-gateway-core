# Session State Management - 部署指南

## 概览

Session State Management 是 llm-gateway-go 的会话状态追踪系统，提供：
- 会话生命周期管理（active/stopped/recovered/expired）
- 凭据轮换审计日志
- 实时统计（tokens, cost, turns）
- 批量异步持久化
- 自动清理机制

## 架构

```
┌─────────────────────────────────────────────────────────┐
│                    HTTP Request                          │
└─────────────────────┬───────────────────────────────────┘
                      │
                      ▼
┌─────────────────────────────────────────────────────────┐
│              ChatHandler (ServeHTTP)                     │
│  ┌────────────────────────────────────────────────────┐ │
│  │  1. 提取 session_id                                 │ │
│  │  2. 路由选择凭据（credential hooks）                │ │
│  │  3. 调用 RotationHook.OnRequestComplete()         │ │
│  │  4. 调用 RotationHook.OnUsageUpdate()             │ │
│  └────────────────────────────────────────────────────┘ │
└─────────────────────┬───────────────────────────────────┘
                      │
                      ▼
┌─────────────────────────────────────────────────────────┐
│                 RotationHook                             │
│  ┌────────────────────────────────────────────────────┐ │
│  │  • 检测凭据切换（old != new）                       │ │
│  │  • EndCredRotation(old) + StartCredRotation(new)  │ │
│  │  • 更新 session_pref                                │ │
│  │  • TouchUsage(tokens, cost)                         │ │
│  └────────────────────────────────────────────────────┘ │
└─────────────────────┬───────────────────────────────────┘
                      │
                      ▼
┌─────────────────────────────────────────────────────────┐
│                   Manager                                │
│  ┌────────────────────────────────────────────────────┐ │
│  │  Redis Hash: session:{id}                          │ │
│  │    - status, tenant_id, api_key_id                 │ │
│  │    - total_turns, tokens, cost                      │ │
│  │    - current_credential_id, model, provider        │ │
│  │    - title, annotation, tags                        │ │
│  │  Redis List: session:{id}:rotations (最多 100)     │ │
│  │    - JSON: {cred_id, model, provider, ...}         │ │
│  │  Redis Set: session:stopped                         │ │
│  └────────────────────────────────────────────────────┘ │
└─────────────────────┬───────────────────────────────────┘
                      │
                      ▼
┌─────────────────────────────────────────────────────────┐
│                  DBWriter (异步)                         │
│  ┌────────────────────────────────────────────────────┐ │
│  │  • 批量大小：10 条                                   │ │
│  │  • Flush 间隔：60 秒                                │ │
│  │  • 持久化到 PostgreSQL:                             │ │
│  │    - session_state_snapshots                        │ │
│  │    - session_credential_rotations                   │ │
│  └────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────┘
                      │
                      ▼
┌─────────────────────────────────────────────────────────┐
│              CleanupWorker (定期)                        │
│  ┌────────────────────────────────────────────────────┐ │
│  │  • 扫描间隔：5 分钟                                  │ │
│  │  • 停止 TTL：30 分钟                                │ │
│  │  • 清理 stopped sessions（Redis + stopped set）    │ │
│  └────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────┘
```

## 部署步骤

### 1. 数据库迁移

运行 migration 331：

```bash
# 在 PostgreSQL Citus 上执行
psql -h <host> -U <user> -d <database> -f sql/migrations/domain/331_session_state_and_rotations.sql
```

**表结构**：
- `session_state_snapshots`：会话停止时的快照（14 字段）
- `session_credential_rotations`：凭据轮换审计日志（17 字段）

### 2. 依赖注入（main.go）

在 `main.go` 的 line 1699 后添加：

```go
// 2026-07-06: Session State Management (Phase 1-5)
// 初始化会话状态管理组件（Manager, DBWriter, CleanupWorker, RotationHook）
if fpSlotRedis != nil && adminHandler != nil {
    sessionComponents, err := InitializeSessionState(
        context.Background(),
        dbConn.Pool(),
        fpSlotRedis,
        adminHandler,
    )
    if err != nil {
        slog.Error("session state init failed", "error", err)
    } else if sessionComponents != nil {
        slog.Info("session state management initialized")
        // 注册到全局 shutdown（如果有的话）
        // 或者在 main 函数末尾 defer sessionComponents.Shutdown()
    }
}
```

**已提供**：`cmd/gateway/session_state_init.go`

### 3. 集成 RotationHook

在请求处理流程中添加：

```go
// 请求完成后（在写响应前）
if sessionComponents != nil && sessionComponents.RotationHook != nil {
    rotCtx := session.ExtractRotationContextFromMetadata(env.Metadata)
    if rotCtx != nil {
        _ = sessionComponents.RotationHook.OnRequestComplete(ctx, rotCtx)
    }
    
    // 更新使用统计
    usage := &session.UsageUpdate{
        PromptTokens:     promptTokens,
        CompletionTokens: completionTokens,
        Model:            model,
        Provider:         provider,
        CredentialID:     credentialID,
        CostUSD:          costUSD,
    }
    _ = sessionComponents.RotationHook.OnUsageUpdate(ctx, sessionID, usage)
}
```

### 4. 前端路由

已在 `web/src/router.ts` 添加：

```typescript
{ path: '/sessions', component: SessionManagementView, meta: { requiresSuper: true } }
```

访问：`https://llmgo.kxpms.cn/sessions`（super_admin only）

## API 端点

### 1. 列出会话

```bash
GET /api/admin/sessions?status=active&tenant_id=xxx&limit=50&offset=0
```

**响应**：
```json
{
  "sessions": [
    {
      "session_id": "sess-001",
      "tenant_id": "tenant-test",
      "api_key_id": 100,
      "status": "active",
      "created_at": "2026-07-06T10:00:00Z",
      "last_active": "2026-07-06T10:05:00Z",
      "total_turns": 5,
      "total_prompt_tokens": 500,
      "total_completion_tokens": 250,
      "total_cost_usd_cents": 50,
      "current_credential_id": 1001,
      "current_model": "gpt-4",
      "current_provider": "openai"
    }
  ],
  "total": 1
}
```

### 2. 会话详情

```bash
GET /api/admin/sessions/{session_id}
```

**响应**：包含完整字段 + 轮换历史

### 3. 凭据轮换历史

```bash
GET /api/admin/sessions/{session_id}/cred-rotations?limit=100
```

**响应**：
```json
{
  "rotations": [
    {
      "credential_id": 1002,
      "model": "gpt-4",
      "provider": "openai",
      "started_at": "2026-07-06T10:05:00Z",
      "ended_at": null,
      "turns": 3,
      "prompt_tokens": 300,
      "completion_tokens": 150,
      "cost_usd_cents": 30,
      "switch_reason": "rotate",
      "fp_slot_index": 5
    },
    {
      "credential_id": 1001,
      "model": "gpt-4",
      "provider": "openai",
      "started_at": "2026-07-06T10:00:00Z",
      "ended_at": "2026-07-06T10:05:00Z",
      "turns": 2,
      "prompt_tokens": 200,
      "completion_tokens": 100,
      "cost_usd_cents": 20,
      "switch_reason": "initial"
    }
  ]
}
```

### 4. 停止会话

```bash
POST /api/admin/sessions/{session_id}/stop?reason=admin_stop
```

**权限**：super_admin only

### 5. 恢复会话

```bash
POST /api/admin/sessions/{session_id}/recover
```

**权限**：super_admin only

### 6. 更新标注

```bash
PUT /api/admin/sessions/{session_id}/annotation
Content-Type: application/json

{
  "annotation": "This is a test session"
}
```

### 7. 更新标签

```bash
PUT /api/admin/sessions/{session_id}/tags
Content-Type: application/json

{
  "tags": ["test", "integration", "v2"]
}
```

## 配置

### Redis 配置

使用已有的 `fpSlotRedis` 连接（共享连接池）。

**Key 前缀**：
- `session:{session_id}` - Hash（会话数据）
- `session:{session_id}:rotations` - List（轮换历史）
- `session:stopped` - Set（停止的会话集合）
- `session_pref:{session_id}` - String（会话偏好，JSON）

### PostgreSQL 配置

使用已有的 `dbConn.Pool()`。

**表名**：
- `session_state_snapshots`
- `session_credential_rotations`

### 参数调整

在 `cmd/gateway/session_state_init.go` 中：

```go
// DBWriter
batchSize := 10              // 批量大小（建议 5-20）
flushInterval := 60 * time.Second  // Flush 间隔（建议 30-120s）

// CleanupWorker
stoppedTTL := 30 * time.Minute     // 停止 TTL（建议 30-60min）
scanInterval := 5 * time.Minute    // 扫描间隔（建议 5-10min）
```

## 监控

### 日志关键字

- `session manager created`
- `session db writer started`
- `session cleanup worker started`
- `session_rotation_hook: detected credential rotation`
- `session_db_writer: batch flush`
- `session_cleanup_worker: deleted stopped session`

### 指标

- Redis Keys 数量：`DBSIZE`
- Redis List 长度：`LLEN session:{id}:rotations`
- PostgreSQL 行数：
  ```sql
  SELECT COUNT(*) FROM session_state_snapshots;
  SELECT COUNT(*) FROM session_credential_rotations;
  ```

### 告警

- DBWriter 队列积压（> 100 条）
- CleanupWorker 扫描失败
- Redis 连接失败

## 故障排查

### 1. 会话统计不更新

**检查**：
- RotationHook 是否初始化
- OnUsageUpdate 是否被调用
- Redis Lua 脚本是否执行成功

**日志**：
```
session_rotation_hook: touch usage failed
```

### 2. 轮换历史为空

**检查**：
- StartCredRotation 是否被调用
- Redis List 是否写入成功
- OnRequestComplete 是否检测到凭据切换

**日志**：
```
session_rotation_hook: start new rotation failed
```

### 3. 数据库未持久化

**检查**：
- DBWriter 是否启动
- 批量大小和 Flush 间隔
- PostgreSQL 连接是否正常

**日志**：
```
session_db_writer: insert failed
session_db_writer: commit failed
```

### 4. 停止的会话未清理

**检查**：
- CleanupWorker 是否启动
- 停止 TTL 是否过期
- Redis Set 是否正确维护

**日志**：
```
session_cleanup_worker: deleted stopped session
```

## 性能优化

### 1. Redis

- 使用 pipeline 批量读取
- 轮换历史限制 100 条（LTRIM）
- 定期清理过期 session keys

### 2. PostgreSQL

- 批量插入（10 条 / 60s）
- 异步持久化（非阻塞）
- 索引优化（session_id, tenant_id）

### 3. 内存

- DBWriter 队列上限（防止积压）
- CleanupWorker 批量删除（每次 100 个）
- 轮换历史截断（超过 100 条）

## 安全

### 权限控制

- 列出会话：tenant 隔离（非 super_admin 只能看自己的）
- 停止/恢复会话：super_admin only
- 更新标注/标签：owner or super_admin

### 数据隔离

- 租户隔离（tenant_id 过滤）
- API Key 隔离（只能操作自己的会话）

### 审计

- 所有停止/恢复操作记录到 audit log
- 轮换历史完整追踪

## 迁移说明

### 从旧版本升级

1. 运行 migration 331
2. 重启 gateway（自动初始化新组件）
3. 验证日志（看到 "session state management initialized"）
4. 访问前端页面（/sessions）

### 回滚

```sql
-- 执行 down migration
\i sql/migrations/domain/331_session_state_and_rotations.down.sql

-- 重启 gateway（跳过初始化）
```

## 测试

### 单元测试

```bash
go test ./domains/session/session_state_test.go -v
```

### 集成测试（需要 Redis）

```bash
# 启动 Redis
docker run -d -p 6379:6379 redis:7

# 运行测试
go test ./domains/session/ -v -tags=integration
```

### 压力测试

```bash
# 模拟 1000 会话
for i in {1..1000}; do
  curl -X POST http://localhost:8080/api/admin/sessions \
    -H "Authorization: Bearer $TOKEN" \
    -d "{\"session_id\":\"sess-$i\",\"tenant_id\":\"tenant-test\"}"
done

# 验证持久化
psql -c "SELECT COUNT(*) FROM session_state_snapshots;"
```

## 常见问题

### Q: 会话数据会丢失吗？

A: 不会。Redis 是主数据源，DBWriter 异步持久化到 PostgreSQL。即使 DB 短暂不可用，数据仍在 Redis 中。

### Q: 轮换历史最多保留多少条？

A: Redis 中保留 100 条（LTRIM），PostgreSQL 中永久保留。

### Q: 停止的会话何时被清理？

A: 停止 30 分钟后，CleanupWorker 自动清理（可配置）。

### Q: 如何查看某个租户的所有会话？

A: `GET /api/admin/sessions?tenant_id=xxx&status=active`

### Q: 如何手动触发持久化？

A: DBWriter 会在 60 秒或积累 10 条时自动 flush，无需手动触发。

## 相关文档

- [Session State Management 设计文档](./session-state-management.md)
- [API 文档](../api/admin-sessions.md)
- [数据库 Schema](../sql/migrations/domain/331_session_state_and_rotations.sql)
- [前端页面源码](../web/src/views/SessionManagementView.vue)

## 贡献者

- @xutaohuang - 初始实现
- ACC Team - 架构设计与 Review

## 更新日志

- 2026-07-06: Phase 1-7 完整实施
  - 数据库表 + Redis 扩展
  - Manager + Workers
  - API 端点
  - 前端页面
  - 单元测试
