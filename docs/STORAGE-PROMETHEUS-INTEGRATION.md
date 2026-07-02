# Prometheus Metrics 集成指南

本文档说明如何在 LLM Gateway 中启用存储操作的 Prometheus 监控。

---

## 快速开始

### 1. 在 main.go 中启用 Metrics

```go
package main

import (
    "github.com/kaixuan/llm-gateway-go/domains/attachments"
    "github.com/prometheus/client_golang/prometheus/promhttp"
    "net/http"
)

func main() {
    // 初始化 Prometheus metrics
    storageMetrics := attachments.NewPrometheusMetrics("llm_gateway")
    attachments.SetMetrics(storageMetrics)
    
    // ... 其他初始化代码
    
    // 注册 Prometheus HTTP handler
    http.Handle("/metrics", promhttp.Handler())
    
    // 启动 HTTP 服务器（如果还没有）
    go http.ListenAndServe(":8781", nil)
    
    // ... 启动主服务
}
```

### 2. 验证 Metrics 端点

```bash
# 访问 metrics 端点
curl http://localhost:8781/metrics | grep storage

# 应该看到类似输出：
# llm_gateway_storage_operation_total{backend="local",op="save",success="true"} 123
# llm_gateway_storage_operation_duration_seconds_bucket{backend="local",op="save",le="0.001"} 45
# llm_gateway_storage_bytes_transferred_total{backend="local",op="save"} 1048576
```

---

## 可用指标

### 1. 操作计数器

**指标名**: `llm_gateway_storage_operation_total`  
**类型**: Counter  
**标签**:
- `op`: 操作类型 (save, load, delete, stat, exists)
- `backend`: 后端类型 (local, oss, s3)
- `success`: 是否成功 (true, false)

**用途**: 统计各类操作的总次数和成功率

**示例查询**:
```promql
# 每秒操作次数
rate(llm_gateway_storage_operation_total[5m])

# 错误率
rate(llm_gateway_storage_operation_total{success="false"}[5m]) 
/ 
rate(llm_gateway_storage_operation_total[5m])

# 按操作类型分组
sum by (op) (rate(llm_gateway_storage_operation_total[5m]))
```

---

### 2. 操作耗时直方图

**指标名**: `llm_gateway_storage_operation_duration_seconds`  
**类型**: Histogram  
**标签**:
- `op`: 操作类型
- `backend`: 后端类型

**Buckets**: 1ms, 2ms, 4ms, 8ms, 16ms, ..., 16s (指数增长)

**用途**: 分析操作延迟分布

**示例查询**:
```promql
# P50 延迟
histogram_quantile(0.5, 
  rate(llm_gateway_storage_operation_duration_seconds_bucket[5m])
)

# P95 延迟
histogram_quantile(0.95, 
  rate(llm_gateway_storage_operation_duration_seconds_bucket[5m])
)

# P99 延迟
histogram_quantile(0.99, 
  rate(llm_gateway_storage_operation_duration_seconds_bucket[5m])
)

# 平均延迟
rate(llm_gateway_storage_operation_duration_seconds_sum[5m])
/
rate(llm_gateway_storage_operation_duration_seconds_count[5m])
```

---

### 3. 传输字节数

**指标名**: `llm_gateway_storage_bytes_transferred_total`  
**类型**: Counter  
**标签**:
- `op`: 操作类型 (save, load)
- `backend`: 后端类型

**用途**: 统计上传/下载流量

**示例查询**:
```promql
# 每秒上传速度 (MB/s)
rate(llm_gateway_storage_bytes_transferred_total{op="save"}[5m]) / 1024 / 1024

# 每秒下载速度 (MB/s)
rate(llm_gateway_storage_bytes_transferred_total{op="load"}[5m]) / 1024 / 1024

# 总流量 (GB)
sum(llm_gateway_storage_bytes_transferred_total) / 1024 / 1024 / 1024
```

---

### 4. 健康检查

**指标名**: `llm_gateway_storage_health_check_total`  
**类型**: Counter  
**标签**:
- `backend`: 后端类型
- `success`: 是否成功

**用途**: 监控存储后端健康状态

**示例查询**:
```promql
# 健康检查失败率
rate(llm_gateway_storage_health_check_total{success="false"}[5m])
/
rate(llm_gateway_storage_health_check_total[5m])

# 最近一次健康检查是否成功
llm_gateway_storage_health_check_total{success="true"} offset 1m > 0
```

---

### 5. 重试统计

**指标名**: `llm_gateway_storage_retry_total`  
**类型**: Counter  
**标签**:
- `op`: 操作类型
- `success`: 重试后是否成功

**用途**: 统计重试操作

**示例查询**:
```promql
# 重试率
rate(llm_gateway_storage_retry_total[5m])
/
rate(llm_gateway_storage_operation_total[5m])

# 重试后仍然失败的比例
rate(llm_gateway_storage_retry_total{success="false"}[5m])
/
rate(llm_gateway_storage_retry_total[5m])
```

**指标名**: `llm_gateway_storage_retry_attempts`  
**类型**: Histogram  
**标签**:
- `op`: 操作类型

**Buckets**: 0, 1, 2, 3, 5, 10

**用途**: 分析重试次数分布

**示例查询**:
```promql
# 平均重试次数
rate(llm_gateway_storage_retry_attempts_sum[5m])
/
rate(llm_gateway_storage_retry_attempts_count[5m])
```

---

## Grafana 仪表板

### 推荐面板

#### 1. 操作QPS

```promql
sum by (op) (rate(llm_gateway_storage_operation_total[5m]))
```

**可视化**: 时序图 (Time series)  
**图例**: save, load, delete, stat, exists

---

#### 2. 成功率

```promql
sum(rate(llm_gateway_storage_operation_total{success="true"}[5m]))
/
sum(rate(llm_gateway_storage_operation_total[5m]))
* 100
```

**可视化**: 仪表盘 (Gauge)  
**单位**: %  
**阈值**: 
- 绿色: > 99.9%
- 黄色: 99% - 99.9%
- 红色: < 99%

---

#### 3. 延迟分布

```promql
# P50
histogram_quantile(0.50, sum by (le, op) (rate(llm_gateway_storage_operation_duration_seconds_bucket[5m])))

# P95
histogram_quantile(0.95, sum by (le, op) (rate(llm_gateway_storage_operation_duration_seconds_bucket[5m])))

# P99
histogram_quantile(0.99, sum by (le, op) (rate(llm_gateway_storage_operation_duration_seconds_bucket[5m])))
```

**可视化**: 时序图 (Time series)  
**单位**: s  
**图例**: P50, P95, P99

---

#### 4. 流量统计

```promql
# 上传
rate(llm_gateway_storage_bytes_transferred_total{op="save"}[5m]) / 1024 / 1024

# 下载
rate(llm_gateway_storage_bytes_transferred_total{op="load"}[5m]) / 1024 / 1024
```

**可视化**: 时序图 (Time series)  
**单位**: MB/s  
**图例**: Upload, Download

---

#### 5. 后端健康状态

```promql
1 - (
  rate(llm_gateway_storage_health_check_total{success="false"}[5m])
  /
  rate(llm_gateway_storage_health_check_total[5m])
)
```

**可视化**: Stat  
**单位**: %  
**映射**: 
- 100% → "Healthy" (绿色)
- < 100% → "Unhealthy" (红色)

---

## 告警规则

### 1. 高错误率告警

```yaml
- alert: StorageHighErrorRate
  expr: |
    (
      rate(llm_gateway_storage_operation_total{success="false"}[5m])
      /
      rate(llm_gateway_storage_operation_total[5m])
    ) > 0.05
  for: 5m
  labels:
    severity: warning
  annotations:
    summary: "存储错误率过高"
    description: "{{ $labels.backend }} 后端错误率 {{ $value | humanizePercentage }}，超过 5% 阈值"
```

---

### 2. 高延迟告警

```yaml
- alert: StorageHighLatency
  expr: |
    histogram_quantile(0.99,
      rate(llm_gateway_storage_operation_duration_seconds_bucket{op="save"}[5m])
    ) > 1.0
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: "存储延迟过高"
    description: "存储 save 操作 P99 延迟 {{ $value }}s，超过 1s 阈值"
```

---

### 3. 健康检查失败告警

```yaml
- alert: StorageUnhealthy
  expr: |
    rate(llm_gateway_storage_health_check_total{success="false"}[5m]) > 0
  for: 5m
  labels:
    severity: critical
  annotations:
    summary: "存储后端不健康"
    description: "{{ $labels.backend }} 后端健康检查失败"
```

---

### 4. 高重试率告警

```yaml
- alert: StorageHighRetryRate
  expr: |
    (
      rate(llm_gateway_storage_retry_total[5m])
      /
      rate(llm_gateway_storage_operation_total[5m])
    ) > 0.2
  for: 10m
  labels:
    severity: warning
  annotations:
    summary: "存储重试率过高"
    description: "重试率 {{ $value | humanizePercentage }}，超过 20% 阈值，可能存在网络或后端问题"
```

---

## 性能影响

### Metrics 收集开销

| 项目 | 开销 |
|------|------|
| CPU | < 0.5% |
| 内存 | ~2MB (固定) |
| 延迟增加 | < 10μs |

### 最佳实践

1. **合理设置抓取间隔**
   - 推荐: 15s - 30s
   - 过短会增加 Prometheus 负载
   - 过长会丢失细节

2. **使用 Recording Rules**
   ```yaml
   # 预聚合常用查询
   - record: job:storage_operation_qps:rate5m
     expr: sum by (op) (rate(llm_gateway_storage_operation_total[5m]))
   
   - record: job:storage_error_rate:rate5m
     expr: |
       rate(llm_gateway_storage_operation_total{success="false"}[5m])
       /
       rate(llm_gateway_storage_operation_total[5m])
   ```

3. **限制 Cardinality**
   - 避免使用高基数标签（如 request_id）
   - 当前标签集合基数: ~20 (op × backend × success)

---

## 故障排查

### 问题 1: 看不到任何 storage metrics

**原因**: Metrics 未初始化

**解决**:
```go
// 确保在 main.go 中调用
storageMetrics := attachments.NewPrometheusMetrics("llm_gateway")
attachments.SetMetrics(storageMetrics)
```

---

### 问题 2: Metrics 值不更新

**原因**: Storage 未使用或操作未被调用

**验证**:
```bash
# 触发一次操作
curl -X POST http://localhost:8781/v1/chat/completions \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"test"}]}'

# 检查 metrics 是否增加
curl http://localhost:8781/metrics | grep storage_operation_total
```

---

### 问题 3: 延迟直方图显示异常

**原因**: Bucket 配置不合理

**调整**:
```go
// 自定义 buckets（例如云存储延迟更高）
Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10, 30}
```

---

## 参考资源

- [Prometheus 查询文档](https://prometheus.io/docs/prometheus/latest/querying/basics/)
- [Grafana 仪表板示例](https://grafana.com/grafana/dashboards/)
- [告警规则最佳实践](https://prometheus.io/docs/practices/alerting/)

---

**相关文档**:
- [存储后端实现报告](../STORAGE-BACKEND-IMPLEMENTATION.md)
- [故障排查指南](./STORAGE-TROUBLESHOOTING.md)
- [重试机制文档](./STORAGE-RETRY.md)
