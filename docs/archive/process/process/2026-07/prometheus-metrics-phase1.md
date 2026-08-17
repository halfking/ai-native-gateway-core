# Phase 1 Prometheus Metrics 规范

**版本**: v1.0
**更新时间**: 2026-07-19
**覆盖模块**: Circuit Breaker, Unified Adapter, Content Safety

---

## 指标命名规范

### 前缀规则
- 所有指标以 `llm_gateway_` 开头
- 模块名作为第二部分: `circuit_breaker_`, `adapter_`, `content_safety_`

### 后缀规则
| 后缀 | 类型 | 含义 | 示例 |
|------|------|------|------|
| `_total` | Counter | 累计总数 | `requests_total` |
| `_seconds` | Histogram | 时间分布 | `duration_seconds` |
| `_bytes` | Histogram | 大小分布 | `request_bytes` |
| 无后缀 | Gauge | 当前值 | `state`, `active_count` |

---

## 1. Circuit Breaker 指标 (circuit/)

### 1.1 状态指标

```prometheus
# 熔断器状态 (0=closed, 1=open, 2=half_open)
llm_gateway_circuit_breaker_state{provider="anthropic"} 0

# 熔断触发次数
llm_gateway_circuit_breaker_triggered_total{provider="anthropic", reason="error_rate"} 5

# 被拒绝的请求数
llm_gateway_circuit_breaker_rejected_total{provider="anthropic"} 120
```

### 1.2 请求指标

```prometheus
# 请求总数
llm_gateway_circuit_breaker_requests_total{provider="anthropic", result="success"} 1000
llm_gateway_circuit_breaker_requests_total{provider="anthropic", result="error"} 50

# 错误率
llm_gateway_circuit_breaker_error_rate{provider="anthropic"} 0.05
```

### 1.3 Labels

| Label | 说明 | 可选值 |
|-------|------|--------|
| `provider` | 提供商标识 | anthropic, openai, etc. |
| `reason` | 触发原因 | error_rate, timeout, manual |
| `result` | 结果 | success, error |

---

## 2. Unified Adapter 指标 (adapter/unified/)

### 2.1 转换指标

```prometheus
# 格式转换总数
llm_gateway_adapter_conversions_total{provider="anthropic", direction="to_unified", result="success"} 500
llm_gateway_adapter_conversions_total{provider="anthropic", direction="from_unified", result="success"} 500

# 转换耗时
llm_gateway_adapter_conversion_duration_seconds{provider="anthropic", direction="to_unified", quantile="0.5"} 0.002
llm_gateway_adapter_conversion_duration_seconds{provider="anthropic", direction="to_unified", quantile="0.99"} 0.010
```

### 2.2 错误指标

```prometheus
# 转换错误数
llm_gateway_adapter_errors_total{provider="anthropic", error_type="validation"} 10
llm_gateway_adapter_errors_total{provider="anthropic", error_type="conversion"} 2
```

### 2.3 数据量指标

```prometheus
# 请求大小分布
llm_gateway_adapter_request_bytes{provider="anthropic", quantile="0.5"} 1024
llm_gateway_adapter_request_bytes{provider="anthropic", quantile="0.99"} 10240

# 响应大小分布
llm_gateway_adapter_response_bytes{provider="anthropic", quantile="0.5"} 2048
llm_gateway_adapter_response_bytes{provider="anthropic", quantile="0.99"} 20480
```

### 2.4 活跃状态

```prometheus
# 活跃 provider 数量
llm_gateway_adapter_active_providers 5
```

### 2.5 Labels

| Label | 说明 | 可选值 |
|-------|------|--------|
| `provider` | 提供商标识 | anthropic, openai, etc. |
| `direction` | 转换方向 | to_unified, from_unified |
| `result` | 结果 | success, error |
| `error_type` | 错误类型 | validation, conversion, unknown |

---

## 3. Content Safety 指标 (security/sensitive/)

### 3.1 检查指标

```prometheus
# 安全检查总数
llm_gateway_content_safety_checks_total{action="allow"} 8000
llm_gateway_content_safety_checks_total{action="warn"} 150
llm_gateway_content_safety_checks_total{action="block"} 50
```

### 3.2 评分指标

```prometheus
# 安全评分分布
llm_gateway_content_safety_score{category="political", quantile="0.5"} 0.2
llm_gateway_content_safety_score{category="political", quantile="0.99"} 0.8

# 匹配数量分布
llm_gateway_content_safety_matches{level="P0", quantile="0.5"} 1
llm_gateway_content_safety_matches{level="P0", quantile="0.99"} 3
```

### 3.3 动作指标

```prometheus
# 阻止请求数
llm_gateway_content_safety_blocked_total{category="terrorism"} 30
llm_gateway_content_safety_blocked_total{category="political"} 20

# 警告数量
llm_gateway_content_safety_warnings_total{category="political"} 100
llm_gateway_content_safety_warnings_total{category="general"} 50
```

### 3.4 Labels

| Label | 说明 | 可选值 |
|-------|------|--------|
| `action` | 建议动作 | allow, warn, block |
| `category` | 内容类别 | political, terrorism, etc. |
| `level` | 告警等级 | P0, P1, P2 |

---

## 4. 跨模块查询示例

### 4.1 整体健康度

```promql
# 熔断器打开的 provider 数量
sum(llm_gateway_circuit_breaker_state == 1)

# 总体请求成功率
sum(rate(llm_gateway_circuit_breaker_requests_total{result="success"}[5m]))
/
sum(rate(llm_gateway_circuit_breaker_requests_total[5m]))
```

### 4.2 适配器性能

```promql
# P99 转换耗时
histogram_quantile(0.99,
  sum(rate(llm_gateway_adapter_conversion_duration_seconds_bucket[5m])) by (le, provider)
)

# 转换错误率
sum(rate(llm_gateway_adapter_conversions_total{result="error"}[5m])) by (provider)
/
sum(rate(llm_gateway_adapter_conversions_total[5m])) by (provider)
```

### 4.3 内容安全

```promql
# 阻止率
sum(rate(llm_gateway_content_safety_checks_total{action="block"}[5m]))
/
sum(rate(llm_gateway_content_safety_checks_total[5m]))

# 高风险内容占比 (score >= 0.6)
histogram_quantile(0.9,
  sum(rate(llm_gateway_content_safety_score_bucket[5m])) by (le)
)
```

---

## 5. Grafana Dashboard 建议

### 5.1 Circuit Breaker 面板

```
Row 1: 概览
- 熔断器状态 (State Chart)
- 熔断触发次数 (Counter)
- 当前错误率 (Gauge)

Row 2: 请求详情
- 请求成功/失败率 (Time Series)
- 拒绝请求数 (Time Series)
```

### 5.2 Adapter 面板

```
Row 1: 转换性能
- 转换 QPS (Time Series)
- P50/P99 转换耗时 (Time Series)

Row 2: 数据量
- 请求/响应大小分布 (Heatmap)
- 错误率 (Time Series)
```

### 5.3 Content Safety 面板

```
Row 1: 检查概览
- Allow/Warn/Block 分布 (Pie Chart)
- 检查 QPS (Time Series)

Row 2: 风险分析
- 评分分布 (Histogram)
- 高危类别 Top 5 (Bar Chart)
```

---

## 6. 告警规则建议

### 6.1 Circuit Breaker 告警

```yaml
groups:
  - name: circuit_breaker
    rules:
      - alert: CircuitBreakerOpen
        expr: llm_gateway_circuit_breaker_state == 1
        for: 1m
        annotations:
          summary: "熔断器打开 {{ $labels.provider }}"

      - alert: HighErrorRate
        expr: llm_gateway_circuit_breaker_error_rate > 0.1
        for: 5m
        annotations:
          summary: "错误率过高 {{ $labels.provider }}: {{ $value }}"
```

### 6.2 Adapter 告警

```yaml
  - name: adapter
    rules:
      - alert: HighConversionLatency
        expr: |
          histogram_quantile(0.99,
            sum(rate(llm_gateway_adapter_conversion_duration_seconds_bucket[5m])) by (le, provider)
          ) > 0.1
        for: 5m
        annotations:
          summary: "转换延迟过高 {{ $labels.provider }}: {{ $value }}s"
```

### 6.3 Content Safety 告警

```yaml
  - name: content_safety
    rules:
      - alert: HighBlockRate
        expr: |
          sum(rate(llm_gateway_content_safety_checks_total{action="block"}[5m]))
          /
          sum(rate(llm_gateway_content_safety_checks_total[5m]))
          > 0.05
        for: 10m
        annotations:
          summary: "阻止率过高: {{ $value }}"
```

---

## 7. 指标采集配置

### 7.1 Prometheus 配置

```yaml
scrape_configs:
  - job_name: 'llm-gateway'
    scrape_interval: 15s
    static_configs:
      - targets:
          - 'localhost:8080'  # /metrics endpoint
    metric_relabel_configs:
      # 只采集 Phase 1 指标
      - source_labels: [__name__]
        regex: 'llm_gateway_(circuit_breaker|adapter|content_safety)_.*'
        action: keep
```

---

## 8. 指标总览

### 统计

| 模块 | Counter | Histogram | Gauge | 总计 |
|------|---------|-----------|-------|------|
| Circuit Breaker | 3 | 0 | 2 | 5 |
| Unified Adapter | 2 | 3 | 1 | 6 |
| Content Safety | 3 | 2 | 0 | 5 |
| **总计** | **8** | **5** | **3** | **16** |

### 所有指标清单

**Circuit Breaker (5)**:
1. `llm_gateway_circuit_breaker_state`
2. `llm_gateway_circuit_breaker_triggered_total`
3. `llm_gateway_circuit_breaker_rejected_total`
4. `llm_gateway_circuit_breaker_requests_total`
5. `llm_gateway_circuit_breaker_error_rate`

**Unified Adapter (6)**:
1. `llm_gateway_adapter_conversions_total`
2. `llm_gateway_adapter_conversion_duration_seconds`
3. `llm_gateway_adapter_errors_total`
4. `llm_gateway_adapter_request_bytes`
5. `llm_gateway_adapter_response_bytes`
6. `llm_gateway_adapter_active_providers`

**Content Safety (5)**:
1. `llm_gateway_content_safety_checks_total`
2. `llm_gateway_content_safety_score`
3. `llm_gateway_content_safety_matches`
4. `llm_gateway_content_safety_blocked_total`
5. `llm_gateway_content_safety_warnings_total`

---

**文档维护**: Infrastructure Team
**下次更新**: Phase 2 完成后
