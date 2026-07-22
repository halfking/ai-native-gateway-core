# Phase 1 并行优化实施计划

> **任务**: LLM Gateway Phase 1 - 基础优化 + 号池优化
> **周期**: Week 2-5 (2026-07-22 ~ 2026-08-15)
> **状态**: 📋 待开始
> **负责人**: Infrastructure Team

---

## 1. 总体目标

Phase 1 包含两个并行优化方向，共同提升系统稳定性、安全性和调度效率。

### 1.1 关键产出

| 方向 | 核心功能 | 验收指标 |
|------|---------|---------|
| **基础优化** | Circuit Breaker + Unified Adapter + Content Safety | 熔断触发率 < 1%，适配器转换成功率 > 99.9% |
| **号池优化** | WRR 调度器 + 健康检查 + 等待队列优化 | 凭据等待 P99 < 100ms，负载均衡偏差 < 5% |

### 1.2 共享基础设施

- ✅ Prometheus 监控栈 (Phase 0 已部署)
- ✅ `request_logs` 表扩展 (Phase 1 字段，本周执行迁移)
- ✅ Grafana 看板模板

---

## 2. Phase 1A: 基础优化

### 2.1 Circuit Breaker (熔断器)

**目标**: 保护上游提供商，防止雪崩效应

#### 任务 1.1: 熔断器核心实现

**产出**: `circuit/breaker.go`

**功能需求**:
- 三态状态机: Closed → Open → Half-Open
- 基于滑动窗口的错误率统计 (10s 窗口)
- 阈值配置: 错误率 > 50%，最小请求数 > 10
- 熔断后自动恢复探测 (30s 间隔)

**技术方案**:
```go
type CircuitBreaker struct {
    state        State  // closed | open | half_open
    errorWindow  *SlidingWindow
    lastOpenTime time.Time
    config       Config
}

func (cb *CircuitBreaker) Call(fn func() error) error {
    if cb.state == Open {
        if time.Since(cb.lastOpenTime) > cb.config.OpenTimeout {
            cb.state = HalfOpen
        } else {
            return ErrCircuitOpen
        }
    }

    err := fn()
    cb.recordResult(err)
    cb.updateState()
    return err
}
```

**测试用例**:
- [ ] 错误率超阈值时触发熔断
- [ ] Half-Open 状态探测成功后恢复 Closed
- [ ] Half-Open 状态探测失败后重新 Open
- [ ] 并发安全 (1000 goroutine 压测)

#### 任务 1.2: 集成到请求链路

**修改文件**: `cmd/gateway/main_pipeline.go`

**集成点**:
```go
// 在 buildV2DispatchPipeline 中注册熔断器中间件
func buildV2DispatchPipeline(...) {
    // ... 现有代码

    // 为每个 provider 创建独立的熔断器
    circuitBreakers := make(map[string]*circuit.Breaker)
    for _, provider := range providers {
        circuitBreakers[provider] = circuit.NewBreaker(circuit.Config{
            ErrorThreshold:  0.5,  // 50%
            MinRequests:     10,
            OpenTimeout:     30 * time.Second,
            HalfOpenMaxTest: 3,
        })
    }

    // 注册熔断器插件
    secRegistry.MustRegister(circuitplugin.New(circuitBreakers))
}
```

#### 任务 1.3: Prometheus Metrics

```go
var (
    CircuitBreakerState = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "llm_gateway_circuit_breaker_state",
            Help: "Circuit breaker state (0=closed, 1=open, 2=half_open)",
        },
        []string{"provider"},
    )

    CircuitBreakerTriggered = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_circuit_breaker_triggered_total",
            Help: "Total number of circuit breaker triggers",
        },
        []string{"provider", "reason"},
    )

    CircuitBreakerRejected = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_circuit_breaker_rejected_total",
            Help: "Total number of requests rejected by circuit breaker",
        },
        []string{"provider"},
    )
)
```

#### 任务 1.4: 告警规则

```yaml
# deploy/prometheus/rules/circuit-breaker.yml
groups:
  - name: circuit_breaker_alerts
    interval: 30s
    rules:
      - alert: CircuitBreakerOpen
        expr: llm_gateway_circuit_breaker_state{state="open"} == 1
        for: 1m
        labels:
          severity: warning
        annotations:
          summary: "Circuit breaker is open for {{ $labels.provider }}"
          description: "Provider {{ $labels.provider }} circuit breaker has been open for 1 minute"

      - alert: CircuitBreakerHighTriggerRate
        expr: rate(llm_gateway_circuit_breaker_triggered_total[5m]) > 0.1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High circuit breaker trigger rate for {{ $labels.provider }}"
```

---

### 2.2 Unified Adapter (统一适配器)

**目标**: 统一多提供商 API 差异，降低维护成本

#### 任务 2.1: Adapter 接口定义

**产出**: `adapter/unified/interface.go`

```go
package unified

// UnifiedRequest 是标准化的请求格式
type UnifiedRequest struct {
    Model       string
    Messages    []Message
    Stream      bool
    MaxTokens   int
    Temperature float64
    // ... 其他通用参数
}

// UnifiedResponse 是标准化的响应格式
type UnifiedResponse struct {
    ID      string
    Choices []Choice
    Usage   Usage
    Model   string
}

// Adapter 是提供商适配器接口
type Adapter interface {
    Name() string
    ToProviderRequest(req *UnifiedRequest) (interface{}, error)
    FromProviderResponse(resp interface{}) (*UnifiedResponse, error)
    SupportedModels() []string
}
```

#### 任务 2.2: OpenAI Adapter 实现

**产出**: `adapter/unified/openai.go`

```go
type OpenAIAdapter struct {
    version string
}

func (a *OpenAIAdapter) ToProviderRequest(req *UnifiedRequest) (interface{}, error) {
    return &openai.ChatCompletionRequest{
        Model:       req.Model,
        Messages:    convertMessages(req.Messages),
        Stream:      req.Stream,
        MaxTokens:   req.MaxTokens,
        Temperature: req.Temperature,
    }, nil
}

func (a *OpenAIAdapter) FromProviderResponse(resp interface{}) (*UnifiedResponse, error) {
    oaiResp := resp.(*openai.ChatCompletionResponse)
    return &UnifiedResponse{
        ID:      oaiResp.ID,
        Choices: convertChoices(oaiResp.Choices),
        Usage:   convertUsage(oaiResp.Usage),
        Model:   oaiResp.Model,
    }, nil
}
```

#### 任务 2.3: Anthropic Adapter 实现

**产出**: `adapter/unified/anthropic.go`

**差异处理**:
- Anthropic 使用 `max_tokens` (必填)，OpenAI 使用 `max_tokens` (可选)
- Anthropic 消息格式: `role` + `content`，OpenAI 格式类似但有细微差异
- Anthropic 流式响应格式不同

#### 任务 2.4: Adapter Registry

**产出**: `adapter/unified/registry.go`

```go
var adapterRegistry = map[string]Adapter{
    "openai":     &OpenAIAdapter{},
    "anthropic":  &AnthropicAdapter{},
    "azure":      &AzureOpenAIAdapter{},
    // ... 其他提供商
}

func GetAdapter(provider string) (Adapter, error) {
    adapter, ok := adapterRegistry[provider]
    if !ok {
        return nil, fmt.Errorf("unsupported provider: %s", provider)
    }
    return adapter, nil
}
```

#### 任务 2.5: Prometheus Metrics

```go
var (
    AdapterRequests = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_adapter_requests_total",
            Help: "Total adapter requests",
        },
        []string{"adapter_version", "provider", "status"},
    )

    AdapterConversionErrors = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_adapter_conversion_errors_total",
            Help: "Total adapter conversion errors",
        },
        []string{"adapter_version", "error_type"},
    )
)
```

---

### 2.3 Content Safety 增强

**目标**: 在 Phase 0 敏感词检测基础上，增加评分和策略

#### 任务 3.1: 内容安全评分

**修改文件**: `security/sensitive/engine.go`

**新增功能**:
```go
type SafetyResult struct {
    Score          float64    // 0.0 (安全) - 1.0 (危险)
    MatchedWords   []string
    Category       string     // sensitive_word | prompt_injection | policy_violation
    Action         Action     // allow | warn | block
}

func (e *Engine) EvaluateSafety(text string) *SafetyResult {
    matches := e.DetectSensitiveWords(text)

    // 基于匹配数和严重程度计算评分
    score := calculateScore(matches)

    // 根据评分决定动作
    action := determineAction(score)

    return &SafetyResult{
        Score:        score,
        MatchedWords: matches,
        Category:     categorize(matches),
        Action:       action,
    }
}
```

#### 任务 3.2: 填充 request_logs 字段

```go
// 在请求处理完成后
log := &RequestLog{
    // ... 现有字段
    ContentSafetyScore:     safetyResult.Score,
    SensitiveWordsMatched:  safetyResult.MatchedWords,
}
db.Insert(log)
```

#### 任务 3.3: Prometheus Metrics

```go
var (
    ContentSafetyViolations = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_content_safety_violations_total",
            Help: "Total content safety violations",
        },
        []string{"category"},
    )

    ContentSafetyScore = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "llm_gateway_content_safety_score",
            Help:    "Content safety score distribution",
            Buckets: []float64{0.1, 0.3, 0.5, 0.7, 0.9, 1.0},
        },
        []string{"tenant_id", "model"},
    )
)
```

---

## 3. Phase 1B: 号池优化

### 3.1 WRR 调度器 (加权轮询)

**目标**: 替代 Round Robin，实现按权重分配凭据

#### 任务 4.1: WRR 调度器核心

**产出**: `pool/scheduler/wrr.go`

**算法**: Smooth Weighted Round-Robin (nginx 算法)

```go
type WRRScheduler struct {
    credentials []*WeightedCredential
    mu          sync.Mutex
}

type WeightedCredential struct {
    Credential      *Credential
    Weight          int  // 配置权重
    CurrentWeight   int  // 动态权重
    EffectiveWeight int  // 有效权重 (根据健康状况调整)
}

func (s *WRRScheduler) Select() *Credential {
    s.mu.Lock()
    defer s.mu.Unlock()

    // 1. 选出 current_weight 最大的
    best := s.selectBest()

    // 2. 更新: best.current_weight -= total_weight
    best.CurrentWeight -= s.totalWeight()

    // 3. 所有 current_weight += effective_weight
    for _, c := range s.credentials {
        c.CurrentWeight += c.EffectiveWeight
    }

    return best.Credential
}
```

**测试用例**:
- [ ] 权重 [5, 1, 1] 分配 1000 次，偏差 < 5%
- [ ] 并发 1000 goroutine 调度，无死锁
- [ ] 权重动态调整 (健康检查失败后降权)

#### 任务 4.2: 健康检查降权

**产出**: `pool/health/checker.go`

```go
type HealthChecker struct {
    pool      *CredentialPool
    interval  time.Duration
    scheduler *wrr.WRRScheduler
}

func (hc *HealthChecker) Start() {
    ticker := time.NewTicker(hc.interval)
    go func() {
        for range ticker.C {
            for _, cred := range hc.pool.Credentials() {
                if err := hc.checkCredential(cred); err != nil {
                    // 降低权重
                    hc.scheduler.DecreaseWeight(cred.ID, 0.5)
                    metrics.CredentialHealthCheckFailures.WithLabelValues(
                        cred.ID, err.Error(),
                    ).Inc()
                } else {
                    // 恢复权重
                    hc.scheduler.RestoreWeight(cred.ID)
                }
            }
        }
    }()
}
```

#### 任务 4.3: 填充 request_logs 字段

```go
log := &RequestLog{
    // ... 现有字段
    CredentialPoolID:       pool.ID,
    CredentialID:           cred.ID,
    CredentialPoolStrategy: "wrr",
    CredentialWaitTimeMs:   waitTime.Milliseconds(),
    CredentialReuseCount:   cred.ReuseCount,
}
```

#### 任务 4.4: Prometheus Metrics

```go
var (
    CredentialPoolUtilization = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "llm_gateway_credential_pool_utilization",
            Help: "Credential pool utilization (0-100%)",
        },
        []string{"pool_id", "strategy"},
    )

    CredentialWaitTime = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "llm_gateway_credential_wait_time_seconds",
            Help:    "Credential allocation wait time",
            Buckets: prometheus.DefBuckets,
        },
        []string{"pool_id"},
    )

    CredentialAllocation = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_credential_allocation_total",
            Help: "Total credential allocations",
        },
        []string{"pool_id", "status"},  // allocated | timeout | pool_exhausted
    )

    CredentialWeight = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "llm_gateway_credential_weight",
            Help: "Current credential weight for WRR",
        },
        []string{"credential_id", "pool_id"},
    )

    CredentialRequestsAllocated = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_credential_requests_allocated_total",
            Help: "Requests allocated to each credential",
        },
        []string{"credential_id", "pool_id"},
    )
)
```

---

### 3.2 等待队列优化

**目标**: 降低凭据等待时间，优化队列调度

#### 任务 5.1: 优先级队列

**产出**: `pool/queue/priority.go`

**功能**:
- 按租户优先级排队 (Premium > Standard > Free)
- 同优先级内 FIFO
- 支持超时自动移除

```go
type PriorityQueue struct {
    queues map[Priority]*list.List
    mu     sync.Mutex
    cond   *sync.Cond
}

func (pq *PriorityQueue) Enqueue(req *Request, priority Priority, timeout time.Duration) error {
    pq.mu.Lock()
    defer pq.mu.Unlock()

    queue := pq.queues[priority]
    entry := &QueueEntry{
        Request:   req,
        EnqueueAt: time.Now(),
        Timeout:   timeout,
    }
    queue.PushBack(entry)
    pq.cond.Signal()

    return nil
}

func (pq *PriorityQueue) Dequeue() *Request {
    pq.mu.Lock()
    defer pq.mu.Unlock()

    // 优先级: High > Medium > Low
    for _, priority := range []Priority{High, Medium, Low} {
        queue := pq.queues[priority]
        if queue.Len() > 0 {
            entry := queue.Remove(queue.Front()).(*QueueEntry)
            return entry.Request
        }
    }

    // 队列空，等待
    pq.cond.Wait()
    return pq.Dequeue()
}
```

#### 任务 5.2: 队列监控

```go
var (
    CredentialQueueLength = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "llm_gateway_credential_queue_length",
            Help: "Current queue length",
        },
        []string{"pool_id"},
    )

    CredentialQueueWaitTime = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "llm_gateway_credential_queue_wait_seconds",
            Help:    "Time spent waiting in queue",
            Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 10},
        },
        []string{"pool_id", "priority"},
    )
)
```

---

## 4. 数据库迁移

### 4.1 执行 Phase 1 字段迁移

**时间**: Week 2 Day 1 (2026-07-22)

**迁移脚本**: `deploy/sql/migrations/V002__add_phase1_columns.sql`

```sql
BEGIN;

-- 基础优化字段
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS content_safety_score NUMERIC(3,2);
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS sensitive_words_matched TEXT[];
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS circuit_breaker_triggered BOOLEAN DEFAULT FALSE;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS circuit_breaker_reason TEXT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS adapter_version TEXT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS upstream_provider TEXT;

-- 号池优化字段
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS credential_pool_id INT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS credential_id INT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS credential_pool_strategy TEXT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS credential_wait_time_ms INT;
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS credential_reuse_count INT;

-- 约束
ALTER TABLE request_logs ADD CONSTRAINT chk_content_safety_score
    CHECK (content_safety_score IS NULL OR (content_safety_score >= 0 AND content_safety_score <= 1));

ALTER TABLE request_logs ADD CONSTRAINT chk_credential_pool_strategy
    CHECK (credential_pool_strategy IS NULL OR credential_pool_strategy IN ('round_robin', 'wrr', 'least_loaded'));

COMMIT;
```

**验证**:
```sql
-- 验证字段已添加
SELECT column_name, data_type, is_nullable
FROM information_schema.columns
WHERE table_name = 'request_logs'
  AND column_name IN (
    'content_safety_score', 'circuit_breaker_triggered',
    'credential_pool_id', 'credential_wait_time_ms'
  );
```

### 4.2 创建 Phase 1 索引

```sql
-- 基础优化索引
CREATE INDEX CONCURRENTLY idx_request_logs_circuit_breaker
    ON request_logs(circuit_breaker_triggered)
    WHERE circuit_breaker_triggered = TRUE;

CREATE INDEX CONCURRENTLY idx_request_logs_content_safety
    ON request_logs(content_safety_score)
    WHERE content_safety_score IS NOT NULL;

-- 号池优化索引
CREATE INDEX CONCURRENTLY idx_request_logs_credential_pool
    ON request_logs(credential_pool_id)
    WHERE credential_pool_id IS NOT NULL;

CREATE INDEX CONCURRENTLY idx_request_logs_credential_wait
    ON request_logs(credential_wait_time_ms)
    WHERE credential_wait_time_ms > 100;
```

---

## 5. 并行开发策略

### 5.1 任务分组

**Week 2 (2026-07-22 ~ 2026-07-26)**:
- [ ] DB 迁移 + 索引创建
- [ ] Circuit Breaker 核心实现 + 测试
- [ ] WRR 调度器核心实现 + 测试

**Week 3 (2026-07-29 ~ 2026-08-02)**:
- [ ] Circuit Breaker 集成到请求链路
- [ ] Unified Adapter 接口 + OpenAI/Anthropic 实现
- [ ] 健康检查模块 + 降权逻辑

**Week 4 (2026-08-05 ~ 2026-08-09)**:
- [ ] Content Safety 评分增强
- [ ] 优先级队列实现
- [ ] 所有 Prometheus metrics 实现

**Week 5 (2026-08-12 ~ 2026-08-15)**:
- [ ] 集成测试 (端到端)
- [ ] Grafana 看板创建
- [ ] 告警规则配置
- [ ] 验收测试

### 5.2 依赖关系

```
Week 2:
  DB 迁移 (Day 1)
    ├─→ Circuit Breaker (Day 2-3)
    └─→ WRR 调度器 (Day 2-3)

Week 3:
  Circuit Breaker 核心完成
    └─→ 集成到请求链路 (Day 1-2)

  WRR 调度器核心完成
    └─→ 健康检查模块 (Day 1-2)

  Unified Adapter (独立, Day 1-4)

Week 4:
  所有核心模块完成
    ├─→ Content Safety 增强 (Day 1-2)
    ├─→ 优先级队列 (Day 3-4)
    └─→ Metrics 实现 (Day 1-5)

Week 5:
  所有功能完成
    └─→ 集成测试 + 验收 (Day 1-4)
```

---

## 6. 验收标准

### 6.1 功能验收

#### 基础优化
- [ ] Circuit Breaker 能在错误率 > 50% 时自动触发熔断
- [ ] Circuit Breaker Half-Open 状态探测成功后恢复 Closed
- [ ] Unified Adapter 支持 OpenAI + Anthropic 转换
- [ ] Adapter 转换成功率 > 99.9% (测试 10000 次)
- [ ] Content Safety 能正确计算评分 (0.0-1.0)
- [ ] 敏感词检测召回率 > 95%

#### 号池优化
- [ ] WRR 调度器按权重分配凭据，偏差 < 5% (测试 10000 次)
- [ ] 健康检查失败后凭据权重自动降低
- [ ] 等待队列 P99 延迟 < 100ms
- [ ] 优先级队列正确排序 (Premium > Standard > Free)

### 6.2 性能验收

| 指标 | 目标 | 测试方法 |
|------|------|---------|
| Circuit Breaker 开销 | < 1ms | 压测 10000 QPS |
| Adapter 转换延迟 | < 0.5ms | 单元测试 benchmark |
| WRR 调度延迟 | < 0.1ms | 单元测试 benchmark |
| 凭据等待 P99 | < 100ms | 压测 1000 并发 |
| 内存占用增量 | < 50MB | 运行 1 小时采样 |

### 6.3 Metrics 验收

- [ ] 所有 14 个新 metrics 正常暴露
- [ ] Prometheus 能正常采集 (无 scrape errors)
- [ ] Grafana 看板展示 Phase 1 指标
- [ ] 告警规则能正常触发 (手动测试)

### 6.4 文档验收

- [ ] 每个模块有 README.md
- [ ] 关键函数有 godoc 注释
- [ ] Runbook 包含故障排查步骤
- [ ] 验收报告完整记录测试结果

---

## 7. Grafana 看板规划

### 7.1 基础优化看板

**Panel 1: Circuit Breaker 状态**
```promql
llm_gateway_circuit_breaker_state
```

**Panel 2: 熔断触发率**
```promql
rate(llm_gateway_circuit_breaker_triggered_total[5m])
```

**Panel 3: Adapter 转换成功率**
```promql
sum(rate(llm_gateway_adapter_requests_total{status="success"}[5m]))
/
sum(rate(llm_gateway_adapter_requests_total[5m]))
```

**Panel 4: Content Safety 评分分布**
```promql
histogram_quantile(0.95, rate(llm_gateway_content_safety_score_bucket[5m]))
```

### 7.2 号池优化看板

**Panel 1: 凭据池利用率**
```promql
llm_gateway_credential_pool_utilization
```

**Panel 2: 等待时间 P99**
```promql
histogram_quantile(0.99, rate(llm_gateway_credential_wait_time_seconds_bucket[5m]))
```

**Panel 3: 按凭据的请求分配数**
```promql
sum(rate(llm_gateway_credential_requests_allocated_total[5m])) by (credential_id)
```

**Panel 4: 队列长度**
```promql
llm_gateway_credential_queue_length
```

---

## 8. 风险与缓解

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|---------|
| WRR 算法并发 bug | 🟡 中 | 🔴 高 | 单元测试 1000 goroutine 压测，code review |
| Circuit Breaker 误触发 | 🟡 中 | 🟡 中 | 阈值可配置，增加最小请求数限制 |
| Adapter 转换遗漏字段 | 🟡 中 | 🟡 中 | 单元测试覆盖所有字段，集成测试端到端 |
| 数据库迁移锁表时间长 | 🟢 低 | 🟡 中 | 使用 CONCURRENTLY 创建索引，低峰期执行 |
| 性能下降 | 🟡 中 | 🟡 中 | 压测对比 baseline，优化热路径 |

---

## 9. 相关文档

- [Phase 0 实施计划](Phase0-实施计划.md)
- [Phase 0 验收报告](Phase0-验收报告.md)
- [request_logs Schema v2](request_logs_schema_v2.md)
- [Prometheus Metrics 命名规范](prometheus_metrics_naming.md)
- [横向审计报告](横向审计报告.md)
- [号池优化/06-修订方案v2.md](号池优化/06-修订方案v2.md)

---

**创建日期**: 2026-07-18
**预计开始**: 2026-07-22
**预计完成**: 2026-08-15
**当前状态**: 📋 待开始
