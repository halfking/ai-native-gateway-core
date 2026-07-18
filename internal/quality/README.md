# Package quality

供应商质量画像数据采集和聚合包。

## 功能

- **分钟级聚合**：从 `request_logs` 表每分钟聚合一次，写入 `provider_metrics_minute`
- **小时级聚合**：从 `provider_metrics_minute` 表每小时聚合一次，写入 `provider_metrics_hour`
- **定时调度**：自动启动两个 goroutine，按固定间隔执行聚合任务
- **幂等性**：使用 `ON CONFLICT DO UPDATE`，支持重复执行
- **并发安全**：使用互斥锁防止并发执行同一聚合任务

## 使用方式

### 基本用法

```go
package main

import (
    "context"
    "database/sql"
    "log"
    
    "github.com/kaixuan/llm-gateway-go/internal/quality"
)

func main() {
    db, err := sql.Open("postgres", "...")
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()
    
    // 创建采集器
    collector := quality.New(db)
    
    // 启动采集器（阻塞运行）
    ctx := context.Background()
    if err := collector.Start(ctx); err != nil {
        log.Fatal(err)
    }
}
```

### 自定义配置

```go
collector := quality.New(db,
    quality.WithMinuteInterval(1*time.Minute),  // 分钟级间隔
    quality.WithHourInterval(1*time.Hour),      // 小时级间隔
    quality.WithTimeout(30*time.Second),        // 单次超时
    quality.WithEnabled(true),                  // 是否启用
)
```

### 手动触发

```go
// 手动触发分钟级聚合
if err := collector.CollectMinuteMetrics(ctx); err != nil {
    log.Error("分钟级聚合失败", "error", err)
}

// 手动触发小时级聚合
if err := collector.CollectHourMetrics(ctx); err != nil {
    log.Error("小时级聚合失败", "error", err)
}
```

## 架构

```
request_logs (原始数据)
    ↓ 每分钟聚合
provider_metrics_minute (分钟级指标)
    ↓ 每小时聚合
provider_metrics_hour (小时级指标)
    ↓ (Phase 2) 质量画像计算
provider_quality_profiles (质量画像)
```

## 数据表

### 输入表

- `request_logs` - 原始请求日志（已有表）
  - 字段：`provider_id`, `model_name`, `endpoint`, `timestamp`, `status_code`, `latency_ms`, `ttft_ms`, `prompt_tokens`, `completion_tokens`, `cost`, `error_type`

### 输出表

- `provider_metrics_minute` - 分钟级聚合指标（Phase 0 创建）
  - 时间桶：`bucket` (分钟对齐)
  - 统计：请求数、成功率、错误分布、延迟百分位数、Token 统计
  - 唯一约束：`(provider_id, model_name, endpoint, bucket)`

- `provider_metrics_hour` - 小时级聚合指标（Phase 0 创建）
  - 时间桶：`bucket` (小时对齐)
  - 统计：累加分钟级数据，重新计算百分位数
  - 唯一约束：`(provider_id, model_name, endpoint, bucket)`

## 聚合逻辑

### 分钟级聚合

**时间窗口**：`[NOW - 1分钟, NOW)`

**SQL 关键点**：
```sql
WHERE timestamp >= date_trunc('minute', NOW() - INTERVAL '1 minute')
  AND timestamp < date_trunc('minute', NOW())
GROUP BY provider_id, model_name, endpoint, date_trunc('minute', timestamp)
```

**指标计算**：
- `total_requests` = COUNT(*)
- `successful_requests` = COUNT(*) FILTER (WHERE status_code BETWEEN 200 AND 299)
- `error_5xx` = COUNT(*) FILTER (WHERE status_code BETWEEN 500 AND 599)
- `latency_p95` = PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY latency_ms)

### 小时级聚合

**时间窗口**：`[NOW - 1小时, NOW)`

**SQL 关键点**：
```sql
WHERE bucket >= date_trunc('hour', NOW() - INTERVAL '1 hour')
  AND bucket < date_trunc('hour', NOW())
GROUP BY provider_id, model_name, endpoint, date_trunc('hour', bucket)
```

**指标计算**：
- `total_requests` = SUM(total_requests)
- `success_rate` = 100.0 * SUM(successful_requests) / SUM(total_requests)
- `latency_p95` = AVG(latency_p95)  （简化，实际可用加权平均）

## 性能考虑

### 时间复杂度

假设：
- 100 个供应商
- 每个供应商 10 个模型
- 3 个 endpoint
- 每分钟 1000 个请求

**分钟级聚合**：
- 扫描：1000 行 (WHERE timestamp 过滤)
- 分组：100 × 10 × 3 = 3000 组
- 输出：3000 行

**小时级聚合**：
- 扫描：3000 组 × 60 分钟 = 180,000 行 (已聚合)
- 分组：3000 组
- 输出：3000 行

### 索引优化

确保 `request_logs` 有时间索引：
```sql
CREATE INDEX IF NOT EXISTS idx_request_logs_timestamp 
ON request_logs(timestamp DESC);
```

### 执行时间

- 分钟级聚合：< 5 秒（正常情况）
- 小时级聚合：< 10 秒（正常情况）

## 错误处理

### 互斥锁

使用 `sync.Mutex` 防止并发执行：
```go
var minuteLock sync.Mutex  // 分钟级互斥锁
var hourLock sync.Mutex    // 小时级互斥锁
```

### 超时控制

每次聚合都有超时限制（默认 30 秒）：
```go
ctx, cancel := context.WithTimeout(ctx, timeout)
defer cancel()
```

### 失败重试

当前版本不自动重试，失败后等待下一个周期。

未来可添加重试逻辑：
```go
for i := 0; i < maxRetries; i++ {
    if err := collectMinuteMetrics(ctx, db, timeout); err == nil {
        break
    }
    time.Sleep(time.Duration(i+1) * time.Second)
}
```

## 监控指标

### TODO: 添加 Prometheus 指标

- `quality_collector_minute_duration_seconds` - 分钟级聚合耗时
- `quality_collector_hour_duration_seconds` - 小时级聚合耗时
- `quality_collector_minute_errors_total` - 分钟级聚合错误数
- `quality_collector_minute_rows_inserted` - 每次插入行数

## 测试

### 单元测试

```bash
go test -v ./internal/quality
```

### 集成测试

```bash
# 启动本地 Docker 环境
docker-compose -f docker-compose.local-r112.yml up -d

# 插入测试数据
psql -h localhost -p 15432 -U kxuser -d llm_gateway < testdata/request_logs.sql

# 手动触发聚合
go run cmd/quality-collector/main.go --once

# 验证结果
psql -h localhost -p 15432 -U kxuser -d llm_gateway \
  -c "SELECT * FROM provider_metrics_minute ORDER BY bucket DESC LIMIT 10;"
```

## 部署

### 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `QUALITY_COLLECTOR_ENABLED` | 是否启用 | `true` |
| `QUALITY_COLLECTOR_MINUTE_INTERVAL` | 分钟级间隔 | `1m` |
| `QUALITY_COLLECTOR_HOUR_INTERVAL` | 小时级间隔 | `1h` |
| `QUALITY_COLLECTOR_TIMEOUT` | 单次超时 | `30s` |

### 集成到 main.go

```go
// cmd/gateway/main.go
func main() {
    // ... 其他初始化
    
    // 启动质量指标采集器
    qualityCollector := quality.New(db)
    go func() {
        if err := qualityCollector.Start(context.Background()); err != nil {
            log.Error("质量采集器退出", "error", err)
        }
    }()
    
    // ... 其他逻辑
}
```

## 未来改进

### Phase 2（质量画像计算）

从 `provider_metrics_*` 表计算质量评分，写入 `provider_quality_profiles`：
- L1 可用性评分（35% 权重）
- L2 性能评分（25% 权重）
- L3 可信度评分（20% 权重）
- L4 稳定性评分（15% 权重）
- L5 成本效益（5% 权重）

### 优化方向

1. **采样计算**：对于大流量供应商，只取 10% 的请求计算百分位数
2. **流式处理**：使用 Kafka + Flink 实现近实时聚合
3. **分区表**：如果 `request_logs` 是分区表，优化查询性能
4. **HyperLogLog**：使用近似算法加速百分位数计算

## 相关文档

- `docs/供应商画像/02-数据库设计.md` - 表结构设计
- `docs/供应商画像/09-Phase1实施计划.md` - Phase 1 实施计划
- `sql/migrations/startup/435_provider_quality_tables.sql` - 数据库 migration
