# Phase 2: 请求刷新延迟修复

**问题**: 前端请求到达网关后，延迟 5 秒才能在实时请求流中看到  
**目标**: 将延迟降至 <1s  
**优先级**: 中期优化

---

## 问题分析

### 当前流程

```
请求到达网关
  ↓
处理请求（调用上游）
  ↓
请求完成（获得响应）
  ↓
telemetry.EmitRequestLog() ← 触发点
  ↓
批处理器收集（200ms 窗口 OR 50 条）
  ↓
批量 INSERT 到数据库
  ↓
触发 NOTIFY 到 LiveStreamSSEHub
  ↓
推送 SSE 到前端
  ↓
前端显示（总延迟 ~5s）
```

### 延迟来源

| 环节 | 延迟 | 说明 |
|------|------|------|
| **请求处理** | 2-4s | 等待上游响应（无法优化） |
| **批处理窗口** | 0-200ms | 等待批量 flush |
| **数据库写入** | 50-200ms | INSERT + NOTIFY |
| **SSE 推送** | 10-50ms | 网络传输 |
| **总延迟** | **5s** | 大部分来自请求处理 |

**关键发现**: 延迟主要来自"等待请求完成"，而不是批处理或数据库。

---

## 解决方案：双写策略

### 核心思路

```
请求到达网关（t=0）
  ↓
立即 INSERT（in_progress 状态） ← 新增
  ↓
推送到前端（显示"处理中"） ← 延迟 <100ms
  ↓
处理请求（调用上游）
  ↓
请求完成（t=2-4s）
  ↓
UPDATE 完整数据 ← 修改
  ↓
推送更新到前端（显示"完成"）
```

**效果**: 前端在请求到达后 **<100ms** 就能看到（显示"处理中"），而不是等 5s。

---

## 实现方案

### 1. 数据库层修改

**新增状态字段**（如果不存在）:
```sql
-- 检查 status 字段是否存在
SELECT column_name 
FROM information_schema.columns 
WHERE table_name = 'request_logs_hot' 
  AND column_name = 'status';

-- 如果不存在，添加字段
ALTER TABLE request_logs_hot 
ADD COLUMN IF NOT EXISTS status VARCHAR(20) DEFAULT 'completed';

-- 创建索引（加速 UPDATE 查询）
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_status 
ON request_logs_hot(status) 
WHERE status = 'in_progress';
```

**状态枚举**:
- `in_progress` - 请求处理中
- `completed` - 请求成功完成
- `failed` - 请求失败
- `rate_limited` - 被限流

### 2. telemetry 层修改

**文件**: `internal/telemetry/client.go`

**新增方法**:
```go
// EmitRequestStart 在请求开始时立即记录（in_progress 状态）
// 2026-07-19: Phase 2 双写策略 - 减少前端刷新延迟
func (c *Client) EmitRequestStart(ctx context.Context, req *RequestLogStart) error {
    if c == nil || c.db == nil {
        return nil
    }
    
    // 立即写入数据库（最小字段集）
    _, err := c.db.ExecContext(ctx, `
        INSERT INTO request_logs_hot (
            request_id, ts, vendor, provider_id, model, 
            status, task_type, tenant_id
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
        ON CONFLICT (request_id) DO NOTHING
    `, 
        req.RequestID,
        req.Timestamp,
        req.Vendor,
        req.ProviderID,
        req.Model,
        "in_progress",
        req.TaskType,
        req.TenantID,
    )
    
    if err != nil {
        slog.Debug("emit request start failed", "request_id", req.RequestID, "err", err)
        return err
    }
    
    // 触发 onEmitted hook（立即推送到 SSE）
    c.mu.RLock()
    hook := c.onEmitted
    c.mu.RUnlock()
    
    if hook != nil {
        hook(RequestLogEntry{
            RequestID:  req.RequestID,
            Timestamp:  req.Timestamp,
            Vendor:     req.Vendor,
            ProviderID: req.ProviderID,
            Model:      req.Model,
            Status:     "in_progress",
            TaskType:   req.TaskType,
            TenantID:   req.TenantID,
        })
    }
    
    return nil
}

// RequestLogStart 请求开始时的最小字段集
type RequestLogStart struct {
    RequestID  string
    Timestamp  time.Time
    Vendor     string
    ProviderID int
    Model      string
    TaskType   string
    TenantID   string
}
```

**修改现有方法**:
```go
// EmitRequestLog 改为 UPDATE 而非 INSERT
func (c *Client) EmitRequestLog(ctx context.Context, entry RequestLogEntry) error {
    if c == nil || c.db == nil {
        return nil
    }
    
    // 2026-07-19: Phase 2 双写策略 - UPDATE 完整数据
    _, err := c.db.ExecContext(ctx, `
        UPDATE request_logs_hot SET
            response_ts = $2,
            status_code = $3,
            prompt_tokens = $4,
            completion_tokens = $5,
            total_tokens = $6,
            cost_usd = $7,
            latency_ms = $8,
            request_body = $9,
            response_body = $10,
            error_kind = $11,
            failure_stage = $12,
            status = $13,
            updated_at = NOW()
        WHERE request_id = $1
    `, 
        entry.RequestID,
        entry.ResponseTs,
        entry.StatusCode,
        entry.PromptTokens,
        entry.CompletionTokens,
        entry.TotalTokens,
        entry.CostUSD,
        entry.LatencyMs,
        entry.RequestBody,
        entry.ResponseBody,
        entry.ErrorKind,
        entry.FailureStage,
        entry.Status, // "completed" or "failed"
    )
    
    if err != nil {
        slog.Debug("emit request log update failed", "request_id", entry.RequestID, "err", err)
        return err
    }
    
    // 触发 onEmitted hook
    c.mu.RLock()
    hook := c.onEmitted
    c.mu.RUnlock()
    
    if hook != nil {
        hook(entry)
    }
    
    return nil
}
```

### 3. 网关层修改

**文件**: `cmd/gateway/main_pipeline.go` 或 `domains/streaming/handler.go`

**在请求开始时调用**:
```go
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    requestID := middleware.GetRequestID(r.Context())
    
    // 2026-07-19: Phase 2 - 请求开始时立即记录
    if h.telemetry != nil {
        _ = h.telemetry.EmitRequestStart(r.Context(), &telemetry.RequestLogStart{
            RequestID:  requestID,
            Timestamp:  time.Now(),
            Vendor:     extractVendor(r),
            ProviderID: extractProviderID(r),
            Model:      extractModel(r),
            TaskType:   "chat_completion",
            TenantID:   extractTenantID(r),
        })
    }
    
    // ... 现有的请求处理逻辑
}
```

### 4. 前端层修改（可选）

**文件**: 前端 SSE 消息处理

**显示处理中状态**:
```typescript
function handleNewRequest(req: LiveStreamTile) {
    if (req.status === 'in_progress') {
        // 显示"处理中"状态
        req.displayStatus = '⏳ 处理中';
        req.statusColor = 'yellow';
    } else if (req.status === 'completed') {
        // 显示"完成"状态
        req.displayStatus = req.status_code === 200 ? '✅ 成功' : '❌ 失败';
        req.statusColor = req.status_code === 200 ? 'green' : 'red';
    }
    
    addToLane(req);
}
```

---

## 实施步骤

### Step 1: 数据库迁移（5 分钟）

```bash
# 创建迁移脚本
psql -h 115.29.212.252 -p 15432 -U llm_gateway -d llm_gateway << 'SQL'
-- 添加 status 字段
ALTER TABLE request_logs_hot 
ADD COLUMN IF NOT EXISTS status VARCHAR(20) DEFAULT 'completed';

-- 创建索引
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_status 
ON request_logs_hot(status) 
WHERE status = 'in_progress';

-- 验证
\d request_logs_hot
SQL
```

### Step 2: 后端代码修改（30 分钟）

1. 修改 `internal/telemetry/client.go`
2. 在网关入口添加 `EmitRequestStart` 调用
3. 编写单元测试

### Step 3: 编译测试（5 分钟）

```bash
go test ./internal/telemetry/...
go build ./cmd/gateway
```

### Step 4: 部署到 154（10 分钟）

```bash
bash scripts/deploy-154.sh
```

### Step 5: 验证（10 分钟）

```sql
-- 查询 in_progress 状态的请求
SELECT request_id, status, ts, latency_ms
FROM request_logs_hot
WHERE status = 'in_progress'
  AND ts > NOW() - INTERVAL '1 minute';

-- 如果有长时间停留在 in_progress 的请求，说明 UPDATE 没生效
SELECT request_id, status, ts, NOW() - ts as age
FROM request_logs_hot
WHERE status = 'in_progress'
  AND ts < NOW() - INTERVAL '10 seconds';
```

---

## 预期效果

| 指标 | 修复前 | 修复后 |
|------|--------|--------|
| 前端首次可见 | ~5s | **<100ms** |
| 完整数据可见 | ~5s | ~5s（不变） |
| 数据库写入次数 | 1 次 INSERT | 1 INSERT + 1 UPDATE |
| 额外 DB 负载 | 0 | **+5%**（可接受） |

---

## 风险和权衡

### 风险

1. **数据库负载增加**
   - 写入次数翻倍（INSERT + UPDATE）
   - 影响：约 +5% 负载（可接受）

2. **可能的 UPDATE 丢失**
   - 如果 INSERT 失败，UPDATE 会找不到记录
   - 缓解：INSERT 时 ON CONFLICT DO NOTHING

3. **in_progress 记录堆积**
   - 如果服务崩溃，会留下 in_progress 记录
   - 缓解：定期清理 + 前端超时处理

### 权衡

| 方面 | 成本 | 收益 |
|------|------|------|
| 数据库负载 | +5% | 延迟 5s → <100ms |
| 代码复杂度 | +50 行 | 用户体验显著提升 |
| 维护成本 | 低 | 功能独立，易回滚 |

---

## 回滚方案

如果出现问题，可以：

1. **临时禁用 EmitRequestStart**:
   ```go
   // 注释掉网关层的调用
   // _ = h.telemetry.EmitRequestStart(...)
   ```

2. **或者回滚到上一个版本**:
   ```bash
   git revert <commit-hash>
   bash scripts/deploy-154.sh
   ```

3. **数据库回滚**（如果需要）:
   ```sql
   ALTER TABLE request_logs_hot DROP COLUMN IF EXISTS status;
   DROP INDEX IF EXISTS idx_request_logs_hot_status;
   ```

---

## 监控指标

部署后监控：

1. **in_progress 记录数量**
   ```sql
   SELECT COUNT(*) FROM request_logs_hot WHERE status = 'in_progress';
   -- 应该保持在个位数
   ```

2. **长时间停留的 in_progress**
   ```sql
   SELECT COUNT(*) FROM request_logs_hot 
   WHERE status = 'in_progress' AND ts < NOW() - INTERVAL '1 minute';
   -- 应该为 0（或极少数）
   ```

3. **前端延迟**
   - 用户反馈
   - 前端 Console 监控（请求时间戳 vs 显示时间戳）

---

## 后续优化（可选）

1. **定期清理 in_progress**
   ```go
   // 每 5 分钟清理一次
   go func() {
       ticker := time.NewTicker(5 * time.Minute)
       for range ticker.C {
           db.Exec(`
               UPDATE request_logs_hot 
               SET status = 'timeout' 
               WHERE status = 'in_progress' 
                 AND ts < NOW() - INTERVAL '5 minutes'
           `)
       }
   }()
   ```

2. **前端超时处理**
   ```typescript
   // 10 秒后自动标记为超时
   setTimeout(() => {
       if (req.status === 'in_progress') {
           req.status = 'timeout';
           req.displayStatus = '⏱️ 超时';
       }
   }, 10000);
   ```

---

**结论**: Phase 2 可以将前端延迟从 5s 降至 <100ms，成本是 +5% 数据库负载。
建议先在测试环境验证，确认无问题后再部署生产。
