---
archived_from: (legacy) docs/archive/2026-07/CHANGELOG_trace_ui_fix.md
archived_at: 2026-08-17
archived_by: docs-archive remediate v1.0
backup_ts: 20260817-190918
status: archived
note: legacy archive, frontmatter retroactively added
---

# 修复请求链路追踪 UI 文案误导问题

## 问题描述

前端请求详情页面中，关于请求链路的空状态提示存在误导性说法：

**错误说法（原文）**：
> 该请求未触发 trace, 或 trace 数据已超出 Redis 10 分钟保留期。

**问题分析**：
这个说法是**不正确的**，因为：

1. **trace 数据有两层存储**：
   - **Redis**：请求进行中暂存（`request:trace:{request_id}`，TTL 10分钟）
   - **PostgreSQL**：请求结束时持久化（`request_logs.trace_events` JSONB 列）

2. **后端已实现完整的读取逻辑**（`admin/request_trace.go` 第 120-145 行）：
   ```go
   func (h *RequestTraceHandler) loadTrace(ctx context.Context, requestID string) (*gwtrace.RequestTrace, string, error) {
       // 1. 优先从 Redis 读取（进行中或刚完成的请求）
       if h.rdb != nil {
           rec := gwtrace.NewRedisRecorder(h.rdb)
           if trace, _, err := rec.Load(ctx, requestID); err == nil && trace != nil {
               return trace, "redis", nil
           }
       }
       // 2. Redis miss 时从 PostgreSQL 读取（已完成的请求）
       if h.db != nil {
           trace, err := gwtrace.LoadFromPG(ctx, h.db, requestID)
           if err != nil {
               return nil, "", err
           }
           if trace != nil {
               return trace, "postgres", nil
           }
       }
       return nil, "", nil
   }
   ```

3. **请求结束时自动 flush**（`internal/trace/trace.go` 第 405-446 行）：
   ```go
   func (r *RedisRecorder) FlushToPG(ctx context.Context, db *pgxpool.Pool, requestID string) error {
       // 从 Redis 读取
       raw, err := r.rdb.Get(runCtx, key).Result()
       // ...
       // 写入 PostgreSQL
       _, err = db.Exec(runCtx, `
           UPDATE request_logs
           SET trace_events = $1::jsonb
           WHERE request_id = $2
       `, raw, requestID)
       // ...
       // 成功后删除 Redis key
       r.rdb.Del(runCtx, key)
   }
   ```

4. **前端已实现调用**（`web/src/api/trace.ts`）：
   - 调用 `GET /api/admin/requests/{request_id}/trace`
   - 后端自动处理 Redis → PostgreSQL 的 fallback

## 修改内容

### 1. 修正中文文案（`web/src/locales/zh-CN/trace.ts`）

**修改前**：
```typescript
empty: {
  title: '暂无链路事件',
  desc: '该请求未触发 trace, 或 trace 数据已超出 Redis 10 分钟保留期。',
  hint1: 'trace 仅在网关代码埋点启用时才会生成(默认开启)',
  hint2: '完成时间 < 10 分钟的请求, Redis 中仍保留 trace',
  hint3: '已完成的请求, trace_events JSONB 已 flush 到 PostgreSQL 后才可显示',
},
```

**修改后**：
```typescript
empty: {
  title: '暂无链路事件',
  desc: '该请求的链路追踪数据不可用。',
  hint1: 'trace 仅在网关代码埋点启用时才会生成(默认开启)',
  hint2: '进行中的请求：trace 数据暂存在 Redis，可实时查看',
  hint3: '已完成的请求：trace 数据已持久化到 request_logs.trace_events (JSONB)',
  hint4: '如果此处显示为空，可能原因：请求未完成 flush、或该请求确实未触发 trace 埋点',
},
```

### 2. 修正英文文案（`web/src/locales/en-US/trace.ts`）

**修改前**：
```typescript
empty: {
  title: 'No trace events',
  desc: 'Trace was not generated, or has expired (Redis retains 10 min).',
  hint1: 'Trace is emitted only when instrumentation is enabled (default: on)',
  hint2: 'Completed within 10 min: still in Redis',
  hint3: 'Already flushed to PostgreSQL: shown via fallback',
},
```

**修改后**：
```typescript
empty: {
  title: 'No trace events',
  desc: 'Trace data is not available for this request.',
  hint1: 'Trace is emitted only when instrumentation is enabled (default: on)',
  hint2: 'In-progress requests: trace data is in Redis and viewable in real-time',
  hint3: 'Completed requests: trace data is persisted to request_logs.trace_events (JSONB)',
  hint4: 'If empty here, possible causes: flush not completed, or trace was not emitted',
},
```

## 关键改进点

1. **移除误导性说法**：
   - ❌ 删除："trace 数据已超出 Redis 10 分钟保留期"
   - ✅ 说明：数据已持久化到 PostgreSQL

2. **清晰说明数据流转**：
   - 进行中的请求 → Redis（实时）
   - 已完成的请求 → PostgreSQL（持久化）

3. **准确描述空状态原因**：
   - 请求未完成 flush（正常情况）
   - 请求确实未触发 trace 埋点（极少见）

4. **新增 hint4**：
   - 提供更多可能的原因说明
   - 避免用户误以为数据永久丢失

## 验证方法

1. **查看进行中的请求**：
   - 请求发起后立即查看链路 → 应该能看到实时 trace（来源：Redis）

2. **查看已完成的请求**：
   - 请求完成 1 小时后查看链路 → 应该能看到历史 trace（来源：PostgreSQL）

3. **空状态提示更新**：
   - 对于确实没有 trace 的请求 → 显示新的准确提示文案

## 其他语言版本

以下语言文件也需要同步更新（待处理）：
- [ ] `web/src/locales/ja-JP/trace.ts`
- [ ] `web/src/locales/zh-TW/trace.ts`
- [ ] `web/src/locales/fr-FR/trace.ts`
- [ ] `web/src/locales/de-DE/trace.ts`
- [ ] `web/src/locales/es-ES/trace.ts`
- [ ] `web/src/locales/ar-SA/trace.ts`

## 相关文件

- 后端 trace 系统：`internal/trace/trace.go`
- 后端 API 处理：`admin/request_trace.go`
- 前端 API 客户端：`web/src/api/trace.ts`
- 前端 UI 组件：`web/src/components/RequestTracePanel.vue`
- 设计文档：`docs/design/request-trace-system.md`

## 结论

通过修正前端文案，现在用户可以清楚地了解：
- trace 数据不会在 10 分钟后丢失
- 已完成的请求可以通过 PostgreSQL 查看历史链路
- 系统已经实现了完整的 Redis → PostgreSQL 持久化机制

---
修改日期：2026-07-18
修改人：AI Agent (Claude)
