# Prometheus Metrics 命名规范

> **版本**: v1.0  
> **日期**: 2026-07-18  
> **状态**: 📋 待审查  
> **目的**: 统一 LLM Gateway 四个优化方向的 Prometheus metrics 命名

---

## 1. 核心原则

### 1.1 Prometheus 官方最佳实践

遵循 [Prometheus Metric and Label Naming](https://prometheus.io/docs/practices/naming/) 规范：

1. **Metric 名称**使用 `snake_case`
2. **Label 名称**使用 `snake_case`
3. **单位后缀**必须附加到 metric 名称（`_seconds`, `_bytes`, `_total`）
4. **累计计数器**必须以 `_total` 结尾
5. **统一前缀**便于分组和查询

### 1.2 LLM Gateway 约定

| 约定 | 规则 | 示例 |
|------|------|------|
| **统一前缀** | 所有 metrics 以 `llm_gateway_` 开头 | `llm_gateway_requests_total` |
| **类型后缀** | Counter: `_total`<br>Gauge: 无<br>Histogram: `_seconds`, `_bytes`<br>Summary: `_seconds`, `_bytes` | `llm_gateway_cpu_usage`<br>`llm_gateway_request_duration_seconds` |
| **Label 值** | 小写，用下划线分隔 | `status="success"` (不是 `Success`) |
| **避免高基数** | Label 值不超过 100 种 | ❌ `user_id="12345"`<br>✅ `tenant_id="10"` |

---

## 2. Metric 类型定义

### 2.1 Counter (累计计数器)

**用途**: 只增不减的累计值（请求数、错误数、字节数）

**命名规则**: 必须以 `_total` 结尾

**示例**:
```prometheus
# 请求总数
llm_gateway_requests_total{method="POST", endpoint="/v1/chat/completions", status="success"}

# 错误总数
llm_gateway_errors_total{provider="openai", error_type="timeout"}

# 发送字节数
llm_gateway_bytes_sent_total{provider="anthropic"}
```

### 2.2 Gauge (瞬时值)

**用途**: 可增可减的瞬时值（当前连接数、队列长度、利用率）

**命名规则**: 不加 `_total` 后缀，通常表示"当前状态"

**示例**:
```prometheus
# GPU 利用率
llm_gateway_gpu_utilization{gpu_type="a100", pool_id="1"}

# 当前连接数
llm_gateway_active_connections{provider="openai"}

# 队列长度
llm_gateway_queue_length{pool_id="2"}
```

### 2.3 Histogram (直方图)

**用途**: 观察值分布（延迟、请求大小）

**命名规则**: 使用单位后缀（`_seconds`, `_bytes`），自动生成 `_bucket`, `_sum`, `_count`

**示例**:
```prometheus
# 请求延迟分布
llm_gateway_request_duration_seconds{method="POST", status="success"}

# 响应大小分布
llm_gateway_response_size_bytes{model="gpt-4"}
```

**自动生成的 metrics**:
- `llm_gateway_request_duration_seconds_bucket{le="0.1"}` — 小于 0.1s 的请求数
- `llm_gateway_request_duration_seconds_sum` — 总耗时
- `llm_gateway_request_duration_seconds_count` — 总请求数

### 2.4 Summary (摘要)

**用途**: 客户端预计算的分位数（P50, P95, P99）

**命名规则**: 同 Histogram，但不生成 `_bucket`

**示例**:
```prometheus
# 网络延迟摘要
llm_gateway_network_latency_seconds{quantile="0.99", provider="openai"}
```

**推荐**: 优先使用 **Histogram**（服务端聚合更灵活），Summary 仅在客户端已预计算时使用。

---

## 3. 四个优化方向的 Metrics 规范

### 3.1 价格优化 (Phase 2)

#### 3.1.1 成本追踪

```prometheus
# 按资源类型拆分的成本（USD）
llm_gateway_cost_breakdown_usd{tenant_id, model, resource_type}
# resource_type: gpu | memory | network | shared

# 单次请求总成本
llm_gateway_request_cost_usd{tenant_id, model, pricing_tier}
# pricing_tier: reserved | on_demand | spot

# 成本累计（按天聚合）
llm_gateway_daily_cost_usd_total{tenant_id, date}
```

#### 3.1.2 资源池监控

```prometheus
# GPU 资源池利用率（0-100%）
llm_gateway_compute_pool_utilization{pool_id, gpu_type}

# Spot 实例价格（USD/hour）
llm_gateway_spot_price_usd{pool_id, tier}

# 资源分配请求总数
llm_gateway_resource_allocation_total{pool_id, status}
# status: allocated | pending | rejected
```

#### 3.1.3 动态定价

```prometheus
# 当前 markup 倍数
llm_gateway_markup_multiplier{model, pricing_tier}

# 价格调整事件总数
llm_gateway_price_adjustment_total{reason}
# reason: spot_price_change | demand_spike | manual
```

---

### 3.2 基础优化 (Phase 1)

#### 3.2.1 Content Safety

```prometheus
# 内容安全违规总数
llm_gateway_content_safety_violations_total{category}
# category: sensitive_word | prompt_injection | policy_violation

# 内容安全评分分布（0-1）
llm_gateway_content_safety_score{tenant_id, model}
```

#### 3.2.2 Circuit Breaker

```prometheus
# 熔断器状态（0=closed, 1=open, 2=half_open）
llm_gateway_circuit_breaker_state{provider}

# 熔断触发总数
llm_gateway_circuit_breaker_triggered_total{provider, reason}
# reason: error_rate | timeout_rate | manual

# 被熔断拒绝的请求数
llm_gateway_circuit_breaker_rejected_total{provider}
```

#### 3.2.3 Unified Adapter

```prometheus
# Adapter 请求总数
llm_gateway_adapter_requests_total{adapter_version, provider, status}

# Adapter 转换错误总数
llm_gateway_adapter_conversion_errors_total{adapter_version, error_type}

# 上游提供商请求总数
llm_gateway_upstream_requests_total{upstream_provider, status}
```

---

### 3.3 号池优化 (Phase 1)

#### 3.3.1 凭据池监控

```prometheus
# 凭据池利用率（0-100%）
llm_gateway_credential_pool_utilization{pool_id, strategy}
# strategy: round_robin | wrr | least_loaded

# 等待凭据的时间分布（秒）
llm_gateway_credential_wait_time_seconds{pool_id}

# 凭据分配总数
llm_gateway_credential_allocation_total{pool_id, status}
# status: allocated | timeout | pool_exhausted
```

#### 3.3.2 凭据健康

```prometheus
# 凭据健康检查失败总数
llm_gateway_credential_health_check_failures_total{credential_id, reason}

# 凭据复用次数
llm_gateway_credential_reuse_count{credential_id}

# 凭据池队列长度
llm_gateway_credential_queue_length{pool_id}
```

#### 3.3.3 WRR 调度

```prometheus
# 凭据权重（用于 WRR）
llm_gateway_credential_weight{credential_id, pool_id}

# 按凭据的请求分配数
llm_gateway_credential_requests_allocated_total{credential_id, pool_id}
```

---

### 3.4 衰减优化 (Phase 3)

#### 3.4.1 网络延迟

```prometheus
# 网络往返延迟分布（秒）
llm_gateway_network_latency_seconds{provider, protocol}
# protocol: http/1.1 | h2 | h3

# 流式响应首 token 延迟（秒）
llm_gateway_streaming_first_token_seconds{model, provider}

# DNS 解析时间（秒）
llm_gateway_dns_resolution_seconds{provider}
```

#### 3.4.2 连接管理

```prometheus
# 连接复用总数
llm_gateway_connection_reuse_total{provider, reused}
# reused: true | false

# 当前活跃连接数
llm_gateway_active_connections{provider, protocol}

# 连接池耗尽事件总数
llm_gateway_connection_pool_exhausted_total{provider}
```

#### 3.4.3 协议分布

```prometheus
# 按协议版本的请求总数
llm_gateway_requests_by_protocol_total{protocol, tls_version}

# HTTP/3 回退到 HTTP/2 的次数
llm_gateway_protocol_fallback_total{from_protocol, to_protocol}
```

---

## 4. 标准 Label 定义

### 4.1 通用 Labels

| Label | 说明 | 值域 | 示例 |
|-------|------|------|------|
| `tenant_id` | 租户 ID | 数字字符串，≤100 种 | `"10"`, `"25"` |
| `model` | 模型名称 | 小写，用下划线 | `"gpt-4"`, `"claude-3-opus"` |
| `provider` | 提供商 | 小写 | `"openai"`, `"anthropic"` |
| `status` | 状态 | 预定义枚举 | `"success"`, `"error"`, `"timeout"` |
| `method` | HTTP 方法 | 大写 | `"GET"`, `"POST"` |
| `endpoint` | API 端点 | 路径 | `"/v1/chat/completions"` |

### 4.2 价格优化 Labels

| Label | 说明 | 值域 |
|-------|------|------|
| `pool_id` | 资源池 ID | `"1"`, `"2"`, `"3"` |
| `gpu_type` | GPU 类型 | `"a100"`, `"h100"`, `"rtx4090"` |
| `pricing_tier` | 定价层级 | `"reserved"`, `"on_demand"`, `"spot"` |
| `resource_type` | 资源类型 | `"gpu"`, `"memory"`, `"network"`, `"shared"` |

### 4.3 基础优化 Labels

| Label | 说明 | 值域 |
|-------|------|------|
| `category` | 内容安全类别 | `"sensitive_word"`, `"prompt_injection"`, `"policy_violation"` |
| `adapter_version` | Adapter 版本 | `"v1.0"`, `"v1.1"` |
| `upstream_provider` | 上游提供商 | `"openai"`, `"azure_openai"` |

### 4.4 号池优化 Labels

| Label | 说明 | 值域 |
|-------|------|------|
| `credential_id` | 凭据 ID | 数字字符串 |
| `strategy` | 调度策略 | `"round_robin"`, `"wrr"`, `"least_loaded"` |

### 4.5 衰减优化 Labels

| Label | 说明 | 值域 |
|-------|------|------|
| `protocol` | HTTP 协议版本 | `"http/1.1"`, `"h2"`, `"h3"` |
| `tls_version` | TLS 版本 | `"TLS 1.2"`, `"TLS 1.3"` |
| `edge_node_id` | 边缘节点 ID | 节点标识符 |
| `reused` | 是否复用连接 | `"true"`, `"false"` |

---

## 5. 避免的反模式

### 5.1 高基数 Labels ❌

**错误**: 使用用户 ID 作为 label
```prometheus
# ❌ 错误：10 万用户 = 10 万个时间序列
llm_gateway_requests_total{user_id="12345"}
```

**正确**: 按租户聚合
```prometheus
# ✅ 正确：按租户聚合（租户数 < 100）
llm_gateway_requests_total{tenant_id="10"}
```

### 5.2 动态 Label 值 ❌

**错误**: 使用时间戳或随机值作为 label
```prometheus
# ❌ 错误：每个请求一个时间序列
llm_gateway_request_timestamp{timestamp="2026-07-18T14:00:00Z"}
```

**正确**: 时间戳用于值，不是 label
```prometheus
# ✅ 正确：时间戳作为 metric 值（或使用默认时间戳）
llm_gateway_last_request_time
```

### 5.3 缺少单位后缀 ❌

**错误**:
```prometheus
# ❌ 错误：无法判断单位
llm_gateway_request_duration{model="gpt-4"}
llm_gateway_response_size{model="gpt-4"}
```

**正确**:
```prometheus
# ✅ 正确：明确单位
llm_gateway_request_duration_seconds{model="gpt-4"}
llm_gateway_response_size_bytes{model="gpt-4"}
```

### 5.4 混淆 Counter 和 Gauge ❌

**错误**: Counter 不加 `_total` 后缀
```prometheus
# ❌ 错误：累计值应加 _total
llm_gateway_requests{status="success"}
```

**正确**:
```prometheus
# ✅ 正确：Counter 必须以 _total 结尾
llm_gateway_requests_total{status="success"}
```

---

## 6. PromQL 查询示例

### 6.1 请求速率（QPS）

```promql
# 过去 5 分钟的平均 QPS
rate(llm_gateway_requests_total[5m])

# 按模型分组的 QPS
sum(rate(llm_gateway_requests_total[5m])) by (model)
```

### 6.2 错误率

```promql
# 5xx 错误率（过去 5 分钟）
sum(rate(llm_gateway_requests_total{status=~"5.."}[5m])) 
/ 
sum(rate(llm_gateway_requests_total[5m]))

# 按提供商的错误率
sum(rate(llm_gateway_errors_total[5m])) by (provider)
/ 
sum(rate(llm_gateway_requests_total[5m])) by (provider)
```

### 6.3 延迟百分位数

```promql
# P99 延迟（过去 5 分钟）
histogram_quantile(0.99, 
  rate(llm_gateway_request_duration_seconds_bucket[5m])
)

# 按模型的 P95 延迟
histogram_quantile(0.95, 
  sum(rate(llm_gateway_request_duration_seconds_bucket[5m])) by (model, le)
)
```

### 6.4 成本追踪

```promql
# 每小时成本趋势
sum(increase(llm_gateway_daily_cost_usd_total[1h])) by (tenant_id)

# GPU 成本占比
sum(llm_gateway_cost_breakdown_usd{resource_type="gpu"}) 
/ 
sum(llm_gateway_cost_breakdown_usd)
```

### 6.5 凭据池利用率

```promql
# 当前凭据池利用率
llm_gateway_credential_pool_utilization

# 平均等待时间（秒）
rate(llm_gateway_credential_wait_time_seconds_sum[5m])
/
rate(llm_gateway_credential_wait_time_seconds_count[5m])
```

---

## 7. Grafana 看板建议

### 7.1 总览看板 (Overview Dashboard)

**Panel 1: 请求速率**
```promql
sum(rate(llm_gateway_requests_total[5m]))
```

**Panel 2: 错误率**
```promql
sum(rate(llm_gateway_requests_total{status=~"5.."}[5m])) 
/ 
sum(rate(llm_gateway_requests_total[5m]))
```

**Panel 3: P99 延迟**
```promql
histogram_quantile(0.99, rate(llm_gateway_request_duration_seconds_bucket[5m]))
```

**Panel 4: 按模型的请求分布**
```promql
sum(rate(llm_gateway_requests_total[5m])) by (model)
```

### 7.2 成本看板 (Cost Dashboard)

**Panel 1: 按租户的每日成本**
```promql
sum(increase(llm_gateway_daily_cost_usd_total[24h])) by (tenant_id)
```

**Panel 2: 按资源类型的成本拆分**
```promql
sum(llm_gateway_cost_breakdown_usd) by (resource_type)
```

**Panel 3: Spot 价格趋势**
```promql
llm_gateway_spot_price_usd
```

### 7.3 号池看板 (Credential Pool Dashboard)

**Panel 1: 凭据池利用率**
```promql
llm_gateway_credential_pool_utilization
```

**Panel 2: 等待时间分布**
```promql
histogram_quantile(0.95, rate(llm_gateway_credential_wait_time_seconds_bucket[5m]))
```

**Panel 3: 按策略的分配数**
```promql
sum(rate(llm_gateway_credential_allocation_total[5m])) by (strategy, status)
```

---

## 8. 实施检查清单

### Phase 0 (当前)
- [x] 命名规范文档完成
- [ ] 技术评审通过

### Phase 1 (Week 2-5)
- [ ] 实现基础优化 metrics (Circuit Breaker + Content Safety)
- [ ] 实现号池优化 metrics (凭据池利用率 + WRR)
- [ ] 创建 Phase 1 Grafana 看板

### Phase 2 (Week 6-15)
- [ ] 实现价格优化 metrics (成本追踪 + 资源池)
- [ ] 创建成本看板
- [ ] 配置成本告警规则

### Phase 3 (Week 16+)
- [ ] 实现衰减优化 metrics (网络延迟 + 连接复用)
- [ ] 创建网络性能看板

---

## 9. Go 代码实现参考

### 9.1 使用 Prometheus Go Client

```go
package metrics

import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

var (
    // Counter: 请求总数
    RequestsTotal = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "llm_gateway_requests_total",
            Help: "Total number of requests processed",
        },
        []string{"method", "endpoint", "status"},
    )

    // Histogram: 请求延迟
    RequestDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "llm_gateway_request_duration_seconds",
            Help:    "Request duration in seconds",
            Buckets: prometheus.DefBuckets, // [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10]
        },
        []string{"method", "status"},
    )

    // Gauge: GPU 利用率
    GPUUtilization = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "llm_gateway_gpu_utilization",
            Help: "GPU utilization percentage (0-100)",
        },
        []string{"pool_id", "gpu_type"},
    )

    // Gauge: 凭据池利用率
    CredentialPoolUtilization = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "llm_gateway_credential_pool_utilization",
            Help: "Credential pool utilization percentage (0-100)",
        },
        []string{"pool_id", "strategy"},
    )
)
```

### 9.2 使用示例

```go
package handler

import (
    "time"
    "your-project/metrics"
)

func HandleRequest(w http.ResponseWriter, r *http.Request) {
    start := time.Now()
    
    // 业务逻辑
    status := processRequest(r)
    
    // 记录 metrics
    duration := time.Since(start).Seconds()
    
    metrics.RequestsTotal.WithLabelValues(
        r.Method, 
        r.URL.Path, 
        status,
    ).Inc()
    
    metrics.RequestDuration.WithLabelValues(
        r.Method, 
        status,
    ).Observe(duration)
}
```

### 9.3 暴露 /metrics 端点

```go
package main

import (
    "net/http"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
    // 业务路由
    http.HandleFunc("/api/v1/chat", HandleChat)
    
    // Prometheus metrics 端点
    http.Handle("/metrics", promhttp.Handler())
    
    http.ListenAndServe(":8080", nil)
}
```

---

## 10. 相关文档

- [Prometheus 官方命名规范](https://prometheus.io/docs/practices/naming/)
- [Prometheus Go Client](https://github.com/prometheus/client_golang)
- [Phase 0 实施计划](Phase0-实施计划.md)
- [request_logs Schema v2](request_logs_schema_v2.md)
- [横向审计报告](横向审计报告.md)

---

**作者**: Infrastructure Team  
**审阅者**: 待定  
**批准日期**: 待定  
**下次复审**: Phase 1 开始前 (2026-07-22)
