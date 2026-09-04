# 任务3: 错误展示优化 - 详细提示词

## 任务3.1: 错误趋势图实现

### 执行提示词

```markdown
# 任务：实现错误趋势可视化

## 背景
当前控制台缺少错误趋势的可视化，运维人员难以快速识别异常模式、供应商质量问题、凭据可用性等。需要构建多维度的错误趋势展示系统。

## 架构设计

### 数据流
```
LLM请求错误
    ↓
[记录到supplier_errors表]
    ↓
[定时聚合任务] → supplier_error_stats (预聚合表)
    ↓
[API查询] → 前端可视化
```

## 实施步骤

### 1. 数据库表设计

#### A. 错误记录表（已存在，需确认结构）

**表**: `supplier_errors_hot` (8小时)

```sql
CREATE TABLE IF NOT EXISTS supplier_errors_hot (
    id BIGSERIAL PRIMARY KEY,
    
    -- 时间维度
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- 请求标识
    request_id TEXT NOT NULL,
    trace_id TEXT,
    
    -- 供应商维度
    supplier VARCHAR(50) NOT NULL,  -- openai, anthropic, gemini等
    credential_id BIGINT NOT NULL,
    model VARCHAR(100) NOT NULL,
    
    -- 错误信息
    error_type VARCHAR(50) NOT NULL,  -- rate_limit, timeout, auth_failed等
    error_code VARCHAR(50),            -- HTTP状态码或API错误码
    error_message TEXT,
    is_retryable BOOLEAN DEFAULT false,
    
    -- 影响范围
    affected_users INTEGER DEFAULT 1,
    
    -- 元数据
    request_metadata JSONB,  -- 请求参数、延迟等
    
    -- 索引
    INDEX idx_occurred_at (occurred_at DESC),
    INDEX idx_supplier_credential (supplier, credential_id, occurred_at DESC),
    INDEX idx_error_type (error_type, occurred_at DESC),
    INDEX idx_request_id (request_id)
);
```

**分区表**: `supplier_errors` (长期存储)

```sql
-- 按天分区的columnar表
CREATE TABLE supplier_errors (
    LIKE supplier_errors_hot INCLUDING ALL
) PARTITION BY RANGE (occurred_at);

-- 创建分区（示例）
CREATE TABLE supplier_errors_2024_01_01 
PARTITION OF supplier_errors 
FOR VALUES FROM ('2024-01-01') TO ('2024-01-02')
USING columnar;
```

#### B. 预聚合统计表

**表**: `supplier_error_stats`

```sql
CREATE TABLE supplier_error_stats (
    id BIGSERIAL PRIMARY KEY,
    
    -- 时间维度
    stat_time TIMESTAMPTZ NOT NULL,
    granularity VARCHAR(10) NOT NULL,  -- 'minute', 'hour', 'day'
    
    -- 维度
    supplier VARCHAR(50),
    credential_id BIGINT,
    error_type VARCHAR(50),
    model VARCHAR(100),
    
    -- 统计指标
    error_count INTEGER NOT NULL,
    unique_requests INTEGER NOT NULL,
    affected_users INTEGER NOT NULL,
    
    -- 错误率（需要结合总请求数）
    success_count INTEGER NOT NULL DEFAULT 0,
    total_requests INTEGER NOT NULL,
    error_rate DECIMAL(5, 2) GENERATED ALWAYS AS (
        CASE WHEN total_requests > 0 
        THEN (error_count::DECIMAL / total_requests) * 100 
        ELSE 0 END
    ) STORED,
    
    -- 聚合时间
    aggregated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- 唯一约束（防止重复聚合）
    UNIQUE (stat_time, granularity, supplier, credential_id, error_type, model)
);

-- 索引
CREATE INDEX idx_stats_time_granularity ON supplier_error_stats(stat_time DESC, granularity);
CREATE INDEX idx_stats_supplier ON supplier_error_stats(supplier, stat_time DESC);
CREATE INDEX idx_stats_credential ON supplier_error_stats(credential_id, stat_time DESC);
CREATE INDEX idx_stats_error_type ON supplier_error_stats(error_type, stat_time DESC);
```

### 2. 数据聚合服务

#### 文件结构
```
bg/
└── error_aggregator/
    ├── aggregator.go       # 主聚合逻辑
    ├── scheduler.go        # 定时调度
    └── aggregator_test.go
```

#### 实现聚合器

**文件**: `bg/error_aggregator/aggregator.go`

```go
package error_aggregator

import (
    "context"
    "database/sql"
    "fmt"
    "time"
    
    "github.com/jmoiron/sqlx"
    "go.uber.org/zap"
)

type Granularity string

const (
    GranularityMinute Granularity = "minute"
    GranularityHour   Granularity = "hour"
    GranularityDay    Granularity = "day"
)

type Aggregator struct {
    db     *sqlx.DB
    logger *zap.Logger
}

func NewAggregator(db *sqlx.DB, logger *zap.Logger) *Aggregator {
    return &Aggregator{
        db:     db,
        logger: logger,
    }
}

// AggregateErrors 聚合指定时间范围的错误
func (a *Aggregator) AggregateErrors(ctx context.Context, startTime, endTime time.Time, granularity Granularity) error {
    query := a.buildAggregationQuery(granularity)
    
    _, err := a.db.ExecContext(ctx, query, startTime, endTime)
    if err != nil {
        return fmt.Errorf("aggregation failed: %w", err)
    }
    
    a.logger.Info("aggregation completed",
        zap.Time("start_time", startTime),
        zap.Time("end_time", endTime),
        zap.String("granularity", string(granularity)),
    )
    
    return nil
}

func (a *Aggregator) buildAggregationQuery(granularity Granularity) string {
    var timeFormat string
    switch granularity {
    case GranularityMinute:
        timeFormat = "date_trunc('minute', occurred_at)"
    case GranularityHour:
        timeFormat = "date_trunc('hour', occurred_at)"
    case GranularityDay:
        timeFormat = "date_trunc('day', occurred_at)"
    default:
        timeFormat = "date_trunc('hour', occurred_at)"
    }
    
    return fmt.Sprintf(`
        INSERT INTO supplier_error_stats (
            stat_time,
            granularity,
            supplier,
            credential_id,
            error_type,
            model,
            error_count,
            unique_requests,
            affected_users,
            total_requests
        )
        SELECT
            %s AS stat_time,
            $3 AS granularity,
            supplier,
            credential_id,
            error_type,
            model,
            COUNT(*) AS error_count,
            COUNT(DISTINCT request_id) AS unique_requests,
            SUM(affected_users) AS affected_users,
            -- 需要join请求总数表（这里简化）
            (SELECT COUNT(*) FROM request_logs_hot WHERE occurred_at BETWEEN $1 AND $2) AS total_requests
        FROM supplier_errors_hot
        WHERE occurred_at >= $1 AND occurred_at < $2
        GROUP BY stat_time, supplier, credential_id, error_type, model
        ON CONFLICT (stat_time, granularity, supplier, credential_id, error_type, model)
        DO UPDATE SET
            error_count = EXCLUDED.error_count,
            unique_requests = EXCLUDED.unique_requests,
            affected_users = EXCLUDED.affected_users,
            total_requests = EXCLUDED.total_requests,
            aggregated_at = NOW()
    `, timeFormat)
}

// RunContinuous 持续运行聚合任务
func (a *Aggregator) RunContinuous(ctx context.Context) error {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            now := time.Now()
            
            // 聚合最近10分钟的分钟级数据
            if err := a.AggregateErrors(ctx, now.Add(-10*time.Minute), now, GranularityMinute); err != nil {
                a.logger.Error("minute aggregation failed", zap.Error(err))
            }
            
            // 每小时聚合小时级数据
            if now.Minute() < 5 {
                if err := a.AggregateErrors(ctx, now.Add(-2*time.Hour), now, GranularityHour); err != nil {
                    a.logger.Error("hour aggregation failed", zap.Error(err))
                }
            }
            
            // 每天聚合日级数据
            if now.Hour() == 0 && now.Minute() < 5 {
                yesterday := now.AddDate(0, 0, -1)
                if err := a.AggregateErrors(ctx, yesterday, now, GranularityDay); err != nil {
                    a.logger.Error("day aggregation failed", zap.Error(err))
                }
            }
        }
    }
}
```

### 3. API接口设计

#### 文件结构
```
api/
└── v1/
    └── errors/
        ├── handler.go
        ├── models.go
        └── handler_test.go
```

#### 请求/响应模型

**文件**: `api/v1/errors/models.go`

```go
package errors

import "time"

// ErrorTrendRequest 错误趋势查询请求
type ErrorTrendRequest struct {
    StartTime   time.Time `json:"start_time" binding:"required"`
    EndTime     time.Time `json:"end_time" binding:"required"`
    Granularity string    `json:"granularity" binding:"required,oneof=minute hour day"`
    
    // 过滤维度（可选）
    Suppliers     []string `json:"suppliers,omitempty"`
    CredentialIDs []int64  `json:"credential_ids,omitempty"`
    ErrorTypes    []string `json:"error_types,omitempty"`
    Models        []string `json:"models,omitempty"`
}

// ErrorTrendResponse 错误趋势响应
type ErrorTrendResponse struct {
    TimeSeries []TimeSeriesPoint `json:"time_series"`
    Summary    ErrorSummary      `json:"summary"`
}

// TimeSeriesPoint 时间序列数据点
type TimeSeriesPoint struct {
    Timestamp    time.Time                `json:"timestamp"`
    ErrorCount   int                      `json:"error_count"`
    ErrorRate    float64                  `json:"error_rate"`
    BySupplier   map[string]int           `json:"by_supplier"`
    ByErrorType  map[string]int           `json:"by_error_type"`
    ByCredential map[int64]CredentialStat `json:"by_credential,omitempty"`
}

// CredentialStat 凭据统计
type CredentialStat struct {
    CredentialID int64   `json:"credential_id"`
    ErrorCount   int     `json:"error_count"`
    ErrorRate    float64 `json:"error_rate"`
}

// ErrorSummary 错误汇总
type ErrorSummary struct {
    TotalErrors       int                `json:"total_errors"`
    AverageErrorRate  float64            `json:"average_error_rate"`
    TopErrorTypes     []ErrorTypeRank    `json:"top_error_types"`
    TopAffectedCreds  []CredentialRank   `json:"top_affected_credentials"`
    WorstSuppliers    []SupplierRank     `json:"worst_suppliers"`
}

// ErrorTypeRank 错误类型排名
type ErrorTypeRank struct {
    ErrorType string  `json:"error_type"`
    Count     int     `json:"count"`
    Percentage float64 `json:"percentage"`
}

// CredentialRank 凭据排名
type CredentialRank struct {
    CredentialID int64   `json:"credential_id"`
    Supplier     string  `json:"supplier"`
    ErrorCount   int     `json:"error_count"`
    ErrorRate    float64 `json:"error_rate"`
}

// SupplierRank 供应商排名
type SupplierRank struct {
    Supplier   string  `json:"supplier"`
    ErrorCount int     `json:"error_count"`
    ErrorRate  float64 `json:"error_rate"`
}
```

#### Handler实现

**文件**: `api/v1/errors/handler.go`

```go
package errors

import (
    "net/http"
    
    "github.com/gin-gonic/gin"
    "github.com/jmoiron/sqlx"
)

type Handler struct {
    db *sqlx.DB
}

func NewHandler(db *sqlx.DB) *Handler {
    return &Handler{db: db}
}

// GetErrorTrend 获取错误趋势
// @Summary 查询错误趋势
// @Tags Errors
// @Accept json
// @Produce json
// @Param request body ErrorTrendRequest true "查询条件"
// @Success 200 {object} ErrorTrendResponse
// @Router /api/v1/errors/trend [post]
func (h *Handler) GetErrorTrend(c *gin.Context) {
    var req ErrorTrendRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return
    }
    
    // 构建查询
    query, args := h.buildTrendQuery(req)
    
    var points []struct {
        StatTime     time.Time `db:"stat_time"`
        Supplier     string    `db:"supplier"`
        ErrorType    string    `db:"error_type"`
        CredentialID int64     `db:"credential_id"`
        ErrorCount   int       `db:"error_count"`
        TotalRequests int      `db:"total_requests"`
    }
    
    if err := h.db.Select(&points, query, args...); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "query failed"})
        return
    }
    
    // 聚合数据
    response := h.aggregateTimeSeries(points, req)
    
    c.JSON(http.StatusOK, response)
}

func (h *Handler) buildTrendQuery(req ErrorTrendRequest) (string, []interface{}) {
    query := `
        SELECT
            stat_time,
            supplier,
            error_type,
            credential_id,
            error_count,
            total_requests
        FROM supplier_error_stats
        WHERE stat_time >= $1 AND stat_time < $2
          AND granularity = $3
    `
    
    args := []interface{}{req.StartTime, req.EndTime, req.Granularity}
    argIdx := 4
    
    // 添加过滤条件
    if len(req.Suppliers) > 0 {
        query += fmt.Sprintf(" AND supplier = ANY($%d)", argIdx)
        args = append(args, pq.Array(req.Suppliers))
        argIdx++
    }
    
    if len(req.CredentialIDs) > 0 {
        query += fmt.Sprintf(" AND credential_id = ANY($%d)", argIdx)
        args = append(args, pq.Array(req.CredentialIDs))
        argIdx++
    }
    
    if len(req.ErrorTypes) > 0 {
        query += fmt.Sprintf(" AND error_type = ANY($%d)", argIdx)
        args = append(args, pq.Array(req.ErrorTypes))
        argIdx++
    }
    
    query += " ORDER BY stat_time ASC"
    
    return query, args
}

func (h *Handler) aggregateTimeSeries(points []struct{...}, req ErrorTrendRequest) ErrorTrendResponse {
    // 按时间戳分组
    timeMap := make(map[time.Time]*TimeSeriesPoint)
    
    for _, p := range points {
        if _, exists := timeMap[p.StatTime]; !exists {
            timeMap[p.StatTime] = &TimeSeriesPoint{
                Timestamp:   p.StatTime,
                BySupplier:  make(map[string]int),
                ByErrorType: make(map[string]int),
                ByCredential: make(map[int64]CredentialStat),
            }
        }
        
        point := timeMap[p.StatTime]
        point.ErrorCount += p.ErrorCount
        point.BySupplier[p.Supplier] += p.ErrorCount
        point.ByErrorType[p.ErrorType] += p.ErrorCount
        
        credStat := point.ByCredential[p.CredentialID]
        credStat.CredentialID = p.CredentialID
        credStat.ErrorCount += p.ErrorCount
        point.ByCredential[p.CredentialID] = credStat
        
        // 计算错误率
        if p.TotalRequests > 0 {
            point.ErrorRate = float64(point.ErrorCount) / float64(p.TotalRequests) * 100
        }
    }
    
    // 转换为切片并排序
    var timeSeries []TimeSeriesPoint
    for _, point := range timeMap {
        timeSeries = append(timeSeries, *point)
    }
    sort.Slice(timeSeries, func(i, j int) bool {
        return timeSeries[i].Timestamp.Before(timeSeries[j].Timestamp)
    })
    
    // 计算汇总统计
    summary := h.calculateSummary(points)
    
    return ErrorTrendResponse{
        TimeSeries: timeSeries,
        Summary:    summary,
    }
}

func (h *Handler) calculateSummary(points []struct{...}) ErrorSummary {
    // 统计各维度
    errorTypeCounts := make(map[string]int)
    credentialCounts := make(map[int64]int)
    supplierCounts := make(map[string]int)
    
    totalErrors := 0
    totalRequests := 0
    
    for _, p := range points {
        totalErrors += p.ErrorCount
        totalRequests += p.TotalRequests
        errorTypeCounts[p.ErrorType] += p.ErrorCount
        credentialCounts[p.CredentialID] += p.ErrorCount
        supplierCounts[p.Supplier] += p.ErrorCount
    }
    
    avgErrorRate := 0.0
    if totalRequests > 0 {
        avgErrorRate = float64(totalErrors) / float64(totalRequests) * 100
    }
    
    // 排序并取Top N
    topErrorTypes := rankErrorTypes(errorTypeCounts, totalErrors)
    topCreds := rankCredentials(credentialCounts)
    worstSuppliers := rankSuppliers(supplierCounts, totalRequests)
    
    return ErrorSummary{
        TotalErrors:      totalErrors,
        AverageErrorRate: avgErrorRate,
        TopErrorTypes:    topErrorTypes[:min(5, len(topErrorTypes))],
        TopAffectedCreds: topCreds[:min(10, len(topCreds))],
        WorstSuppliers:   worstSuppliers,
    }
}
```

### 4. 前端可视化

#### 组件结构
```
console-ui/
└── src/
    └── components/
        └── ErrorDashboard/
            ├── ErrorTrendChart.tsx       # 趋势图
            ├── ErrorDistribution.tsx     # 分布饼图
            ├── SupplierComparison.tsx    # 供应商对比
            ├── CredentialHealthMap.tsx   # 凭据健康热力图
            └── index.tsx
```

#### React组件示例

**文件**: `console-ui/src/components/ErrorDashboard/ErrorTrendChart.tsx`

```typescript
import React, { useEffect, useState } from 'react';
import { Line } from 'react-chartjs-2';
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Title,
  Tooltip,
  Legend,
  TimeScale,
} from 'chart.js';
import 'chartjs-adapter-date-fns';
import { fetchErrorTrend } from '@/api/errors';

ChartJS.register(
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Title,
  Tooltip,
  Legend,
  TimeScale
);

interface ErrorTrendChartProps {
  startTime: Date;
  endTime: Date;
  granularity: 'minute' | 'hour' | 'day';
  suppliers?: string[];
}

export const ErrorTrendChart: React.FC<ErrorTrendChartProps> = ({
  startTime,
  endTime,
  granularity,
  suppliers,
}) => {
  const [data, setData] = useState<any>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const loadData = async () => {
      setLoading(true);
      try {
        const response = await fetchErrorTrend({
          start_time: startTime.toISOString(),
          end_time: endTime.toISOString(),
          granularity,
          suppliers,
        });
        
        // 转换为Chart.js格式
        const chartData = {
          datasets: [
            {
              label: '错误数量',
              data: response.time_series.map(point => ({
                x: new Date(point.timestamp),
                y: point.error_count,
              })),
              borderColor: 'rgb(255, 99, 132)',
              backgroundColor: 'rgba(255, 99, 132, 0.5)',
              yAxisID: 'y',
            },
            {
              label: '错误率 (%)',
              data: response.time_series.map(point => ({
                x: new Date(point.timestamp),
                y: point.error_rate,
              })),
              borderColor: 'rgb(53, 162, 235)',
              backgroundColor: 'rgba(53, 162, 235, 0.5)',
              yAxisID: 'y1',
            },
          ],
        };
        
        setData(chartData);
      } catch (error) {
        console.error('Failed to load error trend:', error);
      } finally {
        setLoading(false);
      }
    };

    loadData();
  }, [startTime, endTime, granularity, suppliers]);

  const options = {
    responsive: true,
    interaction: {
      mode: 'index' as const,
      intersect: false,
    },
    plugins: {
      legend: {
        position: 'top' as const,
      },
      title: {
        display: true,
        text: '错误趋势',
      },
      tooltip: {
        callbacks: {
          label: function(context: any) {
            let label = context.dataset.label || '';
            if (label) {
              label += ': ';
            }
            if (context.parsed.y !== null) {
              label += context.parsed.y.toFixed(2);
              if (context.datasetIndex === 1) {
                label += '%';
              }
            }
            return label;
          }
        }
      },
    },
    scales: {
      x: {
        type: 'time' as const,
        time: {
          unit: granularity,
        },
        display: true,
        title: {
          display: true,
          text: '时间',
        },
      },
      y: {
        type: 'linear' as const,
        display: true,
        position: 'left' as const,
        title: {
          display: true,
          text: '错误数量',
        },
      },
      y1: {
        type: 'linear' as const,
        display: true,
        position: 'right' as const,
        title: {
          display: true,
          text: '错误率 (%)',
        },
        grid: {
          drawOnChartArea: false,
        },
      },
    },
  };

  if (loading) {
    return <div>加载中...</div>;
  }

  if (!data) {
    return <div>无数据</div>;
  }

  return <Line options={options} data={data} />;
};
```

#### 供应商对比组件

**文件**: `console-ui/src/components/ErrorDashboard/SupplierComparison.tsx`

```typescript
import React from 'react';
import { Bar } from 'react-chartjs-2';

interface SupplierComparisonProps {
  data: {
    supplier: string;
    error_count: number;
    error_rate: number;
  }[];
}

export const SupplierComparison: React.FC<SupplierComparisonProps> = ({ data }) => {
  const chartData = {
    labels: data.map(d => d.supplier),
    datasets: [
      {
        label: '错误数量',
        data: data.map(d => d.error_count),
        backgroundColor: 'rgba(255, 99, 132, 0.5)',
        yAxisID: 'y',
      },
      {
        label: '错误率 (%)',
        data: data.map(d => d.error_rate),
        backgroundColor: 'rgba(53, 162, 235, 0.5)',
        yAxisID: 'y1',
      },
    ],
  };

  const options = {
    responsive: true,
    plugins: {
      legend: {
        position: 'top' as const,
      },
      title: {
        display: true,
        text: '供应商错误对比',
      },
    },
    scales: {
      y: {
        type: 'linear' as const,
        display: true,
        position: 'left' as const,
        title: {
          display: true,
          text: '错误数量',
        },
      },
      y1: {
        type: 'linear' as const,
        display: true,
        position: 'right' as const,
        title: {
          display: true,
          text: '错误率 (%)',
        },
        grid: {
          drawOnChartArea: false,
        },
        max: 100,
      },
    },
  };

  return <Bar options={options} data={chartData} />;
};
```

### 5. 部署和配置

#### 环境变量
```bash
# .env
ERROR_AGGREGATION_ENABLED=true
ERROR_AGGREGATION_INTERVAL=5m
ERROR_RETENTION_DAYS_HOT=7
ERROR_RETENTION_DAYS_ARCHIVE=90
```

#### 启动聚合服务
```go
// cmd/aggregator/main.go
func main() {
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    db := setupDB()
    logger := setupLogger()
    
    aggregator := error_aggregator.NewAggregator(db, logger)
    
    // 启动后台任务
    go func() {
        if err := aggregator.RunContinuous(ctx); err != nil {
            logger.Fatal("aggregator failed", zap.Error(err))
        }
    }()
    
    // 等待信号
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    <-sigChan
    
    cancel()
}
```

## 验收标准

- [ ] 数据库表创建并有正确的索引
- [ ] 聚合服务运行并定时执行
- [ ] API接口返回正确的时间序列数据
- [ ] 前端图表正确展示趋势
- [ ] 支持多维度过滤（供应商、凭据、错误类型）
- [ ] 性能测试：查询1天数据<500ms
- [ ] 文档更新：API文档、部署指南

## 交付物

1. 数据库迁移脚本
2. 聚合服务代码
3. API Handler代码
4. 前端React组件
5. 部署配置文件
6. API文档（Swagger）

## 时间估算
- 数据库设计: 0.5天
- 聚合服务: 1天
- API开发: 1天
- 前端开发: 1.5天
- 测试和调优: 0.5天
```

---

## 任务3.2: 错误激增自动告警

### 执行提示词

```markdown
# 任务：实现错误激增自动检测和告警

## 背景
手动监控错误趋势效率低，需要自动检测异常模式并实时告警，包括：
- 突发性错误激增（短时间内错误率大幅上升）
- 特定供应商或凭据持续高错误率
- 新类型错误首次出现

## 架构设计

### 告警流程
```
错误数据采集
    ↓
[滑动窗口分析] → 检测异常
    ↓
[告警规则引擎] → 判断是否触发
    ↓
[去重和聚合] → 避免告警风暴
    ↓
[多渠道通知] → Slack/Email/Webhook
```

## 实施步骤

### 1. 告警规则配置表

**表**: `alert_rules`

```sql
CREATE TABLE alert_rules (
    id BIGSERIAL PRIMARY KEY,
    
    -- 规则基本信息
    name VARCHAR(100) NOT NULL UNIQUE,
    description TEXT,
    enabled BOOLEAN NOT NULL DEFAULT true,
    severity VARCHAR(20) NOT NULL,  -- 'critical', 'warning', 'info'
    
    -- 规则类型
    rule_type VARCHAR(50) NOT NULL,  -- 'spike', 'threshold', 'new_error'
    
    -- 检测维度
    scope JSONB NOT NULL,  -- {"suppliers": ["openai"], "error_types": ["rate_limit"]}
    
    -- 阈值配置
    config JSONB NOT NULL,  -- 规则特定配置
    
    -- 告警渠道
    notification_channels JSONB NOT NULL,  -- [{"type": "slack", "webhook": "..."}]
    
    -- 去重配置
    cooldown_minutes INTEGER DEFAULT 30,
    
    -- 时间范围
    active_hours JSONB,  -- 仅在特定时间激活
    
    -- 元数据
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by VARCHAR(100)
);

CREATE INDEX idx_alert_rules_enabled ON alert_rules(enabled);
CREATE INDEX idx_alert_rules_type ON alert_rules(rule_type);
```

**告警历史表**: `alert_history`

```sql
CREATE TABLE alert_history (
    id BIGSERIAL PRIMARY KEY,
    
    rule_id BIGINT NOT NULL REFERENCES alert_rules(id),
    rule_name VARCHAR(100) NOT NULL,
    
    -- 触发信息
    triggered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    severity VARCHAR(20) NOT NULL,
    
    -- 告警内容
    title TEXT NOT NULL,
    message TEXT NOT NULL,
    details JSONB,  -- 详细数据
    
    -- 影响范围
    affected_supplier VARCHAR(50),
    affected_credentials BIGINT[],
    error_count INTEGER,
    error_rate DECIMAL(5,2),
    
    -- 通知状态
    notification_status JSONB,  -- 各渠道发送状态
    
    -- 处理状态
    acknowledged BOOLEAN DEFAULT false,
    acknowledged_by VARCHAR(100),
    acknowledged_at TIMESTAMPTZ,
    resolved BOOLEAN DEFAULT false,
    resolved_at TIMESTAMPTZ,
    
    INDEX idx_alert_history_triggered_at (triggered_at DESC),
    INDEX idx_alert_history_rule_id (rule_id),
    INDEX idx_alert_history_severity (severity, triggered_at DESC)
);
```

### 2. 告警检测引擎

#### 文件结构
```
bg/
└── alert_engine/
    ├── engine.go            # 主引擎
    ├── detectors/
    │   ├── spike.go         # 突增检测
    │   ├── threshold.go     # 阈值检测
    │   └── anomaly.go       # 异常检测
    ├── notifiers/
    │   ├── slack.go
    │   ├── email.go
    │   └── webhook.go
    └── deduplicator.go      # 去重器
```

#### 主引擎

**文件**: `bg/alert_engine/engine.go`

```go
package alert_engine

import (
    "context"
    "sync"
    "time"
    
    "github.com/jmoiron/sqlx"
    "go.uber.org/zap"
)

type AlertEngine struct {
    db          *sqlx.DB
    logger      *zap.Logger
    detectors   map[string]Detector
    notifiers   map[string]Notifier
    dedup       *Deduplicator
    mu          sync.RWMutex
}

func NewAlertEngine(db *sqlx.DB, logger *zap.Logger) *AlertEngine {
    engine := &AlertEngine{
        db:        db,
        logger:    logger,
        detectors: make(map[string]Detector),
        notifiers: make(map[string]Notifier),
        dedup:     NewDeduplicator(30 * time.Minute),
    }
    
    // 注册检测器
    engine.RegisterDetector("spike", NewSpikeDetector())
    engine.RegisterDetector("threshold", NewThresholdDetector())
    engine.RegisterDetector("new_error", NewAnomalyDetector())
    
    // 注册通知器
    engine.RegisterNotifier("slack", NewSlackNotifier(logger))
    engine.RegisterNotifier("email", NewEmailNotifier(logger))
    engine.RegisterNotifier("webhook", NewWebhookNotifier(logger))
    
    return engine
}

func (e *AlertEngine) RegisterDetector(ruleType string, detector Detector) {
    e.mu.Lock()
    defer e.mu.Unlock()
    e.detectors[ruleType] = detector
}

func (e *AlertEngine) RegisterNotifier(channelType string, notifier Notifier) {
    e.mu.Lock()
    defer e.mu.Unlock()
    e.notifiers[channelType] = notifier
}

// Run 启动告警引擎
func (e *AlertEngine) Run(ctx context.Context) error {
    ticker := time.NewTicker(1 * time.Minute)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            if err := e.evaluate(ctx); err != nil {
                e.logger.Error("evaluation failed", zap.Error(err))
            }
        }
    }
}

// evaluate 评估所有告警规则
func (e *AlertEngine) evaluate(ctx context.Context) error {
    // 加载所有启用的规则
    rules, err := e.loadActiveRules(ctx)
    if err != nil {
        return err
    }
    
    var wg sync.WaitGroup
    for _, rule := range rules {
        wg.Add(1)
        go func(r AlertRule) {
            defer wg.Done()
            if err := e.evaluateRule(ctx, r); err != nil {
                e.logger.Error("rule evaluation failed",
                    zap.String("rule", r.Name),
                    zap.Error(err),
                )
            }
        }(rule)
    }
    
    wg.Wait()
    return nil
}

func (e *AlertEngine) evaluateRule(ctx context.Context, rule AlertRule) error {
    // 获取对应的检测器
    e.mu.RLock()
    detector, exists := e.detectors[rule.RuleType]
    e.mu.RUnlock()
    
    if !exists {
        return fmt.Errorf("unknown rule type: %s", rule.RuleType)
    }
    
    // 检测是否触发
    alert, triggered := detector.Detect(ctx, e.db, rule)
    if !triggered {
        return nil
    }
    
    // 去重检查
    if e.dedup.IsDuplicate(rule.ID, alert.Fingerprint()) {
        e.logger.Debug("alert deduplicated",
            zap.String("rule", rule.Name),
            zap.String("fingerprint", alert.Fingerprint()),
        )
        return nil
    }
    
    // 记录告警历史
    if err := e.saveAlert(ctx, alert); err != nil {
        return err
    }
    
    // 发送通知
    return e.sendNotifications(ctx, rule, alert)
}

func (e *AlertEngine) sendNotifications(ctx context.Context, rule AlertRule, alert *Alert) error {
    var wg sync.WaitGroup
    notificationStatus := make(map[string]string)
    var mu sync.Mutex
    
    for _, channel := range rule.NotificationChannels {
        wg.Add(1)
        go func(ch NotificationChannel) {
            defer wg.Done()
            
            e.mu.RLock()
            notifier, exists := e.notifiers[ch.Type]
            e.mu.RUnlock()
            
            if !exists {
                e.logger.Warn("unknown notifier type", zap.String("type", ch.Type))
                return
            }
            
            err := notifier.Send(ctx, alert, ch.Config)
            
            mu.Lock()
            if err != nil {
                notificationStatus[ch.Type] = fmt.Sprintf("failed: %v", err)
            } else {
                notificationStatus[ch.Type] = "sent"
            }
            mu.Unlock()
        }(channel)
    }
    
    wg.Wait()
    
    // 更新通知状态
    return e.updateNotificationStatus(ctx, alert.ID, notificationStatus)
}

type Detector interface {
    Detect(ctx context.Context, db *sqlx.DB, rule AlertRule) (*Alert, bool)
}

type Notifier interface {
    Send(ctx context.Context, alert *Alert, config map[string]interface{}) error
}
```

#### 突增检测器

**文件**: `bg/alert_engine/detectors/spike.go`

```go
package detectors

import (
    "context"
    "database/sql"
    "math"
    "time"
    
    "github.com/jmoiron/sqlx"
)

type SpikeDetector struct{}

func NewSpikeDetector() *SpikeDetector {
    return &SpikeDetector{}
}

// Detect 检测错误率突增
func (d *SpikeDetector) Detect(ctx context.Context, db *sqlx.DB, rule AlertRule) (*Alert, bool) {
    config := d.parseConfig(rule.Config)
    
    // 获取当前窗口的错误率
    currentRate, err := d.getErrorRate(ctx, db, rule.Scope, config.WindowMinutes)
    if err != nil {
        return nil, false
    }
    
    // 获取基线错误率（过去N个窗口的平均值）
    baselineRate, err := d.getBaselineRate(ctx, db, rule.Scope, config.BaselineMinutes, config.WindowMinutes)
    if err != nil {
        return nil, false
    }
    
    // 计算倍数
    multiplier := currentRate / baselineRate
    
    // 判断是否触发
    if multiplier < config.Threshold {
        return nil, false
    }
    
    // 构建告警
    alert := &Alert{
        RuleID:            rule.ID,
        RuleName:          rule.Name,
        Severity:          rule.Severity,
        Title:             fmt.Sprintf("错误率突增 %s", rule.Scope),
        Message:           fmt.Sprintf("当前错误率 %.2f%% 是基线 %.2f%% 的 %.1f 倍", currentRate, baselineRate, multiplier),
        Details: map[string]interface{}{
            "current_rate":   currentRate,
            "baseline_rate":  baselineRate,
            "multiplier":     multiplier,
            "threshold":      config.Threshold,
            "window_minutes": config.WindowMinutes,
        },
        AffectedSupplier: extractSupplier(rule.Scope),
        ErrorRate:        currentRate,
    }
    
    return alert, true
}

type SpikeConfig struct {
    WindowMinutes    int     `json:"window_minutes"`    // 当前窗口大小
    BaselineMinutes  int     `json:"baseline_minutes"`  // 基线窗口大小
    Threshold        float64 `json:"threshold"`         // 倍数阈值
}

func (d *SpikeDetector) getErrorRate(ctx context.Context, db *sqlx.DB, scope map[string]interface{}, windowMinutes int) (float64, error) {
    query := `
        SELECT
            COALESCE(SUM(error_count), 0) AS errors,
            COALESCE(SUM(total_requests), 1) AS total
        FROM supplier_error_stats
        WHERE stat_time >= NOW() - INTERVAL '%d minutes'
          AND granularity = 'minute'
    `
    
    // 添加scope过滤
    query = addScopeFilters(query, scope)
    
    var result struct {
        Errors int `db:"errors"`
        Total  int `db:"total"`
    }
    
    if err := db.GetContext(ctx, &result, fmt.Sprintf(query, windowMinutes)); err != nil {
        return 0, err
    }
    
    if result.Total == 0 {
        return 0, nil
    }
    
    return float64(result.Errors) / float64(result.Total) * 100, nil
}

func (d *SpikeDetector) getBaselineRate(ctx context.Context, db *sqlx.DB, scope map[string]interface{}, baselineMinutes, windowMinutes int) (float64, error) {
    // 计算过去N个窗口的平均错误率
    query := `
        SELECT
            COALESCE(AVG(error_rate), 0) AS avg_rate
        FROM (
            SELECT
                date_trunc('minute', stat_time) AS window,
                SUM(error_count)::DECIMAL / NULLIF(SUM(total_requests), 0) * 100 AS error_rate
            FROM supplier_error_stats
            WHERE stat_time >= NOW() - INTERVAL '%d minutes'
              AND stat_time < NOW() - INTERVAL '%d minutes'
              AND granularity = 'minute'
            %s
            GROUP BY window
        ) AS baseline
    `
    
    scopeFilters := buildScopeFilters(scope)
    
    var avgRate float64
    err := db.GetContext(ctx, &avgRate, fmt.Sprintf(query, baselineMinutes+windowMinutes, windowMinutes, scopeFilters))
    if err != nil {
        return 0, err
    }
    
    // 避免除零，设置最小基线
    if avgRate < 0.01 {
        avgRate = 0.01
    }
    
    return avgRate, nil
}
```

#### 阈值检测器

**文件**: `bg/alert_engine/detectors/threshold.go`

```go
package detectors

type ThresholdDetector struct{}

func NewThresholdDetector() *ThresholdDetector {
    return &ThresholdDetector{}
}

// Detect 检测错误率是否超过阈值
func (d *ThresholdDetector) Detect(ctx context.Context, db *sqlx.DB, rule AlertRule) (*Alert, bool) {
    config := d.parseConfig(rule.Config)
    
    // 获取当前错误率
    currentRate, errorCount, err := d.getCurrentMetrics(ctx, db, rule.Scope, config.WindowMinutes)
    if err != nil {
        return nil, false
    }
    
    // 判断是否超过阈值
    if currentRate < config.ErrorRateThreshold {
        return nil, false
    }
    
    // 可选：还需要满足最小错误数
    if errorCount < config.MinErrorCount {
        return nil, false
    }
    
    // 构建告警
    alert := &Alert{
        RuleID:   rule.ID,
        RuleName: rule.Name,
        Severity: rule.Severity,
        Title:    fmt.Sprintf("错误率超过阈值 %s", rule.Scope),
        Message: fmt.Sprintf("错误率 %.2f%% 超过阈值 %.2f%%，共 %d 个错误",
            currentRate, config.ErrorRateThreshold, errorCount),
        Details: map[string]interface{}{
            "current_rate": currentRate,
            "threshold":    config.ErrorRateThreshold,
            "error_count":  errorCount,
        },
        AffectedSupplier: extractSupplier(rule.Scope),
        ErrorRate:        currentRate,
        ErrorCount:       errorCount,
    }
    
    return alert, true
}

type ThresholdConfig struct {
    WindowMinutes       int     `json:"window_minutes"`
    ErrorRateThreshold  float64 `json:"error_rate_threshold"`  // 百分比
    MinErrorCount       int     `json:"min_error_count"`        // 最小错误数
}
```

### 3. 通知器实现

#### Slack通知

**文件**: `bg/alert_engine/notifiers/slack.go`

```go
package notifiers

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "time"
)

type SlackNotifier struct {
    client *http.Client
    logger *zap.Logger
}

func NewSlackNotifier(logger *zap.Logger) *SlackNotifier {
    return &SlackNotifier{
        client: &http.Client{Timeout: 10 * time.Second},
        logger: logger,
    }
}

func (n *SlackNotifier) Send(ctx context.Context, alert *Alert, config map[string]interface{}) error {
    webhookURL, ok := config["webhook_url"].(string)
    if !ok {
        return fmt.Errorf("missing webhook_url in config")
    }
    
    // 构建Slack消息
    message := n.buildSlackMessage(alert)
    
    payload, err := json.Marshal(message)
    if err != nil {
        return fmt.Errorf("marshal failed: %w", err)
    }
    
    req, err := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewReader(payload))
    if err != nil {
        return err
    }
    req.Header.Set("Content-Type", "application/json")
    
    resp, err := n.client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("slack returned status %d", resp.StatusCode)
    }
    
    n.logger.Info("slack notification sent",
        zap.String("alert", alert.Title),
        zap.String("severity", alert.Severity),
    )
    
    return nil
}

func (n *SlackNotifier) buildSlackMessage(alert *Alert) map[string]interface{} {
    color := n.severityColor(alert.Severity)
    
    return map[string]interface{}{
        "attachments": []map[string]interface{}{
            {
                "color":     color,
                "title":     alert.Title,
                "text":      alert.Message,
                "timestamp": alert.TriggeredAt.Unix(),
                "fields": []map[string]interface{}{
                    {
                        "title": "严重级别",
                        "value": alert.Severity,
                        "short": true,
                    },
                    {
                        "title": "错误率",
                        "value": fmt.Sprintf("%.2f%%", alert.ErrorRate),
                        "short": true,
                    },
                    {
                        "title": "供应商",
                        "value": alert.AffectedSupplier,
                        "short": true,
                    },
                    {
                        "title": "错误数",
                        "value": fmt.Sprintf("%d", alert.ErrorCount),
                        "short": true,
                    },
                },
                "actions": []map[string]interface{}{
                    {
                        "type": "button",
                        "text": "查看详情",
                        "url":  fmt.Sprintf("https://console.example.com/alerts/%d", alert.ID),
                    },
                },
            },
        },
    }
}

func (n *SlackNotifier) severityColor(severity string) string {
    switch severity {
    case "critical":
        return "danger"
    case "warning":
        return "warning"
    default:
        return "good"
    }
}
```

### 4. 去重器

**文件**: `bg/alert_engine/deduplicator.go`

```go
package alert_engine

import (
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "sync"
    "time"
)

type Deduplicator struct {
    seen     map[string]time.Time
    cooldown time.Duration
    mu       sync.RWMutex
}

func NewDeduplicator(cooldown time.Duration) *Deduplicator {
    d := &Deduplicator{
        seen:     make(map[string]time.Time),
        cooldown: cooldown,
    }
    
    // 定期清理过期记录
    go d.cleanup()
    
    return d
}

func (d *Deduplicator) IsDuplicate(ruleID int64, fingerprint string) bool {
    key := fmt.Sprintf("%d:%s", ruleID, fingerprint)
    
    d.mu.RLock()
    lastSeen, exists := d.seen[key]
    d.mu.RUnlock()
    
    if !exists {
        d.mu.Lock()
        d.seen[key] = time.Now()
        d.mu.Unlock()
        return false
    }
    
    // 检查是否在冷却期内
    if time.Since(lastSeen) < d.cooldown {
        return true
    }
    
    // 更新时间戳
    d.mu.Lock()
    d.seen[key] = time.Now()
    d.mu.Unlock()
    
    return false
}

func (d *Deduplicator) cleanup() {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()
    
    for range ticker.C {
        now := time.Now()
        d.mu.Lock()
        for key, lastSeen := range d.seen {
            if now.Sub(lastSeen) > d.cooldown*2 {
                delete(d.seen, key)
            }
        }
        d.mu.Unlock()
    }
}

// Alert的指纹生成
func (a *Alert) Fingerprint() string {
    data := fmt.Sprintf("%s:%s:%s:%.2f",
        a.AffectedSupplier,
        a.AffectedCredentials,
        a.Message,
        a.ErrorRate,
    )
    
    hash := sha256.Sum256([]byte(data))
    return hex.EncodeToString(hash[:])
}
```

### 5. 预定义告警规则

创建常用告警规则的SQL脚本：

```sql
-- 规则1: 任何供应商错误率突增3倍
INSERT INTO alert_rules (name, description, enabled, severity, rule_type, scope, config, notification_channels, cooldown_minutes)
VALUES (
    'error_rate_spike_3x',
    '任何供应商错误率突增3倍',
    true,
    'warning',
    'spike',
    '{}',  -- 所有供应商
    '{"window_minutes": 5, "baseline_minutes": 60, "threshold": 3.0}',
    '[{"type": "slack", "webhook_url": "https://hooks.slack.com/services/YOUR/WEBHOOK/URL"}]',
    30
);

-- 规则2: OpenAI错误率超过10%
INSERT INTO alert_rules (name, description, enabled, severity, rule_type, scope, config, notification_channels, cooldown_minutes)
VALUES (
    'openai_high_error_rate',
    'OpenAI错误率超过10%',
    true,
    'critical',
    'threshold',
    '{"suppliers": ["openai"]}',
    '{"window_minutes": 10, "error_rate_threshold": 10.0, "min_error_count": 5}',
    '[{"type": "slack", "webhook_url": "https://hooks.slack.com/services/YOUR/WEBHOOK/URL"}, {"type": "email", "recipients": ["ops@example.com"]}]',
    60
);

-- 规则3: 特定凭据错误率持续高
INSERT INTO alert_rules (name, description, enabled, severity, rule_type, scope, config, notification_channels, cooldown_minutes)
VALUES (
    'credential_persistent_errors',
    '特定凭据错误率持续高于5%超过30分钟',
    true,
    'warning',
    'threshold',
    '{}',
    '{"window_minutes": 30, "error_rate_threshold": 5.0, "min_error_count": 10}',
    '[{"type": "slack", "webhook_url": "https://hooks.slack.com/services/YOUR/WEBHOOK/URL"}]',
    120
);
```

## 验收标准

- [ ] 告警规则表和历史表创建
- [ ] 至少3种检测器实现（突增、阈值、异常）
- [ ] 至少2种通知渠道（Slack、Email）
- [ ] 告警去重正常工作
- [ ] 预定义规则加载
- [ ] 告警触发和通知的端到端测试
- [ ] 性能测试：评估1000条规则的延迟
- [ ] 文档：告警配置指南

## 交付物

1. 数据库迁移脚本（alert_rules, alert_history）
2. 告警引擎代码
3. 检测器实现
4. 通知器实现
5. 预定义规则SQL
6. 配置文档
7. 运维手册

## 时间估算
- 数据库设计: 0.5天
- 告警引擎核心: 1天
- 检测器实现: 1天
- 通知器实现: 0.5天
- 去重逻辑: 0.5天
- 测试和调优: 0.5天
```

---

## 总结

任务3的两个子任务为错误监控和告警提供了完整的解决方案：

### 任务3.1: 错误趋势图
- **数据层**: 预聚合表设计，支持高效查询
- **服务层**: 定时聚合任务，多粒度统计
- **API层**: RESTful接口，支持多维度过滤
- **前端层**: React图表组件，可视化展示

### 任务3.2: 自动告警
- **规则引擎**: 可配置的告警规则
- **检测器**: 多种检测算法（突增、阈值、异常）
- **通知系统**: 多渠道通知（Slack、Email、Webhook）
- **去重机制**: 避免告警风暴

**下一步**: 继续生成任务4-10的详细提示词。
