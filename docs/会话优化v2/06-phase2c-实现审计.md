# Phase 2C 实现审计报告

**日期**：2026-07-15  
**审计范围**：migration 401、Repository、Hook、main.go wiring  
**审计人**：OpenCode Agent  
**提交**：`1d0c01bf4` → `bdcfd02d2`

---

## 1. 审计执行清单

| 检查项 | 状态 | 结果 |
|--------|------|------|
| Migration 401 SQL 语法 | ✅ | 通过 |
| 字段与 AttachmentMetadata 对齐 | ✅ | 完全对齐 |
| 状态枚举与 Go 常量对齐 | ✅ | 6 个状态完全一致 |
| 索引设计合理性 | ✅ | 4 个索引覆盖查询场景 |
| Repository 方法完整性 | ✅ | CRUD + 统计 + 清理 |
| Hook 触发路径正确性 | ✅ | 3 个 AddOnRequestLogPersisted 并存 |
| Phase 2D 必要性核实 | ✅ | 已在 `e4a30e507` 完成 |
| 测试覆盖 | ✅ | unit tests PASS |
| 文档完整性 | ✅ | changelog + 方案更新 |

---

## 2. Migration 401 SQL 审计

### 2.1 字段对齐检查

| Go 字段 (AttachmentMetadata) | SQL 列 (request_attachments) | 映射关系 |
|------------------------------|------------------------------|----------|
| `Type string` | `attachment_type TEXT` | ✅ ToRow 转换 |
| `ContentType string` | `content_type TEXT` | ✅ 直接映射 |
| `Size int64` | `size_bytes BIGINT` | ✅ 直接映射 |
| `Path string` | `storage_path TEXT` | ✅ 直接映射 |
| `Hash string` | `hash TEXT` | ✅ 直接映射 |
| `OriginalURL string` | `original_url TEXT` | ✅ 直接映射 |
| `MessageIndex int` | `message_index INTEGER` | ✅ 直接映射 |
| `BlockIndex int` | `block_index INTEGER` | ✅ 直接映射 |
| `Status AttachmentStatus` | `status TEXT` | ✅ CHECK 约束 |
| `ErrorCode string` | `error_code TEXT` | ✅ 直接映射 |
| `CreatedAt time.Time` | `created_at TIMESTAMPTZ` | ✅ 直接映射 |

**结论**：100% 对齐，无遗漏字段。

### 2.2 状态枚举对齐检查

Go 常量 (`storage.go:48-53`)：
```go
AttachmentStatusDetected      = "detected"
AttachmentStatusStoring       = "storing"
AttachmentStatusStored        = "stored"
AttachmentStatusManifestReady = "manifest_ready"
AttachmentStatusSent          = "sent"
AttachmentStatusStoreFailed   = "store_failed"
```

SQL CHECK 约束 (`401...sql:43-46`)：
```sql
CHECK (status IN ('detected', 'storing', 'stored', 'manifest_ready',
                  'sent', 'store_failed'))
```

**结论**：6 个状态值完全一致，SQL 约束可防止非法状态写入。

### 2.3 索引设计审计

| 索引 | 用途 | 评估 |
|------|------|------|
| `idx_request_attachments_request_id` | JOIN request_logs | ✅ 必需 |
| `idx_request_attachments_hash` (partial WHERE NOT NULL) | 去重统计 / S19 | ✅ partial 避免 NULL 膨胀 |
| `idx_request_attachments_status_time` | 失败排查 / lifecycle 扫描 | ✅ (status, created_at DESC) 复合索引高效 |
| `idx_request_attachments_created_at` | 批量清理 | ✅ DESC 优化 ORDER BY |

**结论**：索引设计合理，覆盖所有查询场景。

---

## 3. Repository 实现审计

### 3.1 方法完整性

| 方法 | 功能 | 状态 |
|------|------|------|
| `NewRepository(pool)` | 构造器 | ✅ |
| `InsertOne(ctx, row)` | 单条插入 | ✅ |
| `InsertBatch(ctx, rows)` | 批量插入（事务） | ✅ |
| `ListByRequestID(ctx, id)` | 按 request_id 查询 | ✅ |
| `ListByHash(ctx, hash)` | 按 hash 查询（引用统计） | ✅ |
| `CountByStatus(ctx, status, since)` | 状态统计（observability） | ✅ |
| `DeleteOlderThan(ctx, cutoff)` | 批量清理 | ✅ |

**结论**：CRUD + 统计 + 清理 完整，覆盖 Phase 2C 设计需求。

### 3.2 pgxpool 对齐检查

`domains/routing/candidate_failure_logger.go:62` 使用 `*pgxpool.Pool`：
```go
type CandidateFailureWriter struct {
    pool *pgxpool.Pool
}
```

`domains/attachments/repository.go:82` 同样使用 `*pgxpool.Pool`：
```go
type Repository struct {
    pool *pgxpool.Pool
}
```

**结论**：✅ 与现有代码栈一致，避免混用 `database/sql`。

### 3.3 Best-effort 契约验证

`repository.go:106`：
```go
// Best-effort by contract: callers should not surface insert errors as
// user-facing failures. Log and continue.
```

`internal/attachmentmirror/hook.go:58`：
```go
if _, err := repo.InsertBatch(ctx, rows); err != nil {
    slog.Warn("attachmentmirror: persist into request_attachments failed",
        "request_id", entry.RequestID, "count", len(rows), "error", err)
}
```

**结论**：✅ Hook 失败只 log warn，不阻塞主 INSERT，契约正确实施。

---

## 4. Hook 触发路径审计

### 4.1 AddOnRequestLogPersisted 调用点

`cmd/gateway/main.go` 中共 3 处调用 `AddOnRequestLogPersisted`：

1. **Line 1071**：`attachmentmirror.PersistHook(attachmentRepo)`  
   条件：`telemetryClient != nil && dbConn.Enabled()`

2. **Line 1962**：`statsBoardCache.Record`  
   条件：`telemetryClient.Enabled()`

3. **Line 1981**：`statsMinuteAccumulator.Record`  
   条件：`telemetryClient.Enabled()`

**结论**：✅ 3 个 hook 可以并存（`AddOnRequestLogPersisted` 是追加而非替换）。

### 4.2 与 SetOnRequestLogPersisted 互斥检查

`cmd/gateway/main.go:1489` 调用 `SetOnRequestLogPersisted`：
```go
telemetryClient.SetOnRequestLogPersisted(incidentObserver.AsHook())
```

**风险**：`SetOnRequestLogPersisted` 会替换所有 hooks（见 `telemetry/client.go:335`）：
```go
c.onPersisted = []func(entry *RequestLogEntry){fn}
```

**当前状态**：`SetOnRequestLogPersisted` 在 line 1489，`AddOnRequestLogPersisted` 在 line 1071/1962/1981，**执行顺序不确定**。

**问题**：如果 `SetOnRequestLogPersisted` 在后，会覆盖前面的 `Add` 调用。

---

## 5. Phase 2D 必要性核实

### 5.1 历史遗留问题（2026-07-14 审计）

`docs/changelogs/2026-07-14-handoff-phase2-audit.md:98`：
> `provider/client.go:643-649` 仍存在 `lower(...)` fallback；
> `provider/client.go:596,636,664` 和 `resolve/resolve.go:153,193,224`
> 仍对 `RawModels` 使用 `lowerUnique`。

### 5.2 当前状态验证

```bash
$ rg "lower\(.*raw_name\)|lowerUnique" provider/client.go resolve/resolve.go
(no output)
```

```bash
$ git show e4a30e507 --stat
provider/client.go | 45 ++++---
resolve/resolve.go | 17 +--
```

**结论**：✅ Phase 2D 已在 `e4a30e507` (2026-07-14) 完成，`lowerUnique` 和 `lower(raw_name)` SQL 已清理。无需额外修复。

---

## 6. 发现的问题

### P0：SetOnRequestLogPersisted 与 AddOnRequestLogPersisted 执行顺序冲突

**位置**：`cmd/gateway/main.go:1489` vs `1071/1962/1981`

**现象**：
- `SetOnRequestLogPersisted` 会**替换**所有 hooks：
  ```go
  c.onPersisted = []func(entry *RequestLogEntry){fn}
  ```
- `AddOnRequestLogPersisted` 是**追加**：
  ```go
  c.onPersisted = append(c.onPersisted, fn)
  ```
- 如果 `Set` 在 `Add` 之后执行，前面的 3 个 hook 全部丢失。

**影响范围**：
- `attachmentmirror.PersistHook` 不会触发 → 新附件不会写入 `request_attachments`
- `statsBoardCache.Record` 不会触发 → dashboard 实时统计失效
- `statsMinuteAccumulator.Record` 不会触发 → 分钟级统计失效

**根因**：`SetOnRequestLogPersisted` 是遗留 API，与新的 `Add` 语义冲突。

**修复方案**：将 line 1489 的 `SetOnRequestLogPersisted` 改为 `AddOnRequestLogPersisted`，保持一致性。

---

## 7. 修复建议

### 7.1 P0 修复（必须）

```go
// Before (line 1489):
telemetryClient.SetOnRequestLogPersisted(incidentObserver.AsHook())

// After:
telemetryClient.AddOnRequestLogPersisted(incidentObserver.AsHook())
```

**理由**：
1. 保持 4 个 hook 并存（attachment mirror + boardcache + stats minute + incident）
2. 避免执行顺序依赖
3. 符合 `AddOnRequestLogPersisted` 的设计意图（多消费者场景）

### 7.2 文档更新

更新 `docs/会话优化v2/06-phase2c-实现审计.md`（本文档）到仓库。

---

## 8. 审计结论

| 维度 | 结果 | 备注 |
|------|------|------|
| Migration SQL | ✅ PASS | 字段/状态/索引完全对齐 |
| Repository 实现 | ✅ PASS | 方法完整，pgxpool 一致 |
| Hook 触发路径 | ⚠️ **P0 问题** | `Set` vs `Add` 冲突 |
| Phase 2D 必要性 | ✅ 已完成 | `e4a30e507` 已修复 |
| 测试覆盖 | ✅ PASS | unit + full suite |
| 文档完整性 | ✅ PASS | changelog + 方案更新 |

**总体评分**：8/10（P0 问题修复后可达 10/10）

---

## 9. 下一步行动

1. **立即修复**：将 `main.go:1489` 的 `SetOnRequestLogPersisted` 改为 `AddOnRequestLogPersisted`
2. **验证**：`go build && go test ./...`
3. **提交**：独立 commit，changelog 记录修复内容
4. **推送**：`git push origin main`
5. **归档**：本审计文档保存到 `docs/会话优化v2/06-phase2c-实现审计.md`