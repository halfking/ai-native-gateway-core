# Phase 2 实施计划 - 质量计算

**开始时间**: 2026-07-19 02:50  
**预计耗时**: 5 天  
**状态**: 🚧 进行中

---

## 1. 目标

从 `provider_metrics_*` 表计算 5 个维度的质量评分，写入 `provider_quality_profiles` 表。

---

## 2. 质量评分体系

### 2.1 五个维度

| 维度 | 权重 | 说明 | 数据源 |
|------|------|------|--------|
| L1 可用性 | 35% | 成功率、错误率 | 24h 窗口 |
| L2 性能 | 25% | 延迟、TTFT | 1h 窗口（P95/P99） |
| L3 可信度 | 20% | 稳定性、一致性 | 历史趋势 |
| L4 稳定性 | 15% | 抖动、波动 | 5min 窗口 |
| L5 成本效益 | 5% | 性价比 | Token 成本 |

### 2.2 评分算法

每个维度输出 0-100 分，综合质量分 = 加权平均。

---

## 3. 任务分解

### 3.1 核心任务

| # | 任务 | 文件 | 耗时 | 状态 |
|---|------|------|------|------|
| 1 | L1 可用性评分算法 | `scorer_availability.go` | 1.5h | ⏳ |
| 2 | L2 性能评分算法 | `scorer_performance.go` | 1h | ⏳ |
| 3 | L3 可信度评分算法 | `scorer_reliability.go` | 1.5h | ⏳ |
| 4 | L4 稳定性评分算法 | `scorer_stability.go` | 1h | ⏳ |
| 5 | L5 成本效益评分算法 | `scorer_cost.go` | 0.5h | ⏳ |
| 6 | 综合评分计算器 | `profile_calculator.go` | 1h | ⏳ |
| 7 | 定时更新任务 | `profile_updater.go` | 1h | ⏳ |
| 8 | 单元测试 | `*_test.go` | 2h | ⏳ |
| 9 | 集成测试 | `profile_integration_test.go` | 1h | ⏳ |
| 10 | 集成到 main.go | `cmd/gateway/main.go` | 0.5h | ⏳ |

---

## 4. L1 可用性评分（35% 权重）

### 4.1 数据来源

**时间窗口**: 24 小时

**数据源**: `provider_metrics_hour` 表

**指标**:
- `success_rate_24h`: 成功率（24小时平均）
- `error_rate_5xx_24h`: 5xx 错误率
- `error_rate_4xx_24h`: 4xx 错误率
- `timeout_rate_24h`: 超时率

### 4.2 评分算法

```
可用性分 = 成功率权重 × f(成功率) 
          + 5xx惩罚 × g(5xx率) 
          + 4xx惩罚 × h(4xx率)
          + 超时惩罚 × k(超时率)
```

**权重分配**:
- 成功率：70%
- 5xx 错误率：20%（严重）
- 4xx 错误率：5%（轻微）
- 超时率：5%

**评分函数**:
```
f(成功率) = 成功率 × 100                     (线性)
g(5xx率) = max(0, 100 - 5xx率 × 500)         (5xx每增加1%扣50分)
h(4xx率) = max(0, 100 - 4xx率 × 200)         (4xx每增加1%扣20分)
k(超时率) = max(0, 100 - 超时率 × 300)       (超时每增加1%扣30分)
```

**SQL 查询**:
```sql
SELECT
    provider_id,
    model_name,
    AVG(success_rate) as success_rate_24h,
    AVG(error_rate_5xx) as error_rate_5xx_24h,
    100.0 * SUM(error_4xx) / NULLIF(SUM(total_requests), 0) as error_rate_4xx_24h,
    100.0 * SUM(error_timeout) / NULLIF(SUM(total_requests), 0) as timeout_rate_24h
FROM provider_metrics_hour
WHERE bucket >= NOW() - INTERVAL '24 hours'
GROUP BY provider_id, model_name;
```

---

## 5. L2 性能评分（25% 权重）

### 5.1 数据来源

**时间窗口**: 1 小时

**数据源**: `provider_metrics_hour` 表（最新 1 行）

**指标**:
- `latency_p95_1h`: P95 延迟
- `latency_p99_1h`: P99 延迟
- `ttft_p95_1h`: 首 token 时间 P95

### 5.2 评分算法

```
性能分 = P95权重 × f(P95) 
       + P99权重 × g(P99)
       + TTFT权重 × h(TTFT)
```

**权重分配**:
- P95 延迟：50%
- P99 延迟：30%
- TTFT：20%

**评分函数**（分段线性）:
```
f(P95延迟):
  ≤ 500ms:   100 分
  500-1000:  100 - (P95 - 500) / 5     (每增加100ms扣20分)
  1000-3000: 90 - (P95 - 1000) / 40    (每增加1s扣25分)
  > 3000:    max(0, 40 - (P95 - 3000) / 100)
  
g(P99延迟): 类似 P95，阈值 × 1.5

h(TTFT):
  ≤ 200ms:   100 分
  200-500:   100 - (TTFT - 200) / 3
  500-1000:  90 - (TTFT - 500) / 10
  > 1000:    max(0, 40 - (TTFT - 1000) / 50)
```

---

## 6. L3 可信度评分（20% 权重）

### 6.1 数据来源

**时间窗口**: 7 天历史

**数据源**: `provider_metrics_hour` 表

**指标**:
- `uptime_ratio_7d`: 7 天在线时长占比
- `consistency_score`: 一致性分数（变异系数）
- `error_trend`: 错误趋势（是否改善）

### 6.2 评分算法

```
可信度分 = 在线率权重 × f(在线率)
         + 一致性权重 × g(一致性)
         + 趋势权重 × h(趋势)
```

**权重分配**:
- 在线率：50%（有数据的小时数 / 168 小时）
- 一致性：30%（成功率标准差越小越好）
- 趋势：20%（近 3 天 vs 前 4 天的错误率变化）

**计算逻辑**:
```
在线率 = (有数据的小时数 / 168) × 100

一致性 = 100 - (成功率标准差 × 2)   (标准差越小越好)

趋势:
  错误率改善 > 20%: 100 分
  改善 0-20%:       80-100 分
  持平:             60 分
  恶化 < 20%:       40-60 分
  恶化 > 20%:       0-40 分
```

---

## 7. L4 稳定性评分（15% 权重）

### 7.1 数据来源

**时间窗口**: 5 分钟

**数据源**: `provider_metrics_minute` 表（最近 5 行）

**指标**:
- `latency_jitter_5m`: 延迟抖动（P95 变异系数）
- `request_rate_stability`: 请求量稳定性
- `error_spike_count`: 错误突增次数

### 7.2 评分算法

```
稳定性分 = 延迟稳定权重 × f(抖动)
         + 流量稳定权重 × g(流量波动)
         + 错误稳定权重 × h(错误突增)
```

**权重分配**:
- 延迟抖动：40%
- 流量波动：30%
- 错误突增：30%

**计算逻辑**:
```
延迟抖动 = 5分钟内 P95 的变异系数
  CV < 0.1:  100 分
  CV 0.1-0.3: 100 - CV × 200
  CV > 0.3:   max(0, 40 - (CV - 0.3) × 100)

流量波动 = 5分钟内请求量的变异系数（类似）

错误突增 = 5分钟内错误率 > 平均值 × 2 的次数
  0 次: 100 分
  1 次: 80 分
  2 次: 60 分
  ≥3 次: 40 分
```

---

## 8. L5 成本效益评分（5% 权重）

### 8.1 数据来源

**时间窗口**: 24 小时

**数据源**: `provider_metrics_hour` 表

**指标**:
- `cost_per_1k_tokens`: 每千 Token 成本
- `cost_vs_benchmark`: 与基准模型对比

### 8.2 评分算法

```
成本效益分 = 绝对成本权重 × f(成本)
           + 相对成本权重 × g(对比)
```

**权重分配**:
- 绝对成本：60%
- 相对成本：40%

**评分函数**:
```
f(每千Token成本):
  ≤ $0.01:  100 分
  0.01-0.05: 100 - (cost - 0.01) × 1000
  0.05-0.1:  90 - (cost - 0.05) × 800
  > 0.1:     max(0, 50 - (cost - 0.1) × 500)

g(相对成本) = 100 × (基准成本 / 当前成本)
  成本比基准低: > 100 分（封顶 120）
  成本比基准高: < 100 分
```

---

## 9. 综合质量分计算

### 9.1 加权平均

```
quality_score = L1 × 0.35 
              + L2 × 0.25 
              + L3 × 0.20 
              + L4 × 0.15 
              + L5 × 0.05
```

### 9.2 等级划分

| 分数区间 | 等级 | 标签 |
|---------|------|------|
| 90-100 | S | 卓越 |
| 80-89 | A | 优秀 |
| 70-79 | B | 良好 |
| 60-69 | C | 及格 |
| < 60 | D | 较差 |

### 9.3 更新频率

- **分钟级更新**: L4 稳定性（依赖 5 分钟窗口）
- **小时级更新**: L2 性能、L1/L3/L5 部分指标
- **每日更新**: L1/L3/L5 完整计算

**建议策略**: 每小时执行一次完整计算

---

## 10. 数据库写入

### 10.1 更新 SQL

```sql
INSERT INTO provider_quality_profiles (
    provider_id,
    model_name,
    quality_score,
    availability_score,
    performance_score,
    reliability_score,
    stability_score,
    cost_efficiency_score,
    -- 24h 指标
    success_rate_24h,
    error_rate_5xx_24h,
    error_rate_4xx_24h,
    timeout_rate_24h,
    -- 1h 指标
    latency_p95_1h,
    latency_p99_1h,
    ttft_p95_1h,
    -- 5min 指标
    latency_jitter_5m,
    error_spike_count_5m,
    -- 7d 指标
    uptime_ratio_7d,
    consistency_score_7d,
    -- 成本指标
    cost_per_1k_tokens,
    -- 元数据
    last_request_at,
    calculated_at
)
VALUES (?, ?, ?, ...)
ON CONFLICT (provider_id, model_name) DO UPDATE SET
    quality_score = EXCLUDED.quality_score,
    availability_score = EXCLUDED.availability_score,
    ...
    calculated_at = EXCLUDED.calculated_at;
```

---

## 11. 实施步骤

### Step 1: 创建评分器接口 ✅

```go
// internal/quality/scorer.go
type Scorer interface {
    Calculate(ctx context.Context, providerID int64, modelName string) (float64, error)
}
```

### Step 2: 实现 L1-L5 评分器

- `scorer_availability.go` - L1 可用性
- `scorer_performance.go` - L2 性能
- `scorer_reliability.go` - L3 可信度
- `scorer_stability.go` - L4 稳定性
- `scorer_cost.go` - L5 成本效益

### Step 3: 实现综合计算器

- `profile_calculator.go` - 调用 5 个评分器，计算综合分

### Step 4: 实现定时更新

- `profile_updater.go` - 每小时扫描所有供应商，更新画像

### Step 5: 单元测试

- 每个评分器独立测试
- 边界条件测试
- 评分范围验证（0-100）

### Step 6: 集成测试

- 端到端：插入 metrics 数据 → 计算画像 → 验证结果

### Step 7: 集成到 main.go

- 启动 profile updater
- 与 Phase 1 的 collector 并行运行

---

## 12. 代码结构

```
internal/quality/
├── collector.go              # Phase 1: 数据采集
├── minute_aggregator.go
├── hour_aggregator.go
├── scorer.go                 # Phase 2: 评分接口 ⏳
├── scorer_availability.go    # L1 可用性评分 ⏳
├── scorer_performance.go     # L2 性能评分 ⏳
├── scorer_reliability.go     # L3 可信度评分 ⏳
├── scorer_stability.go       # L4 稳定性评分 ⏳
├── scorer_cost.go            # L5 成本效益评分 ⏳
├── profile_calculator.go     # 综合计算器 ⏳
├── profile_updater.go        # 定时更新任务 ⏳
├── scorer_test.go            # 评分器测试 ⏳
└── profile_integration_test.go # 集成测试 ⏳
```

---

## 13. 测试数据

### 13.1 模拟场景

| 场景 | L1 | L2 | L3 | L4 | L5 | 综合 | 等级 |
|------|----|----|----|----|----|----|------|
| 完美供应商 | 100 | 100 | 100 | 100 | 100 | 100 | S |
| 高可用低性能 | 95 | 60 | 85 | 70 | 80 | 81 | A |
| 不稳定 | 80 | 80 | 60 | 40 | 80 | 70 | B |
| 高错误率 | 50 | 70 | 50 | 60 | 80 | 58 | D |

### 13.2 测试数据插入

```sql
-- 插入测试 metrics 数据，模拟不同场景
INSERT INTO provider_metrics_hour (...) VALUES (...);
```

---

## 14. 监控指标

| 指标 | 说明 | 告警阈值 |
|------|------|----------|
| `profile_calculator_duration_ms` | 单次计算耗时 | > 5s |
| `profile_calculator_errors_total` | 计算错误数 | > 3/小时 |
| `profile_updated_total` | 更新画像数 | < 预期 50% |
| `profile_score_distribution` | 评分分布直方图 | - |

---

## 15. 验收标准

Phase 2 完成的验收标准：

- [ ] 5 个评分器实现并测试通过
- [ ] 综合计算器实现并测试通过
- [ ] 定时更新任务实现
- [ ] 单元测试覆盖率 > 80%
- [ ] 集成测试通过
- [ ] 集成到 main.go 并启动成功
- [ ] 本地验证：计算画像 → 查询 `provider_quality_profiles` 有数据
- [ ] 252 服务器验证：部署后正常运行

---

**下一步**: 开始实现 Step 1 - 创建评分器接口
