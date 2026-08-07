# 本地配额追踪设计

> 因大多数免费提供商无 usage API，需本地计量并从 429 响应校准

---

## 🎯 设计目标

1. **本地优先**: 在网关侧追踪配额，无需依赖上游 API
2. **双窗口模型**: 同时追踪短期 burst (5h) 和长期限制 (日/月)
3. **429 校准**: 从 `Retry-After` / `X-RateLimit-*` 响应头动态校准限制
4. **预检过滤**: 请求前检查配额剩余，避免浪费调用
5. **多租户隔离**: 每个租户独立追踪，RLS 保护

---

## 📐 追踪模型

### 窗口类型设计

参考 OmniRoute `freeModelQuotaFetcher.ts` 和 `openrouterFreeWindow.ts`：

```
Window Type       | Duration    | Reset Logic           | Use Case
------------------|-------------|----------------------|---------------------------
hour-5            | 5 hours     | 滚动窗口              | 短期 burst 限制 (如 FreeModel.dev)
day-1             | UTC 日历日   | UTC 00:00 重置        | 最常见免费配额窗口 (OpenRouter, Groq)
day-7             | 7 days      | 滚动窗口              | 周级配额 (如 Codex)
month-1           | UTC 日历月   | 每月 1 日 00:00 重置   | 月度配额 (Mistral, Gemini)
```

### 数据流

```
请求前 (Preflight)
  ↓
fn_quota_preflight_check(credential_id, provider, model)
  ├─ 查询 free_quota_tracker (当前窗口)
  ├─ 计算剩余百分比: (limit - used) / limit
  └─ 返回 TRUE (可用) / FALSE (耗尽)
  ↓
执行请求
  ↓
响应后 (Record)
  ↓
QuotaTracker.Record(tokens, status)
  ├─ INSERT INTO free_quota_tracker
  │   ON CONFLICT (credential_id, provider_code, model_id, window_type, window_start)
  │   DO UPDATE SET
  │     request_count = request_count + 1,
  │     token_count = token_count + $tokens,
  │     success_count = success_count + CASE WHEN $status='ok' THEN 1 ELSE 0 END,
  │     error_count = error_count + CASE WHEN $status='error' THEN 1 ELSE 0 END
  └─ 如果返回 429:
      └─ CorrectFromHeaders(response_headers)
          ├─ 提取 Retry-After: 秒数或 HTTP-date
          ├─ 提取 X-RateLimit-Reset: Unix timestamp
          ├─ 提取 X-RateLimit-Limit: 配额上限
          └─ UPDATE free_quota_tracker SET
                is_exhausted = TRUE,
                exhausted_at = now(),
                auto_reset_at = now() + (retry_after * interval '1 second'),
                corrected_limit = X-RateLimit-Limit
```

---

## 🛠️ Go 实现

### 核心结构

```go
// domains/freeresource/quota_tracker.go

package freeresource

import (
    "context"
    "database/sql"
    "time"
)

// WindowType 定义配额追踪窗口类型
type WindowType string

const (
    WindowTypeHour5  WindowType = "hour-5"   // 5 小时滚动
    WindowTypeDay1   WindowType = "day-1"    // UTC 日历日
    WindowTypeDay7   WindowType = "day-7"    // 7 日滚动
    WindowTypeMonth1 WindowType = "month-1"  // UTC 日历月
)

// QuotaWindow 配额窗口元数据
type QuotaWindow struct {
    Type       WindowType
    Start      time.Time
    End        time.Time
    Limit      int64  // 配额上限 (从文档或 429 校准)
    Used       int64  // 已使用
    Exhausted  bool
    ResetAt    *time.Time
}

// QuotaTracker 配额追踪器
type QuotaTracker struct {
    db *sql.DB
}

// Record 记录一次请求的配额消耗
func (qt *QuotaTracker) Record(ctx context.Context, req RecordRequest) error {
    // 1. 确定窗口起止时间
    windows := qt.computeWindows(req.Timestamp, req.WindowTypes)
    
    // 2. 批量 UPSERT
    for _, w := range windows {
        _, err := qt.db.ExecContext(ctx, `
            INSERT INTO free_quota_tracker (
                credential_id, provider_code, model_id, window_type,
                window_start, window_end, request_count, token_count,
                success_count, error_count, tenant_id
            ) VALUES ($1, $2, $3, $4, $5, $6, 1, $7, $8, $9, $10)
            ON CONFLICT (credential_id, provider_code, model_id, window_type, window_start, tenant_id)
            DO UPDATE SET
                request_count = free_quota_tracker.request_count + 1,
                token_count = free_quota_tracker.token_count + $7,
                success_count = free_quota_tracker.success_count + $8,
                error_count = free_quota_tracker.error_count + $9,
                updated_at = now()
        `, req.CredentialID, req.ProviderCode, req.ModelID, w.Type,
            w.Start, w.End, req.TokenCount,
            boolToInt(req.Success), boolToInt(!req.Success), req.TenantID)
        
        if err != nil {
            return fmt.Errorf("upsert quota tracker: %w", err)
        }
    }
    
    return nil
}

// CorrectFromHeaders 从 429 响应头校准配额限制
func (qt *QuotaTracker) CorrectFromHeaders(ctx context.Context, req CorrectionRequest) error {
    var retryAfterSec int
    var resetAt time.Time
    var limit int64
    
    // 1. 解析 Retry-After
    if ra := req.Headers.Get("Retry-After"); ra != "" {
        if sec, err := strconv.Atoi(ra); err == nil {
            retryAfterSec = sec
            resetAt = time.Now().Add(time.Duration(sec) * time.Second)
        } else if t, err := http.ParseTime(ra); err == nil {
            resetAt = t
            retryAfterSec = int(time.Until(t).Seconds())
        }
    }
    
    // 2. 解析 X-RateLimit-Reset (优先级更高)
    if reset := req.Headers.Get("X-RateLimit-Reset"); reset != "" {
        if ts, err := strconv.ParseInt(reset, 10, 64); err == nil {
            resetAt = time.Unix(ts, 0)
        }
    }
    
    // 3. 解析 X-RateLimit-Limit
    if lim := req.Headers.Get("X-RateLimit-Limit"); lim != "" {
        limit, _ = strconv.ParseInt(lim, 10, 64)
    }
    
    // 4. 更新数据库
    _, err := qt.db.ExecContext(ctx, `
        UPDATE free_quota_tracker
        SET is_exhausted = TRUE,
            exhausted_at = now(),
            auto_reset_at = $1,
            last_429_at = now(),
            last_429_reset_after = $2,
            last_429_limit_header = $3,
            corrected_limit = CASE WHEN $4 > 0 THEN $4 ELSE corrected_limit END,
            updated_at = now()
        WHERE credential_id = $5
          AND provider_code = $6
          AND model_id = $7
          AND window_type = $8
          AND window_start <= now()
          AND window_end >= now()
          AND tenant_id = $9
    `, resetAt, retryAfterSec, req.Headers.Get("X-RateLimit-Limit"), limit,
        req.CredentialID, req.ProviderCode, req.ModelID, WindowTypeDay1, req.TenantID)
    
    return err
}

// Preflight 配额预检 - 返回是否可用
func (qt *QuotaTracker) Preflight(ctx context.Context, req PreflightRequest) (bool, error) {
    var limit, used int64
    var exhausted bool
    var resetAt sql.NullTime
    
    err := qt.db.QueryRowContext(ctx, `
        SELECT 
            COALESCE(corrected_limit, $1) AS limit,
            request_count AS used,
            is_exhausted,
            auto_reset_at
        FROM free_quota_tracker
        WHERE credential_id = $2
          AND provider_code = $3
          AND model_id = $4
          AND window_type = $5
          AND window_start <= now()
          AND window_end >= now()
          AND tenant_id = $6
    `, req.DefaultLimit, req.CredentialID, req.ProviderCode, req.ModelID,
        req.WindowType, req.TenantID).Scan(&limit, &used, &exhausted, &resetAt)
    
    if err == sql.ErrNoRows {
        return true, nil  // 无追踪记录，允许使用
    }
    if err != nil {
        return false, err
    }
    
    // 检查是否已过重置时间
    if exhausted && resetAt.Valid && time.Now().After(resetAt.Time) {
        // 自动解除耗尽状态
        _, _ = qt.db.ExecContext(ctx, `
            UPDATE free_quota_tracker
            SET is_exhausted = FALSE, exhausted_at = NULL
            WHERE credential_id = $1 AND provider_code = $2 AND model_id = $3
              AND window_type = $4 AND tenant_id = $5
        `, req.CredentialID, req.ProviderCode, req.ModelID, req.WindowType, req.TenantID)
        return true, nil
    }
    
    if exhausted {
        return false, nil
    }
    
    // 检查剩余配额百分比
    remaining := float64(limit-used) / float64(limit)
    return remaining >= req.MinRemainingPct, nil
}

// computeWindows 计算给定时间点的所有窗口起止
func (qt *QuotaTracker) computeWindows(ts time.Time, types []WindowType) []QuotaWindow {
    var windows []QuotaWindow
    
    for _, wt := range types {
        var start, end time.Time
        
        switch wt {
        case WindowTypeHour5:
            // 滚动 5 小时窗口
            start = ts.Add(-5 * time.Hour)
            end = ts
            
        case WindowTypeDay1:
            // UTC 日历日
            start = time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC)
            end = start.Add(24 * time.Hour)
            
        case WindowTypeDay7:
            // 滚动 7 日
            start = ts.Add(-7 * 24 * time.Hour)
            end = ts
            
        case WindowTypeMonth1:
            // UTC 日历月
            start = time.Date(ts.Year(), ts.Month(), 1, 0, 0, 0, 0, time.UTC)
            end = start.AddDate(0, 1, 0)
        }
        
        windows = append(windows, QuotaWindow{
            Type:  wt,
            Start: start,
            End:   end,
        })
    }
    
    return windows
}

func boolToInt(b bool) int {
    if b {
        return 1
    }
    return 0
}
```

---

## 🔌 集成点

### 1. 在 Streaming Executor 中记录配额

```go
// domains/streaming/executors/free_quota_hook.go

func (e *StreamExecutor) executeWithQuotaTracking(ctx context.Context, req *Request) error {
    // 1. 预检
    if req.Credential.IsFree {
        ok, err := e.quotaTracker.Preflight(ctx, freeresource.PreflightRequest{
            CredentialID:     req.Credential.ID,
            ProviderCode:     req.ProviderCode,
            ModelID:          req.ModelID,
            WindowType:       freeresource.WindowTypeDay1,
            DefaultLimit:     1000,
            MinRemainingPct:  0.1,  // 最少剩余 10%
            TenantID:         req.TenantID,
        })
        
        if err != nil {
            return fmt.Errorf("quota preflight: %w", err)
        }
        
        if !ok {
            return errors.New("quota exhausted, switching credential")
        }
    }
    
    // 2. 执行请求
    resp, err := e.doRequest(ctx, req)
    
    // 3. 记录配额
    if req.Credential.IsFree {
        recordErr := e.quotaTracker.Record(ctx, freeresource.RecordRequest{
            CredentialID:  req.Credential.ID,
            ProviderCode:  req.ProviderCode,
            ModelID:       req.ModelID,
            TokenCount:    resp.Usage.TotalTokens,
            Success:       err == nil,
            WindowTypes:   []freeresource.WindowType{
                freeresource.WindowTypeDay1,
                freeresource.WindowTypeMonth1,
            },
            TenantID:      req.TenantID,
            Timestamp:     time.Now(),
        })
        
        if recordErr != nil {
            log.Warnf("failed to record quota: %v", recordErr)
        }
    }
    
    // 4. 处理 429
    if resp.StatusCode == 429 {
        corrErr := e.quotaTracker.CorrectFromHeaders(ctx, freeresource.CorrectionRequest{
            CredentialID: req.Credential.ID,
            ProviderCode: req.ProviderCode,
            ModelID:      req.ModelID,
            Headers:      resp.Header,
            TenantID:     req.TenantID,
        })
        
        if corrErr != nil {
            log.Warnf("failed to correct from 429 headers: %v", corrErr)
        }
        
        return errors.New("rate limit exceeded")
    }
    
    return err
}
```

### 2. 在凭据选择器中过滤耗尽凭据

```go
// domains/credential/selector.go

func (s *Selector) SelectCredentials(ctx context.Context, req SelectRequest) ([]Credential, error) {
    candidates, err := s.loadCandidates(ctx, req)
    if err != nil {
        return nil, err
    }
    
    // 过滤配额耗尽的免费凭据
    filtered := make([]Credential, 0, len(candidates))
    for _, cred := range candidates {
        if !cred.IsFree {
            filtered = append(filtered, cred)
            continue
        }
        
        ok, err := s.quotaTracker.Preflight(ctx, freeresource.PreflightRequest{
            CredentialID:    cred.ID,
            ProviderCode:    req.ProviderCode,
            ModelID:         req.ModelID,
            WindowType:      freeresource.WindowTypeDay1,
            DefaultLimit:    cred.FreeQuotaLimit,
            MinRemainingPct: 0.05,  // 5% buffer
            TenantID:        req.TenantID,
        })
        
        if err != nil {
            log.Warnf("quota preflight error for cred %d: %v", cred.ID, err)
            continue
        }
        
        if ok {
            filtered = append(filtered, cred)
        } else {
            log.Infof("skipping exhausted credential %d (provider=%s, model=%s)",
                cred.ID, req.ProviderCode, req.ModelID)
        }
    }
    
    return filtered, nil
}
```

---

## 🔄 后台维护

### Quota Reset Worker

```go
// bg/freequotareset/worker.go

package freequotareset

import (
    "context"
    "time"
)

type Worker struct {
    db *sql.DB
}

func (w *Worker) Run(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            w.resetExpiredWindows(ctx)
        }
    }
}

func (w *Worker) resetExpiredWindows(ctx context.Context) {
    // 重置已过期的耗尽状态
    result, err := w.db.ExecContext(ctx, `
        UPDATE free_quota_tracker
        SET is_exhausted = FALSE,
            exhausted_at = NULL
        WHERE is_exhausted = TRUE
          AND auto_reset_at IS NOT NULL
          AND auto_reset_at <= now()
    `)
    
    if err != nil {
        log.Errorf("failed to reset expired windows: %v", err)
        return
    }
    
    rows, _ := result.RowsAffected()
    if rows > 0 {
        log.Infof("reset %d expired quota windows", rows)
    }
}
```

### Quota Cleanup Worker

```go
// bg/freequotacleanup/worker.go

func (w *Worker) cleanupOldWindows(ctx context.Context) {
    result, err := w.db.ExecContext(ctx, `
        DELETE FROM free_quota_tracker
        WHERE (window_type = 'hour-5' AND window_end < now() - interval '48 hours')
           OR (window_type = 'day-1' AND window_end < now() - interval '30 days')
           OR (window_type = 'day-7' AND window_end < now() - interval '90 days')
           OR (window_type = 'month-1' AND window_end < now() - interval '12 months')
    `)
    
    if err != nil {
        log.Errorf("failed to cleanup old quota windows: %v", err)
        return
    }
    
    rows, _ := result.RowsAffected()
    if rows > 0 {
        log.Infof("cleaned up %d old quota windows", rows)
    }
}
```

---

## 📊 监控与可观测

### 关键指标

```go
// 配额耗尽率 (按提供商)
gauge("free_quota.exhausted_ratio", ratio, tags: [provider, model])

// 429 校准次数
counter("free_quota.429_corrections", count, tags: [provider, model])

// 配额剩余百分比
gauge("free_quota.remaining_pct", pct, tags: [credential_id, provider, model])

// 预检拦截次数
counter("free_quota.preflight_blocks", count, tags: [provider, model])
```

### Prometheus 查询示例

```promql
# 配额耗尽最严重的提供商 (Top 5)
topk(5, 
  sum by (provider) (free_quota_exhausted_ratio)
)

# 429 校准频率趋势
rate(free_quota_429_corrections_total[5m])

# 平均配额剩余 (低于 20% 告警)
avg(free_quota_remaining_pct) < 0.2
```

---

## 🧪 测试场景

### 单元测试

```go
func TestQuotaTracker_Record(t *testing.T) {
    // 测试基础记录
    // 测试窗口边界 (UTC 日/月切换)
    // 测试并发 UPSERT
}

func TestQuotaTracker_CorrectFromHeaders(t *testing.T) {
    // 测试 Retry-After (秒数)
    // 测试 Retry-After (HTTP-date)
    // 测试 X-RateLimit-Reset
    // 测试限制覆盖
}

func TestQuotaTracker_Preflight(t *testing.T) {
    // 测试无记录场景
    // 测试耗尽但已过期
    // 测试剩余不足阈值
}
```

### 集成测试

```bash
# 模拟 OpenRouter 日配额耗尽
curl -X POST http://localhost:8781/v1/chat/completions \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"model": "openai/gpt-3.5-turbo:free", "messages": [...]}'
# 第 51 次请求应触发 429 → 校准 → 下一请求自动跳过该凭据
```

---

**下一步**: 阅读 `03-AUTO-COMBO.md` 了解虚拟路由实现。
