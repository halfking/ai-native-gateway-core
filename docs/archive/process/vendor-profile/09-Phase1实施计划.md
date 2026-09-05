# Phase 1 实施计划 - 数据采集

**开始时间**: 2026-07-19 01:35
**预计耗时**: 3 天
**状态**: 🚧 进行中

---

## 1. 任务分解

### 1.1 核心任务

| # | 任务 | 文件 | 耗时 | 状态 |
|---|------|------|------|------|
| 1 | 创建 collector 包结构 | `internal/collector/` | 0.5h | ⏳ |
| 2 | 实现分钟级聚合逻辑 | `minute_aggregator.go` | 2h | ⏳ |
| 3 | 实现小时级聚合逻辑 | `hour_aggregator.go` | 1h | ⏳ |
| 4 | 实现定时调度器 | `scheduler.go` | 1h | ⏳ |
| 5 | 单元测试 | `*_test.go` | 2h | ⏳ |
| 6 | 集成到 main.go | `cmd/gateway/main.go` | 0.5h | ⏳ |

### 1.2 依赖关系

```
minute_aggregator.go  (核心)
    ↓
hour_aggregator.go    (依赖分钟级数据)
    ↓
scheduler.go          (编排定时任务)
    ↓
main.go 集成          (启动时运行)
```

---

## 2. 技术方案

### 2.1 分钟级聚合（从 request_logs）

**输入**: `request_logs` 表（最近 1 分钟的数据）
**输出**: `provider_metrics_minute` 表

**SQL 查询**:
```sql
INSERT INTO provider_metrics_minute (...)
SELECT
    provider_id,
    model_name,
    endpoint,
    date_trunc('minute', timestamp) as bucket,
    COUNT(*) as total_requests,
    COUNT(*) FILTER (WHERE status_code BETWEEN 200 AND 299) as successful_requests,
    -- ... 其他指标
FROM request_logs
WHERE timestamp >= date_trunc('minute', NOW() - INTERVAL '1 minute')
  AND timestamp < date_trunc('minute', NOW())
GROUP BY provider_id, model_name, endpoint, bucket
ON CONFLICT (provider_id, model_name, endpoint, bucket) DO UPDATE SET ...;
```

**关键点**:
1. 使用 `date_trunc('minute', timestamp)` 对齐时间桶
2. 使用 `FILTER` 子句条件计数
3. 使用 `PERCENTILE_CONT` 计算百分位数
4. 使用 `ON CONFLICT DO UPDATE` 实现幂等

### 2.2 小时级聚合（从分钟级）

**输入**: `provider_metrics_minute` 表（最近 1 小时的 60 行）
**输出**: `provider_metrics_hour` 表

**SQL 查询**:
```sql
INSERT INTO provider_metrics_hour (...)
SELECT
    provider_id,
    model_name,
    endpoint,
    date_trunc('hour', bucket) as bucket,
    SUM(total_requests) as total_requests,
    SUM(successful_requests) as successful_requests,
    -- ... 重新计算聚合指标
FROM provider_metrics_minute
WHERE bucket >= date_trunc('hour', NOW() - INTERVAL '1 hour')
  AND bucket < date_trunc('hour', NOW())
GROUP BY provider_id, model_name, endpoint, date_trunc('hour', bucket)
ON CONFLICT (...) DO UPDATE SET ...;
```

### 2.3 定时调度

**方案**: Go ticker（推荐）

```go
func StartQualityMetricsCollector(ctx context.Context, db *sql.DB) {
    // 分钟级聚合：每分钟执行
    go func() {
        ticker := time.NewTicker(1 * time.Minute)
        defer ticker.Stop()

        for {
            select {
            case <-ticker.C:
                if err := CollectMinuteMetrics(db); err != nil {
                    log.Error("分钟级聚合失败", "error", err)
                }
            case <-ctx.Done():
                return
            }
        }
    }()

    // 小时级聚合：每小时执行
    go func() {
        ticker := time.NewTicker(1 * time.Hour)
        defer ticker.Stop()

        for {
            select {
            case <-ticker.C:
                if err := CollectHourMetrics(db); err != nil {
                    log.Error("小时级聚合失败", "error", err)
                }
            case <-ctx.Done():
                return
            }
        }
    }()
}
```

---

## 3. 代码结构

### 3.1 目录结构

```
internal/collector/
├── collector.go              - 导出接口和启动函数
├── minute_aggregator.go      - 分钟级聚合逻辑
├── hour_aggregator.go        - 小时级聚合逻辑
├── scheduler.go              - 定时调度器
├── types.go                  - 数据结构定义
├── minute_aggregator_test.go - 单元测试
├── hour_aggregator_test.go   - 单元测试
└── README.md                 - 模块说明
```

### 3.2 接口设计

```go
package collector

import (
    "context"
    "database/sql"
)

// Collector 质量指标采集器接口
type Collector interface {
    // Start 启动采集器（阻塞）
    Start(ctx context.Context) error

    // CollectMinuteMetrics 手动触发分钟级聚合
    CollectMinuteMetrics() error

    // CollectHourMetrics 手动触发小时级聚合
    CollectHourMetrics() error
}

// New 创建采集器实例
func New(db *sql.DB, opts ...Option) Collector {
    return &collector{
        db: db,
        // ...
    }
}

// Option 配置选项
type Option func(*collector)

// WithInterval 设置聚合间隔（用于测试）
func WithInterval(minute, hour time.Duration) Option {
    return func(c *collector) {
        c.minuteInterval = minute
        c.hourInterval = hour
    }
}
```

---

## 4. 实施步骤

### Step 1: 创建包结构 ✅

```bash
mkdir -p internal/collector
touch internal/collector/{collector.go,minute_aggregator.go,hour_aggregator.go,scheduler.go,types.go,README.md}
```

### Step 2: 实现 types.go（数据结构）

定义聚合结果的 Go 结构体，对应数据库表字段。

### Step 3: 实现 minute_aggregator.go（核心）

实现从 `request_logs` 到 `provider_metrics_minute` 的聚合逻辑。

### Step 4: 实现 hour_aggregator.go

实现从 `provider_metrics_minute` 到 `provider_metrics_hour` 的聚合。

### Step 5: 实现 scheduler.go

实现定时调度逻辑，启动两个 goroutine。

### Step 6: 实现 collector.go（入口）

导出公共接口，供 main.go 调用。

### Step 7: 单元测试

为每个聚合器编写单元测试，使用 mock 数据。

### Step 8: 集成到 main.go

在 `cmd/gateway/main.go` 中启动采集器。

---

## 5. 测试计划

### 5.1 单元测试

**测试场景**:
1. 空数据测试（request_logs 为空）
2. 单条数据测试
3. 多供应商多模型测试
4. 边界条件测试（时间跨度、NULL 值）
5. 幂等性测试（重复执行相同时间窗口）

**测试数据准备**:
```sql
-- 插入测试数据到 request_logs
INSERT INTO request_logs (provider_id, model_name, endpoint, timestamp, status_code, latency_ms, ...)
VALUES
    (1, 'gpt-4', 'chat', NOW() - INTERVAL '30 seconds', 200, 1500, ...),
    (1, 'gpt-4', 'chat', NOW() - INTERVAL '45 seconds', 200, 1800, ...),
    (1, 'gpt-4', 'chat', NOW() - INTERVAL '50 seconds', 500, 5000, ...);
```

### 5.2 集成测试

**测试流程**:
1. 启动本地 Docker 环境
2. 插入 100 条 request_logs 数据
3. 手动触发 `CollectMinuteMetrics()`
4. 验证 `provider_metrics_minute` 数据正确
5. 手动触发 `CollectHourMetrics()`
6. 验证 `provider_metrics_hour` 数据正确

---

## 6. 性能优化

### 6.1 查询优化

1. **限制时间窗口**
   ```sql
   WHERE timestamp >= date_trunc('minute', NOW() - INTERVAL '1 minute')
     AND timestamp < date_trunc('minute', NOW())
   ```

2. **使用时间索引**
   ```sql
   -- 确保 request_logs 有时间索引
   CREATE INDEX IF NOT EXISTS idx_request_logs_timestamp
   ON request_logs(timestamp DESC);
   ```

3. **批量写入**
   ```sql
   -- 使用 INSERT ... SELECT，避免逐行插入
   ```

### 6.2 并发控制

1. **互斥锁**（避免重复执行）
   ```go
   var (
       minuteLock sync.Mutex
       hourLock   sync.Mutex
   )

   func CollectMinuteMetrics(db *sql.DB) error {
       minuteLock.Lock()
       defer minuteLock.Unlock()
       // ...
   }
   ```

2. **超时控制**
   ```go
   ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
   defer cancel()
   ```

---

## 7. 监控指标

### 7.1 采集器自身指标

| 指标 | 说明 | 告警阈值 |
|------|------|----------|
| `collector_minute_duration_ms` | 分钟级聚合耗时 | > 30s |
| `collector_hour_duration_ms` | 小时级聚合耗时 | > 60s |
| `collector_minute_errors_total` | 分钟级聚合错误数 | > 3 次/小时 |
| `collector_minute_rows_inserted` | 每次插入行数 | < 100 (异常少) |

### 7.2 日志输出

```go
log.Info("分钟级聚合完成",
    "duration_ms", duration.Milliseconds(),
    "rows_inserted", rowsInserted,
    "bucket", bucket.Format(time.RFC3339),
)
```

---

## 8. 错误处理

### 8.1 错误分类

| 错误类型 | 处理方式 |
|----------|----------|
| 数据库连接失败 | 重试 3 次，失败后告警 |
| SQL 语法错误 | 立即告警，停止任务 |
| 空结果集 | 正常（记录 INFO 日志） |
| 超时 | 取消当前任务，下次重试 |

### 8.2 重试策略

```go
func CollectWithRetry(fn func() error, maxRetries int) error {
    var lastErr error
    for i := 0; i < maxRetries; i++ {
        if err := fn(); err == nil {
            return nil
        } else {
            lastErr = err
            time.Sleep(time.Duration(i+1) * time.Second) // 指数退避
        }
    }
    return fmt.Errorf("重试 %d 次后仍失败: %w", maxRetries, lastErr)
}
```

---

## 9. 配置项

### 9.1 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `QUALITY_COLLECTOR_ENABLED` | 是否启用采集器 | `true` |
| `QUALITY_COLLECTOR_MINUTE_INTERVAL` | 分钟级间隔 | `1m` |
| `QUALITY_COLLECTOR_HOUR_INTERVAL` | 小时级间隔 | `1h` |
| `QUALITY_COLLECTOR_TIMEOUT` | 单次执行超时 | `30s` |

### 9.2 配置文件（可选）

```yaml
# config/quality_collector.yaml
collector:
  enabled: true
  minute_interval: 1m
  hour_interval: 1h
  timeout: 30s
  retry:
    max_attempts: 3
    initial_backoff: 1s
```

---

## 10. 验收标准

Phase 1 完成的验收标准：

- [ ] `internal/collector/` 包创建完成
- [ ] 分钟级聚合逻辑实现并测试通过
- [ ] 小时级聚合逻辑实现并测试通过
- [ ] 定时调度器实现
- [ ] 单元测试覆盖率 > 80%
- [ ] 集成测试通过
- [ ] 集成到 main.go 并启动成功
- [ ] 本地验证：插入 request_logs → 等待 1 分钟 → 查询 provider_metrics_minute 有数据
- [ ] 252 服务器验证：部署后正常运行
- [ ] 监控指标输出正常

---

**下一步**: 开始实现 Step 1 - 创建包结构
