# LLM Gateway 流程优化改造规划

## 概述

基于对当前请求处理流程的深入分析，本文档列出需要优化改造的关键点，按优先级和实施难度分类。

---

## 一、核心问题分析

### 1.1 入口/出口逻辑匹配度分析

#### ✅ 已验证的匹配点
| 维度 | 入口 | 出口 | 匹配状态 |
|------|------|------|----------|
| RequestID | 中间件注入 | Header回传 | ✅ 完全匹配 |
| Auth | Middleware验证 | 4xx拒绝 | ✅ Fail-closed |
| Resource Gate | Acquire | Release | ✅ 对称释放 |
| Token计数 | 请求时估算 | 响应时精确 | ✅ 双向闭环 |

#### ⚠️ 需要改进的匹配点
| 维度 | 当前问题 | 影响 | 优先级 |
|------|----------|------|--------|
| Session降级写入 | DB失败时仅内存，可能丢失 | 审计不完整 | **P0** |
| 流式错误聚合 | 首字节后错误处理不优雅 | 客户端体验差 | **P1** |
| Panic恢复 | 资源可能未释放 | 资源泄漏风险 | **P1** |
| 客户端断开检测 | 检测延迟较高(~10s) | 资源浪费 | **P2** |
| 审计日志降级 | 降级路径无明确告警 | 运维盲区 | **P1** |

### 1.2 客户端保持方案评估

#### 当前机制
```
HTTP/2 Keep-Alive
  → TCP_NODELAY
  → Graceful Shutdown（HTTP drain 23s + worker/依赖关闭 5s）
  → 流式心跳 (10s interval, 6次限制)
  → 全局超时保护 (300s for stream)
```

#### 存在的问题
1. **心跳机制不够明确**
   - 历史候选心跳格式: `data: {"type": "heartbeat"}\n\n`（当前不采用；实际协议为 SSE comment）
   - 部分客户端可能误解为实际数据
   - **影响**: 客户端可能尝试解析心跳并报错

2. **客户端断开检测延迟**
   - 当前检测点: WriteChunk返回错误
   - 延迟: 最多等到下一个chunk写入
   - **影响**: 平均浪费5-10秒计算资源

3. **Graceful Shutdown等待时间固定**
   - 固定30秒等待
   - 不区分流式/非流式请求
   - **影响**: 流式请求可能被强制中断

### 1.3 异常中断可能性矩阵

| 中断点 | 触发条件 | 当前处理 | 恢复能力 | 风险等级 |
|--------|----------|----------|----------|----------|
| **Panic** | 代码bug/空指针 | Recovery→500 | ❌ 不可恢复 | 🔴 HIGH |
| **Auth失败** | Key无效/过期 | 401/403 | ❌ 客户端修复 | 🟢 LOW |
| **限流触发** | 超过配额 | 429+Retry-After | ✅ 自动恢复 | 🟡 MEDIUM |
| **资源门控拒绝** | 并发/内存超限 | 503 | ✅ 自动恢复 | 🟡 MEDIUM |
| **DB不可用** | 连接断开/超时 | 降级+告警 | ⚠️ 部分恢复 | 🔴 HIGH |
| **Redis不可用** | 连接断开 | 降级到内存 | ⚠️ 部分恢复 | 🟡 MEDIUM |
| **Provider超时** | 网络/上游慢 | 重试+切换 | ✅ 自动恢复 | 🟡 MEDIUM |
| **流中断** | 网络抖动 | L0-L4恢复 | ✅ 大部分恢复 | 🟡 MEDIUM |
| **客户端断开** | 主动取消 | 立即中止 | ❌ 不需恢复 | 🟢 LOW |
| **Graceful Shutdown** | SIGTERM | HTTP drain 23s + worker/依赖关闭 5s | ⚠️ 可能中断 | 🟡 MEDIUM |

---

## 二、优化改造计划

### 阶段一: 紧急修复 (1-2周)

#### 2.1.1 Session降级写入增强 【P0】

**问题**: DB失败时Session仅内存记录，进程重启后丢失

**方案**:
```go
// 当前实现
if err := db.CreateSession(session); err != nil {
    log.Warn("session create failed, memory only")
    memCache.Set(session.ID, session)  // 仅内存
}

// 改进方案
if err := db.CreateSession(session); err != nil {
    log.Error("session create failed", "error", err)

    // 1. 写入本地持久化队列
    if err := localQueue.Enqueue(session); err != nil {
        log.Error("local queue full", "error", err)
        metrics.Inc("session_fallback_failed")
    }

    // 2. 异步重试机制
    retryQueue.AddWithBackoff(session, 3, "1s,5s,30s")

    // 3. 明确告警
    if dbFailureCount.Inc() > 10 {
        alert.Fire("DB_SESSION_WRITE_CRITICAL")
    }

    // 4. 仍保留内存缓存以供查询
    memCache.Set(session.ID, session)
}
```

**验收标准**:
- [ ] Session丢失率 < 0.01%
- [ ] DB故障时明确告警触发
- [ ] 本地队列满时有降级策略

---

#### 2.1.2 Panic资源释放保护 【P1】

**问题**: Panic时可能未释放资源(连接/锁/file descriptors)

**方案**:
```go
// 在Recovery Middleware中增强
func RecoveryMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // 注册资源清理函数
        cleanup := &CleanupStack{}
        ctx := context.WithValue(r.Context(), "cleanup", cleanup)

        defer func() {
            if rec := recover(); rec != nil {
                // 1. 记录panic详情
                log.Error("panic", "error", rec, "stack", debug.Stack())

                // 2. 强制清理资源
                cleanup.RunAll()

                // 3. 释放特定资源
                if session := ctx.Value("session"); session != nil {
                    releaseSessionResources(session)
                }
                if provider := ctx.Value("provider_conn"); provider != nil {
                    provider.Close()
                }

                // 4. 更新指标
                metrics.Inc("panic_recovered", "path", r.URL.Path)

                // 5. 返回错误
                w.WriteHeader(500)
                json.NewEncoder(w).Encode(map[string]string{
                    "error": "internal server error",
                })
            }
        }()

        next.ServeHTTP(w, r.WithContext(ctx))
    })
}

// CleanupStack 实现
type CleanupStack struct {
    mu       sync.Mutex
    cleanups []func()
}

func (c *CleanupStack) Add(f func()) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.cleanups = append(c.cleanups, f)
}

func (c *CleanupStack) RunAll() {
    c.mu.Lock()
    defer c.mu.Unlock()

    for i := len(c.cleanups) - 1; i >= 0; i-- {
        func() {
            defer recover() // 防止清理函数自身panic
            c.cleanups[i]()
        }()
    }
}
```

**验收标准**:
- [ ] Panic后连接数不增长
- [ ] File descriptor不泄漏
- [ ] 锁被正确释放

---

#### 2.1.3 流式错误处理优化 【P1】

**问题**: 首字节后错误内联到流中，客户端体验差

**方案**:
```go
// 当前: 直接内联错误
data: {"error": {"message": "...", "code": "..."}}
data: [DONE]

// 改进: 添加错误上下文
data: {"type": "error", "error": {"message": "...", "code": "...", "recoverable": false}}
data: {"type": "metadata", "usage": {"input_tokens": 100, "output_tokens": 50}}
data: [DONE]

// 实现
func (w *StreamWriter) WriteError(err error) {
    // 1. 判断是否可恢复
    recoverable := isRecoverableError(err)

    // 2. 构造错误chunk
    errorChunk := map[string]interface{}{
        "type": "error",
        "error": map[string]interface{}{
            "message":     err.Error(),
            "code":        getErrorCode(err),
            "recoverable": recoverable,
            "timestamp":   time.Now().Unix(),
        },
    }

    // 3. 如果可恢复，添加重试建议
    if recoverable {
        errorChunk["retry_after"] = 5 // seconds
    }

    // 4. 写入流
    w.WriteChunk(errorChunk)

    // 5. 添加usage元数据(即使失败也要记录)
    w.WriteMetadata(map[string]interface{}{
        "usage": w.GetCurrentUsage(),
    })

    // 6. 结束流
    w.WriteDone()
}
```

**验收标准**:
- [ ] 客户端SDK能区分可恢复/不可恢复错误
- [ ] Token使用量在错误情况下也被记录
- [ ] 错误类型统计完整

---

#### 2.1.4 审计降级告警 【P1】

**问题**: 审计日志写入失败时无明确告警

**方案**:
```go
// 增强审计写入逻辑
func (a *AuditLogger) Write(entry *AuditEntry) error {
    // 1. 尝试写入DB
    err := a.db.Insert(entry)
    if err == nil {
        a.metrics.Inc("audit_write_success")
        return nil
    }

    // 2. 记录失败
    log.Warn("audit DB write failed", "error", err)
    a.metrics.Inc("audit_write_failed")

    // 3. 写入本地队列
    if err := a.localQueue.Enqueue(entry); err != nil {
        log.Error("audit local queue full", "error", err)
        a.metrics.Inc("audit_queue_full")
    }

    // 4. 告警逻辑
    failureRate := a.calculateFailureRate() // 滑动窗口5分钟
    if failureRate > 0.05 { // 5%
        a.alertOnce.Do(func() {
            alert.Fire("AUDIT_WRITE_DEGRADED", map[string]interface{}{
                "failure_rate": failureRate,
                "queue_size":   a.localQueue.Size(),
            })
        })
    }

    // 5. 异步重试
    go a.retryWithBackoff(entry, 3)

    return err
}

// 定期刷新本地队列到DB
func (a *AuditLogger) FlushLocalQueue(ctx context.Context) {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            batch := a.localQueue.DequeueBatch(100)
            if len(batch) == 0 {
                continue
            }

            if err := a.db.BatchInsert(batch); err != nil {
                log.Error("batch flush failed", "count", len(batch))
                // 重新入队
                for _, entry := range batch {
                    a.localQueue.Enqueue(entry)
                }
            } else {
                log.Info("batch flushed", "count", len(batch))
                a.metrics.Add("audit_flushed_total", float64(len(batch)))
            }
        }
    }
}
```

**验收标准**:
- [ ] 审计失败率>5%时告警触发
- [ ] 本地队列积压>1000条告警
- [ ] 恢复后自动清空队列

---

### 阶段二: 体验优化 (2-4周)

#### 2.2.1 心跳机制明确化（当前协议已确定）

当前实现使用 SSE comment `: keep-alive\n\n`，而不是 `data` JSON 或独立 `event`。客户端必须忽略 comment 行，不将其交给模型响应 JSON 解析器。以下格式仅保留为历史候选方案，不应直接实现：
```text
历史候选方案（不作为当前协议）：
: heartbeat

: {"type": "heartbeat", "timestamp": 1693641234}

event: heartbeat
data: {"timestamp": 1693641234, "seq": 1}
```

**客户端SDK示例**:
```typescript
const eventSource = new EventSource('/v1/chat/completions');

eventSource.addEventListener('heartbeat', (e) => {
  console.log('Heartbeat received', e.data);
  // 客户端可以选择忽略或显示连接状态
});

eventSource.addEventListener('message', (e) => {
  const data = JSON.parse(e.data);
  if (data.type === 'heartbeat') {
    // 显式过滤
    return;
  }
  // 处理实际数据
});
```

**验收标准**:
- [ ] 心跳不触发客户端解析错误
- [ ] 客户端SDK正确过滤心跳
- [ ] 文档明确说明心跳格式

---

#### 2.2.2 客户端断开快速检测 【P2】

**方案**:
```go
// 当前: 被动等待WriteChunk错误
// 改进: 主动检测

type ConnectionMonitor struct {
    ctx        context.Context
    cancel     context.CancelFunc
    lastActive atomic.Int64
}

func (m *ConnectionMonitor) Start() {
    go func() {
        ticker := time.NewTicker(1 * time.Second)
        defer ticker.Stop()

        for {
            select {
            case <-m.ctx.Done():
                return
            case <-ticker.C:
                // 检查连接活跃度
                if time.Since(time.Unix(m.lastActive.Load(), 0)) > 10*time.Second {
                    log.Info("client inactive, checking connection")
                    if !m.isConnected() {
                        log.Info("client disconnected, canceling")
                        m.cancel()
                        return
                    }
                }
            }
        }
    }()
}

func (m *ConnectionMonitor) isConnected() bool {
    // 尝试写入零字节数据检测连接状态
    // 或使用HTTP/2 PING frame
    return true
}

// 在StreamWriter中集成
func (w *StreamWriter) WriteChunk(data []byte) error {
    w.monitor.lastActive.Store(time.Now().Unix())

    // 检查context
    select {
    case <-w.ctx.Done():
        return fmt.Errorf("client disconnected")
    default:
    }

    // 写入数据
    return w.writer.Write(data)
}
```

**验收标准**:
- [ ] 客户端断开检测延迟 < 2秒
- [ ] Provider请求能被及时取消
- [ ] 无误判(正常连接被判断为断开)

---

#### 2.2.3 Graceful Shutdown智能化 【P2】

**方案**:
```go
// 当前: 固定30秒等待
// 改进: 区分请求类型 + 动态调整

type GracefulShutdownManager struct {
    streamRequests    sync.Map // request_id -> *StreamRequest
    nonStreamRequests sync.Map
    shutdownStarted   atomic.Bool
}

func (m *GracefulShutdownManager) Shutdown(ctx context.Context) error {
    m.shutdownStarted.Store(true)

    // 1. 立即停止接受新请求
    server.SetKeepAlivesEnabled(false)

    // 2. 统计当前请求
    streamCount := m.countStreamRequests()
    nonStreamCount := m.countNonStreamRequests()

    log.Info("graceful shutdown started",
        "stream", streamCount,
        "nonstream", nonStreamCount)

    // 3. 分阶段等待
    // 阶段1: 等待非流式请求 (最多10秒)
    if err := m.waitForNonStream(10 * time.Second); err != nil {
        log.Warn("non-stream requests timeout", "remaining", m.countNonStreamRequests())
    }

    // 阶段2: 等待流式请求 (最多50秒)
    if err := m.waitForStream(50 * time.Second); err != nil {
        log.Warn("stream requests timeout", "remaining", m.countStreamRequests())
    }

    // 4. 强制关闭剩余连接
    m.forceCloseAll()

    // 5. 刷新审计日志
    auditLogger.Flush()

    return nil
}

func (m *GracefulShutdownManager) waitForStream(timeout time.Duration) error {
    ticker := time.NewTicker(1 * time.Second)
    defer ticker.Stop()

    deadline := time.Now().Add(timeout)

    for time.Now().Before(deadline) {
        if m.countStreamRequests() == 0 {
            return nil
        }

        <-ticker.C
        log.Info("waiting for stream requests", "count", m.countStreamRequests())
    }

    return fmt.Errorf("timeout waiting for stream requests")
}
```

**验收标准**:
- [ ] 非流式请求不被过早中断
- [ ] 流式请求有充足时间完成
- [ ] 强制关闭前审计日志已刷新

---

### 阶段三: 架构优化 (1-2个月)

#### 2.3.1 per-tenant资源配额 【P1】

**目标**: 根据租户历史负载动态调整资源配额

**方案**:
```go
type TenantQuotaManager struct {
    quotas sync.Map // tenant_id -> *TenantQuota
}

type TenantQuota struct {
    TenantID          string
    MaxConcurrent     int     // 最大并发
    MaxQPS            int     // 最大QPS
    MaxTokensPerMin   int     // 每分钟token上限
    Priority          int     // 优先级

    // 动态调整参数
    HistoricalAvgQPS  float64
    BurstMultiplier   float64 // 允许突发倍数

    // 实时状态
    CurrentConcurrent atomic.Int32
    CurrentQPS        atomic.Int32
}

func (m *TenantQuotaManager) AdjustQuota(tenantID string) {
    quota := m.getQuota(tenantID)

    // 获取过去7天的统计
    stats := m.getHistoricalStats(tenantID, 7*24*time.Hour)

    // 计算P95负载
    p95Concurrent := stats.ConcurrentP95
    p95QPS := stats.QPSP95

    // 动态调整 = P95 * 1.5 (留50%余量)
    newConcurrent := int(float64(p95Concurrent) * 1.5)
    newQPS := int(float64(p95QPS) * 1.5)

    // 应用调整
    quota.MaxConcurrent = newConcurrent
    quota.MaxQPS = newQPS

    log.Info("quota adjusted",
        "tenant", tenantID,
        "concurrent", newConcurrent,
        "qps", newQPS)
}
```

**验收标准**:
- [ ] 租户配额根据历史自动调整
- [ ] 高优先级租户能抢占资源
- [ ] 低负载租户不占用过多配额

---

#### 2.3.2 分布式追踪集成 【P1】

**方案**: 集成OpenTelemetry

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/trace"
)

func ChatHandler(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    tracer := otel.Tracer("llm-gateway")

    // 1. 创建根span
    ctx, span := tracer.Start(ctx, "chat.request",
        trace.WithAttributes(
            attribute.String("request.id", requestID),
            attribute.String("tenant.id", tenantID),
            attribute.String("model", model),
        ))
    defer span.End()

    // 2. Auth span
    ctx, authSpan := tracer.Start(ctx, "auth.verify")
    err := authService.Verify(ctx, apiKey)
    if err != nil {
        authSpan.RecordError(err)
        authSpan.SetStatus(codes.Error, "auth failed")
    }
    authSpan.End()

    // 3. Resource Gate span
    ctx, gateSpan := tracer.Start(ctx, "resource.acquire")
    acquired := resourceGate.Acquire(ctx, tenant)
    gateSpan.SetAttributes(attribute.Bool("acquired", acquired))
    gateSpan.End()

    // 4. Provider Request span
    ctx, providerSpan := tracer.Start(ctx, "provider.request",
        trace.WithAttributes(
            attribute.String("provider", provider),
            attribute.String("provider.instance", instance),
        ))
    resp, err := provider.Request(ctx, req)
    if err != nil {
        providerSpan.RecordError(err)
    }
    providerSpan.End()

    // 5. 记录最终结果
    span.SetAttributes(
        attribute.Int("tokens.input", inputTokens),
        attribute.Int("tokens.output", outputTokens),
        attribute.Int("status.code", statusCode),
    )
}
```

**验收标准**:
- [ ] 能追踪完整请求链路
- [ ] 能定位性能瓶颈
- [ ] 支持跨服务追踪(Gateway→Provider)

---

#### 2.3.3 智能重试策略 【P2】

**方案**: 基于错误类型和Provider历史表现动态调整

```go
type SmartRetryPolicy struct {
    providerStats sync.Map // provider -> *ProviderStats
}

type ProviderStats struct {
    Provider          string

    // 历史统计
    SuccessRate       float64
    AvgLatency        time.Duration
    TimeoutRate       float64
    ErrorRate5xx      float64

    // 实时状态
    ConsecutiveErrors atomic.Int32
    LastErrorTime     atomic.Int64
}

func (p *SmartRetryPolicy) ShouldRetry(
    provider string,
    attempt int,
    err error,
) (shouldRetry bool, delay time.Duration) {

    stats := p.getStats(provider)

    // 1. 基于错误类型
    errorType := classifyError(err)
    switch errorType {
    case ErrorTypeTimeout:
        // 超时: 重试概率取决于历史超时率
        if stats.TimeoutRate > 0.3 {
            // 超时率高，降低重试次数
            return attempt < 1, 2 * time.Second
        }
        return attempt < 3, time.Duration(attempt) * 500 * time.Millisecond

    case ErrorType5xx:
        // 5xx: 重试概率取决于连续错误次数
        consecutive := stats.ConsecutiveErrors.Load()
        if consecutive > 5 {
            // 连续失败太多，快速放弃
            return false, 0
        }
        return attempt < 2, time.Duration(attempt) * time.Second

    case ErrorTypeRateLimit:
        // 429: 遵守Retry-After
        retryAfter := extractRetryAfter(err)
        return attempt < 5, retryAfter

    default:
        return false, 0
    }
}

// 定期更新统计
func (p *SmartRetryPolicy) UpdateStats(ctx context.Context) {
    ticker := time.NewTicker(1 * time.Minute)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            p.providerStats.Range(func(key, value interface{}) bool {
                provider := key.(string)
                stats := value.(*ProviderStats)

                // 从Prometheus查询过去5分钟的数据
                metrics := queryProviderMetrics(provider, 5*time.Minute)

                // 更新统计
                stats.SuccessRate = metrics.SuccessRate
                stats.AvgLatency = metrics.AvgLatency
                stats.TimeoutRate = metrics.TimeoutRate

                return true
            })
        }
    }
}
```

**验收标准**:
- [ ] 成功率提升5%
- [ ] 平均重试次数降低
- [ ] 快速识别并跳过故障Provider

---

## 三、关键指标定义

### 3.1 核心SLI

| 指标 | 目标 | 当前 | 优化后目标 |
|------|------|------|-----------|
| **请求成功率** | 99.9% | 99.5% | 99.95% |
| **P95延迟** | <2s | 2.5s | <1.5s |
| **流式TTFB** | <500ms | 800ms | <400ms |
| **Session完整率** | 99.9% | 99.0% | 99.99% |
| **审计完整率** | 99.9% | 98.5% | 99.95% |
| **资源泄漏率** | 0% | 0.01% | 0% |

### 3.2 告警阈值

```yaml
alerts:
  - name: SessionWriteFailureRate
    expr: rate(session_write_failed[5m]) > 0.05
    severity: P1

  - name: AuditWriteDegraded
    expr: rate(audit_write_failed[5m]) > 0.05
    severity: P1

  - name: PanicRate
    expr: rate(panic_recovered[5m]) > 0.001
    severity: P0

  - name: DBConnectionLoss
    expr: db_connection_status == 0
    duration: 5m
    severity: P0

  - name: ResourceGateRejectionHigh
    expr: rate(resource_gate_rejected[5m]) > 0.2
    severity: P1

  - name: ClientDisconnectRateAnomaly
    expr: |
      rate(client_disconnect[5m]) >
      avg_over_time(rate(client_disconnect[5m])[1h:5m]) * 2
    severity: P2
```

---

## 四、实施roadmap

### Week 1-2: 紧急修复
- [x] Session降级写入增强
- [x] Panic资源释放保护
- [x] 流式错误处理优化
- [x] 审计降级告警

### Week 3-4: 体验优化
- [x] 心跳机制明确化（SSE comment `: keep-alive\\n\\n`，已有生命周期测试；仍需客户端/线上验收）
- [x] 客户端断开快速检测（ConnectionMonitor 组件与流式写入接入；仍需真实 HTTP/客户端延迟验收）
- [x] Graceful Shutdown智能化（流/非流分阶段管理器；仍需 gateway 进程级演练）

### Week 5-8: 架构优化
- [x] per-tenant资源配额（进程内动态 P95 调整与 QPS/token/concurrency 限制；Redis/多实例一致性仍待验收）
- [ ] 分布式追踪集成（现有 OTLP plumbing 可复用；本次未新增请求级 span 接入）
- [x] 智能重试策略（provider 统计驱动策略组件；实际 failover 接线与压测仍待完成）

---

## 五、风险评估

### 高风险改动
1. **Panic资源释放**: 可能引入新的清理逻辑bug
   - **缓解**: 充分测试 + 灰度发布

2. **Graceful Shutdown改造**: 可能影响现有部署流程
   - **缓解**: 保留旧配置兼容性

### 中风险改动
1. **Session降级写入**: 本地队列可能占用过多内存
   - **缓解**: 设置队列上限 + 监控

2. **智能重试**: 复杂逻辑可能导致意外行为
   - **缓解**: Feature flag控制 + A/B测试

---

## 六、验收checklist

### 功能验收
- [ ] 所有单元测试通过
- [ ] 集成测试覆盖新逻辑
- [ ] 压力测试无性能劣化
- [ ] 异常场景测试(模拟DB/Redis故障)

### 监控验收
- [ ] 新增指标已接入Prometheus
- [ ] 告警规则已配置
- [ ] Dashboard已更新

### 文档验收
- [ ] API文档更新
- [ ] 运维手册更新
- [ ] 架构图更新

---

**文档版本**: v1.0
**最后更新**: 2026-09-03
**负责团队**: Gateway Core Team
**审核人**: Tech Lead
