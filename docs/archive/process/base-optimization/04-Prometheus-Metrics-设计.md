# Prometheus Metrics 集成设计

> **版本**: v1.0
> **日期**: 2026-07-19
> **状态**: Draft
> **负责人**: Infrastructure Team

---

## 1. 背景与目标

### 1.1 背景

Phase 1 实现的各模块需要统一的 Prometheus 监控体系：
- Circuit Breaker 熔断状态
- Unified Adapter 转换性能
- WRR 调度器负载分布
- Content Safety 拦截率

### 1.2 目标

- ✅ **统一命名**: 遵循 Prometheus 命名规范
- ✅ **分层监控**: 服务级 + 模块级 + 接口级
- ✅ **关键指标**: 延迟/错误率/吞吐/利用率
- ✅ **告警就绪**: 提供预置告警规则

---

## 2. 指标体系

### 2.1 命名规范

```
<namespace>_<subsystem>_<metric>_<unit>

namespace:  llm_gateway
subsystem:  circuit|adapter|scheduler|safety|pool
metric:     具体指标名
unit:       total|seconds|bytes|ratio
```

### 2.2 指标清单 (35 个)

#### Circuit Breaker (8 个)

```go
// 请求总数
llm_gateway_circuit_requests_total{state="closed|open|half_open"}

// 成功/失败数
llm_gateway_circuit_successes_total
llm_gateway_circuit_failures_total

// 状态转换
llm_gateway_circuit_state_transitions_total{from="",to=""}

// 当前状态
llm_gateway_circuit_state{state="closed|open|half_open"} = 1

// 请求延迟
llm_gateway_circuit_request_duration_seconds{quantile="0.5|0.95|0.99"}

// 错误率
llm_gateway_circuit_error_rate

// 触发次数
llm_gateway_circuit_trips_total
```

#### Unified Adapter (6 个)

```go
// 转换总数
llm_gateway_adapter_conversions_total{provider="openai|anthropic",direction="request|response"}

// 转换延迟
llm_gateway_adapter_conversion_duration_seconds{provider=""}

// 转换失败
llm_gateway_adapter_conversion_failures_total{provider="",reason=""}

// 系统消息提取
llm_gateway_adapter_system_extractions_total{provider="anthropic"}

// Token 使用
llm_gateway_adapter_tokens_total{provider="",type="prompt|completion"}

// 活跃适配器
llm_gateway_adapter_active{provider=""} = 1
```

#### WRR Scheduler (7 个)

```go
// 调度总数
llm_gateway_scheduler_selections_total

// 每个凭据选择次数
llm_gateway_scheduler_selections_by_credential{credential_id=""}

// 权重分布
llm_gateway_scheduler_weight{credential_id=""}

// 当前权重
llm_gateway_scheduler_current_weight{credential_id=""}

// 有效权重
llm_gateway_scheduler_effective_weight{credential_id=""}

// 调度延迟
llm_gateway_scheduler_selection_duration_seconds

// 可用凭据数
llm_gateway_scheduler_available_credentials
```

#### Content Safety (9 个)

```go
// 检测总数
llm_gateway_safety_checks_total{type="request|response"}

// 拦截总数
llm_gateway_safety_blocked_total{severity="low|medium|high|critical"}

// 动作分布
llm_gateway_safety_actions_total{action="allow|block|warn|sanitize"}

// 规则命中
llm_gateway_safety_rule_hits_total{rule_id="",rule_name=""}

// 检测延迟
llm_gateway_safety_check_duration_seconds

// 误报率
llm_gateway_safety_false_positive_rate

// 漏报率
llm_gateway_safety_false_negative_rate

// 规则数量
llm_gateway_safety_rules_count{enabled="true|false"}

// 白名单命中
llm_gateway_safety_whitelist_hits_total
```

#### Pool (5 个)

```go
// 池容量
llm_gateway_pool_capacity{pool_id=""}

// 池利用率
llm_gateway_pool_utilization{pool_id=""}

// 活跃凭据
llm_gateway_pool_active_credentials{pool_id=""}

// 健康凭据
llm_gateway_pool_healthy_credentials{pool_id=""}

// 池请求总数
llm_gateway_pool_requests_total{pool_id="",status="success|failure"}
```

---

## 3. 实施方案

### 3.1 统一 Metrics 包

```
metrics/
├── interface.go      - 核心接口
├── prometheus.go     - Prometheus 实现
├── circuit.go        - Circuit Breaker 指标
├── adapter.go        - Adapter 指标
├── scheduler.go      - Scheduler 指标
├── safety.go         - Safety 指标
├── pool.go           - Pool 指标
└── metrics_test.go   - 单元测试
```

### 3.2 接口设计

```go
// MetricsRecorder 统一指标记录器
type MetricsRecorder interface {
    // Circuit Breaker
    RecordCircuitRequest(state string)
    RecordCircuitSuccess()
    RecordCircuitFailure()
    RecordCircuitStateChange(from, to string)
    RecordCircuitTrip()
    ObserveCircuitLatency(duration time.Duration)

    // Adapter
    RecordAdapterConversion(provider, direction string, duration time.Duration)
    RecordAdapterFailure(provider, reason string)

    // Scheduler
    RecordSchedulerSelection(credentialID string, duration time.Duration)
    UpdateSchedulerWeight(credentialID string, weight int)

    // Safety
    RecordSafetyCheck(checkType string, duration time.Duration)
    RecordSafetyAction(action string, severity string)
    RecordSafetyRuleHit(ruleID, ruleName string)

    // Pool
    UpdatePoolUtilization(poolID string, utilization float64)
    RecordPoolRequest(poolID, status string)
}
```

---

## 4. 集成方式

### 4.1 各模块集成

**Circuit Breaker**:
```go
func (cb *CircuitBreaker) Do(ctx context.Context, fn func() error) error {
    state := cb.State()
    metrics.RecordCircuitRequest(state.String())

    start := time.Now()
    defer func() {
        metrics.ObserveCircuitLatency(time.Since(start))
    }()

    err := fn()
    if err != nil {
        metrics.RecordCircuitFailure()
    } else {
        metrics.RecordCircuitSuccess()
    }
    return err
}
```

**Unified Adapter**:
```go
func (a *OpenAIAdapter) ToProviderRequest(req *UnifiedRequest) (*ProviderRequest, error) {
    start := time.Now()
    defer func() {
        metrics.RecordAdapterConversion("openai", "request", time.Since(start))
    }()

    // ... conversion logic
}
```

**WRR Scheduler**:
```go
func (s *WRRScheduler) Select(ctx context.Context) (*Credential, error) {
    start := time.Now()
    cred, err := s.selectBest()

    if err == nil {
        metrics.RecordSchedulerSelection(
            strconv.Itoa(cred.ID),
            time.Since(start),
        )
    }
    return cred, err
}
```

**Content Safety**:
```go
func (cf *ContentFilter) check(content string) (*CheckResult, error) {
    start := time.Now()
    defer func() {
        metrics.RecordSafetyCheck("request", time.Since(start))
    }()

    result := cf.applyRules(content, hits)
    metrics.RecordSafetyAction(string(result.Action), string(highestSeverity))

    return result, nil
}
```

---

## 5. Grafana Dashboard

### 5.1 面板布局

```
+----------------------------------+----------------------------------+
| Circuit Breaker 状态              | Adapter 转换性能                  |
| - 请求总数 (按状态)               | - 转换延迟 P50/P95/P99           |
| - 错误率                         | - 失败率 (按提供商)               |
| - 熔断次数                       | - Token 使用量                    |
+----------------------------------+----------------------------------+
| WRR 调度器负载                    | Content Safety 拦截              |
| - 选择分布 (饼图)                 | - 拦截率                         |
| - 权重趋势                       | - 规则命中 Top 10                |
| - 调度延迟                       | - 动作分布                       |
+----------------------------------+----------------------------------+
| Pool 利用率                       | 全局 QPS                         |
| - 容量/使用                      | - 总请求数                       |
| - 健康凭据数                     | - 成功率                         |
| - 利用率趋势                     | - 平均延迟                       |
+----------------------------------+----------------------------------+
```

### 5.2 告警规则

```yaml
groups:
  - name: llm_gateway_phase1
    interval: 30s
    rules:
      # Circuit Breaker
      - alert: CircuitBreakerOpen
        expr: llm_gateway_circuit_state{state="open"} == 1
        for: 1m
        labels:
          severity: warning
        annotations:
          summary: "Circuit breaker is open"

      - alert: HighCircuitErrorRate
        expr: llm_gateway_circuit_error_rate > 0.1
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "Circuit error rate > 10%"

      # Content Safety
      - alert: HighSafetyBlockRate
        expr: rate(llm_gateway_safety_blocked_total[5m]) > 100
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High content block rate"

      # Scheduler
      - alert: UnbalancedScheduler
        expr: stddev(llm_gateway_scheduler_selections_by_credential) > 1000
        for: 10m
        labels:
          severity: info
        annotations:
          summary: "Scheduler load unbalanced"

      # Pool
      - alert: LowPoolUtilization
        expr: llm_gateway_pool_utilization < 0.2
        for: 30m
        labels:
          severity: info
        annotations:
          summary: "Pool underutilized"
```

---

## 6. 实施计划

### 6.1 里程碑

| 里程碑 | 产物 | 预计 |
|--------|------|------|
| M1: Metrics 接口 | `metrics/interface.go` | 15 min |
| M2: Prometheus 实现 | `metrics/prometheus.go` | 20 min |
| M3: 各模块集成 | 4 个模块 | 30 min |
| M4: 单元测试 | `metrics_test.go` | 15 min |
| M5: Grafana Dashboard | `dashboard.json` | 10 min |
| **总计** | | **1.5 小时** |

---

## 7. 参考

- Prometheus Best Practices: https://prometheus.io/docs/practices/naming/
- Grafana Dashboard: https://grafana.com/docs/grafana/latest/dashboards/
- Go Prometheus Client: https://github.com/prometheus/client_golang
